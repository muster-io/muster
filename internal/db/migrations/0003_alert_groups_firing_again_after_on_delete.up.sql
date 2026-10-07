-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Lets summary retention delete the summary row of an Alert Group resolved by a person whose Alerts, still firing when
-- its Grace period ended, started a later Alert Group: alert_groups.firing_again_after_id now becomes null with it, and
-- the later Alert Group loses its notice firing_again_after_manual_resolve. Expand only: the column and its values stay;
-- the constraint is replaced without a scan under the exclusive lock, then validated under a lock that lets writes
-- through.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups
    DROP CONSTRAINT alert_groups_firing_again_after_id_fkey,
    ADD CONSTRAINT alert_groups_firing_again_after_id_fkey
        FOREIGN KEY (firing_again_after_id) REFERENCES alert_groups (id) ON DELETE SET NULL NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups VALIDATE CONSTRAINT alert_groups_firing_again_after_id_fkey;

COMMIT;
