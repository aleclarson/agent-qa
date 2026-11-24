package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const supervisorArg = "__agent-qa-server"

type serverState struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	Address  string `json:"address"`
	Identity string `json:"identity"`
}

type serverLaunch struct {
	Command []string
	Token   string
	Address string
}

var localHTTP = &http.Client{
	Transport: &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext,
		DisableKeepAlives: true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       700 * time.Millisecond,
}

func serverIdentity(head string, s *serverConfig) string {
	return fingerprint(struct {
		Head   string
		Server *serverConfig
	}{head, s})
}

func (w workspace) serverLease() string { return filepath.Join(w.dir, "server.lock") }
func (w workspace) serverLog() string   { return filepath.Join(w.dir, "server.log") }
func (w workspace) serverState() string { return filepath.Join(w.dir, "server.json") }

func (w workspace) serverActive() (bool, error) {
	f, err := os.Open(w.serverLease())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	free, err := tryLock(f)
	return !free, err
}

func (w workspace) ownedServer() (*serverState, error) {
	active, err := w.serverActive()
	if err != nil || !active {
		return nil, err
	}
	var state serverState
	if err := readJSON(w.serverState(), &state); err != nil {
		return nil, fmt.Errorf("read active server state: %w", err)
	}
	host, _, err := net.SplitHostPort(state.Endpoint)
	if err != nil || host != "127.0.0.1" || len(state.Token) != 64 {
		return nil, errors.New("invalid active server state")
	}
	return &state, nil
}

func localRequest(ctx context.Context, method, url, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := localHTTP.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}

func (s *serverState) request(ctx context.Context, method, path string) bool {
	ok, _ := localRequest(ctx, method, "http://"+s.Endpoint+path, s.Token)
	return ok
}

func portOccupied(ctx context.Context, address string) bool {
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (w workspace) stopServer(ctx context.Context) error {
	state, err := w.ownedServer()
	if err != nil {
		return err
	}
	if state == nil {
		return context.Cause(ctx)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for {
		state.request(cleanup, http.MethodPost, "/stop")
		active, err := w.serverActive()
		if err != nil {
			return err
		}
		if !active {
			return context.Cause(ctx)
		}
		if err := pause(cleanup, 60*time.Millisecond); err != nil {
			return fmt.Errorf("wait for owned server to stop (log %s): %w", w.serverLog(), err)
		}
	}
}

func (w workspace) launchServer(ctx context.Context, s *serverConfig, identity, address string) (*serverState, error) {
	if portOccupied(ctx, address) {
		return nil, fmt.Errorf("readiness port %s is already occupied", address)
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}

	lease, err := os.OpenFile(w.serverLease(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	free, err := tryLock(lease)
	if err != nil {
		return nil, err
	}
	if !free {
		return nil, errors.New("server lease already held")
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	control, err := listener.File()
	if err != nil {
		return nil, err
	}
	defer control.Close()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	state := &serverState{listener.Addr().String(), hex.EncodeToString(token[:]), address, identity}
	if err := writeJSON(w.serverState(), state, true, 0600); err != nil {
		return nil, err
	}
	data, err := encodeJSON(serverLaunch{s.Command, state.Token, address})
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(w.serverLog(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, supervisorArg)
	cmd.Dir, cmd.Env = w.tree, projectEnv(w.tree)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(string(data)), log, log
	cmd.ExtraFiles = []*os.File{control, lease}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Reap while this CLI is alive. The detached supervisor owns the lease
	// and remains independent of the CLI after it exits.
	go func() { _ = cmd.Wait() }()
	return state, nil
}

func (a app) ensureServer(ctx context.Context, w workspace, s *serverConfig, head string) error {
	if s == nil {
		return w.stopServer(ctx)
	}
	identity := serverIdentity(head, s)
	state, err := w.ownedServer()
	if err != nil {
		return err
	}
	if state != nil && state.Identity == identity {
		if state.request(ctx, http.MethodGet, "/status") {
			ready, _ := localRequest(ctx, http.MethodGet, s.ReadinessURL, "")
			if ready && state.request(ctx, http.MethodGet, "/status") {
				return nil
			}
		}
	}
	if err := w.stopServer(ctx); err != nil {
		return err
	}
	address, err := readinessAddress(s.ReadinessURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.err, "agent-qa: starting server (log %s)\n", w.serverLog())
	state, err = w.launchServer(ctx, s, identity, address)
	if err != nil {
		return err
	}
	if err := w.waitReady(ctx, state, s); err != nil {
		// A canceled caller still owns cleanup until the supervisor exits.
		return errors.Join(err, w.stopServer(context.Background()))
	}
	return nil
}

func (w workspace) waitReady(ctx context.Context, state *serverState, s *serverConfig) error {
	timeout, _ := time.ParseDuration(s.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stability := min(150*time.Millisecond, timeout/4)
	interval := min(50*time.Millisecond, timeout/8)
	var readySince time.Time
	for {
		active, err := w.serverActive()
		if err != nil {
			return err
		}
		if !active {
			return fmt.Errorf("server exited before readiness (log %s)", w.serverLog())
		}
		ready, _ := localRequest(ctx, http.MethodGet, s.ReadinessURL, "")
		if ready && state.request(ctx, http.MethodGet, "/status") {
			if readySince.IsZero() {
				readySince = time.Now()
			} else if time.Since(readySince) >= stability {
				return nil
			}
		} else {
			readySince = time.Time{}
		}
		if err := pause(ctx, interval); err != nil {
			return fmt.Errorf("wait for server readiness (log %s): %w", w.serverLog(), err)
		}
	}
}

func serveManaged() error {
	control := os.NewFile(3, "server control")
	lease := os.NewFile(4, "server lease")
	defer lease.Close()
	syscall.CloseOnExec(int(lease.Fd()))
	listener, err := net.FileListener(control)
	control.Close()
	if err != nil {
		return err
	}
	defer listener.Close()

	var launch serverLaunch
	if err := decodeJSON(os.Stdin, &launch); err != nil {
		return err
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(interrupt)
	if portOccupied(context.Background(), launch.Address) {
		return fmt.Errorf("readiness port %s is already occupied", launch.Address)
	}
	if len(launch.Command) == 0 {
		return errors.New("missing server command")
	}
	cmd := exec.Command(launch.Command[0], launch.Command[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := make(chan struct{}, 1)
	controlServer := &http.Server{
		ReadHeaderTimeout: time.Second,
		WriteTimeout:      time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+launch.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/status":
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodPost && r.URL.Path == "/stop":
				w.WriteHeader(http.StatusNoContent)
				select {
				case stop <- struct{}{}:
				default:
				}
			default:
				http.NotFound(w, r)
			}
		}),
	}
	controlDone := make(chan error, 1)
	go func() { controlDone <- controlServer.Serve(listener) }()
	select {
	case <-interrupt:
	case <-stop:
	case err := <-done:
		fmt.Fprintf(os.Stderr, "agent-qa: server command exited (%v)\n", err)
	case err := <-controlDone:
		fmt.Fprintf(os.Stderr, "agent-qa: server control exited (%v)\n", err)
	}
	controlServer.Close()
	// The supervisor is the session and process-group leader. Keeping it in
	// the group until the final signal prevents signaling a recycled group ID.
	_ = syscall.Kill(-os.Getpid(), syscall.SIGTERM)
	time.Sleep(350 * time.Millisecond)
	_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
	return nil
}
