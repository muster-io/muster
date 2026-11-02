-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0029_route_message_labels.up.sql. The lists of "Labels in the message" are lost — every Route computes its
-- list again — and so are the descriptions of the Alert Groups and the re-renders still to do: their
-- open_group_updates rows are deleted. The kind CHECK of 0027 is restored NOT VALID under its name and validated in a
-- second transaction; the new CHECKs are dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM open_group_updates WHERE kind = 'route_message_labels';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v3_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation'))
        NOT VALID;

ALTER TABLE alert_groups DROP COLUMN description;

ALTER TABLE routes DROP COLUMN message_labels;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v3_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates
    DROP CONSTRAINT open_group_updates_kind_v4_check,
    DROP CONSTRAINT open_group_updates_route_scope_check;

COMMIT;
