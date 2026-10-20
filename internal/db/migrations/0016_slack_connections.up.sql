-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Slack Connection (C-24.FR-1): the type slack, its event mode, its app-level token and Slack signing secret,
-- what its check learned — the workspace with its address, the app and the bot's id — and what the last Socket Mode
-- hello said, which the Leader writes from S-101; and both secrets in encrypted_values. The bot token keeps the
-- bot_token columns, the bot's user id and name bot_user_id and bot_username. Expand only: the new columns are
-- nullable; the CHECKs are replaced without a scan under the exclusive lock and validated in a second transaction,
-- under a lock that lets writes through; every row an earlier binary writes (a Mattermost, Telegram or Bridge
-- Connection, the Slack columns null) satisfies them; encrypted_values keeps its columns.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections
    ADD COLUMN slack_event_mode text,
    ADD COLUMN slack_app_token_ciphertext bytea,
    ADD COLUMN slack_app_token_key_id text,
    ADD COLUMN slack_app_token_updated_at timestamptz,
    ADD COLUMN slack_signing_secret_ciphertext bytea,
    ADD COLUMN slack_signing_secret_key_id text,
    ADD COLUMN slack_signing_secret_updated_at timestamptz,
    ADD COLUMN slack_team_id text,
    ADD COLUMN slack_team_name text,
    ADD COLUMN slack_team_url text,
    ADD COLUMN slack_app_id text,
    ADD COLUMN slack_bot_id text,
    ADD COLUMN slack_socket_hello_at timestamptz,
    ADD COLUMN slack_socket_other_connections integer,
    DROP CONSTRAINT connections_type_check,
    ADD CONSTRAINT connections_type_check CHECK (type IN ('mattermost', 'telegram', 'bridge', 'slack')) NOT VALID,
    DROP CONSTRAINT connections_type_fields_check,
    ADD CONSTRAINT connections_type_fields_check CHECK (
        (type = 'mattermost' AND mattermost_server_url IS NOT NULL
            AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL
            AND bridge_base_url IS NULL AND bridge_event_delivery IS NULL AND slack_event_mode IS NULL)
        OR (type = 'telegram' AND telegram_bot_api_base_url IS NOT NULL AND telegram_update_mode IS NOT NULL
            AND mattermost_server_url IS NULL AND bridge_base_url IS NULL AND bridge_event_delivery IS NULL
            AND slack_event_mode IS NULL)
        OR (type = 'bridge' AND bridge_base_url IS NOT NULL AND bridge_event_delivery IN ('poll', 'push')
            AND mattermost_server_url IS NULL AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL
            AND slack_event_mode IS NULL
            AND (bridge_event_delivery = 'poll' OR deleted_at IS NOT NULL
                 OR bridge_push_secret_ciphertext IS NOT NULL))
        OR (type = 'slack' AND slack_event_mode IN ('socket', 'http')
            AND mattermost_server_url IS NULL AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL
            AND bridge_base_url IS NULL AND bridge_event_delivery IS NULL
            AND (deleted_at IS NOT NULL
                 OR (slack_event_mode = 'socket' AND slack_app_token_ciphertext IS NOT NULL)
                 OR (slack_event_mode = 'http' AND slack_signing_secret_ciphertext IS NOT NULL)))) NOT VALID,
    ADD CONSTRAINT connections_slack_app_token_check
        CHECK ((slack_app_token_ciphertext IS NULL) = (slack_app_token_key_id IS NULL)) NOT VALID,
    ADD CONSTRAINT connections_slack_signing_secret_check
        CHECK ((slack_signing_secret_ciphertext IS NULL) = (slack_signing_secret_key_id IS NULL)) NOT VALID;

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
UNION ALL SELECT 'connections.slack_app_token', org_id, id::text, slack_app_token_key_id
            FROM connections WHERE slack_app_token_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.slack_signing_secret', org_id, id::text, slack_signing_secret_key_id
            FROM connections WHERE slack_signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.proxy_password', org_id, id::text, proxy_password_key_id
            FROM destinations WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.signing_secret', org_id, id::text, signing_secret_key_id
            FROM destinations WHERE signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.previous_signing_secret', org_id, id::text, previous_signing_secret_key_id
            FROM destinations WHERE previous_signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destination_secrets.value', org_id, destination_id::text || '/' || name, value_key_id
            FROM destination_secrets;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections VALIDATE CONSTRAINT connections_type_check;
ALTER TABLE connections VALIDATE CONSTRAINT connections_type_fields_check;
ALTER TABLE connections VALIDATE CONSTRAINT connections_slack_app_token_check;
ALTER TABLE connections VALIDATE CONSTRAINT connections_slack_signing_secret_check;

COMMIT;
