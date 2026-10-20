-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0018_slack_presses_and_links.up.sql. The Slack Account links and link requests are lost; the old messenger
-- CHECKs are restored NOT VALID under their names and validated in a second transaction. The Timeline and the Audit log
-- are history, and the Audit log is append-only, so their rows with the Transport slack are kept: the old transport
-- CHECKs are restored on the parents NOT VALID, which adds them to every partition and holds every new row, and
-- validated in a second transaction only when no such row exists; the new CHECKs are dropped in a third. Up again works
-- from either state.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM account_link_requests WHERE messenger = 'slack';

DELETE FROM account_links WHERE messenger = 'slack';

DROP INDEX account_link_requests_slack_target_idx;

ALTER TABLE account_link_requests
    DROP CONSTRAINT account_link_requests_slack_target_check,
    DROP CONSTRAINT account_link_requests_messenger_check,
    ADD CONSTRAINT account_link_requests_messenger_check
        CHECK (messenger IN ('telegram', 'mattermost', 'bridge')) NOT VALID;

ALTER TABLE account_links
    DROP CONSTRAINT account_links_messenger_check,
    ADD CONSTRAINT account_links_messenger_check CHECK (messenger IN ('telegram', 'mattermost', 'bridge')) NOT VALID;

ALTER TABLE timeline_entries
    ADD CONSTRAINT timeline_entries_transport_v2_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'cli', 'system')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_v2_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'cli', 'system')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE account_links VALIDATE CONSTRAINT account_links_messenger_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_messenger_check;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM timeline_entries WHERE transport = 'slack') THEN
        ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_v2_check;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM audit_log WHERE transport = 'slack') THEN
        ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_v2_check;
    END IF;
END
$$;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries DROP CONSTRAINT timeline_entries_transport_v3_check;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_v3_check;

COMMIT;
