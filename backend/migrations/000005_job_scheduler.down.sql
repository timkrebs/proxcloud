-- Reverse 000005: drop the job store and the audit actor_system column.
DROP TABLE IF EXISTS jobs;
ALTER TABLE audit_log DROP COLUMN IF EXISTS actor_system;
