-- Reverse 000006: drop schedules and the scheduler-stop marker.
DROP TABLE IF EXISTS schedules;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS auto_stopped;
