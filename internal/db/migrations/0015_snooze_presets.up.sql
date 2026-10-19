-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the start of the working day and the first day of the week of the Organization (C-20.FR-9), at which the Snooze
-- presets end (C-10.FR-17): workday_start, a time of day HH:MM on the 24-hour clock, and week_start, monday to
-- sunday. Expand only: the defaults give the existing Organization 09:00 and monday (organization.workday_start,
-- organization.week_start) without rewriting its row, and an earlier binary never writes either column; the CHECKs
-- are added without a scan under the exclusive lock and validated in a second transaction, under a lock that lets
-- writes through.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations
    ADD COLUMN workday_start text NOT NULL DEFAULT '09:00',
    ADD COLUMN week_start text NOT NULL DEFAULT 'monday',
    ADD CONSTRAINT organizations_workday_start_check
        CHECK (workday_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$') NOT VALID,
    ADD CONSTRAINT organizations_week_start_check
        CHECK (week_start IN ('monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations VALIDATE CONSTRAINT organizations_workday_start_check;
ALTER TABLE organizations VALIDATE CONSTRAINT organizations_week_start_check;

COMMIT;
