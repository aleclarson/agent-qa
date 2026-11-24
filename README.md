# agent-qa

You want an agent to open your app and verify a fix. Your editor is on one
branch, the fix is on another, and a second agent might need the same server.

`agent-qa` runs the check at the commit you choose. It checks out the code,
prepares the app, and waits for the server. Your working checkout stays free
for editing.

## Run your first check

Install on macOS or Linux with Go 1.22+ and Git 2.31+.

```sh
curl -fsSL https://raw.githubusercontent.com/aleclarson/agent-qa/main/install.sh | bash
```

The installer puts the binary in `~/.local/bin` and sets PATH for future Bash
and Zsh sessions. Open a new terminal or run `export PATH="$HOME/.local/bin:$PATH"`
in your current shell.

If your app repo already has a `web` target and a browser test script, run
this from that repo. [Add a target](#add-a-target) if you're setting it up.

```sh
sha=$(git rev-parse HEAD)
agent-qa run web --ref "$sha" -- ./qa/check-browser --url http://127.0.0.1:4100
```

A **target** is a named setup for part of your app, such as `web` or `ios`.
Each target gets a spare Git checkout called a **worktree**. Like booking a
test device, reserving a target gives one caller exclusive use while others
wait. Keep the whole check and its cleanup after `--`. The runner's exit
status is the result of `run`.

## Add a target

Start with the command that launches your app and a URL that responds when
it is ready. This example assumes your project provides `./qa/serve-web`,
a script that stays in the foreground and serves `/health` on the given port.

```sh
agent-qa init --from - <<'JSON'
{
  "version": 1,
  "targets": {
    "web": {
      "settings": { "port": "4100" },
      "server": {
        "command": ["./qa/serve-web", "--port", "${port}"],
        "readiness_url": "http://127.0.0.1:${port}/health",
        "timeout": "20s"
      }
    }
  }
}
JSON
```

Run `agent-qa init` to answer prompts instead. Use `--update` to replace an
existing config. Commit `.agent-qa.json` and the scripts your target needs.
`agent-qa` reads config from your current checkout and runs the scripts from
the QA checkout.

Each command is a JSON array with one argument per element. Settings are
strings, and `${name}` works in commands and readiness URLs. Use a dedicated
loopback port and have the server script fail if it cannot bind. A target
with a self-contained test runner can omit the server.

## Open the app before running a check

Use `up` to get the app ready for a quick look. On first use it prints the
path to a worktree at your current commit. Later calls reuse the worktree's
current commit. The reservation ends when `up` returns, so use `run` to hold
it through a check.

```sh
agent-qa up web
agent-qa servers
```

The server stays warm after `up` or `run`. Testing a different commit or
changing the server config replaces it. Restart directly to pick up new
environment variables. Use `stop` when you're done with the server.

```sh
agent-qa restart web
agent-qa stop web
```

If setup fails, run `agent-qa doctor --json` for missing tools, port conflicts,
and the paths to the QA checkout and server log. If a check leaves that
checkout dirty, inspect its changes before retrying.

## Prepare dependencies and native builds

Add `dependencies` to a target if it needs an install step. For example,
add this to `web` if your project uses npm.

```json
"dependencies": {
  "command": ["npm", "ci"],
  "inputs": ["package.json", "package-lock.json"],
  "outputs": ["node_modules"]
}
```

List the tracked files that affect setup. Use Git globs such as
`packages/**/package.json` for groups of files. Preparation reruns when the
command or inputs change or an expected output is missing. With no input
globs, the commit controls reuse. Keep generated outputs Git-ignored.

For an iOS or Android target, add a `build` step with the same fields and
give it your project's native build command. `run` performs the build when
needed. `up` prepares dependencies and starts the server. `restart` starts
the server using the existing QA checkout without running either step.

## Use a different port or device

Put machine-specific settings in `.agent-qa.local.json` and add that file
to `.gitignore`. For example, this sets a port and device for an `ios` target
whose commands use `${port}` and `${device}`.

```json
{
  "targets": {
    "ios": { "settings": { "device": "my-test-device", "port": "4250" } }
  }
}
```

Local settings override committed settings. In each file, target settings
override global settings. Pass secrets through the environment. If different
targets share a physical device, reserve it through your project's device
tools.

```sh
agent-qa run ios --ref "$sha" -- ./qa/check-ios --device my-test-device --port 4250
```

## Manage several servers

```sh
agent-qa servers --json
agent-qa up all
agent-qa run all --ref "$sha" -- ./qa/check-application
agent-qa restart all
agent-qa stop all
```

The server list groups linked worktrees under the primary checkout. Each
clone has its own group. The list includes configured targets and running
servers whose config was removed.

`all` selects configured server targets. `run all` uses the same command and
commit for each target, so use a runner that works across your targets.
Bulk commands visit targets in name order and return the first failure
after trying the rest. `stop all` also stops owned servers with missing or
invalid config.

[Agent skill](skills/agent-qa/SKILL.md) · [Contributing](CONTRIBUTING.md)
