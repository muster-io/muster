-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0009_bridge_destinations.up.sql. The restored CHECKs refuse the type bridge, so the Bridge Destinations are
-- deleted, deleted ones included, with what refers to them — their deliveries and so their Thread replies, their
-- places on Routes, their delivery events and their limiter buckets; their destination_secrets go by cascade — and are
-- lost.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM thread_replies WHERE destination_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM deliveries WHERE destination_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM webhook_events WHERE destination_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM route_destinations WHERE destination_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM delivery_events WHERE destination_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM rate_limit_buckets
 WHERE subject_kind = 'destination' AND subject_id IN (SELECT id FROM destinations WHERE type = 'bridge');
DELETE FROM destinations WHERE type = 'bridge';

ALTER TABLE destinations
    DROP CONSTRAINT destinations_type_fields_check,
    ADD CONSTRAINT destinations_type_fields_check CHECK (
        (type = 'mattermost' AND connection_id IS NOT NULL
            AND mattermost_team_id IS NOT NULL AND mattermost_channel_id IS NOT NULL AND webhook_mode IS NULL)
        OR (type = 'telegram' AND connection_id IS NOT NULL
            AND telegram_channel_id IS NOT NULL AND webhook_mode IS NULL)
        -- The Signing secret exists from creation until a deleted Destination's secrets are wiped.
        OR (type = 'webhook' AND connection_id IS NULL AND webhook_mode IS NOT NULL
            AND (signing_secret_ciphertext IS NOT NULL OR deleted_at IS NOT NULL)
            AND (webhook_mode = 'template' OR webhook_events_config IS NOT NULL)
            AND (webhook_mode = 'events' OR webhook_template_config IS NOT NULL))) NOT VALID,
    DROP CONSTRAINT destinations_type_check,
    ADD CONSTRAINT destinations_type_check CHECK (type IN ('mattermost', 'telegram', 'webhook')) NOT VALID,
    DROP COLUMN bridge_channel_id,
    DROP COLUMN bridge_channel_name;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_check;
ALTER TABLE destinations VALIDATE CONSTRAINT destinations_type_fields_check;

COMMIT;
