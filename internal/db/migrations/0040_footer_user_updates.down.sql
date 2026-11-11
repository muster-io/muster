-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0040_footer_user_updates.up.sql. The re-renders still to do are lost: their open_group_updates rows of
-- user_name, account_links and service_account_name are deleted, and those Root messages catch up at each Alert Group's
-- next change. The kind CHECK of 0039 is restored NOT VALID under its name first, so that no row of a new kind is
-- written after the deletion, and validated in a second transaction; the new CHECKs, the columns with their foreign
-- keys and the indexes are dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v7_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations',
                        'route_message_settings', 'organization_links')) NOT VALID;

DELETE FROM open_group_updates WHERE kind IN ('user_name', 'account_links', 'service_account_name');

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v7_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates
    DROP CONSTRAINT open_group_updates_service_account_scope_check,
    DROP CONSTRAINT open_group_updates_user_scope_check,
    DROP CONSTRAINT open_group_updates_kind_v8_check,
    DROP COLUMN service_account_id,
    DROP COLUMN user_id;

DROP INDEX alert_groups_snoozed_by_service_account_idx;
DROP INDEX alert_groups_snoozed_by_user_idx;

COMMIT;
