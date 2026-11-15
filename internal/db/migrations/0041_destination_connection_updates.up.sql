-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the kind destination_connection of open_group_updates rows, which re-render the open Root messages of a
-- Destination after a change of its Connection (C-12.FR-12): the update of a Destination that names another Connection
-- inserts it, in the transaction of the save, with the Destination in destination_id, as a changed button set inserts
-- destination_buttons. Its run walks the open Alert Groups with a delivery in the Destination that is not being retired,
-- as destination_buttons does, and only re-renders them, so that delivery edits, once and Quietly, each Root message
-- whose footer reads otherwise in the identity space of the new Connection, and a second run edits nothing. A move
-- between Telegram Connections, which share one identity space, and a move that also changes the button set, whose
-- row destination_buttons walks the same Alert Groups, insert none.
-- open_group_updates_destination_connection_scope_check gives such a row its Destination.
-- The kind CHECK is replaced by open_group_updates_kind_v9_check with every kind of _v8 (0040) and the new one.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update never writes or claims rows of the new kind — a change of a Destination's Connection it saves
-- re-renders nothing, and its Root messages catch up at each Alert Group's next change — and a row a newer replica
-- writes waits for a replica that knows its kind. The CHECKs are added NOT VALID in a first transaction, validated in
-- a second under a lock that lets writes through, and the old kind CHECK is dropped in a third, as 0039 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v9_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations',
                        'route_message_settings', 'organization_links', 'user_name', 'account_links',
                        'service_account_name', 'destination_connection')) NOT VALID,
    ADD CONSTRAINT open_group_updates_destination_connection_scope_check
        CHECK (kind <> 'destination_connection' OR destination_id IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v9_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_destination_connection_scope_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v8_check;

COMMIT;
