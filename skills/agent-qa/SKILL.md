---
name: agent-qa
description: Verify a running GUI app at the intended Git revision with agent-qa. Use for browser, mobile, or desktop smoke tests, reproducing UI bugs, and checking user flows or visual regressions in a live app.
---

# GUI QA with agent-qa

Use `agent-qa` to hold the target's reservation for the full GUI check. Use the
project's browser or device runner to drive the app.

1. In the project checkout, read the repo's QA instructions and `.agent-qa.json`.
   Pick the configured target and use the intended commit SHA. If the changes
   are uncommitted, establish which committed revision can be tested or resolve
   how to put the changes in a testable commit before claiming coverage.
2. Run `agent-qa doctor --json` to inspect configuration, tools, worktree state,
   and port conflicts. An unused target can have a stopped server or missing
   QA worktree. If setup is in scope, derive the config from the project's
   existing commands and use `agent-qa init --from -`. See the
   [agent-qa README](https://github.com/aleclarson/agent-qa) for installation
   and configuration examples.
3. Run the full test and cleanup inside the reservation.

   ```sh
   agent-qa run "$target" --ref "$sha" -- ./qa/check-flow
   ```

   Replace `./qa/check-flow` with the actual runner. `run` checks out the
   revision, prepares the target, waits for the managed server, then invokes
   the runner. If GUI actions use a separate tool, start a controllable runner
   that signals when it has entered the reservation and stays active until
   those actions and cleanup finish. Return the GUI result as the runner's
   exit status. `up` prepares the target and starts the server, then releases
   the reservation. Use `run` for the interaction itself.
4. Use the target's effective URL and device settings. Follow the project's
   device reservation rules if other targets can share the same hardware.
   Use only GUI tools and evidence collection authorized for the task. Leave
   a dirty QA worktree for inspection and report setup or readiness failures
   alongside any checks that could run.

Report the tested commit, target, flows exercised, and observed results.
