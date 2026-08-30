# ADR-0028: Provisioning completion detection (provisioning → configuring → ready)

Date: 2026-08-28 · Status: accepted · Provisioning/Deploy engine

## Context

A bare-guest deployment finishes the moment its Proxmox tasks succeed: the deploy
engine runs `create` (→ optional `start`) and calls `finish(id, "succeeded")` as
soon as `awaitTask` reports the start UPID `OK` (`internal/deploy/engine.go:
146-177`). For a catalog service that is a lie. When the start task returns `OK`
the VM has merely powered on — cloud-init (ADR-0025) has not installed packages,
written files, or started the service. Declaring `ready` at power-on would tell the
user "your Postgres is up" while `apt` is still running, violating the honest
task-states iron rule (CLAUDE.md rule 5, "Honest task states").

We need a truthful signal that the service is actually usable. The hard constraint:
**there is no clean completion signal available.** Cloud-init exposes
`cloud-init status --wait`, but reaching it requires running a command inside the
guest, and `proxmox.Client` has **no guest-exec method** (`internal/proxmox/
client.go`) — and adding one means the QEMU guest agent must already be up, which is
itself a post-boot race. There is no PVE task that represents "cloud-init done."

What we *do* have: `AgentInterfaces` (`internal/proxmox/guest_config.go:98-164`),
which returns the guest's live IPs from the QEMU guest agent and honestly reports
`ErrAgentUnavailable` while the agent is not yet up. A guest that reports IPs has
booted far enough for the agent to run — a real, if coarse, "the OS is up" signal.
And ADR-0026 gives every service a `readiness` probe target (e.g. `tcp:5432`).

## Decision

**After the create/start task succeeds, the deploy engine runs a new `configuring`
step before declaring `ready`, and never declares `ready` at power-on.** The step
machine for a catalog deployment becomes:

```
create  → (start) → configuring → ready       (happy path)
                          └──────→ failed       (timeout / probe never passes)
```

The `configuring` step does two things, in order, within the existing
`stepTimeout` budget:

1. **Wait for boot.** Poll `AgentInterfaces` until the guest reports at least one
   non-loopback IP, treating `ErrAgentUnavailable` as "not yet" (retry), not as a
   failure — exactly the honest state the method already models. This confirms the
   OS booted and cloud-init's datasource has run.
2. **Probe readiness.** Once IPs are known, dial the service's `readiness` target
   from the catalog definition (ADR-0026) — e.g. `tcp:5432` against a reported IP —
   until it accepts a connection. Only then advance to `ready`.

Reaching the step timeout without the probe passing marks `configuring` **failed**
with an honest message ("guest booted but <service> did not become reachable on
:5432 within N minutes"), never a silent `ready`. The engine keeps the existing
`updateStep`/`finish` mechanics and the SSE `deployment` frame (`engine.go:225-268`)
— `configuring` is just another step the deployment page renders live.

### `cloud-init status --wait` is deferred, on purpose

The higher-fidelity signal — "cloud-init reported *done*", not merely "the port is
open" — needs guest-exec, which the client does not have. **We defer it as an
optional future fidelity upgrade**, recorded here so it is a known, deliberate
choice rather than an oversight: if a `GuestExec` capability is later added to
`proxmox.Client`, `configuring` can additionally gate on
`cloud-init status --wait` before the port probe. Until then, "OS booted (agent
IPs) + service port reachable" is the truthful readiness bar, and it is strictly
more honest than today's power-on `ready`.

### New response fields on `Deployment`

To carry a service's usable coordinates to the post-provision success view, the
`Deployment` type (`backend/api/types/create.go:82-91`) gains:

- `Connection` — the resolved reachable address (host/IP + primary port) the user
  connects to, populated from the probed `AgentInterfaces` IP + `readiness` port.
- `Ports` — the service's exposed ports (from the catalog `ports`, ADR-0026), for
  display and firewall hints.
- `CredentialHint` — a **non-secret** pointer to how to authenticate (e.g. "user:
  admin, password: shown once at creation" or "SSH key you selected"). It never
  carries the secret value — generated secrets are surfaced once through the create
  response, per the secrets-server-side iron rule.

`DeploymentStep` gains no new fields; `configuring` reuses the existing
`Key/Label/Status/Message` shape, with `Key: "configuring"`.

## Consequences

- A catalog service reports `ready` only when it is actually reachable, so the
  success view and its connection details are truthful — no "up" badge over a
  still-installing guest.
- Deployments take visibly longer to reach `ready` (they now wait for boot + a
  service probe, not just a PVE task), which is the honest cost of a real
  readiness signal and matches how the design already streams per-step progress.
- The engine gains a dependency on the guest agent being present for qemu catalog
  guests — consistent with ADR-0025 seeding qemu services (whose cloud-init installs
  `qemu-guest-agent`, per the prod template precedent
  `deploy/terraform/templates/cloud-init.yaml.tftpl`). A guest whose agent never
  comes up fails `configuring` honestly rather than hanging past `stepTimeout`.
- The completion bar is "port reachable," not "cloud-init done": a service that
  opens its port before finishing first-boot config could be called `ready` a beat
  early. Accepted for v1; the deferred `cloud-init status --wait` upgrade closes
  the gap when guest-exec exists.
- The new `Connection`/`Ports`/`CredentialHint` fields are additive and
  `omitempty`, so the existing bare-guest deploy path (which sets none of them) is
  unchanged and stays green.

## Alternatives considered

- **Declare `ready` at power-on (status quo).** Rejected: it reports a service as
  usable while cloud-init is still running — a fabricated success state,
  incompatible with the honest-task-states iron rule.
- **Guest-exec `cloud-init status --wait` as the primary signal.** The most
  faithful option, rejected for v1 only because `proxmox.Client` has no exec method
  and adding one still races the agent's startup. Kept explicitly as the future
  upgrade path, not discarded.
- **A fixed post-start sleep before `ready`.** Rejected: a timer is a guess — too
  short lies, too long wastes the user's time — and it is exactly the fabricated
  signal the iron rules forbid. An actual boot + port probe is real evidence.
- **HTTP/application-level health check instead of a TCP probe.** Rejected as the
  v1 default: it needs per-service health-path knowledge and TLS handling that a
  `tcp:PORT` probe from the catalog def does not; a service may add a richer
  `readiness` form later, but TCP reachability is the honest common denominator.

See ADR-0025 (the cloud-init the guest is running during `configuring`) and
ADR-0026 (the `readiness` and `ports` fields this step consumes).
