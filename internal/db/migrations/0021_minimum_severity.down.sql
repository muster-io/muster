-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0021_minimum_severity.up.sql: drops open_group_updates, with any change still to reach the open Alert
-- Groups, and the Minimum severity of the Routes. routes.urgent and organizations.critical_is_urgent stay as the up
-- migration left them, so that a Route that had the Minimum severity critical stays marked urgent, and the stored
-- Severity levels and urgency of the Alert Groups are not computed again.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE open_group_updates;

ALTER TABLE routes DROP COLUMN minimum_severity;

COMMIT;
