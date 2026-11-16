-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds who made the first acknowledgement of an Alert Group (C-09.FR-27): alert_groups.first_acknowledged_by_user_id,
-- a User, written by the dispatcher's first Acknowledge in the transaction that sets first_acknowledged_at and never
-- changed afterwards. There is no Service account column: only Users acknowledge (C-04.FR-2).
-- alert_groups_first_acknowledged_by_check keeps it from naming anyone without first_acknowledged_at. Users are never
-- hard-deleted, so the foreign key never meets a removed row.
-- Expand only: a replica of 0.1, or of a development image without this migration, that still runs during a rolling
-- update neither reads nor writes the column; its SaveGroup leaves it as it is, and a first acknowledgement it records
-- names nobody, which By people counts as "Unknown". Three transactions: the column, nullable and without a default,
-- with the foreign key and the CHECK NOT VALID, a change of the catalog only; their VALIDATE, under a lock that lets
-- writes through; and the backfill.
-- The backfill fills the column of the Alert Groups that have first_acknowledged_at and nobody named for it from the
-- earliest acknowledged entry of their Timeline by (at, id), on timeline_entries_group_idx, only where their Timeline
-- is still whole: their created entry is still kept, and since retention drops whole monthly partitions, oldest first,
-- so is every later entry. An Alert Group whose first acknowledgement fell in a dropped month and a later one in a
-- kept month is left out, and so is one whose Timeline is gone. The entry's at is not matched with
-- first_acknowledged_at: the transition and its record read the clock separately. It updates at most the Alert Groups
-- whose Timeline is kept, retention.alert_details.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups
    ADD COLUMN first_acknowledged_by_user_id bigint,
    ADD CONSTRAINT alert_groups_first_acknowledged_by_user_id_fkey FOREIGN KEY (first_acknowledged_by_user_id)
        REFERENCES users (id) NOT VALID,
    ADD CONSTRAINT alert_groups_first_acknowledged_by_check
        CHECK (first_acknowledged_by_user_id IS NULL OR first_acknowledged_at IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE alert_groups VALIDATE CONSTRAINT alert_groups_first_acknowledged_by_user_id_fkey;
ALTER TABLE alert_groups VALIDATE CONSTRAINT alert_groups_first_acknowledged_by_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE alert_groups g
SET first_acknowledged_by_user_id = f.actor_user_id
FROM (
    SELECT s.org_id, s.id, a.actor_user_id
    FROM alert_groups s
    CROSS JOIN LATERAL (
        SELECT e.actor_user_id
        FROM timeline_entries e
        WHERE e.org_id = s.org_id AND e.alert_group_id = s.id AND e.event = 'acknowledged'
        ORDER BY e.at, e.id
        LIMIT 1
    ) a
    WHERE s.first_acknowledged_at IS NOT NULL AND s.first_acknowledged_by_user_id IS NULL
      AND EXISTS (SELECT 1
                  FROM timeline_entries c
                  WHERE c.org_id = s.org_id AND c.alert_group_id = s.id AND c.event = 'created')
) f
WHERE g.org_id = f.org_id AND g.id = f.id AND f.actor_user_id IS NOT NULL;

COMMIT;
