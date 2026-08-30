# ADR-0026: Service-catalog definition format & loader

Date: 2026-08-28 · Status: accepted · Service catalog

## Context

ADR-0025 gives the backend a way to *deliver* a rendered cloud-init snippet. This
ADR decides where the thing being rendered — the catalog service itself — lives,
what it declares, and how it enters the process. A service is a triple:
presentation metadata (what the "Create a resource → from catalog" tile shows),
the cloud-init template to render (ADR-0025), and human-readable next-steps to
show after `ready` (ADR-0028). We need a format that a single engineer can review
in a PR, that fails loudly when malformed, and that does not require a database
migration to add a service.

The existing deploy path is stateless with respect to definitions — a
`CreateGuestRequest` fully describes a bare guest. The catalog adds a layer of
curated, versioned definitions above it. The open question this ADR closes is
whether those definitions are **data** (rows in Postgres, editable at runtime) or
**code** (files in the repo, shipped with the binary).

## Decision

### Definitions are code, embedded at build time

Catalog services are repo files under `catalog/services/<id>/`, three files each:

- `service.yaml` — metadata + sizing + inputs (schema below).
- `cloud-init.yaml.tftpl` — the user-data template ADR-0025 renders and uploads.
- `next-steps.md.tftpl` — post-`ready` guidance rendered with the live
  connection details (ADR-0028).

A new `backend/internal/catalog` package loads them via **`//go:embed`**, so the
definitions ship inside the binary — no runtime filesystem or DB dependency, and
the running image and its catalog can never drift. The loader **validates every
definition at startup and fails fast** (the process refuses to boot on a malformed
or duplicate-`id` definition), mirroring the fail-fast config validation in
`config.go`. Adding or changing a service is a reviewed, versioned edit to a
snippet — not a runtime write to a golden template. We explicitly accept a slower
first boot (cloud-init installs at provision time) in exchange for definitions that
are diffable, testable, and rollback-safe with the code.

### `service.yaml` schema

```yaml
id:            string   # stable slug, [a-z0-9-], dir name; PK, must be unique
displayName:   string   # tile title
description:   string   # one-line tile subtitle
icon:          string   # design-system icon key (no hardcoded asset)
category:      string   # grouping for the catalog gallery (e.g. "Databases")
kind:          qemu      # single | set (v1: single); guest family (v1: qemu only, ADR-0025)
baseImage:                # image the guest is created from
  ref:         string     #   storage-scoped volid or download ref
sizing:
  default: { cores: int, memoryMb: int, diskGb: int }
  min:     { cores: int, memoryMb: int, diskGb: int }   # wizard floor
credentials:              # inputs the service accepts and injects via the snippet
  - name:        string   #   e.g. "admin"
    userSettable: bool    #   user may override the value in the wizard
    generatedDefault: bool #  backend generates a strong default when not set
ports:         [int]      # default service ports (informational + firewall hints)
readiness:     string     # completion probe, e.g. "tcp:5432" (consumed by ADR-0028)
docs:          string     # upstream documentation URL
testedOn:      date       # YYYY-MM-DD this def was last verified on a real PVE
```

`kind: single` provisions one guest; `kind: set` is reserved for future
multi-guest services and is not implemented in v1. `credentials` is the contract
ADR-0025's renderer consumes: `userSettable` decides whether the wizard exposes the
field; `generatedDefault` decides whether the backend mints a secret when the user
leaves it blank. `readiness` is the single source of the completion probe target so
the state machine (ADR-0028) never hardcodes a port.

### Scope: seeded services are global (platform)

Definitions carry no tenant. Seeded catalog entries are **global/platform** — the
same curated set is offered to every tenant, consistent with definitions being
code shipped in the image. Tenant-scoped catalog entries (a tenant publishing its
own service) are a possible future layer that would live in Postgres, keyed by
`tenant_id`; the embedded loader is the platform tier and is unaffected by it. v1
ships only the global tier.

### Provenance columns: deferred, not added now

We considered adding nullable `catalog_service` / `catalog_service_version`
columns to `resource_ownership` (the guest ownership row,
`migrations/000001_init.up.sql:66-79`) in a new migration **000008**, so the
resource-detail blade could later say "this VM is a Postgres from catalog vX".

**Decision: defer. Do not add the columns in Phase A.** Phase A carries a
service's connection details on the **in-memory `Deployment`** (ADR-0028's new
`Connection`/`Ports`/`CredentialHint` fields), which is where the create wizard and
the just-provisioned success view read them from — the guest and the PVE task log
remain the durable truth, exactly as the deploy engine already treats deployments
(`engine.go:40-41`). Provenance on `resource_ownership` only earns its migration
once the **resource blade** (a separate, later view) needs to render "created from
catalog" for a guest long after its deployment fell out of memory. Adding nullable
columns now would be a schema change with no reader — so we add the 000008
migration **if and when** the blade needs it, not speculatively.

## Consequences

- A service is reviewed like code: a PR diff shows exactly what cloud-init a new
  catalog entry will run, and a bad definition fails CI/startup rather than
  half-provisioning a guest.
- No migration and no runtime write are needed to add, edit, or remove a service —
  the catalog evolves at the speed of a commit, and the shipped image always
  matches its embedded catalog (no drift between binary and definitions).
- First boot is slower than a golden-image clone (packages install at provision
  time), accepted deliberately for definition transparency and easy updates —
  editing a snippet, never re-baking an image.
- Because Phase A keeps provenance in memory, a resource that was created from the
  catalog will **not** be labeled as such on a cold resource blade after its
  `Deployment` is gone. That is the known, accepted gap the deferred 000008
  migration closes later; it is called out here so the future blade work knows the
  trigger.
- `testedOn` makes staleness visible: a definition not verified in a long time is
  an at-a-glance signal, not a surprise at provision time.

## Alternatives considered

- **Definitions as Postgres rows, editable at runtime.** Rejected for v1: it makes
  cloud-init that runs on real infrastructure runtime-mutable with no PR review,
  splits the source of truth between DB and code, and demands a migration + admin
  UI before the first service ships. Code-with-`//go:embed` gives review, version
  control, and rollback for free. The DB tier remains available *later*, only for
  the tenant-scoped catalog.
- **Golden images (bake each service into a template, clone at create).** Faster
  first boot, but every update means re-baking and re-uploading an image, images
  are opaque to review, and it multiplies storage. Snippet-rendered cloud-init
  keeps the definition legible and the update path a one-line diff; the slower
  boot is the accepted cost.
- **A single monolithic `catalog.yaml`** instead of a directory per service.
  Rejected: it couples unrelated services in one file, makes the template + docs
  awkward to colocate, and turns every change into a merge-conflict magnet. One
  directory per `id` keeps a service self-contained.
- **Adding the `resource_ownership` provenance columns now (migration 000008).**
  Rejected for Phase A: a schema change with no current reader. Deferred until the
  resource blade needs cold-path provenance, at which point the nullable columns
  are cheap and backfill-free.

See ADR-0025 (how the rendered template is delivered) and ADR-0028 (how `readiness`
and the connection fields drive completion detection).
