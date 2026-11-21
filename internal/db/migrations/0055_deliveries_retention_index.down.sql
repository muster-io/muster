-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0055_deliveries_retention_index.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX deliveries_retention_idx;

COMMIT;
