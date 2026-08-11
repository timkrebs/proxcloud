---
description: Run the full review gate on the current diff
---
Run the full Proxcloud review gate on the current uncommitted/branch diff ($ARGUMENTS may scope it).

1. Delegate to **qa-engineer** for the Definition-of-done check and test run.
2. Delegate to **code-reviewer** for the quality review.
3. If the diff touches auth, config, the Proxmox client wrapper, the console proxy, or adds/changes API endpoints, delegate to **security-reviewer**.
4. Consolidate all findings into one list ordered by severity, with the responsible agent for each fix.
5. Give a single final verdict: READY TO COMMIT or BLOCKED (with the blocking items).
