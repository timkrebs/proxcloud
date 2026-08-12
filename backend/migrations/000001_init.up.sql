-- Migration 000001: initial multi-tenancy schema (Phase 1, schema only).
-- Enums are modelled with CHECK constraints (consistent across the schema);
-- UUID primary keys via gen_random_uuid() from pgcrypto.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Global identities. Access to tenants/projects is granted via memberships.
CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text NOT NULL,
    display_name      text NOT NULL DEFAULT '',
    password_hash     text,
    password_algo     text CHECK (password_algo IS NULL OR password_algo IN ('bcrypt', 'argon2id')),
    is_platform_admin boolean NOT NULL DEFAULT false,
    totp_enabled      boolean NOT NULL DEFAULT false,
    disabled          boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
-- Case-insensitive uniqueness on email.
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));

-- Tenant ≙ Azure Directory.
CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    slug       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Project ≙ Resource Group; mirrors a Proxmox pool via pool_id.
CREATE TABLE projects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL,
    slug       text NOT NULL,
    pool_id    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug),
    -- Target for the composite FK below so a project's tenant can never drift
    -- from an ownership row's tenant.
    UNIQUE (tenant_id, id)
);

-- Role grant at a tenant or project scope. Tenant roles inherit to projects;
-- a project role can only add. scope_id is polymorphic (tenant or project id).
CREATE TABLE memberships (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    scope_type text NOT NULL CHECK (scope_type IN ('tenant', 'project')),
    scope_id   uuid NOT NULL,
    role       text NOT NULL CHECK (role IN ('owner', 'contributor', 'reader')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, scope_type, scope_id)
);
CREATE INDEX memberships_user_id_idx ON memberships (user_id);
CREATE INDEX memberships_scope_idx ON memberships (scope_type, scope_id);

-- VMID -> project -> tenant ownership. vmid is globally unique (cluster-wide).
-- The composite FK (tenant_id, project_id) -> projects(tenant_id, id) makes it
-- impossible for a row's tenant_id to disagree with its project's tenant — the
-- DB enforces the isolation invariant the IDOR check relies on, so a write-path
-- bug can never silently mis-scope an ownership row.
CREATE TABLE resource_ownership (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    vmid       integer NOT NULL UNIQUE,
    guest_type text NOT NULL CHECK (guest_type IN ('qemu', 'lxc')),
    node       text NOT NULL,
    created_by uuid REFERENCES users (id),
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'tombstoned')),
    pve_upid   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
);
CREATE INDEX resource_ownership_project_id_idx ON resource_ownership (project_id);
CREATE INDEX resource_ownership_tenant_id_idx ON resource_ownership (tenant_id);
-- (vmid already has a unique btree from the UNIQUE constraint; no extra index.)

-- Per-scope limits; NULL column = unlimited for that dimension.
CREATE TABLE quotas (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_type  text NOT NULL CHECK (scope_type IN ('tenant', 'project')),
    scope_id    uuid NOT NULL,
    max_vcpu    integer,
    max_ram_mb  bigint,
    max_disk_gb bigint,
    max_count   integer,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scope_type, scope_id)
);

-- Server-side sessions: opaque token stored hashed; idle + absolute expiry.
CREATE TABLE sessions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash          text NOT NULL UNIQUE,
    user_id             uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    active_tenant_id    uuid REFERENCES tenants (id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    absolute_expires_at timestamptz NOT NULL,
    revoked_at          timestamptz,
    ip                  text,
    user_agent          text
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- Membership invitations: single-use token stored hashed; role bound in-row.
CREATE TABLE invitations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  text NOT NULL UNIQUE,
    email       text NOT NULL,
    scope_type  text NOT NULL CHECK (scope_type IN ('tenant', 'project')),
    scope_id    uuid NOT NULL,
    role        text NOT NULL CHECK (role IN ('owner', 'contributor', 'reader')),
    invited_by  uuid REFERENCES users (id),
    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- TOTP secret per user, encrypted at rest (AES-256-GCM via SECRETS_KEY).
CREATE TABLE totp_secrets (
    user_id          uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret_encrypted bytea NOT NULL,
    confirmed_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- One-time recovery codes; stored hashed, single-use via used_at.
CREATE TABLE recovery_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  text NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX recovery_codes_user_id_idx ON recovery_codes (user_id);

-- Append-only audit trail. The FK references are ON DELETE SET NULL so the
-- historical record outlives the entities it names — deleting an empty
-- tenant/project/user severs the link but never erases the trail (corrections
-- are new rows, never edits).
CREATE TABLE audit_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ts            timestamptz NOT NULL DEFAULT now(),
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    tenant_id     uuid REFERENCES tenants (id) ON DELETE SET NULL,
    project_id    uuid REFERENCES projects (id) ON DELETE SET NULL,
    action        text NOT NULL,
    target_type   text,
    target_id     text,
    outcome       text NOT NULL,
    ip            text,
    detail        jsonb
);
CREATE INDEX audit_log_tenant_ts_idx ON audit_log (tenant_id, ts DESC);

-- Static seed: the default tenant + default project (pool pc-default-default).
-- No user is seeded here — that requires env (ADMIN_USER/HASH) and is done in
-- Phase 2.
INSERT INTO tenants (name, slug) VALUES ('Default', 'default');
INSERT INTO projects (tenant_id, name, slug, pool_id)
SELECT id, 'Default', 'default', 'pc-default-default'
FROM tenants
WHERE slug = 'default';
