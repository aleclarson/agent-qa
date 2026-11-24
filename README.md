# agent-qa

Run an exact Git revision in a reusable, isolated QA checkout. Each repository
and target has one persistent worktree and one exclusive reservation shared
across the repository's linked worktrees. The calling checkout stays untouched.

## Install

On macOS or Linux with Go 1.22+ and Git 2.31+, run:

```sh
curl -fsSL https://raw.githubusercontent.com/aleclarson/agent-qa/main/install.sh | bash
```

The installer clones `main`, builds with Go, and installs to
`~/.local/bin/agent-qa`. It adds that directory to PATH for future Bash or Zsh
sessions. Other shells can add it through their own startup config. To use
the binary in your current Bash or Zsh session, run:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

From an existing checkout, build directly with:

```sh
go build -o agent-qa .
```

Run the installed CLI from inside a consuming Git
repository. Use project-owned scripts and a test runner of your choice.

## Configure a project

Run `agent-qa init` for an interactive setup, or feed a complete JSON document
to `agent-qa init --from -`. Add `--update` to replace an existing config.
Commit the resulting `.agent-qa.json` with the project.

This example assumes the repository provides the scripts under `qa/`. The
dependency scripts install into ignored paths, the native build script prepares
an app for the selected device, and both server scripts stay in the foreground
and serve a local readiness endpoint.

```json
{
  "version": 1,
  "targets": {
    "web": {
      "settings": { "port": "4100" },
      "dependencies": {
        "command": ["./qa/install-web"],
        "inputs": ["package.json", "package-lock.json", "packages/**/package.json", "qa/install-web"],
        "outputs": ["node_modules"]
      },
      "server": {
        "command": ["./qa/serve-web", "--port", "${port}"],
        "readiness_url": "http://127.0.0.1:${port}/health",
        "timeout": "20s"
      }
    },
    "ios": {
      "settings": { "port": "4200", "device": "booted" },
      "dependencies": {
        "command": ["./qa/install-ios"],
        "inputs": ["ios/Podfile", "ios/Podfile.lock", "qa/install-ios"],
        "outputs": ["ios/Pods"]
      },
      "build": {
        "command": ["./qa/build-ios", "--device", "${device}"],
        "inputs": ["ios/**", "qa/build-ios"],
        "outputs": [".qa-build/app"]
      },
      "server": {
        "command": ["./qa/serve-native", "--port", "${port}"],
        "readiness_url": "http://127.0.0.1:${port}/status",
        "timeout": "45s"
      }
    }
  }
}
```

An Android target can declare its own installer, build, device selector, and
server. Targets can omit any preparation step or the managed server. Keep
`.agent-qa.local.json` ignored in the consuming repository and use it for
machine-specific values. For example:

```json
{
  "targets": {
    "ios": { "settings": { "device": "my-test-device", "port": "4250" } }
  }
}
```

Settings are strings. From lowest to highest precedence, the layers are
committed global, committed target, local global, and local target.
`${name}` is substituted inside configured command arguments and readiness URLs.
Commands are argv arrays, with shell behavior available by explicitly invoking
a shell. Put credentials in the inherited environment and have project scripts
read them at runtime. The CLI reads config and overrides from the **calling**
checkout, then runs project commands from the **QA** checkout. Restart a warm
server to pick up changes to its inherited environment.

## Run browser and native QA

The bundled [agent skill](skills/agent-qa/SKILL.md) guides coding agents through
GUI checks that hold the target reservation for the full test.

```sh
sha=$(git rev-parse HEAD)
agent-qa doctor --json
agent-qa up web
agent-qa run web --ref "$sha" -- ./qa/check-browser --url http://127.0.0.1:4100
agent-qa run ios --ref "$sha" -- ./qa/check-ios --device my-test-device --port 4250
agent-qa restart ios
agent-qa stop web
agent-qa stop ios
```

- `doctor --json` inspects config, configured executables, worktree and
  preparation state, and server health. It exits nonzero for problems such as
  missing tools, a dirty worktree, an unhealthy owned server, or a port
  conflict. Missing worktrees and stopped servers are expected before setup.
- `up` initializes the worktree at the calling checkout's HEAD on first use.
  Later calls keep the QA worktree's current revision. It prepares dependencies,
  ensures server readiness, and prints the QA worktree path.
- `run` waits for exclusive access, resolves the provided commit in the
  calling repository, and uses a detached checkout. The runner executes only
  after preparation and server readiness, and its exit status is returned.
  Place the whole QA script, including its cleanup, after `--`.
- `restart` uses the current QA checkout and replaces the owned server, or
  starts it if it is stopped. It runs neither preparation step. `stop` shuts
  down an owned server and works even if its config has been removed.

### Manage all servers in a repository

```sh
agent-qa servers
agent-qa servers --json
agent-qa up all
agent-qa run all --ref "$sha" -- ./qa/check-application
agent-qa restart all
agent-qa stop all
```

`servers` labels the repository by its primary checkout. Linked worktrees
share the same server state through Git's common directory. The list shows
managed servers configured in the invoking checkout, including stopped servers,
as well as owned servers whose configuration is unavailable or was removed.

`all` is a reserved target name. For `up`, `run`, and `restart`, it selects
the invoking checkout's targets that declare managed servers. `run all` sends
the same command and resolved commit to each selected target. `stop all` uses
shared repository state to find owned servers, including those with missing or
invalid config. Bulk commands work in target name order, take one reservation
at a time, and continue after target failures. They return the first failing
status. An interruption ends the bulk operation after the current target's
cleanup.

### Preparation and server lifecycle

Preparation runs on first use, when its command or declared inputs change,
or when a declared output is missing. Input patterns use Git's root-relative
glob pathspecs, such as `**/package.json`, and match tracked files. With no input
patterns, the cache key includes the full commit. List the scripts and files
that affect each step. Declare output paths to detect missing local artifacts,
and ignore generated files so the QA worktree stays clean.

A server stays warm while the commit, expanded server configuration, and
preparation state are current. A change of commit or server configuration
replaces it. Preparation that needs to run also stops the warm server first.
Use a dedicated loopback TCP port per managed server. Readiness requires an
HTTP 2xx response and a responsive supervisor. The supervisor checks for an
occupied port before launch. Project server scripts should fail if they cannot
bind the port and should keep their children in the foreground process group.

The QA worktree and server log paths are in the doctor report. State lives
under `agent-qa/` in Git's common directory. A dirty QA worktree is left in
place for inspection. Preserve or move its changes before retrying. Separate
clones have independent reservations. Device selection, device reservations
across targets or clones, and pre-merge policy belong to the consuming
repository. A host resource broker such as Goddard can wrap the QA command
when a device also needs exclusive access.

## Verify locally

```sh
go test ./...
```

Integration tests create temporary Git repositories, linked worktrees, and
loopback HTTP servers. They require no browser or native device.
