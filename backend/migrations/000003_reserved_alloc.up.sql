-- Migration 000003: reserved allocation columns on resource_ownership.
--
-- A pending create has no PVE guest yet, so /cluster/resources returns nothing
-- for its VMID — it cannot be counted from the live snapshot, which would let a
-- burst of parallel pending creates each read as zero and all slip past the cap
-- (ADR-0012 §2). These three nullable columns carry the requested allocation of
-- a pending reservation so it is always countable; an ACTIVE row still reads its
-- usage from the live snapshot (0 if the VMID is absent). The partial index
-- accelerates the reconciler's stale-pending sweep (Phase-4 chunk B).
ALTER TABLE resource_ownership
  ADD COLUMN reserved_vcpu    integer,
  ADD COLUMN reserved_ram_mb  bigint,
  ADD COLUMN reserved_disk_gb bigint;

CREATE INDEX resource_ownership_pending_created_idx
  ON resource_ownership (created_at) WHERE status = 'pending';
