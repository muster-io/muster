-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0010_bridge_edit_windows.up.sql. The delivery events republished_past_edit_window, the status each Root
-- message shows, the edit window flags and the ids of the older Root messages a republication replaced are lost: an
-- earlier binary edits a Root message past its window again, and sends an edit the bridge refuses as past its window
-- into the deleted Root message flow. The old kind CHECK is restored on the parent NOT VALID, which adds it to every
-- partition, and validated in a second transaction before the new one is dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM delivery_events WHERE kind = 'republished_past_edit_window';

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_kind_check CHECK (kind IN (
        'publication', 'possible_duplicate', 'not_delivered', 'delivered_late', 'deleted_in_messenger',
        'republished', 'thread_not_attached', 'markup_rejected', 'destination_broken',
        'destination_recovered', 'storm_summary', 'final_edit')) NOT VALID;

DROP INDEX deliveries_superseded_idx;

ALTER TABLE deliveries
    DROP CONSTRAINT deliveries_actual_status_check,
    DROP COLUMN actual_status,
    DROP COLUMN past_edit_window,
    DROP COLUMN superseded_message_ids;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_kind_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_kind_v2_check;

COMMIT;
