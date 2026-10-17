-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the edit windows of Bridge Root messages (C-22.FR-9): the status a delivery's Root message shows
-- (actual_status), whether it is past its bridge's edit window (past_edit_window) and the ids of the older Root
-- messages a republication past the window replaced (superseded_message_ids), with a partial GIN index that finds the
-- delivery of an older Root message; and the delivery event republished_past_edit_window. Expand only: the new columns
-- are nullable or have defaults, which an earlier binary never writes, and it never writes the new kind. The CHECK of
-- actual_status is added without a scan; delivery_events is partitioned, so its new kind CHECK is added to the parent
-- NOT VALID, which adds it to every partition, validated in a second transaction, which recurses to every partition,
-- under a lock that lets writes through, and the old CHECK is dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE deliveries
    ADD COLUMN actual_status text,
    ADD COLUMN past_edit_window boolean NOT NULL DEFAULT false,
    ADD COLUMN superseded_message_ids text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT deliveries_actual_status_check
        CHECK (actual_status IN ('firing', 'acknowledged', 'snoozed', 'resolved', 'storm')) NOT VALID;

CREATE INDEX deliveries_superseded_idx ON deliveries USING gin (superseded_message_ids)
    WHERE superseded_message_ids <> '{}';

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_kind_v2_check CHECK (kind IN (
        'publication', 'possible_duplicate', 'not_delivered', 'delivered_late', 'deleted_in_messenger',
        'republished', 'thread_not_attached', 'markup_rejected', 'destination_broken',
        'destination_recovered', 'storm_summary', 'final_edit', 'republished_past_edit_window')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE deliveries VALIDATE CONSTRAINT deliveries_actual_status_check;
ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_kind_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_kind_check;

COMMIT;
