-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds "Labels in the message" of a Route (route.message_labels, C-29.FR-2) and the description of an Alert Group
-- (C-29.FR-1): routes.message_labels, the label names of the line of key labels of its Root messages, null while the
-- list is computed from the Group key and the Organization's settings; and alert_groups.description, the description
-- annotation of the first of its Alerts that has one, set once like its summary, null when none has one. A change of
-- the list, of the Group key while it is computed, or of the Route's name re-renders the open Root messages of the
-- Route through open_group_updates, whose kind CHECK is replaced by open_group_updates_kind_v4_check with every kind of
-- _v3 and route_message_labels, and whose new CHECK open_group_updates_route_scope_check gives a row of
-- route_message_labels its Route.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update neither reads nor writes the new columns — a Route it creates or updates keeps the computed list, and
-- an Alert Group it creates has no description — renders the layout of its own version, and never claims or writes
-- rows of the new kind. Alert Groups created before this migration have no description. The new CHECKs are added NOT
-- VALID, validated in a second transaction under a lock that lets writes through, and the old kind CHECK is dropped in
-- a third, as 0023 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE routes ADD COLUMN message_labels text[];

ALTER TABLE alert_groups ADD COLUMN description text;

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v4_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels')) NOT VALID,
    ADD CONSTRAINT open_group_updates_route_scope_check
        CHECK (kind <> 'route_message_labels' OR route_id IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v4_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_route_scope_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v3_check;

COMMIT;
