-- Migration 000008: deployment sets (ADR-0029/0030).
-- A deployment set is ONE catalog action that provisions N linked guests sharing
-- a lifecycle (the K3s cluster is the seed set). Membership is a nullable FK on
-- resource_ownership, NOT a parallel table, so every existing ownership behaviour
-- (quota accounting, tombstone revive, TTL expired_at, the stale-pending
-- reconciler) keeps working UNCHANGED on a member row. Conventions mirror
-- 000001/000005-000007: UUID PKs via gen_random_uuid(), CHECK-constraint enums,
-- and the composite FK (tenant_id, project_id) -> projects so a set's tenant can
-- never drift from its project's tenant.

CREATE TABLE deployment_set (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL,
    project_id  uuid NOT NULL,
    service_id  text NOT NULL,                 -- e.g. 'k3s-cluster'
    status      text NOT NULL DEFAULT 'provisioning'
                  CHECK (status IN ('provisioning', 'ready', 'degraded', 'failed', 'deleting')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
);
CREATE INDEX deployment_set_tenant_idx ON deployment_set (tenant_id);
CREATE INDEX deployment_set_project_idx ON deployment_set (project_id);

-- Membership: a member is an ownership row that ALSO names a set + its role
-- (ADR-0029: 'server' | 'agent'). Both columns are nullable so every non-set
-- guest row is unchanged. ON DELETE SET NULL lets a set row be removed after its
-- members are torn down/tombstoned without tripping the FK.
ALTER TABLE resource_ownership
    ADD COLUMN deployment_set_id uuid REFERENCES deployment_set (id) ON DELETE SET NULL,
    ADD COLUMN role text;
CREATE INDEX resource_ownership_set_idx ON resource_ownership (deployment_set_id)
    WHERE deployment_set_id IS NOT NULL;

-- Schedules gain a 'set' scope (ADR-0029): one set-scope schedule fans out to the
-- set's member VMIDs, mirroring the project-scope fan-out. The scope enum, the
-- nullable set_id column, and the scope/vmid consistency CHECK are all extended,
-- and a partial unique index mirrors schedules_resource_uidx / schedules_project_uidx.
ALTER TABLE schedules DROP CONSTRAINT schedules_scope_check;
ALTER TABLE schedules ADD CONSTRAINT schedules_scope_check
    CHECK (scope IN ('resource', 'project', 'set'));
ALTER TABLE schedules ADD COLUMN set_id uuid REFERENCES deployment_set (id) ON DELETE CASCADE;
ALTER TABLE schedules DROP CONSTRAINT schedules_scope_vmid;
ALTER TABLE schedules ADD CONSTRAINT schedules_scope_vmid CHECK (
    (scope = 'resource' AND vmid IS NOT NULL AND set_id IS NULL) OR
    (scope = 'project'  AND vmid IS NULL     AND set_id IS NULL) OR
    (scope = 'set'      AND vmid IS NULL     AND set_id IS NOT NULL)
);
CREATE UNIQUE INDEX schedules_set_uidx ON schedules (tenant_id, set_id) WHERE scope = 'set';
