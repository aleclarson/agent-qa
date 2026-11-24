package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var cliDirectory string

var buildCLI = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "agent-qa-test-")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "agent-qa")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("%w: %s", err, output)
	}
	cliDirectory = dir
	return binary, nil
})

func TestMain(m *testing.M) {
	code := m.Run()
	if cliDirectory != "" {
		os.RemoveAll(cliDirectory)
	}
	os.Exit(code)
}

type fixture struct {
	t      *testing.T
	dir    string
	binary string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := buildCLI()
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{t, dir, binary}
	f.git("init", "-q")
	f.git("config", "user.email", "test@example.invalid")
	f.git("config", "user.name", "QA Fixture")
	return f
}

func (f fixture) git(args ...string) string {
	f.t.Helper()
	out, err := git(context.Background(), f.dir, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func writeFixture(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) config(c config) {
	f.t.Helper()
	if err := writeJSON(filepath.Join(f.dir, configName), c, true, 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixture) commit() string {
	f.t.Helper()
	f.git("add", ".")
	f.git("commit", "-qm", "fixture")
	return f.git("rev-parse", "HEAD")
}

func (f fixture) command(args ...string) *exec.Cmd {
	cmd := exec.Command(f.binary, args...)
	cmd.Dir = f.dir
	return cmd
}

func (f fixture) call(want int, args ...string) string {
	f.t.Helper()
	cmd := f.command(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); exitStatus(err) != want {
		f.t.Fatalf("%v: wanted exit %d, got %v\n%s%s", args, want, err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func (f fixture) doctor(want int) doctorReport {
	f.t.Helper()
	var report doctorReport
	if err := json.Unmarshal([]byte(f.call(want, "doctor", "--json")), &report); err != nil {
		f.t.Fatal(err)
	}
	return report
}

func TestInitAndDoctor(t *testing.T) {
	f := newFixture(t)
	initStdin := func(input string, want int, flags ...string) {
		t.Helper()
		cmd := f.command(append([]string{"init"}, flags...)...)
		cmd.Stdin = strings.NewReader(input)
		if out, err := cmd.CombinedOutput(); exitStatus(err) != want {
			t.Fatalf("init: wanted exit %d, got %v\n%s", want, err, out)
		}
	}
	report := f.doctor(1)
	if len(report.ConfigErrors) != 1 {
		t.Fatalf("missing config report: %+v", report)
	}
	initStdin(`{"version":1,"targets":{"web":{"unknown":true}}}`, 1, "--from", "-")
	initStdin(`{"version":2,"targets":{"web":{}}}`, 1, "--from", "-")
	if _, err := os.Stat(filepath.Join(f.dir, configName)); !os.IsNotExist(err) {
		t.Fatalf("invalid config created a file: %v", err)
	}
	initStdin("web\n\n\n\n\n\n", 0)
	initStdin(`{"version":1,"targets":{"other":{}}}`, 1, "--from", "-")
	if r := f.doctor(0); len(r.Targets) != 1 || r.Targets[0].Name != "web" || r.Targets[0].Ready {
		t.Fatalf("interactive config or existing config protection: %+v", r)
	}
	initStdin(`{"version":1,"settings":{"device":"default"},"targets":{"ios":{"dependencies":{"command":["${device}"]}}}}`, 0, "--from", "-", "--update")
	writeFixture(t, f.dir, localName, `{"targets":{"ios":{"settings":{"device":"agent-qa-test-nonexistent"}}}}`)
	r := f.doctor(1)
	if got := r.Targets[0].MissingTools; len(got) != 1 || got[0] != "agent-qa-test-nonexistent" {
		t.Fatalf("missing overridden tool: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "agent-qa")); !os.IsNotExist(err) {
		t.Fatalf("doctor created state: %v", err)
	}
}

func TestSharedWorktreeAndDependencyReuse(t *testing.T) {
	f := newFixture(t)
	f.config(config{Version: 1, Targets: map[string]target{
		"web": {Dependencies: &step{
			Command: []string{"sh", "-c", "mkdir -p .cache; echo setup >> .cache/calls; cat locks/nested/tool.lock > .cache/installed"},
			Inputs:  []string{"locks/**/*.lock"},
			Outputs: []string{".cache/installed"},
		}},
	}})
	writeFixture(t, f.dir, ".gitignore", ".cache/\n")
	writeFixture(t, f.dir, "locks/nested/tool.lock", "first")
	writeFixture(t, f.dir, "code", "one")
	first := f.commit()
	writeFixture(t, f.dir, "code", "two")
	second := f.commit()
	writeFixture(t, f.dir, "locks/nested/tool.lock", "second")
	third := f.commit()

	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "--detach", linked, first)
	other := fixture{t, linked, f.binary}
	tree := other.call(0, "up", "web")
	if got := other.call(0, "run", "web", "--ref", first, "--", "sh", "-c", "printf '%s ' \"$PWD\"; cat code"); got != tree+" one" {
		t.Fatalf("linked run got %q", got)
	}
	if got := f.call(0, "run", "web", "--ref", second, "--", "cat", "code"); got != "two" {
		t.Fatalf("second checkout got %q", got)
	}
	readCalls := func(want int) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(tree, ".cache", "calls"))
		if err != nil || string(b) != strings.Repeat("setup\n", want) {
			t.Fatalf("expected %d dependency runs, got %q (%v)", want, b, err)
		}
	}
	readCalls(1)
	f.call(0, "run", "web", "--ref", third, "--", "true")
	readCalls(2)
	if err := os.Remove(filepath.Join(tree, ".cache", "installed")); err != nil {
		t.Fatal(err)
	}
	f.call(0, "run", "web", "--ref", third, "--", "true")
	readCalls(3)
	f.call(42, "run", "web", "--ref", third, "--", "sh", "-c", "exit 42")
	if head, err := git(context.Background(), tree, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || head != "HEAD" {
		t.Fatalf("QA checkout must be detached: %q, %v", head, err)
	}
	if f.git("rev-parse", "HEAD") != third || other.git("rev-parse", "HEAD") != first {
		t.Fatal("calling checkout changed")
	}
	writeFixture(t, tree, "untracked", "keep me")
	other.call(1, "run", "web", "--ref", first, "--", "true")
	if head, err := git(context.Background(), tree, "rev-parse", "HEAD"); err != nil || head != third {
		t.Fatalf("dirty worktree was switched: %q, %v", head, err)
	}
}

func awaitFixtureFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("did not see %s", path)
}

func TestReservationAcrossWorktreesThroughInterruptCleanup(t *testing.T) {
	f := newFixture(t)
	f.config(config{Version: 1, Targets: map[string]target{"web": {}}})
	writeFixture(t, f.dir, "code", "first")
	first := f.commit()
	writeFixture(t, f.dir, "code", "second")
	second := f.commit()
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "--detach", linked, first)
	other := fixture{t, linked, f.binary}
	started, cleaned, entered := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "cleaned"), filepath.Join(t.TempDir(), "entered")
	hold := f.command("run", "web", "--ref", first, "--", "sh", "-c",
		`trap 'sleep 0.15; cat code > "$2"; exit 0' TERM; touch "$1"; while :; do sleep 1; done`, "sh", started, cleaned)
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Process.Signal(syscall.SIGTERM); _ = hold.Wait() })
	awaitFixtureFile(t, started)
	waiter := other.command("run", "web", "--ref", second, "--", "sh", "-c",
		`test "$(cat "$1")" = first && cat code > "$2"`, "sh", cleaned, entered)
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waiter.Process.Signal(syscall.SIGTERM); _ = waiter.Wait() })
	cancelWaiter := other.command("run", "web", "--ref", second, "--", "false")
	if err := cancelWaiter.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := cancelWaiter.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cancelWaiter.Wait(); exitStatus(err) != 130 {
		t.Fatalf("queued cancellation: %v", err)
	}
	if _, err := os.Stat(entered); !os.IsNotExist(err) {
		t.Fatalf("second command entered before reservation release: %v", err)
	}
	if err := hold.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := hold.Wait(); exitStatus(err) != 130 {
		t.Fatalf("interrupted run: %v", err)
	}
	if err := waiter.Wait(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(entered)
	if err != nil || string(got) != "second" {
		t.Fatalf("second checkout: %q, %v", got, err)
	}
}

func TestFixtureServer(t *testing.T) {
	args := os.Args
	if args[len(args)-1] != "agent-qa-fixture-server" {
		return
	}
	address, mode := args[len(args)-3], args[len(args)-2]
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		code, err := os.ReadFile("code")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%d %s", os.Getpid(), code)
	})
	if err := http.ListenAndServe(address, mux); err != nil {
		t.Fatal(err)
	}
}

func bindFixturePort(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func fixtureServer(t *testing.T, address, mode, timeout string) *serverConfig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &serverConfig{
		Command:      []string{self, "-test.run=^TestFixtureServer$", "--", address, mode, "agent-qa-fixture-server"},
		ReadinessURL: "http://" + address + "/ready",
		Timeout:      timeout,
	}
}

func TestServerLifecycle(t *testing.T) {
	f := newFixture(t)
	conflict := bindFixturePort(t)
	address := conflict.Addr().String()
	f.config(config{Version: 1, Targets: map[string]target{
		"native": {
			Build: &step{
				Command: []string{"sh", "-c", "mkdir -p .cache; echo build >> .cache/builds"},
				Inputs:  []string{"native.lock"},
				Outputs: []string{".cache/builds"},
			},
			Server: fixtureServer(t, address, "ready", "4s"),
		},
	}})
	writeFixture(t, f.dir, ".gitignore", ".cache/\n")
	writeFixture(t, f.dir, "code", "first")
	writeFixture(t, f.dir, "native.lock", "lock")
	first := f.commit()
	writeFixture(t, f.dir, "code", "second")
	second := f.commit()
	t.Cleanup(func() { f.call(0, "stop", "native") })

	if r := f.doctor(1).Targets[0].Server; !r.PortConflict || r.Owned {
		t.Fatalf("expected foreign port conflict: %+v", r)
	}
	f.call(1, "up", "native")
	f.call(1, "restart", "native")
	if !portOccupied(context.Background(), address) {
		t.Fatal("foreign listener was stopped")
	}
	conflict.Close()
	tree := f.call(0, "up", "native")
	readServer := func(want string) string {
		t.Helper()
		res, err := localHTTP.Get("http://" + address + "/ready")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		pid, content, ok := strings.Cut(string(body), " ")
		if err != nil || !ok || res.StatusCode != 200 || content != want {
			t.Fatalf("expected %q, got %s %q (%v)", want, res.Status, body, err)
		}
		return pid
	}
	initial := readServer("second")
	f.call(0, "restart", "native")
	restarted := readServer("second")
	if initial == restarted {
		t.Fatal("restart reused the existing process")
	}
	buildLog := filepath.Join(tree, ".cache", "builds")
	if _, err := os.Stat(buildLog); !os.IsNotExist(err) {
		t.Fatalf("up/restart ran native build: %v", err)
	}
	if r := f.doctor(0).Targets[0]; r.Ready || r.Build != "pending" || r.Server.Status != "ready" {
		t.Fatalf("expected pending build and a ready server: %+v", r)
	}
	f.call(0, "run", "native", "--ref", second, "--", "true")
	built := readServer("second")
	f.call(0, "run", "native", "--ref", second, "--", "true")
	if built != readServer("second") {
		t.Fatal("identical run replaced a warm server")
	}
	f.call(0, "run", "native", "--ref", first, "--", "true")
	if built == readServer("first") {
		t.Fatal("switched commit reused stale server")
	}
	f.call(0, "restart", "native")
	if b, err := os.ReadFile(buildLog); err != nil || string(b) != "build\n" {
		t.Fatalf("build ran more than once: %q (%v)", b, err)
	}
	if r := f.doctor(0).Targets[0]; !r.Ready || !r.Server.Owned || r.Head != first {
		t.Fatalf("expected ready target at first commit: %+v", r)
	}
	beforeConfigChange := readServer("first")
	cfgPath := filepath.Join(f.dir, configName)
	var cfg config
	if err := readJSON(cfgPath, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Targets["native"].Server.ReadinessURL += "?configured=2"
	f.config(cfg)
	if r := f.doctor(1).Targets[0]; r.Server.Status != "stale" {
		t.Fatalf("expected stale server after config changed: %+v", r)
	}
	f.call(0, "run", "native", "--ref", first, "--", "true")
	if beforeConfigChange == readServer("first") {
		t.Fatal("changed server definition reused the old server")
	}
	f.call(0, "stop", "native")
	if portOccupied(context.Background(), address) {
		t.Fatal("stopped server still occupies port")
	}
}

func TestInterruptedStartupIsCleanedUp(t *testing.T) {
	f := newFixture(t)
	listener := bindFixturePort(t)
	address := listener.Addr().String()
	listener.Close()
	f.config(config{Version: 1, Targets: map[string]target{
		"web": {Server: fixtureServer(t, address, "unready", "10s")},
	}})
	writeFixture(t, f.dir, "code", "first")
	head := f.commit()
	t.Cleanup(func() { f.call(0, "stop", "web") })
	run := f.command("run", "web", "--ref", head, "--", "false")
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Process.Signal(syscall.SIGTERM); _ = run.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for !portOccupied(context.Background(), address) {
		if time.Now().After(deadline) {
			t.Fatal("server did not begin listening")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := run.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(); exitStatus(err) != 143 {
		t.Fatalf("startup interruption: %v", err)
	}
	if portOccupied(context.Background(), address) {
		t.Fatal("interrupted startup left the server running")
	}
	if r := f.doctor(0).Targets[0]; r.Server.Owned || r.Server.Status != "stopped" {
		t.Fatalf("startup interruption left the server lease held: %+v", r)
	}
}

func TestUnreadyServerIsCleanedUp(t *testing.T) {
	f := newFixture(t)
	listener := bindFixturePort(t)
	address := listener.Addr().String()
	listener.Close()
	f.config(config{Version: 1, Targets: map[string]target{
		"web": {Server: fixtureServer(t, address, "unready", "300ms")},
	}})
	writeFixture(t, f.dir, "code", "first")
	head := f.commit()
	t.Cleanup(func() { f.call(0, "stop", "web") })
	f.call(1, "run", "web", "--ref", head, "--", "false")
	if portOccupied(context.Background(), address) {
		t.Fatal("readiness timeout left the server running")
	}
	if r := f.doctor(0).Targets[0]; r.Ready || r.Server.Owned || r.Server.Status != "stopped" {
		t.Fatalf("unexpected server state after failure: %+v", r)
	}
}
