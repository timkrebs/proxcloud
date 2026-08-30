# ADR-0030: K3s cluster sequencing & pre-shared-token join strategy

Date: 2026-08-28 · Status: accepted · Provisioning/Service catalog

## Context

K3s is the first `kind: set` service (ADR-0029): one catalog action provisions a
small Kubernetes cluster — a control-plane node plus worker nodes — as one
deployment set. A K3s cluster is not just "N of the same guest": the workers must
**join** the control plane, and a join needs two facts the workers cannot know at
provision time under our constraints:

1. **The server URL** — `https://<server-ip>:6443`. The server's IP is normally
   learned only after it boots and the guest agent reports it (`AgentInterfaces`,
   the same signal `configure` waits on, `engine.go:373`). Agents provisioned in
   parallel would have no address to point at.
2. **The join token.** In the default K3s flow the server *generates* a node
   token at first boot and writes it to `/var/lib/rancher/k3s/server/node-token`
   on the control-plane node. A worker must read that file off the server to
   join. Reading a file out of a running guest requires **executing a command
   inside it**, and `proxmox.Client` has **no guest-exec method** — a constraint
   ADR-0025 and ADR-0028 both hit and record ("`proxmox.Client` has no exec
   method", ADR-0028 Context; "no guest-exec method", ADR-0025 §Alternatives).
   There is no API path by which the backend captures a server-generated token
   after boot. Post-boot token capture is **infeasible** with the current client
   seam, not merely inconvenient.

The whole point of the set abstraction is that all members are cloud-init-ed up
front and provisioned together (ADR-0029). A join strategy that depends on
serially booting the server, reaching into it to capture a secret, then rendering
the agents defeats that and rides on a capability we do not have.

## Decision

**The backend removes the join dependency by generating the cluster's shared
secret and fixing the server's address up front, then injecting both into every
member's cloud-init before any guest boots.** Concretely:

### Roles and topology (v1)

- **1 server** (K3s control plane) + **N agents** (workers). Default **N = 2**,
  adjustable within the tenant/project quota the `ReserveOwnershipBatch` gate
  enforces (ADR-0029).
- **Single server, no HA, in v1.** One control-plane node, embedded SQLite
  datastore. Multi-server HA (embedded etcd, an odd server count, a fronting VIP)
  is explicitly out of scope now — but the set abstraction must **not preclude**
  it: `role` on the member row (ADR-0029) already distinguishes `server` from
  `agent`, so a future HA build adds server members and an etcd-init/`--server`
  join flow without changing the set model.

### Join strategy: pre-shared token + static server IP (the de-risk)

Before any Proxmox call, the backend:

1. **Generates `K3S_TOKEN`** with `crypto/rand` — the same primitive the deploy
   engine already uses for IDs (`engine.go:5,166`) and ADR-0027 uses for
   generated passwords. K3s accepts an operator-supplied cluster token via
   `--token`; when set on the server, it is the token agents present, so we never
   need the server's auto-generated node-token file.
2. **Assigns the server a static IP** up front, so the server URL is known before
   the server boots. This uses the existing static-IP path — the request carries
   `IPConfig.Mode == "static"` and `params.go` renders
   `ipconfig0 = ip=<CIDR>[,gw=<gw>]` (`params.go:296-301`); DHCP is not usable
   here because the agents' cloud-init must embed the server address at render
   time.

Both facts are injected into **every** member's cloud-init snippet (ADR-0025):

- **Server member** — cloud-init installs k3s in server mode with
  `--token <K3S_TOKEN>`, `--node-external-ip <static-ip>` (and `--node-ip`), and
  `--tls-san <static-ip>` so the API server's certificate is valid for the
  address agents dial.
- **Agent members** — cloud-init installs k3s agent with
  `K3S_URL=https://<static-ip>:6443` and the same `K3S_TOKEN`.

This eliminates the join-token capture step entirely: there is nothing to read
back out of the server, so the missing guest-exec capability is not on the
critical path.

### Sequencing (and reverse teardown)

- **Server first.** Provision the server member; its `configuring` step
  (ADR-0028) waits for a routable IP + the readiness probe (`tcp:6443`, the K3s
  API) before the server is considered up.
- **Agents next.** Once the server's API answers, provision the agents; each
  `K3S_URL` already points at the static server IP with the shared token, so the
  join is immediate on agent boot.
- **Teardown is the reverse** (ADR-0029 teardown order): agents before the
  server, so workers deregister before the control plane they depend on
  disappears — a `deployment_set` `deleting` walk in reverse `role`/member order.

The set reaches **`ready`** only when the server and all agents are up (each
member's `configuring` passed); a member coming up short flips the set to
`degraded`/`failed` per ADR-0029's member-failure policy — no fabricated
cluster-ready.

### Credential / secret handling

`K3S_TOKEN` is a generated secret and is handled exactly like ADR-0027's
generated passwords:

- Injected **only** via the ADR-0027 **base64 transport** into each member's
  snippet (base64 blob → `printf … | base64 -d` in a `runcmd`), never
  interpolated raw into YAML or a shell line.
- **Never stored, logged, or returned.** It is not written to the DB, not placed
  in an audit `detail` (the audit records only the non-secret fact that a set was
  created, tenancy rule 3), and not echoed in any response or SSE frame. Unlike
  the single-service one-time password reveal (ADR-0027 §2), the cluster token is
  an internal join secret with no operator-facing use, so it is not even
  surfaced once — it exists solely inside the rendered snippets.
- **The kubeconfig is the operator's credential, and Proxcloud does not hold
  it.** The rendered **next-steps** (ADR-0027 §4 pattern) tells the operator how
  to fetch it — `sudo cat /etc/rancher/k3s/k3s.yaml` on the server, replacing the
  `127.0.0.1` server address with the static IP — and how to reach the API
  (`https://<static-ip>:6443`). Proxcloud surfaces **instructions**, never the
  kubeconfig contents: the same "model the service's real security lifecycle
  honestly, stay out of the trust path" stance ADR-0027 §4 takes for Vault's
  unseal keys. `CredentialHint` points at the next-steps kubeconfig step, not a
  secret value.

### Runtime privilege / networking assumptions (recorded, not hidden)

- **Static IP requires the static path and a free address.** The server member
  goes down `IPConfig.Mode == "static"` (`params.go:296`); the operator (or the
  wizard) must supply a CIDR + gateway that is actually free on the LAN. A
  collision or a wrong gateway fails the server's `configuring` honestly
  (no routable IP / `:6443` unreachable), never a silent bad cluster.
- **Agents must reach the server on the LAN.** `K3S_URL=https://<static-ip>:6443`
  assumes the members share an L2/L3 segment with no firewall blocking 6443
  (and 8472/UDP for the default flannel VXLAN between nodes). This is the homelab
  single-subnet assumption the rest of Proxcloud already makes; cross-subnet or
  segmented-network K3s is out of scope for v1.

## Consequences

- The cluster join works with the client we have: no guest-exec, no post-boot
  secret capture, no serialized boot-then-render-then-boot dance. The server and
  agents are all fully cloud-init-ed up front and provisioned as one set
  (ADR-0029), which is the abstraction's whole premise.
- The server's address is fixed at request time, so it must be a real free LAN
  address; DHCP is not an option for a joinable cluster. This pushes an IP-mgmt
  responsibility onto the operator/wizard that a single DHCP guest does not have —
  recorded as the cost of a static, joinable control plane.
- `K3S_TOKEN` never leaving the rendered snippets means there is no cluster-token
  store to breach and no way to "look it up" later — consistent with ADR-0027's
  no-stored-secrets rule. Re-keying a compromised token is a re-provision or an
  in-guest `k3s` operation, not a Proxcloud lookup.
- v1 is single-server (no HA): a lost control plane is a lost cluster. The `role`
  column and set model leave the HA door open (add server members + an
  etcd/`--server` join), but that is a later ADR, not this one.
- Readiness is honest per member (ADR-0028): the set is `ready` only when the API
  and every agent are actually up, so "cluster ready" is not declared over a
  still-joining worker.

## Alternatives considered

- **Capture the server-generated node-token via guest-exec after the server
  boots**, then render the agents. Rejected / deferred: `proxmox.Client` has no
  exec method (ADR-0025, ADR-0028), and adding one still races the guest agent's
  startup — so this is infeasible with the current seam, not just slower. It also
  serializes the whole set (server must fully boot before agents can even be
  rendered), defeating the up-front cloud-init model. If a `GuestExec` capability
  is ever added (the same future upgrade ADR-0028 parks), token capture becomes
  possible, but the pre-shared token is strictly simpler and is kept regardless.
- **DHCP for the server + discover its IP post-boot** and inject into agents
  afterwards. Rejected: it reintroduces the exact post-boot, reach-into-the-guest
  ordering the pre-shared design removes, and needs the agents rendered after the
  server is up — the same serialization and capture problem, minus the token.
- **A fronting load-balancer / VIP for the API address** so the server IP need
  not be static. Rejected for v1: it adds a component (keepalived/kube-vip) and
  network assumptions disproportionate to a single-server homelab cluster; a
  static IP + `--tls-san` is the boring, proven minimum. Revisit with HA.
- **Multi-server HA in v1** (embedded etcd, odd server count). Rejected as scope:
  more moving parts (etcd quorum, cluster-init vs join ordering) than a homelab
  first cut needs. The set model does not preclude it (the `role` column already
  exists), so it is a clean later addition rather than a rewrite.

See ADR-0029 (the deployment-set model — member roles, atomic batch quota,
teardown ordering, and durable set status this cluster rides on), ADR-0025 (the
per-member snippet delivery carrying these cloud-init directives), ADR-0027 (the
base64 transport and no-stored-secrets / next-steps pattern the `K3S_TOKEN` and
kubeconfig follow), and ADR-0028 (the per-member `configuring` readiness probe
against `tcp:6443`).
