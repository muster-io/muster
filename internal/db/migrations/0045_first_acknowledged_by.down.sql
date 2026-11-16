-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0045_first_acknowledged_by.up.sql: drops alert_groups_first_acknowledged_by_check and
-- alert_groups.first_acknowledged_by_user_id with its foreign key, so who made each first acknowledgement is lost; the
-- time of the first acknowledgement stays. Up again fills the column again from the Timeline that is still whole.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups
    DROP CONSTRAINT alert_groups_first_acknowledged_by_check,
    DROP COLUMN first_acknowledged_by_user_id;

COMMIT;
