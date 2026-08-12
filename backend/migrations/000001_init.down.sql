-- Reverse of 000001_init.up.sql. Drop in dependency-safe order.
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS recovery_codes;
DROP TABLE IF EXISTS totp_secrets;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS quotas;
DROP TABLE IF EXISTS resource_ownership;
DROP TABLE IF EXISTS memberships;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS tenants;
DROP TABLE IF EXISTS users;
-- pgcrypto is intentionally left installed: dropping a shared extension could
-- break unrelated objects, and re-creating it is idempotent.
