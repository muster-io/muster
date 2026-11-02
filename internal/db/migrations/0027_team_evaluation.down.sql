-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0027_team_evaluation.up.sql. The evaluations still to do are lost: their open_group_updates rows are deleted.
-- The kind CHECK of 0023 is restored NOT VALID under its name and validated in a second transaction; the new one is
-- dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM open_group_updates WHERE kind = 'team_evaluation';

ALTER TABLE open_group_updates
    DROP COLUMN team_id,
    ADD CONSTRAINT open_group_updates_kind_v2_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v3_check;

COMMIT;
