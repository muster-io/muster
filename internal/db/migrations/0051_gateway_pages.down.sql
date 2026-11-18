-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0051_gateway_pages.up.sql. The Alert Groups of calls lose the Page that queued them and its stop check,
-- which the sender of 0050 does not read, and stay in their calls; the Pages through Notification gateways are lost,
-- with the people they named; the CHECKs of the reasons of pages and page_users are those of 0049 again.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE gateway_call_items
    DROP COLUMN page_id,
    DROP COLUMN notification_rule_run_id,
    DROP COLUMN escalation_run_id;

DELETE FROM pages WHERE kind = 'call';

ALTER TABLE page_users
    DROP CONSTRAINT page_users_reason_v2_check,
    ADD CONSTRAINT page_users_reason_check CHECK (reason IN ('no_root_message', 'no_account_link', 'not_delivered'));

ALTER TABLE pages
    DROP CONSTRAINT pages_reason_v2_check,
    DROP CONSTRAINT pages_gateway_check,
    DROP COLUMN gateway_call_id,
    DROP COLUMN gateway_connection_id,
    ADD CONSTRAINT pages_reason_check CHECK (reason IN ('no_root_message', 'no_account_link', 'not_delivered'));

COMMIT;
