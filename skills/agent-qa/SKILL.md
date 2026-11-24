---
name: agent-qa
description: Set up agent-qa or verify a running GUI app at the intended Git revision. Use for browser, mobile, or desktop smoke tests, reproducing UI bugs, and checking user flows or visual regressions in a live app.
---

Run from the project checkout. Read its QA instructions, `.agent-qa.json` (v1, argv arrays), and machine overrides in `.agent-qa.local.json`. Use effective ports/device selectors and environment credentials. Read the [setup reference](references/setup.md) when installing agent-qa or configuring a project.

Commands (prefix `agent-qa`):

- `--help` / `help` / `-h` — syntax.
- `init [--from -] [--update]` — interactive by default, validated JSON on stdin with `--from -`. Replacing config requires `--update`. Derive setup from project commands when in scope.
- `doctor` — JSON-only diagnostics for config, tools, worktree, prep, port conflicts, and readiness. For agent checks, use `servers` and the target log.
- `up <target|all>` — initialize missing worktree at caller HEAD, otherwise retain its commit. Prepare dependencies, ensure server readiness, print worktree path, release reservation.
- `run <target|all> --ref <sha> -- <command> [args...]` — reserve target, require clean worktree, detach at commit, prepare dependencies/build, ensure server readiness, run in QA worktree, preserve runner status. Keep test and cleanup inside the reservation. For external GUI tools, hold a runner with a ready/result handshake through cleanup.
- `restart <target|all>` — restart or start the owned server at the existing QA checkout. Wait for readiness. Skips prep and builds.
- `stop <target|all>` — stop owned servers, including removed targets.
- `servers` — list configured servers and owned servers without config, grouped under the repository's primary checkout. Includes addresses, status, and logs. A stopped server is normal before setup.

`all` is reserved. It selects configured server targets for `up`, `run`, and `restart`, and all owned servers in the repository for `stop`. Bulk operations reserve targets sequentially in name order, continue after target failures, and return the first failure. `run all` uses one resolved commit and the same argv throughout.

Linked worktrees share server state and reservations. Coordinate shared physical devices separately. Prep caches key on command and tracked input globs (full commit by default) and require declared outputs. Servers stay warm across compatible runs. Restart after server environment changes. Preserve dirty QA worktrees for inspection. Report the tested SHA, target, and outcomes. Coverage applies to committed code.
