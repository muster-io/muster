-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the button sets (C-28.FR-8 to FR-10): the Organization's, organizations.buttons, all buttons by default, as an
-- object {"all": true} or {"all": false, "items": [...]}; and a Destination's own, destinations.buttons, an array of
-- items, null for "As in the Organization". A change of either re-renders the open Root messages of the Destinations
-- that use it through open_group_updates, whose kind CHECK is replaced by open_group_updates_kind_v2_check with the
-- kinds destination_buttons and organization_buttons, and whose new column destination_id names the Destination of a
-- row of destination_buttons.
-- Expand only: a replica of 0.1 that still runs during a rolling update reads neither column and renders every button
-- on the messages it touches; it writes Organizations and Destinations with the defaults — every button, and "As in
-- the Organization" — and, having no open_group_updates worker, never claims or writes these rows. Every new CHECK and
-- the foreign key are added NOT VALID, validated in a second transaction under a lock that lets writes through, and
-- the old kind CHECK is dropped in a third, as 0018 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations
    ADD COLUMN buttons jsonb NOT NULL DEFAULT '{"all": true}',
    ADD CONSTRAINT organizations_buttons_check CHECK (jsonb_typeof(buttons) = 'object') NOT VALID;

ALTER TABLE destinations
    ADD COLUMN buttons jsonb,
    ADD CONSTRAINT destinations_buttons_check CHECK (buttons IS NULL OR jsonb_typeof(buttons) = 'array') NOT VALID;

ALTER TABLE open_group_updates
    ADD COLUMN destination_id bigint,
    ADD CONSTRAINT open_group_updates_destination_id_fkey FOREIGN KEY (destination_id) REFERENCES destinations (id)
        NOT VALID,
    ADD CONSTRAINT open_group_updates_kind_v2_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons')) NOT VALID,
    ADD CONSTRAINT open_group_updates_destination_scope_check
        CHECK (kind <> 'destination_buttons' OR destination_id IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations VALIDATE CONSTRAINT organizations_buttons_check;
ALTER TABLE destinations VALIDATE CONSTRAINT destinations_buttons_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_destination_id_fkey;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v2_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_destination_scope_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_check;

COMMIT;
