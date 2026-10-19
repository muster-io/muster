-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0015_snooze_presets.up.sql. The start of the working day and the first day of the week are lost; a Snooze a
-- preset set keeps its end, which is stored as an instant.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    DROP COLUMN workday_start,
    DROP COLUMN week_start;

COMMIT;
