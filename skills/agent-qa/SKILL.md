---
name: agent-qa
description: Set up agent-qa or verify a running GUI app at the intended Git revision. Use for browser, mobile, or desktop smoke tests, reproducing UI bugs, and checking user flows or visual regressions in a live app.
---

Run from the project checkout. Use `targets` to pick a target and read the project's QA instructions for the runner and device selector. Use environment credentials. Read the [setup reference](references/setup.md) when installing agent-qa or configuring a project.

Configured dependency, build, and server commands can override inherited variables with an `env` map. Values can reference settings with `${name}`. Changes to resolved values invalidate prep or replace the server on the next `up`.

Commands (prefix `agent-qa`):

- `--help` / `help` / `-h` — syntax.
- `init [--from -] [--update]` — interactive by default, validated JSON on stdin with `--from -`. Replacing config requires `--update`. Derive setup from project commands when in scope.
- `targets` — list every target in the calling checkout's config after local overrides, including targets without a managed server. Shows configured prep steps, app URLs, and the canonical Git common directory for leases.
- `doctor` — JSON-only diagnostics for config, tools, worktree, prep, port conflicts, and readiness. For agent checks, use `servers` and the target log.
- `up <target|all> --ref <sha>` — create or switch a clean QA worktree to the resolved commit, prepare dependencies/build, wait for server readiness, print commit, worktree, and app URL.
- `run <target|all> -- <command> [args...]` — execute in the existing QA worktree and preserve exit status. After `--`, literal `${name}` expands using each target's effective settings, preserving argument boundaries and unmatched placeholders. Single-quote placeholders in the calling shell. Use after `up` for scripted QA. For interactive QA, pass the app URL or device ID to the GUI tool directly.
- `restart <target|all>` — restart or start the owned server at the existing QA checkout. Wait for readiness. Skips prep and builds.
- `stop <target|all>` — stop owned servers, including removed targets.
- `servers` — list configured servers and owned servers without config, grouped under the repository's primary checkout. Includes addresses, status, and logs. A stopped server is normal before setup.

`all` selects configured server targets for `up`, `run`, and `restart`, and owned servers for `stop`. Bulk commands use name order and return the first failure after attempting the rest. `up all` resolves one commit. `run all` uses each existing worktree's current revision.

Lease each shared QA target through the harness before checkout or server changes. Key on the canonical Git common directory and target name across linked worktrees. Keep the lease through testing and cleanup. Reacquire it and call `up --ref` on later turns. Coordinate shared devices through the harness as needed. Servers stay warm after the turn.

For Maestro web flows, use the reported app URL and run `agent-qa run web -- maestro test .maestro/web-smoke.yaml`. For Maestro MCP, supply the app URL or native device ID. Verify browser state between MCP calls. Report the tested SHA, target, and outcomes.
