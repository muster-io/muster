-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0028 adds schedules.gap_alert, schedule.gap_alert of C-27.FR-7: whether a Schedule reports its gaps as the
-- Internal alert MusterScheduleGap — by_default, active while the Schedule is its Team's main one, on or off. Expand
-- only: every Schedule, and every one a replica that does not know the column creates, takes by_default.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE schedules ADD COLUMN gap_alert text NOT NULL DEFAULT 'by_default'
    CONSTRAINT schedules_gap_alert_check CHECK (gap_alert IN ('by_default', 'on', 'off'));

COMMIT;
