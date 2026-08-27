-- Migration 000006: auto-shutdown schedules (ADR-0019).
-- Structured schedule definitions (never user-entered cron); the scheduler
-- projects each into `jobs` rows. Conventions mirror 000001: UUID PKs, CHECK
-- enums, composite FK (tenant_id, project_id) -> projects(tenant_id, id).

CREATE TABLE schedules (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- resource scope targets one guest (vmid set); project scope applies to all
    -- guests in the project (vmid NULL) unless a resource row overrides/opts out.
    scope           text NOT NULL CHECK (scope IN ('resource', 'project')),
    tenant_id       uuid NOT NULL,
    project_id      uuid NOT NULL,
    vmid            integer,
    -- Structured fields (local to `timezone`); cron is derived internally.
    shutdown_time   text NOT NULL,          -- "HH:MM" 24h
    auto_start_time text,                    -- optional "HH:MM" power-on
    days_of_week    integer[] NOT NULL,      -- 0..6 (Sun..Sat)
    timezone        text NOT NULL,           -- IANA name, validated against tzdata
    grace_seconds   integer NOT NULL DEFAULT 120,
    enabled         boolean NOT NULL DEFAULT true,
    -- opt_out on a resource row means "exempt this guest from the project schedule".
    opt_out         boolean NOT NULL DEFAULT false,
    created_by      uuid REFERENCES users (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    CONSTRAINT schedules_scope_vmid CHECK (
        (scope = 'resource' AND vmid IS NOT NULL) OR
        (scope = 'project'  AND vmid IS NULL)
    )
);
-- One schedule per guest (resource scope) and one per project (project scope).
CREATE UNIQUE INDEX schedules_resource_uidx ON schedules (tenant_id, vmid) WHERE scope = 'resource';
CREATE UNIQUE INDEX schedules_project_uidx  ON schedules (tenant_id, project_id) WHERE scope = 'project';
CREATE INDEX schedules_project_idx ON schedules (project_id);

-- Distinguish a scheduler-initiated stop from a user stop (ADR-0019): the paired
-- auto-start job only powers a guest back on if the scheduler stopped it, and the
-- UI renders "stopped by schedule" distinctly. Set by autoshutdown.stop, cleared
-- by autoshutdown.start and by any user-initiated start.
ALTER TABLE resource_ownership ADD COLUMN auto_stopped boolean NOT NULL DEFAULT false;
