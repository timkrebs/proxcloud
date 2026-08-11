---
description: Full feature workflow — architect plans, engineers build, QA and reviewers gate
---
Build the following Proxcloud feature end to end: $ARGUMENTS

Workflow — follow strictly, in order:
1. Delegate to the **architect** agent to produce the implementation plan, API contract, and any ADR. Do not write code before the plan exists.
2. If the plan touches Proxmox API details that aren't already documented in docs/proxmox/, delegate the open questions to the **proxmox-specialist** agent first.
3. Delegate backend tasks to **backend-engineer** and frontend tasks to **frontend-engineer**, following the plan's task order. Backend contract before frontend consumption.
4. Delegate to **qa-engineer** to test the result against the Definition of done in CLAUDE.md.
5. Delegate to **code-reviewer**; if the feature touches auth, config, the Proxmox client, the console proxy, or new endpoints, also delegate to **security-reviewer**.
6. Fix all blocking findings by sending them back to the responsible engineer agent, then re-run the gate that failed.
7. Only when QA passes and reviews are clean: summarize what was built, files changed, and how it was verified.
