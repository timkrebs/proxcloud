-- Migration 000004: interim login challenges for the TOTP second factor.
--
-- Password-success-with-TOTP must NOT be a full session before the second factor
-- succeeds (ADR-0013 §3). Instead it mints a stored, hashed, single-use challenge
-- carried in a separate proxcloud_totp cookie: the challenge grants nothing except
-- the right to attempt step two for its bound user_id. The row counts failures so
-- a per-account lockout works without in-process state (multi-instance safe). Only
-- the SHA-256 token_hash is stored, mirroring sessions/invitations — a DB leak
-- never yields a usable challenge token.
--
-- The three feature tables (invitations, totp_secrets, recovery_codes) were
-- pre-created in migration 000001 and are used as-is; only login_challenges is new.
CREATE TABLE login_challenges (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  text NOT NULL UNIQUE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    attempts    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    ip          text,
    user_agent  text
);
CREATE INDEX login_challenges_user_id_idx ON login_challenges (user_id);
