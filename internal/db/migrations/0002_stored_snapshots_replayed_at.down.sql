-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0002_stored_snapshots_replayed_at.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE stored_snapshots DROP COLUMN replayed_at;

COMMIT;
