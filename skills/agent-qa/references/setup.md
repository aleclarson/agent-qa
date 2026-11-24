Install on macOS or Linux with Go 1.22+ and Git 2.31+ if `agent-qa` is missing.

```sh
curl -fsSL https://raw.githubusercontent.com/aleclarson/agent-qa/main/install.sh | bash
export PATH="$HOME/.local/bin:$PATH"
```

The installer builds from `main`, installs to `~/.local/bin`, and adds PATH
for future Bash and Zsh sessions. Use the export for the current session.

Use existing project commands. Pass version 1 JSON to `agent-qa init --from -`.
If updating an existing config, preserve relevant entries and add `--update`.
This example assumes npm dependencies and a `./qa/serve-web` script that reads
`PORT`.

```sh
agent-qa init --from - <<'JSON'
{
  "version": 1,
  "targets": {
    "web": {
      "settings": { "port": "4100" },
      "dependencies": {
        "command": ["npm", "ci"],
        "inputs": ["package.json", "package-lock.json"],
        "outputs": ["node_modules"]
      },
      "server": {
        "command": ["./qa/serve-web"],
        "env": { "PORT": "${port}" },
        "readiness_url": "http://127.0.0.1:${port}/health",
        "timeout": "20s"
      }
    }
  }
}
JSON
```

- `dependencies`, `build`, and `server` are optional. An iOS or Android
  target can define `build` with the same fields as `dependencies` and a
  native build command that uses its device selector.
- Commands are argv arrays. Use an explicit shell for shell behavior.
  `${name}` expands string settings in command arguments and server URLs.
- Set an optional `env` object in `dependencies`, `build`, or `server` when
  configuring with `init --from -`. Use environment variable names as keys and
  strings as values. Values support `${name}` and override the inherited
  environment for that command and its children. Use the calling environment
  or an explicit path to locate the command's executable.
- Changing a resolved `env` value invalidates the affected prep cache or
  replaces the managed server on the next `up`.
- Prep inputs are repo-relative Git globs over tracked files. Include scripts,
  manifests, and lockfiles that affect the step. Omitting inputs keys the
  cache on the commit. Declare expected outputs and Git-ignore generated files.
- Use a dedicated loopback port and an HTTP(S) readiness endpoint with an
  explicit port. Readiness requires a 2xx response. The timeout defaults to
  `30s`. Keep server processes in the foreground and fail if they cannot bind.
- `up` reports the readiness URL's origin as the app URL by default. Set
  `server.app_url` for a different URL, such as an app served under `/app`.
- Config is read from the calling checkout. Commands run in the QA checkout.
  Required scripts must exist at the commit being tested.

Put machine-specific overrides in Git-ignored `.agent-qa.local.json`.

```json
{
  "targets": {
    "ios": { "settings": { "device": "my-test-device", "port": "4250" } }
  }
}
```

Settings precedence, low to high, is committed global, committed target,
local global, local target. Pass secrets through the environment.

`run` expands literal `${name}` placeholders after `--` with the effective
settings for the selected target. Use single quotes so the calling shell
passes the placeholder through. Expansion preserves each argument, including
spaces in setting values. Unmatched placeholders pass through for the invoked
shell or tool to interpret. `run all` uses each target's own settings.

```sh
agent-qa run ios -- maestro --device '${device}' test .maestro/ios-smoke.yaml
```

Set `device` to the iOS device ID accepted by the runner. A managed server can
start without a device setting when its setup and server config do not use it.

List configured targets with `agent-qa targets`. Acquire a harness lease
if available, prepare with `agent-qa up <target> --ref <sha>`, then smoke-check
with `agent-qa run <target> -- <runner>`. Report unavailable devices, tools,
or committed scripts if they block the check.
