-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Fills alert_groups.first_published_at, which the ack timeout starts from (C-17.FR-1), for the Alert Groups published
-- before the binary that sets it: the earliest published_at of their Root messages and, for outgoing webhooks in the
-- events mode, the earliest delivered_at of their delivered events. Without it the first delivered call after the
-- upgrade would mark them at that old time, and every open firing Alert Group would collapse its overdue notices into
-- one Loud reply at once. ack_timeout_started_at and the timers are left alone: an Alert Group published before the
-- upgrade gets an ack timeout on its next transition into firing. Data only, no change of the columns.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE alert_groups g
SET first_published_at = p.at
FROM (
    SELECT org_id, alert_group_id, min(at) AS at
    FROM (
        SELECT org_id, alert_group_id, published_at AS at
        FROM deliveries
        WHERE alert_group_id IS NOT NULL AND published_at IS NOT NULL
        UNION ALL
        SELECT org_id, alert_group_id, delivered_at AS at
        FROM webhook_events
        WHERE state = 'delivered' AND delivered_at IS NOT NULL
    ) published
    GROUP BY org_id, alert_group_id
) p
WHERE g.first_published_at IS NULL AND g.org_id = p.org_id AND g.id = p.alert_group_id;

COMMIT;
