-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Slack Destination (C-24.FR-3): the type slack, the channel id it posts to, as entered, and the channel's name
-- its Destination check read. Expand only: the new columns are nullable; the CHECKs are replaced without a scan under
-- the exclusive lock and validated in a second transaction, under a lock that lets writes through; every row an earlier
-- binary writes (a Mattermost, Telegram, webhook or Bridge Destination, the Slack columns null) satisfies them. The
-- foreign key (connection_id, type) REFERENCES connections (id, type) takes a Slack Destination as it is: Connections
-- accept the type slack since 0016.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations
    ADD COLUMN slack_channel_id text,
    ADD COLUMN slack_channel_name text,
    DROP CONSTRAINT destinations_type_check,
    ADD CONSTRAINT destinations_type_check
        CHECK (type IN ('mattermost', 'telegram', 'webhook', 'bridge', 'slack')) NOT VALID,
    DROP CONSTRAINT destinations_type_fields_check,
    ADD CONSTRAINT destinations_type_fields_check CHECK (
        (type = 'mattermost' AND connection_id IS NOT NULL
            AND mattermost_team_id IS NOT NULL AND mattermost_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL
            AND slack_channel_id IS NULL AND slack_channel_name IS NULL)
        OR (type = 'telegram' AND connection_id IS NOT NULL
            AND telegram_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL
            AND slack_channel_id IS NULL AND slack_channel_name IS NULL)
        -- The Signing secret exists from creation until a deleted Destination's secrets are wiped.
        OR (type = 'webhook' AND connection_id IS NULL AND webhook_mode IS NOT NULL
            AND (signing_secret_ciphertext IS NOT NULL OR deleted_at IS NOT NULL)
            AND (webhook_mode = 'template' OR webhook_events_config IS NOT NULL)
            AND (webhook_mode = 'events' OR webhook_template_config IS NOT NULL)
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL
            AND slack_channel_id IS NULL AND slack_channel_name IS NULL)
        OR (type = 'bridge' AND connection_id IS NOT NULL
            AND bridge_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND slack_channel_id IS NULL AND slack_channel_name IS NULL)
        -- A Slack Destination: a Connection and a channel id; the other types' columns are null.
        OR (type = 'slack' AND connection_id IS NOT NULL
            AND slack_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND mattermost_team_id IS NULL AND mattermost_channel_id IS NULL
            AND mattermost_team_name IS NULL AND mattermost_channel_name IS NULL
            AND telegram_channel_id IS NULL AND telegram_channel_chat_id IS NULL
            AND telegram_discussion_chat_id IS NULL AND telegram_channel_title IS NULL
            AND telegram_discussion_group_title IS NULL
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL)) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_check;
ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_fields_check;

COMMIT;
