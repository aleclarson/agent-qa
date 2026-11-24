# Documentation

Give each document the detail its reader needs. Link to the other documents
when a topic belongs there.

Use plain-text CLI output in agent workflows. Omit `--json` invocations from
agent-facing instructions and examples. Project configuration uses JSON.

## README.md

- Write for people discovering or using agent-qa. Open with a concrete
  problem and what agent-qa makes possible. Give readers a useful command
  early.
- Show what a reader can ask their agent to do. Introduce capabilities one
  concept at a time. Define unfamiliar terms where readers first need them.
  Use a short analogy when it makes a concept easier to grasp.
- Offer a copyable prompt to hand project setup to an agent. Keep installation
  and configuration procedures in the skill's setup reference.
- Make the page easy to skim. Use descriptive headings, short paragraphs,
  and frequent command examples. Assume readers may stop at any point.
- Keep source builds, test instructions, and other contributor or maintainer
  workflows in `CONTRIBUTING.md`.

## skills/agent-qa/SKILL.md

- Write a compact reference for agents. Omit the H1, narrative, and tutorial
  framing. Keep the frontmatter trigger focused on live GUI QA.
- Cover every public command and useful flag. Capture behavior that affects
  agent decisions, including harness leases, cleanup, server ownership,
  configuration, and bulk operations. Update the skill when the CLI changes.

## CONTRIBUTING.md

- Write for people changing or maintaining this repository. Keep source
  builds, validation, and maintainer workflows here, with exact commands and
  any prerequisites contributors need.
