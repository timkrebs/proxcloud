-- Reverse of 000003_reserved_alloc.up.sql.
DROP INDEX IF EXISTS resource_ownership_pending_created_idx;
ALTER TABLE resource_ownership
  DROP COLUMN IF EXISTS reserved_vcpu,
  DROP COLUMN IF EXISTS reserved_ram_mb,
  DROP COLUMN IF EXISTS reserved_disk_gb;
