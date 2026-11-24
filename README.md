# agent-qa

You ask your agent to open your app and verify a fix. The fix is on another
branch, your editor is still open, and a second agent might need the same
server.

`agent-qa` keeps a separate QA checkout at the commit you choose. It gets
the app ready and lets the agent run the test while you keep editing.

```sh
agent-qa targets
```

In a configured project, this lists each named setup, called a **target**,
along with its preparation steps and the app URL for any managed server.

## Ask your agent to set up the project

Copy this prompt into an agent working in your project.

```text
Set up this project for GUI QA with agent-qa.

Read skills/agent-qa/SKILL.md in https://github.com/aleclarson/agent-qa and follow its setup reference. Use this project's existing commands. Configure targets that can run on this machine, preserve existing QA settings, and keep machine-specific values local and credentials in the environment.

Check the configured targets with agent-qa targets. Run one smoke check if there is a suitable commit and an available runner. Tell me which targets are ready, what you verified, and what still needs my input.
```

## Verify a fix in the running app

Ask your agent to reproduce a bug, check a user flow, or run a browser smoke
test at a specific commit. `up` gets that version running and prints the
app URL. Your agent can open it with a browser tool or run a saved Maestro
flow against it.

```sh
sha=$(git rev-parse HEAD)
agent-qa up web --ref "$sha"
agent-qa run web -- maestro test .maestro/web-smoke.yaml
```

Each target, such as `web` or `ios`, gets one shared QA checkout, called
a **worktree**. If several agents use it, their harness can lease the
target for a turn, like booking a test device. The lease covers preparation,
testing, and cleanup.

## Test an app on a phone or emulator

A native target can install dependencies and build the app when needed. Your
agent can then explore the installed app with Maestro MCP or run a saved
flow on the selected device.

```sh
agent-qa up ios --ref "$sha"
agent-qa run ios -- maestro --device '${device}' test .maestro/ios-smoke.yaml
```

Set `device` to your iOS device ID in `.agent-qa.local.json`. The single
quotes pass `${device}` to `agent-qa`, which substitutes the target's setting
as one argument. Placeholders without a matching setting pass through unchanged.
See the [setup reference](skills/agent-qa/references/setup.md) for an example.

## Keep the app open for another look

Your agent can start the app so you can open it yourself, then leave the
server running for the next check.

```sh
agent-qa up web --ref "$sha"
agent-qa servers
```

Testing a different commit or changing the server config replaces the
server. Your agent can also restart it directly, for example after an
environment change. A server restart skips native builds.

```sh
agent-qa restart web
agent-qa stop web
```

## Check what is running

Ask your agent which servers are up, or why a target could not start. The
server list shows status, addresses, and log paths. It includes running
servers whose target was removed from config.

```sh
agent-qa servers
```

## Run the same check across targets

If your project has one smoke test that works across targets, your agent
can run it against each configured server target at the same commit.
It can also start, restart, or stop servers as a group.

```sh
agent-qa up all --ref "$sha"
agent-qa run all -- ./qa/check-application
agent-qa stop all
```

The group covers the repository and its linked worktrees. When using leases,
the harness holds each affected target. Separate clones have their own servers.
`run all` expands settings separately for each target.

[Agent skill](skills/agent-qa/SKILL.md) · [Contributing](CONTRIBUTING.md)
