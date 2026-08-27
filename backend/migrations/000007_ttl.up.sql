-- Migration 000007: TTL / ephemeral resources (ADR-0020).
-- A guest may be made ephemeral: at expires_at it is stopped or destroyed. The
-- durable state lives here; warn + expire are scheduler `jobs`. Conventions
-- mirror 000001 (UUID PKs, CHECK enums, composite FK to projects).

CREATE TABLE ttls (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL,
    project_id        uuid NOT NULL,
    vmid              integer NOT NULL UNIQUE,   -- one TTL per guest
    expires_at        timestamptz NOT NULL,
    action            text NOT NULL CHECK (action IN ('stop', 'delete')),
    warned_24h        boolean NOT NULL DEFAULT false,
    warned_1h         boolean NOT NULL DEFAULT false,
    -- The TTL length as originally chosen, in SECONDS (bigint, not interval, for a
    -- clean int64<->time.Duration mapping in Go) — used to size an extend (extend
    -- adds one original_duration, capped at the project max_ttl).
    original_duration bigint NOT NULL,
    created_by        uuid REFERENCES users (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
);
CREATE INDEX ttls_project_idx ON ttls (project_id);
CREATE INDEX ttls_expires_idx ON ttls (expires_at);

-- Per-project TTL policy sidecar (kept off the hot projects row, mirroring
-- quotas). Locked policy: default none, max 30 days. No TTL (create or extend)
-- may exceed max_ttl.
-- Durations in SECONDS (bigint) for the same int64<->Duration reason as ttls.
CREATE TABLE project_ttl_policy (
    tenant_id   uuid NOT NULL,
    project_id  uuid NOT NULL,
    default_ttl bigint,                              -- NULL = no default (permanent unless opted in)
    max_ttl     bigint NOT NULL DEFAULT 2592000,     -- 30 days
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
);

-- The "expired" marker: a TTL stop-expiry marks the guest expired (distinct from
-- a user-stop and from an auto-shutdown stop). Reversible — cleared by a user
-- start. NULL = not expired.
ALTER TABLE resource_ownership ADD COLUMN expired_at timestamptz;
