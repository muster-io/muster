-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0023_button_sets.up.sql. The button sets of the Organization and the Destinations are lost, and so are the
-- re-renders still to do for them: their open_group_updates rows are deleted. The old kind CHECK is restored NOT VALID
-- under its name and validated in a second transaction; the new one is dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM open_group_updates WHERE kind IN ('destination_buttons', 'organization_buttons');

ALTER TABLE open_group_updates
    DROP COLUMN destination_id,
    ADD CONSTRAINT open_group_updates_kind_check CHECK (kind IN ('route_minimum_severity')) NOT VALID;

ALTER TABLE destinations DROP COLUMN buttons;

ALTER TABLE organizations DROP COLUMN buttons;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v2_check;

COMMIT;
