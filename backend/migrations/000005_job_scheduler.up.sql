-- Migration 000005: job scheduler foundation (ADR-0018).
-- A persistent, tenant-aware job store claimed with SELECT … FOR UPDATE SKIP
-- LOCKED so a second backend instance never double-fires. Schema conventions
-- mirror 000001: UUID PKs via gen_random_uuid(), CHECK-constraint enums, jsonb
-- payload/detail, the composite FK (tenant_id, project_id) -> projects so a job's
-- tenant can never drift from its project's tenant, and partial indexes on status.

CREATE TABLE jobs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- recurring jobs re-fire on a cron; one_shot jobs run once at run_at.
    kind          text NOT NULL CHECK (kind IN ('recurring', 'one_shot')),
    -- dispatch key -> a registered handler func (autoshutdown.stop/start/warn,
    -- ttl.expire/warn). Free text: an unknown handler dead-letters, never panics.
    handler       text NOT NULL,
    -- Owner: the resource (and its project/tenant) whose lifecycle this job acts
    -- on. Nullable for future non-resource jobs; resource jobs always set all
    -- three. The composite FK below is MATCH SIMPLE, so a NULL owner is allowed.
    tenant_id     uuid,
    project_id    uuid,
    vmid          integer,
    payload       jsonb,
    -- Recurring-only: the internally-derived cron spec + its IANA timezone
    -- (never user-entered; ADR-0019 derives it from structured schedule fields).
    cron          text,
    timezone      text,
    -- Next fire time. one_shot uses it directly; recurring recomputes it from
    -- cron+timezone after each run.
    run_at        timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'scheduled'
                    CHECK (status IN ('scheduled', 'running', 'failed', 'succeeded', 'cancelled')),
    attempts      integer NOT NULL DEFAULT 0,
    max_attempts  integer NOT NULL DEFAULT 5,
    last_error    text,
    -- The claim: set atomically with status='running' inside the SKIP LOCKED tx.
    -- A crash mid-handler leaves a stale (running, locked_at) row that the
    -- lock-expiry sweep re-claims, giving at-least-once delivery.
    locked_at     timestamptz,
    locked_by     text,
    -- What to do when run_at is already in the past at claim time (ADR-0018):
    -- catch_up = run once now + reschedule (missed auto-shutdown); run_late =
    -- still execute (missed TTL expiry / late warning); skip = abandon the miss.
    missed_policy text NOT NULL DEFAULT 'catch_up'
                    CHECK (missed_policy IN ('catch_up', 'skip', 'run_late')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    -- Owner columns are all-or-nothing: a resource job sets tenant+project+vmid,
    -- a non-resource job sets none. This forbids a partially-owned row (e.g. vmid
    -- set but tenant_id NULL) that would slip past the composite FK's MATCH SIMPLE
    -- semantics (a NULL in the FK tuple disables the check).
    CONSTRAINT jobs_owner_all_or_none CHECK (
        (tenant_id IS NULL) = (project_id IS NULL)
        AND (project_id IS NULL) = (vmid IS NULL)
    )
);

-- The claim hot path: due, still-schedulable jobs, oldest first.
CREATE INDEX jobs_due_idx ON jobs (run_at) WHERE status = 'scheduled';
-- Owner-cancel (guest deleted -> cancel its jobs) and the admin jobs view.
CREATE INDEX jobs_vmid_idx ON jobs (vmid);
CREATE INDEX jobs_tenant_status_idx ON jobs (tenant_id, status);
-- The stale-running reclaim sweep: rows stuck 'running' past the lock-expiry.
CREATE INDEX jobs_running_locked_idx ON jobs (locked_at) WHERE status = 'running';

-- Audit-as-system (ADR-0018): actor_user_id is a UUID FK to users and cannot
-- hold "system:scheduler". Scheduler mutations write actor_user_id = NULL and
-- set actor_system so the activity log renders the actor honestly; the owning
-- schedule_id/ttl_id/job_id ride in the existing detail jsonb.
ALTER TABLE audit_log ADD COLUMN actor_system text;
