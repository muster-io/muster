-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0038_deliveries_configuration_only.up.sql. The pending edits of a re-render are claimed again by urgency and
-- next_attempt_at alone, among the other due deliveries; none is lost. Up again works: every delivery starts at false.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX deliveries_due_idx ON deliveries (org_id, urgent DESC, next_attempt_at) WHERE state = 'pending';
DROP INDEX deliveries_due_v2_idx;

ALTER TABLE deliveries DROP COLUMN configuration_only;

COMMIT;
