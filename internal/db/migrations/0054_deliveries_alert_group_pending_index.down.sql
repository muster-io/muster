-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0054_deliveries_alert_group_pending_index.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX deliveries_alert_group_pending_idx;

COMMIT;
