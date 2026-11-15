-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0042_team_mentions.up.sql. The Team mentions are lost, and so is what the checks of the Connections found of
-- their groups; a Connection's next check on 0042 finds it again. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE team_mentions;

ALTER TABLE connections
    DROP COLUMN slack_usergroups_read,
    DROP COLUMN mattermost_groups_read;

COMMIT;
