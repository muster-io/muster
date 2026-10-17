-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0012_bridge_account_link_requests.up.sql. The Bridge link requests are lost; the Bridge Account links
-- themselves belong to 0011, whose down migration deletes them. The old messenger CHECK is restored NOT VALID under its
-- name and validated in a second transaction. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM account_link_requests WHERE messenger = 'bridge';

DROP INDEX account_link_requests_bridge_target_idx;

DROP INDEX account_link_requests_bridge_token_key;

ALTER TABLE account_link_requests
    DROP CONSTRAINT account_link_requests_bridge_target_check,
    DROP CONSTRAINT account_link_requests_bridge_method_check,
    DROP CONSTRAINT account_link_requests_method_check,
    DROP COLUMN method,
    DROP COLUMN target_display_name,
    DROP CONSTRAINT account_link_requests_messenger_check,
    ADD CONSTRAINT account_link_requests_messenger_check CHECK (messenger IN ('telegram', 'mattermost')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_messenger_check;

COMMIT;
