-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the delivery events of undelivered Mentions (C-28.FR-3, C-11.FR-21 revised for L2): mention_not_delivered, for
-- each person whom a Mention entry names in a delivered Loud message but who has no Account link in the Destination's
-- Identity space, or whom a bridge without native_mentions names by the plain name, and mention_delivered, which closes
-- the warning of such a person and entry once a later Loud message mentions them through it; the detail of both holds
-- the person's id, the entry's id, position and target, the Loud event and, for mention_not_delivered, the reason. A
-- partial index finds the latest of them per Destination, person and entry, from which the warnings of a Destination
-- are computed. Expand only: an earlier binary never writes the new kinds and reads past them, as its Timeline lists
-- them as delivery events of a kind it does not know. delivery_events is partitioned, so its new kind CHECK is added to
-- the parent NOT VALID, which adds it to every partition, validated in a second transaction, which recurses to every
-- partition, under a lock that lets writes through, and the old CHECK is dropped in a third, as 0010 does. The index is
-- created on the parent alone first, invalid, which every partition created from then on gets as well; a fourth
-- transaction then builds it on each partition that has none, oldest first, and attaches it, which makes the parent's
-- index valid once the last one is attached; a partition whose concurrent detach is pending is left out, as it can
-- take no attached index. A partition is locked against writes only from its own build to the
-- commit, so the current month's partition, the only one written, waits for its own scan and the empty ones after it.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_kind_v3_check CHECK (kind IN (
        'publication', 'possible_duplicate', 'not_delivered', 'delivered_late', 'deleted_in_messenger',
        'republished', 'thread_not_attached', 'markup_rejected', 'destination_broken',
        'destination_recovered', 'storm_summary', 'final_edit', 'republished_past_edit_window',
        'mention_not_delivered', 'mention_delivered')) NOT VALID;

CREATE INDEX delivery_events_mentions_idx ON ONLY delivery_events (org_id, destination_id, kind, occurred_at DESC)
    WHERE kind IN ('mention_not_delivered', 'mention_delivered');

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_kind_v3_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_kind_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DO $$
DECLARE
    p record;
BEGIN
    FOR p IN
        SELECT c.oid, c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'delivery_events'::regclass
          -- A partition that retention left detached concurrently but not finalized takes no index: its detach
          -- finishes, and the parent's index is valid without it.
          AND NOT i.inhdetachpending
          -- A partition created since the first transaction has the index already.
          AND NOT EXISTS (SELECT 1
                          FROM pg_inherits ii
                          JOIN pg_index x ON x.indexrelid = ii.inhrelid
                          WHERE ii.inhparent = 'delivery_events_mentions_idx'::regclass AND x.indrelid = c.oid)
        ORDER BY c.relpartbound IS NULL, pg_get_expr(c.relpartbound, c.oid), c.relname
    LOOP
        EXECUTE format('CREATE INDEX %I ON %I (org_id, destination_id, kind, occurred_at DESC) '
                           || 'WHERE kind IN (''mention_not_delivered'', ''mention_delivered'')',
                       left(p.relname, 50) || '_mentions_idx', p.relname);
        EXECUTE format('ALTER INDEX delivery_events_mentions_idx ATTACH PARTITION %I',
                       left(p.relname, 50) || '_mentions_idx');
    END LOOP;
END
$$;

COMMIT;
