-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0043_mention_entries.up.sql. The Mention entries and the Alert Groups each Storm counts are lost: an earlier
-- binary mentions by destinations.mentions, which kept the L1 settings the up migration found, nobody for a Destination
-- created since. The Mention storm_started of the delivery events of Storm summaries reads again as new_alert_group,
-- which an L1 Storm summary carried. The old Mention CHECK is restored on the parent NOT VALID, which adds it to every
-- partition, and validated in a second transaction before the new one is dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE storm_alert_groups;

DROP TABLE mention_entries;

UPDATE delivery_events
SET mentions = array_replace(mentions, 'storm_started', 'new_alert_group')
WHERE 'storm_started' = ANY(mentions);

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[]) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_mentions_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_mentions_v2_check;

COMMIT;
