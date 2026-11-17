-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0048_mention_delivery_events.up.sql. The delivery events mention_not_delivered and mention_delivered are
-- lost: the Timelines no longer say that a Mention was not delivered, and the warnings of the Destinations are gone
-- until the next undelivered Mention a binary with 0048 records. Dropping the index of the parent drops it on every
-- partition. The old kind CHECK is restored on the parent NOT VALID, which adds it to every partition, and validated in
-- a second transaction before the new one is dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM delivery_events WHERE kind IN ('mention_not_delivered', 'mention_delivered');

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_kind_v2_check CHECK (kind IN (
        'publication', 'possible_duplicate', 'not_delivered', 'delivered_late', 'deleted_in_messenger',
        'republished', 'thread_not_attached', 'markup_rejected', 'destination_broken',
        'destination_recovered', 'storm_summary', 'final_edit', 'republished_past_edit_window')) NOT VALID;

DROP INDEX delivery_events_mentions_idx;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_kind_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_kind_v3_check;

COMMIT;
