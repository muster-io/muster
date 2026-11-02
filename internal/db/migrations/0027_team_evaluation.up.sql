-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the evaluation of the open Alert Groups when a Team or the Team label is saved (C-25.FR-13): a save of a new
-- Team, of a changed slug or Team matchers, or of a new Team label inserts an open_group_updates row of the kind
-- team_evaluation, whose new column team_id names the Team of the save and is null for the Team label. The kind CHECK
-- is replaced by open_group_updates_kind_v3_check, with every kind of _v2 and team_evaluation. Teams are deleted
-- softly, so the foreign key never meets a deleted row; should a Team row ever be removed, its pending evaluation
-- stays, with no Team (ON DELETE SET NULL).
-- Expand only: a replica of 0.1 that still runs during a rolling update has no open_group_updates worker and writes no
-- row; a replica of a development image without this migration writes only the kinds of _v2, which the new CHECK
-- keeps, and claims only the kinds it handles, so it never claims a row of team_evaluation. Neither writes a row of
-- team_evaluation: a Team or a Team label saved on such a replica is not evaluated, and saving it again on a replica of
-- this version evaluates it. The CHECK and the foreign key are added NOT VALID, validated in a second transaction
-- under a lock that lets writes through, and the old CHECK is dropped in a third, as 0023 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD COLUMN team_id bigint,
    ADD CONSTRAINT open_group_updates_team_id_fkey FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE SET NULL
        NOT VALID,
    ADD CONSTRAINT open_group_updates_kind_v3_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation'))
        NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_team_id_fkey;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v3_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v2_check;

COMMIT;
