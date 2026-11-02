-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds two kinds of open_group_updates rows that re-render the open Root messages after a change of what they show that
-- is not a Route's (C-29.FR-1 to FR-3): organization_message_settings, which an update of the Organization that changes
-- its Severity label, Team label, Instance labels, time zone, start of the working day or first day of the week
-- inserts, without a scope, and whose run walks every open Alert Group of the Organization; and team_name, which a
-- rename of a Team inserts, with the Team in team_id, and whose run walks the open Alert Groups that have the Team.
-- organization_message_settings re-renders, then evaluates the Teams of the Alert Groups of each batch as
-- team_evaluation does, in one transaction, so that a new Team label, which also writes that evaluation, edits each
-- Root message once; team_name evaluates nothing, since the classification is per Team. Each batch of either kind waits
-- for the Snapshots in flight under the lock of the evaluation before it lists its Alert Groups. team_name has no scope
-- CHECK: team_id is ON DELETE SET NULL, and a row whose Team is gone is simply done. The kind CHECK is replaced by open_group_updates_kind_v5_check with every kind
-- of _v4 and the two new ones.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update never writes or claims rows of the new kinds — a save it makes re-renders nothing, and its messages
-- catch up at each Alert Group's next change — and a row a newer replica writes waits for a replica that knows its
-- kind. The new CHECK is added NOT VALID, validated in a second transaction under a lock that lets writes through, and
-- the old one is dropped in a third, as 0029 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v5_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v5_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v4_check;

COMMIT;
