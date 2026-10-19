-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0014_alerts_org_last_seen_index.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX alerts_org_last_seen_idx;

COMMIT;
