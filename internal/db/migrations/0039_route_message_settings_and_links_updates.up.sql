-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds two kinds of open_group_updates rows that re-render the open Root messages after a change of what they show
-- that no earlier kind covers: route_message_settings, which an update of a Route that changes its language, its Snooze
-- durations or its root_message or line template inserts, with the Route in route_id, and whose run walks the open
-- Alert Groups of the Route; and organization_links, which a creation, update or deletion of a Link rule or of a Lookup
-- table inserts, without a scope, and whose run walks every open Alert Group of the Organization, since the links of
-- each depend on its labels. Both only re-render: delivery edits, Quietly, the Root messages whose Desired state
-- changed and leaves the others, so that a second run edits nothing. Each batch of organization_links waits for the
-- Snapshots in flight under the lock of the evaluation before it lists its Alert Groups, as organization_message_settings
-- does; route_message_settings needs no barrier, since grouping holds the Route FOR SHARE, which the update takes FOR
-- UPDATE. open_group_updates_route_settings_scope_check gives a row of route_message_settings its Route. The kind CHECK
-- is replaced by open_group_updates_kind_v7_check with every kind of _v6 (0032) and the two new ones.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update never writes or claims rows of the new kinds — a save it makes re-renders nothing, and its messages
-- catch up at each Alert Group's next change — and a row a newer replica writes waits for a replica that knows its
-- kind. The new CHECKs are added NOT VALID, validated in a second transaction under a lock that lets writes through,
-- and the old kind CHECK is dropped in a third, as 0031 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v7_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations',
                        'route_message_settings', 'organization_links')) NOT VALID,
    ADD CONSTRAINT open_group_updates_route_settings_scope_check
        CHECK (kind <> 'route_message_settings' OR route_id IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v7_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_route_settings_scope_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v6_check;

COMMIT;
