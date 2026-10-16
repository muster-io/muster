-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Bridge Destination (C-22.FR-3): the type bridge, the channel id it posts to, as its messenger names it, and
-- the channel's name its Destination check read. Expand only: the new columns are nullable; the CHECKs are replaced
-- without a scan under the exclusive lock and validated in a second transaction, under a lock that lets writes
-- through; every row an earlier binary writes (a Mattermost, Telegram or webhook Destination, the Bridge columns null)
-- satisfies them. The foreign key (connection_id, type) REFERENCES connections (id, type) takes a Bridge Destination
-- as it is: Connections accept the type bridge since 0008.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations
    ADD COLUMN bridge_channel_id text,
    ADD COLUMN bridge_channel_name text,
    DROP CONSTRAINT destinations_type_check,
    ADD CONSTRAINT destinations_type_check CHECK (type IN ('mattermost', 'telegram', 'webhook', 'bridge')) NOT VALID,
    DROP CONSTRAINT destinations_type_fields_check,
    ADD CONSTRAINT destinations_type_fields_check CHECK (
        (type = 'mattermost' AND connection_id IS NOT NULL
            AND mattermost_team_id IS NOT NULL AND mattermost_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL)
        OR (type = 'telegram' AND connection_id IS NOT NULL
            AND telegram_channel_id IS NOT NULL AND webhook_mode IS NULL
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL)
        -- The Signing secret exists from creation until a deleted Destination's secrets are wiped.
        OR (type = 'webhook' AND connection_id IS NULL AND webhook_mode IS NOT NULL
            AND (signing_secret_ciphertext IS NOT NULL OR deleted_at IS NOT NULL)
            AND (webhook_mode = 'template' OR webhook_events_config IS NOT NULL)
            AND (webhook_mode = 'events' OR webhook_template_config IS NOT NULL)
            AND bridge_channel_id IS NULL AND bridge_channel_name IS NULL)
        OR (type = 'bridge' AND connection_id IS NOT NULL
            AND bridge_channel_id IS NOT NULL AND webhook_mode IS NULL)) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_check;
ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_fields_check;

COMMIT;
