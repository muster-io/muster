-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Team mentions (C-25.FR-8): team_mentions, at most one per Team and Connection, the name of a Mattermost
-- group, of a Slack user group (its handle, with its id in slack_usergroup_id) or of a group of a bridge, which
-- internal/teams validates against what the Connection can mention; and what the last successful check of a
-- Connection found of its groups: connections.slack_usergroups_read, whether a Slack bot token has the optional scope
-- usergroups:read (C-24.FR-1, FR-2), and connections.mattermost_groups_read, whether a Mattermost bot may read the
-- groups. Both are null until a check sets them. Teams and Connections are deleted softly, so the rows of a deleted
-- Team or Connection are deleted by the deletion, in its transaction, and every read joins a Team and a Connection that
-- are not deleted.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update reads and writes neither the table nor the columns. A Team or a Connection it deletes keeps its rows
-- of team_mentions, which no read shows and a later save of the Team replaces; a check it runs leaves the columns as
-- they were, and a new bot token or server URL it saves keeps what the last check found of the old bot's groups until
-- the Connection's next check on a replica with this migration. One transaction, as 0037 does: the table is new and the columns are nullable without a default.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections
    ADD COLUMN slack_usergroups_read boolean,
    ADD COLUMN mattermost_groups_read boolean;

CREATE TABLE team_mentions (
    org_id             bigint NOT NULL REFERENCES organizations (id),
    team_id            bigint NOT NULL REFERENCES teams (id),
    connection_id      bigint NOT NULL REFERENCES connections (id),
    group_name         text NOT NULL CHECK (char_length(group_name) BETWEEN 1 AND 64),
    slack_usergroup_id text CHECK (slack_usergroup_id ~ '^S[0-9A-Z]+$'),
    created_at         timestamptz NOT NULL,
    PRIMARY KEY (team_id, connection_id)
);

-- The Team mentions of a Connection, which its deletion deletes.
CREATE INDEX team_mentions_connection_idx ON team_mentions (org_id, connection_id);

COMMIT;
