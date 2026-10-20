-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds what Slack presses and Slack Account links need (C-24.FR-10, FR-12): the Transport slack in the transport CHECKs
-- of the Timeline and the Audit log; the messenger slack in the CHECKs of account_links and account_link_requests,
-- whose identity space is already '<messenger>:<connection_id>' since 0011 and whose type-checked foreign key to
-- connections then binds a Slack link or request to a Slack Connection; a CHECK that a Slack request names its target
-- account, as a Mattermost one does; and a partial index that counts the Slack code requests per target account, as
-- account_link_requests_target_idx does for Mattermost.
-- Expand only: an earlier binary writes no Slack row, and every row it writes satisfies the widened CHECKs.
-- timeline_entries and audit_log are partitioned, so each new transport CHECK is added to the parent NOT VALID, which
-- adds it to every partition, validated in a second transaction, which recurses to every partition, under a lock that
-- lets writes through, and the old CHECK is dropped in a third, as 0011 did; the messenger CHECKs keep their names.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries
    ADD CONSTRAINT timeline_entries_transport_v3_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'cli', 'system')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_v3_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'cli', 'system')) NOT VALID;

ALTER TABLE account_links
    DROP CONSTRAINT account_links_messenger_check,
    ADD CONSTRAINT account_links_messenger_check
        CHECK (messenger IN ('telegram', 'mattermost', 'bridge', 'slack')) NOT VALID;

ALTER TABLE account_link_requests
    DROP CONSTRAINT account_link_requests_messenger_check,
    ADD CONSTRAINT account_link_requests_messenger_check
        CHECK (messenger IN ('telegram', 'mattermost', 'bridge', 'slack')) NOT VALID,
    ADD CONSTRAINT account_link_requests_slack_target_check
        CHECK (messenger <> 'slack' OR target_external_id IS NOT NULL) NOT VALID;

CREATE INDEX account_link_requests_slack_target_idx
    ON account_link_requests (org_id, connection_id, target_external_id, created_at)
    WHERE messenger = 'slack';

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_v3_check;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_v3_check;
ALTER TABLE account_links VALIDATE CONSTRAINT account_links_messenger_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_messenger_check;
ALTER TABLE account_link_requests VALIDATE CONSTRAINT account_link_requests_slack_target_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries DROP CONSTRAINT timeline_entries_transport_v2_check;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_v2_check;

COMMIT;
