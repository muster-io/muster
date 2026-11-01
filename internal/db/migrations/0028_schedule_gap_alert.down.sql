-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0028_schedule_gap_alert.up.sql. The gap alert an Admin set on or off on a Schedule is lost; migrating up
-- again gives every Schedule by_default.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE schedules DROP COLUMN gap_alert;

COMMIT;
