-- Reverse 000007: drop TTL state, the project policy, and the expired marker.
DROP TABLE IF EXISTS ttls;
DROP TABLE IF EXISTS project_ttl_policy;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS expired_at;
