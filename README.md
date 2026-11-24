# agent-qa

You ask your agent to open your app and verify a fix. The fix is on another
branch, your editor is still open, and a second agent might need the same
server.

`agent-qa` keeps a separate QA checkout at the commit you choose. It gets
the app ready and lets the agent run the test while you keep editing.

## Ask your agent to set up the project

Copy this prompt into an agent working in your project.

```text
Set up this project for GUI QA with agent-qa.

Read skills/agent-qa/SKILL.md in https://github.com/aleclarson/agent-qa
and follow its setup reference. Use this project's existing commands.
Configure targets that can run on this machine, preserve existing QA
settings, and keep machine-specific values local and credentials in the
environment.

Validate the setup with agent-qa doctor --json. Run one smoke check if
there is a suitable commit and an available runner. Tell me which targets
are ready, what you verified, and what still needs my input.
```

## Verify a fix in the running app

Ask your agent to reproduce a bug, check a user flow, or run a browser smoke
test at a specific commit. It can run your project's own test command.

```sh
sha=$(git rev-parse HEAD)
agent-qa run web --ref "$sha" -- ./qa/check-browser
```

A **target** is a named setup, such as `web` or `ios`. Each target gets a
spare Git checkout called a **worktree**. A reservation works like booking
a test device. The next check on that target waits until the current test
and its cleanup finish.

## Test an app on a phone or emulator

A native target can install dependencies, build the app when needed, and
run your project's device tests. Your agent can use the device settings for
this machine and keep them out of Git.

```sh
agent-qa run ios --ref "$sha" -- ./qa/check-ios
```

## Keep the app open for another look

Your agent can start the app so you can open it yourself, then leave the
server running for the next check.

```sh
agent-qa up web
agent-qa servers
```

`up` releases the reservation after the server is ready. A check that needs
exclusive access uses `run`.

Testing a different commit or changing the server config replaces the
server. Your agent can also restart it directly, for example after an
environment change. A server restart skips native builds.

```sh
agent-qa restart web
agent-qa stop web
```

## Check what is running

Ask your agent which servers are up, or why a target could not start. The
server list includes running servers whose target was removed from config.
The diagnostic report points to missing tools, port conflicts, the QA
checkout, and the server log.

```sh
agent-qa servers --json
agent-qa doctor --json
```

## Run the same check across targets

If your project has one smoke test that works across targets, your agent
can run it against each configured server target at the same commit.
It can also start, restart, or stop servers as a group.

```sh
agent-qa run all --ref "$sha" -- ./qa/check-application
agent-qa stop all
```

The group covers the repository and its linked worktrees. Separate clones
have their own servers.

[Agent skill](skills/agent-qa/SKILL.md) · [Contributing](CONTRIBUTING.md)
