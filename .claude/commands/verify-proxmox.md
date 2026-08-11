---
description: Verify real connectivity and privileges against the Proxmox server
---
Verify the backend can talk to the real Proxmox server using the configured env vars ($ARGUMENTS may contain overrides or a specific check to run).

Steps:
1. Load config from .env (never print PROXMOX_TOKEN_SECRET).
2. Using the backend's Proxmox client (or a small Go test program under backend/cmd/verify/), call in order: GET /version, GET /nodes, GET /nodes/{node}/status, GET /cluster/resources, GET /cluster/nextid, GET /nodes/{node}/storage.
3. Report for each call: HTTP status, latency, and a one-line summary of the payload (counts, versions — no secrets).
4. If any call fails with 401/403, delegate to the proxmox-specialist agent to determine the missing token privilege and output the exact pveum commands to fix it.
5. If TLS fails, report whether PROXMOX_TLS_INSECURE is set and what the certificate issue is.
6. End with a clear PASS/FAIL verdict on readiness for development against this server.
