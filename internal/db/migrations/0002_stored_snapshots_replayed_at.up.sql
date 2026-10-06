-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Records when `muster ingest replay` last set a Stored Snapshot back to pending, so that processing counts a Stored
-- Snapshot on its Integration (`integrations.snapshot_count`) only when it leaves pending for the first time.
-- Expand only: a nullable column without a default changes no existing row.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE stored_snapshots ADD COLUMN replayed_at timestamptz;

COMMIT;
