-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0011_bridge_events.up.sql. The Bridge Account links, their display names, the record of handled Bridge event
-- ids and the keys of superseded Root messages are lost; identity_space goes back to 'mattermost:<connection_id>' for
-- every link that is not a Telegram one, swapped as the up migration swaps it, under the old constraint names. The
-- Timeline and the Audit log are history, and the Audit log is append-only, so their rows with the Transport bridge are
-- kept: the old transport CHECKs are restored on the parents NOT VALID, which adds them to every partition and holds
-- every new row, and validated in a second transaction only when no such row exists; the new CHECKs are dropped in a
-- third. Up again works from either state.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM account_links WHERE messenger = 'bridge';

ALTER TABLE account_links
    ADD COLUMN identity_space_v1 text NOT NULL GENERATED ALWAYS AS (
        CASE WHEN messenger = 'telegram' THEN 'telegram'
             ELSE 'mattermost:' || connection_id::text END) STORED;

ALTER TABLE account_links
    DROP CONSTRAINT account_links_account_key,
    DROP CONSTRAINT account_links_user_space_key,
    DROP COLUMN identity_space;

ALTER TABLE account_links RENAME COLUMN identity_space_v1 TO identity_space;

ALTER TABLE account_links
    ADD CONSTRAINT account_links_account_key UNIQUE (org_id, identity_space, external_id),
    ADD CONSTRAINT account_links_user_space_key UNIQUE (org_id, user_id, identity_space),
    DROP COLUMN display_name,
    DROP CONSTRAINT account_links_messenger_check,
    ADD CONSTRAINT account_links_messenger_check CHECK (messenger IN ('telegram', 'mattermost')) NOT VALID;

DROP TABLE bridge_events_seen;

DROP INDEX thread_replies_reminder_message_idx;

DROP INDEX deliveries_superseded_keys_idx;

ALTER TABLE deliveries DROP COLUMN superseded_button_key_ids;

ALTER TABLE timeline_entries
    ADD CONSTRAINT timeline_entries_transport_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'cli', 'system')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'cli', 'system')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE account_links VALIDATE CONSTRAINT account_links_messenger_check;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM timeline_entries WHERE transport = 'bridge') THEN
        ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_check;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM audit_log WHERE transport = 'bridge') THEN
        ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_check;
    END IF;
END
$$;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries DROP CONSTRAINT timeline_entries_transport_v2_check;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_v2_check;

COMMIT;
