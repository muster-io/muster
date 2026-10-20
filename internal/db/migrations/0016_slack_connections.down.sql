-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0016_slack_connections.up.sql. The restored CHECKs refuse the type slack, so the Slack Connections are
-- deleted with their Request log records: nothing else refers to one at this version (no Destination or Account link
-- can), and they are lost. The view stops reading the Slack columns before they are dropped.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM connections WHERE type = 'slack';

CREATE OR REPLACE VIEW encrypted_values AS
          SELECT 'organizations.outgoing_heartbeat_url' AS field, id AS org_id, id::text AS row_key,
                 outgoing_heartbeat_url_key_id AS key_id
            FROM organizations WHERE outgoing_heartbeat_url_ciphertext IS NOT NULL
UNION ALL SELECT 'organizations.outgoing_heartbeat_proxy_password', id, id::text, outgoing_heartbeat_proxy_password_key_id
            FROM organizations WHERE outgoing_heartbeat_proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_settings.client_secret', org_id, org_id::text, client_secret_key_id
            FROM oidc_settings WHERE client_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_settings.proxy_password', org_id, org_id::text, proxy_password_key_id
            FROM oidc_settings WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_auth_requests.code_verifier', org_id, encode(state_hash, 'hex'), code_verifier_key_id
            FROM oidc_auth_requests
UNION ALL SELECT 'users.oidc_offline_token', org_id, id::text, oidc_offline_token_key_id
            FROM users WHERE oidc_offline_token_ciphertext IS NOT NULL
UNION ALL SELECT 'user_totp.seed', org_id, user_id::text, seed_key_id
            FROM user_totp WHERE seed_ciphertext IS NOT NULL
UNION ALL SELECT 'user_totp.pending_seed', org_id, user_id::text, pending_seed_key_id
            FROM user_totp WHERE pending_seed_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.bot_token', org_id, id::text, bot_token_key_id
            FROM connections
UNION ALL SELECT 'connections.proxy_password', org_id, id::text, proxy_password_key_id
            FROM connections WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.telegram_webhook_secret', org_id, id::text, telegram_webhook_secret_key_id
            FROM connections WHERE telegram_webhook_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.bridge_push_secret', org_id, id::text, bridge_push_secret_key_id
            FROM connections WHERE bridge_push_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.proxy_password', org_id, id::text, proxy_password_key_id
            FROM destinations WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.signing_secret', org_id, id::text, signing_secret_key_id
            FROM destinations WHERE signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.previous_signing_secret', org_id, id::text, previous_signing_secret_key_id
            FROM destinations WHERE previous_signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destination_secrets.value', org_id, destination_id::text || '/' || name, value_key_id
            FROM destination_secrets;

ALTER TABLE connections
    DROP CONSTRAINT connections_slack_signing_secret_check,
    DROP CONSTRAINT connections_slack_app_token_check,
    DROP CONSTRAINT connections_type_fields_check,
    ADD CONSTRAINT connections_type_fields_check CHECK (
        (type = 'mattermost' AND mattermost_server_url IS NOT NULL
            AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL
            AND bridge_base_url IS NULL AND bridge_event_delivery IS NULL)
        OR (type = 'telegram' AND telegram_bot_api_base_url IS NOT NULL AND telegram_update_mode IS NOT NULL
            AND mattermost_server_url IS NULL AND bridge_base_url IS NULL AND bridge_event_delivery IS NULL)
        OR (type = 'bridge' AND bridge_base_url IS NOT NULL AND bridge_event_delivery IN ('poll', 'push')
            AND mattermost_server_url IS NULL AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL
            AND (bridge_event_delivery = 'poll' OR deleted_at IS NOT NULL
                 OR bridge_push_secret_ciphertext IS NOT NULL))) NOT VALID,
    DROP CONSTRAINT connections_type_check,
    ADD CONSTRAINT connections_type_check CHECK (type IN ('mattermost', 'telegram', 'bridge')) NOT VALID,
    DROP COLUMN slack_event_mode,
    DROP COLUMN slack_app_token_ciphertext,
    DROP COLUMN slack_app_token_key_id,
    DROP COLUMN slack_app_token_updated_at,
    DROP COLUMN slack_signing_secret_ciphertext,
    DROP COLUMN slack_signing_secret_key_id,
    DROP COLUMN slack_signing_secret_updated_at,
    DROP COLUMN slack_team_id,
    DROP COLUMN slack_team_name,
    DROP COLUMN slack_team_url,
    DROP COLUMN slack_app_id,
    DROP COLUMN slack_bot_id,
    DROP COLUMN slack_socket_hello_at,
    DROP COLUMN slack_socket_other_connections;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections VALIDATE CONSTRAINT connections_type_check;
ALTER TABLE connections VALIDATE CONSTRAINT connections_type_fields_check;

COMMIT;
