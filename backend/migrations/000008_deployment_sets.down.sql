-- Reverse 000008: drop the 'set' schedule scope, the resource_ownership
-- membership columns, and the deployment_set table — restoring the exact
-- 000007 schema. Columns that FK-reference deployment_set are dropped before the
-- table itself, so the teardown never trips a foreign-key dependency.

DROP INDEX IF EXISTS schedules_set_uidx;
ALTER TABLE schedules DROP CONSTRAINT IF EXISTS schedules_scope_vmid;
ALTER TABLE schedules ADD CONSTRAINT schedules_scope_vmid CHECK (
    (scope = 'resource' AND vmid IS NOT NULL) OR
    (scope = 'project'  AND vmid IS NULL)
);
ALTER TABLE schedules DROP COLUMN IF EXISTS set_id;
ALTER TABLE schedules DROP CONSTRAINT IF EXISTS schedules_scope_check;
ALTER TABLE schedules ADD CONSTRAINT schedules_scope_check
    CHECK (scope IN ('resource', 'project'));

DROP INDEX IF EXISTS resource_ownership_set_idx;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS deployment_set_id;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS role;

DROP TABLE IF EXISTS deployment_set;
