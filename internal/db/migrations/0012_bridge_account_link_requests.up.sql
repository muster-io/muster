-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds what Bridge Account links need (C-22.FR-15): the messenger bridge in the CHECK of account_link_requests, whose
-- type-checked foreign key to connections then binds a Bridge request to a Bridge Connection; method, which tells a
-- token sent to the bot from a code sent by direct message, required for a Bridge request, with a Bridge code naming
-- its target account; target_display_name, the name the bridge reported for that account, which the link takes on
-- confirmation; a partial unique index that finds a Bridge token by its hash alone, as an account_link event names it,
-- beside the Telegram one, which stays as it is; and a partial index that counts the Bridge code requests per target
-- account, as account_link_requests_target_idx does for Mattermost.
-- Expand only: an earlier binary writes Telegram and Mattermost requests with method and target_display_name NULL,
-- and every row it writes satisfies the new CHECKs; its plans keep account_link_requests_token_key. The widened
-- messenger CHECK is added NOT VALID under the old name and validated in a second transaction, under a lock that lets
-- writes through.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE account_link_requests
    DROP CONSTRAINT account_link_requests_messenger_check,
    ADD CONSTRAINT account_link_requests_messenger_check
        CHECK (messenger IN ('telegram', 'mattermost', 'bridge')) NOT VALID,
    ADD COLUMN method text,
    ADD COLUMN target_display_name text,
    ADD CONSTRAINT account_link_requests_method_check CHECK (method IN ('token', 'code')) NOT VALID,
    ADD CONSTRAINT account_link_requests_bridge_method_check
        CHECK (messenger <> 'bridge' OR method IS NOT NULL) NOT VALID,
    ADD CONSTRAINT account_link_requests_bridge_target_check
        CHECK (messenger <> 'bridge' OR method <> 'code' OR target_external_id IS NOT NULL) NOT VALID;

-- An account_link event names the token alone, as `/start` does for Telegram.
CREATE UNIQUE INDEX account_link_requests_bridge_token_key ON account_link_requests (secret_hash)
    WHERE messenger = 'bridge' AND method = 'token';

CREATE INDEX account_link_requests_bridge_target_idx
    ON account_link_requests (org_id, connection_id, target_external_id, created_at)
    WHERE messenger = 'bridge' AND method = 'code';

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_messenger_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_method_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_bridge_method_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_bridge_target_check;

COMMIT;
