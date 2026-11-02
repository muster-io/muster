-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0031_message_settings_updates.up.sql. The re-renders still to do are lost: their open_group_updates rows of
-- organization_message_settings and team_name are deleted, and those Root messages catch up at each Alert Group's next
-- change; a team_evaluation row that the same save inserted stays. The kind CHECK of 0029 is restored NOT VALID under
-- its name first, so that no row of a new kind is written after the deletion, and validated in a second transaction;
-- the new CHECK is dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v4_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels')) NOT VALID;

DELETE FROM open_group_updates WHERE kind IN ('organization_message_settings', 'team_name');

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v4_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v5_check;

COMMIT;
