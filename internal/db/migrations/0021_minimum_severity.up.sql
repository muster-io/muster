-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Minimum severity of a Route (route.minimum_severity, C-26.FR-8) and the queue of changes of configuration
-- that reach the open Alert Groups after the save, open_group_updates (C-26.FR-9), and makes urgency the Severity level
-- alone: an Alert Group is Urgent exactly when its level is critical (C-26.FR-10).
-- Expand only. routes.urgent and organizations.critical_is_urgent stay, so that a replica of 0.1 that still runs during
-- a rolling update finds the same urgency: every Route marked urgent gets the Minimum severity critical, and from now
-- on routes.urgent is written as minimum_severity = 'critical'; critical_is_urgent is set to true and never written
-- again. The Severity level and the urgency of every open Alert Group — the highest level of its firing Alerts, info
-- when none fires, raised to its Route's Minimum severity — and the urgency of its pending deliveries are computed
-- once by the new rule, without a Timeline entry, a delivery, an open_group_updates row or a change of a Snooze or an
-- Owner. A row of open_group_updates is deleted in the transaction of its last batch, so the table holds only work
-- still to do; later kinds replace open_group_updates_kind_check with _v2, _v3, ….

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE routes
    ADD COLUMN minimum_severity text NOT NULL DEFAULT 'none',
    ADD CONSTRAINT routes_minimum_severity_check CHECK (minimum_severity IN ('none', 'warning', 'critical'));

UPDATE routes SET minimum_severity = 'critical' WHERE urgent;

UPDATE organizations SET critical_is_urgent = true WHERE NOT critical_is_urgent;

WITH levels AS (
    SELECT g.org_id, g.id,
           greatest(coalesce(max(CASE a.severity_level WHEN 'critical' THEN 2 WHEN 'warning' THEN 1 ELSE 0 END), 0),
                    CASE r.minimum_severity WHEN 'critical' THEN 2 WHEN 'warning' THEN 1 ELSE 0 END) AS rank
    FROM alert_groups g
    JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
    LEFT JOIN alert_group_alerts m ON m.org_id = g.org_id AND m.alert_group_id = g.id AND m.state = 'firing'
    LEFT JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
    WHERE g.status <> 'resolved'
    GROUP BY g.org_id, g.id, r.minimum_severity
), computed AS (
    SELECT org_id, id, CASE rank WHEN 2 THEN 'critical' WHEN 1 THEN 'warning' ELSE 'info' END AS level
    FROM levels
)
UPDATE alert_groups g
SET severity_level = c.level, urgent = (c.level = 'critical')
FROM computed c
WHERE g.org_id = c.org_id AND g.id = c.id
  AND (g.severity_level <> c.level OR g.urgent <> (c.level = 'critical'));

UPDATE deliveries d
SET urgent = g.urgent
FROM alert_groups g
WHERE d.org_id = g.org_id AND d.alert_group_id = g.id AND g.status <> 'resolved' AND d.state = 'pending'
  AND d.urgent <> g.urgent;

-- A change of configuration that must reach the open Alert Groups of its scope after the save, claimed by the worker
-- of any replica with a lease on the real clock and walked by id in batches from after_alert_group_id.
CREATE TABLE open_group_updates (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id               bigint NOT NULL REFERENCES organizations (id),
    kind                 text NOT NULL,
    route_id             bigint REFERENCES routes (id),
    requested_at         timestamptz NOT NULL,                       -- business clock
    after_alert_group_id bigint NOT NULL DEFAULT 0 CHECK (after_alert_group_id >= 0),
    lease_until          timestamptz,                                -- real clock
    leased_by            text,
    CONSTRAINT open_group_updates_kind_check CHECK (kind IN ('route_minimum_severity')),
    CONSTRAINT open_group_updates_scope_check CHECK (kind <> 'route_minimum_severity' OR route_id IS NOT NULL),
    CONSTRAINT open_group_updates_lease_check CHECK ((leased_by IS NULL) = (lease_until IS NULL))
);

CREATE INDEX open_group_updates_due_idx ON open_group_updates (org_id, requested_at, id);

COMMIT;
