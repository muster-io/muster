-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0003_alert_groups_firing_again_after_on_delete.up.sql: the foreign key again refuses to delete an Alert
-- Group that a later one fires again after.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups
    DROP CONSTRAINT alert_groups_firing_again_after_id_fkey,
    ADD CONSTRAINT alert_groups_firing_again_after_id_fkey
        FOREIGN KEY (firing_again_after_id) REFERENCES alert_groups (id) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups VALIDATE CONSTRAINT alert_groups_firing_again_after_id_fkey;

COMMIT;
