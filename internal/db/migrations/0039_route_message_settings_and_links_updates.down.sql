-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0039_route_message_settings_and_links_updates.up.sql. The re-renders still to do are lost: their
-- open_group_updates rows of route_message_settings and organization_links are deleted, and those Root messages catch
-- up at each Alert Group's next change. The kind CHECK of 0032 is restored NOT VALID under its name first, so that no
-- row of a new kind is written after the deletion, and validated in a second transaction; the new CHECKs are dropped
-- in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v6_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations'))
        NOT VALID;

DELETE FROM open_group_updates WHERE kind IN ('route_message_settings', 'organization_links');

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v6_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates
    DROP CONSTRAINT open_group_updates_route_settings_scope_check,
    DROP CONSTRAINT open_group_updates_kind_v7_check;

COMMIT;
