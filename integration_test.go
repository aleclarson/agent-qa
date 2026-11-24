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

func (f fixture) up(name, ref string) string {
	f.t.Helper()
	output := f.call(0, "up", name, "--ref", ref)
	for _, line := range strings.Split(output, "\n") {
		if tree, ok := strings.CutPrefix(line, "Worktree: "); ok {
			return tree
		}
	}
	f.t.Fatalf("up did not report a worktree: %q", output)
	return ""
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
	initStdin(`{"version":1,"targets":{"all":{}}}`, 1, "--from", "-")
	initStdin(`{"version":1,"targets":{"web":{"server":{"command":["true"],"readiness_url":"http://localhost:4100/health","app_url":"/relative"}}}}`, 1, "--from", "-")
	initStdin(`{"version":1,"targets":{"web":{"build":{"command":["true"],"env":{"A=B":"bad"}}}}}`, 1, "--from", "-")
	initStdin(`{"version":1,"targets":{"web":{"build":{"command":["true"],"env":{"A":"${missing}"}}}}}`, 1, "--from", "-")
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
	other.call(1, "run", "web", "--", "true")
	other.call(1, "up", "web")
	tree := other.up("web", "HEAD")
	if got := f.call(0, "run", "web", "--", "sh", "-c", "printf '%s ' \"$PWD\"; cat code"); got != tree+" one" {
		t.Fatalf("linked run got %q", got)
	}
	other.call(1, "run", "web", "--ref", second, "--", "true")
	f.up("web", second)
	if got := other.call(0, "run", "web", "--", "cat", "code"); got != "two" {
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
	f.up("web", third)
	readCalls(2)
	if err := os.Remove(filepath.Join(tree, ".cache", "installed")); err != nil {
		t.Fatal(err)
	}
	f.call(42, "run", "web", "--", "sh", "-c", "exit 42")
	readCalls(2)
	f.up("web", third)
	readCalls(3)
	if head, err := git(context.Background(), tree, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || head != "HEAD" {
		t.Fatalf("QA checkout must be detached: %q, %v", head, err)
	}
	if f.git("rev-parse", "HEAD") != third || other.git("rev-parse", "HEAD") != first {
		t.Fatal("calling checkout changed")
	}
	writeFixture(t, tree, "untracked", "keep me")
	other.call(1, "up", "web", "--ref", first)
	if got := other.call(0, "run", "web", "--", "cat", "untracked"); got != "keep me" {
		t.Fatalf("run could not inspect dirty checkout: %q", got)
	}
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

func TestRunDoesNotHoldMutationLockAndCleansUpOnInterrupt(t *testing.T) {
	f := newFixture(t)
	f.config(config{Version: 1, Targets: map[string]target{"web": {}}})
	writeFixture(t, f.dir, "code", "first")
	first := f.commit()
	writeFixture(t, f.dir, "code", "second")
	second := f.commit()
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "--detach", linked, first)
	other := fixture{t, linked, f.binary}
	f.up("web", first)
	markers := t.TempDir()
	started, cleaned := filepath.Join(markers, "started"), filepath.Join(markers, "cleaned")
	hold := f.command("run", "web", "--", "sh", "-c",
		`trap 'sleep 0.15; touch "$2"; exit 0' TERM; touch "$1"; while :; do sleep 1; done`, "sh", started, cleaned)
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Process.Signal(syscall.SIGTERM); _ = hold.Wait() })
	awaitFixtureFile(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	up := exec.CommandContext(ctx, other.binary, "up", "web", "--ref", second)
	up.Dir = other.dir
	if output, err := up.CombinedOutput(); err != nil {
		t.Fatalf("run blocked checkout preparation: %v\n%s", err, output)
	}
	if got := other.call(0, "run", "web", "--", "cat", "code"); got != "second" {
		t.Fatalf("second checkout: %q", got)
	}
	if err := hold.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := hold.Wait(); exitStatus(err) != 130 {
		t.Fatalf("interrupted run: %v", err)
	}
	if _, err := os.Stat(cleaned); err != nil {
		t.Fatalf("interrupted command did not clean up: %v", err)
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
		if mode == "env" {
			fmt.Fprint(w, os.Getenv("AGENT_QA_FIXTURE_VALUE"))
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

func TestConfiguredCommandEnvironment(t *testing.T) {
	t.Setenv("AGENT_QA_FIXTURE_VALUE", "inherited")
	t.Setenv("AGENT_QA_FIXTURE_CLEAR", "inherited")
	f := newFixture(t)
	port := bindFixturePort(t)
	address := port.Addr().String()
	port.Close()
	server := fixtureServer(t, address, "env", "4s")
	server.Env = map[string]string{"AGENT_QA_FIXTURE_VALUE": "server ${value}"}
	f.config(config{Version: 1, Settings: map[string]string{"value": "default", "variant": "base"}, Targets: map[string]target{
		"web": {
			Dependencies: &step{
				Command: []string{"sh", "-c", `printf '%s\n' "$AGENT_QA_FIXTURE_VALUE" >> deps.out`},
				Env:     map[string]string{"AGENT_QA_FIXTURE_VALUE": "deps ${value}"},
				Outputs: []string{"deps.out"},
			},
			Build: &step{
				Command: []string{"sh", "-c", `printenv AGENT_QA_FIXTURE_CLEAR >/dev/null && printf '%s\n' "$AGENT_QA_FIXTURE_VALUE:$AGENT_QA_FIXTURE_CLEAR" >> build.out`},
				Env:     map[string]string{"AGENT_QA_FIXTURE_VALUE": "build ${value} ${variant}", "AGENT_QA_FIXTURE_CLEAR": ""},
				Outputs: []string{"build.out"},
			},
			Server: server,
		},
	}})
	writeFixture(t, f.dir, ".gitignore", "*.out\n"+localName+"\n")
	commit := f.commit()
	t.Cleanup(func() { f.call(0, "stop", "web") })
	tree := f.up("web", commit)
	f.up("web", commit)
	writeFixture(t, f.dir, localName, `{"targets":{"web":{"settings":{"value":"local"}}}}`)
	f.up("web", commit)
	writeFixture(t, f.dir, localName, `{"targets":{"web":{"settings":{"value":"local","variant":"changed"}}}}`)
	f.up("web", commit)
	for name, want := range map[string]string{
		"deps.out":  "deps default\ndeps local\n",
		"build.out": "build default base:\nbuild local base:\nbuild local changed:\n",
	} {
		got, err := os.ReadFile(filepath.Join(tree, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v, want %q", name, got, err, want)
		}
	}
	response, err := localHTTP.Get(server.ReadinessURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got, err := io.ReadAll(response.Body); err != nil || string(got) != "server local" {
		t.Fatalf("server response = %q, %v, want %q", got, err, "server local")
	}
	if got := f.call(0, "run", "web", "--", "printenv", "AGENT_QA_FIXTURE_VALUE"); got != "inherited" {
		t.Fatalf("run got %q, want inherited environment", got)
	}
}

func TestBulkServerOperationsAndListing(t *testing.T) {
	f := newFixture(t)
	servers := make(map[string]*serverConfig)
	var listeners []net.Listener
	for _, name := range []string{"alpha", "bravo"} {
		listener := bindFixturePort(t)
		servers[name] = fixtureServer(t, listener.Addr().String(), "ready", "4s")
		listeners = append(listeners, listener)
	}
	for _, listener := range listeners {
		listener.Close()
	}
	cfg := config{Version: 1, Targets: map[string]target{
		"alpha": {Server: servers["alpha"]},
		"bravo": {Server: servers["bravo"]},
		"shell": {},
	}}
	f.config(cfg)
	writeFixture(t, f.dir, "code", "first")
	head := f.commit()
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "--detach", linked, head)
	other := fixture{t, linked, f.binary}
	t.Cleanup(func() { f.call(0, "stop", "all") })

	list := func(caller fixture) serverList {
		t.Helper()
		var report serverList
		if err := json.Unmarshal([]byte(caller.call(0, "servers", "--json")), &report); err != nil {
			t.Fatal(err)
		}
		if report.PrimaryCheckout != f.dir || len(report.Servers) != 2 {
			t.Fatalf("unexpected server listing: %+v", report)
		}
		return report
	}
	stopped := list(other)
	for _, row := range stopped.Servers {
		if row.Status != "stopped" || !row.Configured || row.Owned {
			t.Fatalf("expected a configured, stopped server: %+v", row)
		}
	}
	if _, err := os.Stat(filepath.Join(stopped.GitCommonDir, "agent-qa")); !os.IsNotExist(err) {
		t.Fatalf("servers created state: %v", err)
	}
	if got := other.call(0, "up", "all", "--ref", "HEAD"); strings.Count(got, "Worktree: ") != 2 ||
		strings.Count(got, "Commit: "+head) != 2 {
		t.Fatalf("up all should print both worktrees: %q", got)
	}
	readBodies := func() []string {
		t.Helper()
		var bodies []string
		for _, name := range []string{"alpha", "bravo"} {
			res, err := localHTTP.Get(servers[name].ReadinessURL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("server %s: %s %q (%v)", name, res.Status, body, err)
			}
			bodies = append(bodies, string(body))
		}
		return bodies
	}
	before := readBodies()
	f.call(0, "restart", "all")
	for i, body := range readBodies() {
		if body == before[i] {
			t.Fatalf("server %d was not restarted: %q", i, body)
		}
	}
	if got := other.call(42, "run", "all", "--", "sh", "-c",
		`case "$PWD" in */alpha/worktree) echo alpha; exit 42;; */bravo/worktree) echo bravo; exit 19;; *) exit 99;; esac`); got != "alpha\nbravo" {
		t.Fatalf("run all did not visit both server targets in name order: %q", got)
	}

	delete(cfg.Targets, "alpha")
	f.config(cfg)
	r := list(f)
	if r.GitCommonDir != stopped.GitCommonDir || r.Servers[0].Target != "alpha" ||
		r.Servers[0].Configured || !r.Servers[0].Owned || r.Servers[0].Status != "running" ||
		r.Servers[1].Status != "ready" {
		t.Fatalf("expected the removed target's running server: %+v", r)
	}
	if out := other.call(0, "servers"); !strings.Contains(out, "Primary checkout: "+r.PrimaryCheckout) ||
		!strings.Contains(out, "alpha") || !strings.Contains(out, "bravo") {
		t.Fatalf("unexpected text listing: %q", out)
	}
	writeFixture(t, linked, configName, "malformed")
	if r := list(other); r.ConfigError == "" || r.Servers[0].Configured || r.Servers[1].Configured {
		t.Fatalf("expected live servers despite config error: %+v", r)
	}
	other.call(0, "stop", "all")
	other.call(0, "stop", "all")
	for _, s := range servers {
		address, err := readinessAddress(s.ReadinessURL)
		if err != nil || portOccupied(context.Background(), address) {
			t.Fatalf("stop all left server at %s (%v)", address, err)
		}
	}
}

func TestServerLifecycle(t *testing.T) {
	f := newFixture(t)
	conflict := bindFixturePort(t)
	address := conflict.Addr().String()
	f.config(config{Version: 1, Targets: map[string]target{
		"native": {
			Build: &step{
				Command: []string{"sh", "-c", "mkdir -p .cache; echo build >> .cache/builds; touch .cache/app"},
				Inputs:  []string{"native.lock"},
				Outputs: []string{".cache/app"},
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
	f.call(1, "up", "native", "--ref", second)
	f.call(1, "restart", "native")
	if !portOccupied(context.Background(), address) {
		t.Fatal("foreign listener was stopped")
	}
	conflict.Close()
	out := f.call(0, "up", "native", "--ref", second)
	if !strings.Contains(out, "Commit: "+second) || !strings.HasSuffix(out, "App URL: http://"+address) {
		t.Fatalf("expected commit and app origin in handoff: %q", out)
	}
	tree := f.up("native", second)
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
	buildLog := filepath.Join(tree, ".cache", "builds")
	appOutput := filepath.Join(tree, ".cache", "app")
	if b, err := os.ReadFile(buildLog); err != nil || string(b) != "build\n" {
		t.Fatalf("up should build once: %q (%v)", b, err)
	}
	if err := os.Remove(appOutput); err != nil {
		t.Fatal(err)
	}
	f.call(0, "restart", "native")
	restarted := readServer("second")
	if initial == restarted {
		t.Fatal("restart reused the existing process")
	}
	f.call(0, "run", "native", "--", "true")
	if r := f.doctor(0).Targets[0]; r.Ready || r.Build != "pending" || r.Server.Status != "ready" {
		t.Fatalf("restart and run should leave the build pending: %+v", r)
	}
	if _, err := os.Stat(appOutput); !os.IsNotExist(err) {
		t.Fatalf("restart or run prepared the native build: %v", err)
	}
	f.up("native", second)
	built := readServer("second")
	f.up("native", second)
	if built != readServer("second") {
		t.Fatal("identical up replaced a warm server")
	}
	f.up("native", first)
	if built == readServer("first") {
		t.Fatal("switched commit reused stale server")
	}
	f.call(0, "restart", "native")
	if b, err := os.ReadFile(buildLog); err != nil || string(b) != "build\nbuild\n" {
		t.Fatalf("build should run on initial setup and missing output: %q (%v)", b, err)
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
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings = map[string]string{"port": port}
	cfg.Targets["native"].Server.AppURL = "http://127.0.0.1:${port}/app"
	f.config(cfg)
	out = f.call(0, "up", "native", "--ref", first)
	if !strings.HasSuffix(out, "App URL: http://"+address+"/app") || beforeConfigChange != readServer("first") {
		t.Fatalf("expected updated handoff URL with warm server: %q", out)
	}
	cfg.Targets["native"].Server.ReadinessURL += "?configured=2"
	f.config(cfg)
	if r := f.doctor(1).Targets[0]; r.Server.Status != "stale" {
		t.Fatalf("expected stale server after config changed: %+v", r)
	}
	f.up("native", first)
	if beforeConfigChange == readServer("first") {
		t.Fatal("changed server definition reused the old server")
	}
	f.call(0, "stop", "native")
	if got := f.call(0, "run", "native", "--", "cat", "code"); got != "first" {
		t.Fatalf("run with stopped server got %q", got)
	}
	if portOccupied(context.Background(), address) {
		t.Fatal("run started the stopped server")
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
	up := f.command("up", "web", "--ref", head)
	if err := up.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = up.Process.Signal(syscall.SIGTERM); _ = up.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for !portOccupied(context.Background(), address) {
		if time.Now().After(deadline) {
			t.Fatal("server did not begin listening")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := up.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := up.Wait(); exitStatus(err) != 143 {
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
	f.call(1, "up", "web", "--ref", head)
	if portOccupied(context.Background(), address) {
		t.Fatal("readiness timeout left the server running")
	}
	if r := f.doctor(0).Targets[0]; r.Ready || r.Server.Owned || r.Server.Status != "stopped" {
		t.Fatalf("unexpected server state after failure: %+v", r)
	}
}
