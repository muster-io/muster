-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds what Bridge events need (C-22.FR-10 to FR-15, FR-24): the Transport bridge in the transport CHECKs of the
-- Timeline and the Audit log; Bridge Account links — the messenger bridge, the display_name a Bridge link stores, and
-- the identity space <messenger>:<connection_id>, telegram unchanged; the claim record of Bridge event ids,
-- bridge_events_seen, a short-lived table pruned after bridge.event_dedup_window; a partial index that finds a sent
-- Reminder by its message id, for presses on it; and the keys that signed the buttons of the older Root messages a
-- republication past the edit window replaced (deliveries.superseded_button_key_ids), with a partial GIN index.
-- Expand only: an earlier binary never writes a Bridge row or the new columns, and every row it writes satisfies the
-- widened CHECKs. A generated column cannot change its expression before PostgreSQL 17, so identity_space is added
-- again under a new name, the two unique constraints and the old column are dropped, the new column takes the old name
-- and the constraints are added again under their old names, all under one exclusive lock of the small table; the
-- values of Mattermost and Telegram links do not change, so an earlier binary reads and writes the column and catches
-- the unique violations by name as before. timeline_entries and audit_log are partitioned, so each new transport
-- CHECK is added to the parent NOT VALID, which adds it to every partition, validated in a second transaction, which
-- recurses to every partition, under a lock that lets writes through, and the old CHECK is dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries
    ADD CONSTRAINT timeline_entries_transport_v2_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'cli', 'system')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_v2_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'cli', 'system')) NOT VALID;

ALTER TABLE account_links
    DROP CONSTRAINT account_links_messenger_check,
    ADD CONSTRAINT account_links_messenger_check CHECK (messenger IN ('telegram', 'mattermost', 'bridge')) NOT VALID,
    ADD COLUMN display_name text,
    ADD COLUMN identity_space_v2 text NOT NULL GENERATED ALWAYS AS (
        CASE WHEN messenger = 'telegram' THEN 'telegram'
             ELSE messenger || ':' || connection_id::text END) STORED;

ALTER TABLE account_links
    DROP CONSTRAINT account_links_account_key,
    DROP CONSTRAINT account_links_user_space_key,
    DROP COLUMN identity_space;

ALTER TABLE account_links RENAME COLUMN identity_space_v2 TO identity_space;

ALTER TABLE account_links
    ADD CONSTRAINT account_links_account_key UNIQUE (org_id, identity_space, external_id),
    ADD CONSTRAINT account_links_user_space_key UNIQUE (org_id, user_id, identity_space);

-- The ids of the Bridge events a Connection handled, recorded once each event's handling succeeded, under the
-- transaction advisory lock of the event's claim; pruned bridge.event_dedup_window after seen_at, on the business clock.
CREATE TABLE bridge_events_seen (
    org_id        bigint NOT NULL REFERENCES organizations (id),
    connection_id bigint NOT NULL REFERENCES connections (id),
    event_id      text NOT NULL CHECK (event_id ~ '^[!-~]{1,200}$'),
    seen_at       timestamptz NOT NULL,
    PRIMARY KEY (org_id, connection_id, event_id)
);

CREATE INDEX bridge_events_seen_prune_idx ON bridge_events_seen (org_id, seen_at);

CREATE INDEX thread_replies_reminder_message_idx ON thread_replies (destination_id, message_id)
    WHERE event = 'reminder' AND message_id IS NOT NULL;

ALTER TABLE deliveries ADD COLUMN superseded_button_key_ids text[] NOT NULL DEFAULT '{}';

CREATE INDEX deliveries_superseded_keys_idx ON deliveries USING gin (superseded_button_key_ids)
    WHERE superseded_button_key_ids <> '{}';

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_v2_check;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_v2_check;
ALTER TABLE account_links VALIDATE CONSTRAINT account_links_messenger_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries DROP CONSTRAINT timeline_entries_transport_check;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_check;

COMMIT;
