-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0053_alert_groups_evaluated_index.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX alert_groups_evaluated_idx;

COMMIT;
