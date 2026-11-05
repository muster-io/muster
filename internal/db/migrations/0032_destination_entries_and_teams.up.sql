-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Severity conditions of the Destinations of Routes and Teams and the delivery to the Teams of an Alert Group
-- (C-26.FR-1 to FR-5): route_destinations.severity_condition, `any` for every existing row; routes.deliver_to_teams
-- ("Also to the Teams from the label", off) and routes.run_team_chains ("Run the Teams' Escalation chains", on);
-- team_destinations, the Destinations of each Team with their Severity condition; deliveries.entries, the entries that
-- reached a delivery, appended and never removed, empty for the deliveries made before; and
-- timeline_entries.destination_id, the Destination of the lifecycle event destination_joined (kind system, with the
-- same system_event).
-- The CHECKs of the Timeline's events, event kinds, system events and system rule are replaced by _v3 ones that list
-- every value of _v2 (0024) and destination_joined; webhook_events_event_v3_check adds destination_joined to _v2 so
-- that events mode can store it; and open_group_updates_kind_v6_check lists every kind of _v5 (0031) and
-- team_destinations, the kind a later version writes when the Destinations of a Team change, which no replica of this
-- version writes or claims.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update writes none of the new columns, which have defaults or are nullable, nor team_destinations, and
-- delivers to every Destination of a Route whatever its condition, without the Destinations of Teams, until the rollout
-- ends; every row it writes satisfies the widened CHECKs. timeline_entries is partitioned, so each new CHECK is added
-- to the parent NOT VALID, which adds it to every partition; every new CHECK is validated in a second transaction under
-- a lock that lets writes through, and the old ones are dropped in a third, as 0024 and 0031 do.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE route_destinations
    ADD COLUMN severity_condition text NOT NULL DEFAULT 'any',
    ADD CONSTRAINT route_destinations_severity_condition_check
        CHECK (severity_condition IN ('any', 'critical', 'below_critical')) NOT VALID;

ALTER TABLE routes
    ADD COLUMN deliver_to_teams boolean NOT NULL DEFAULT false,
    ADD COLUMN run_team_chains boolean NOT NULL DEFAULT true;

CREATE TABLE team_destinations (
    org_id             bigint NOT NULL REFERENCES organizations (id),
    team_id            bigint NOT NULL REFERENCES teams (id),
    destination_id     bigint NOT NULL REFERENCES destinations (id),
    severity_condition text NOT NULL DEFAULT 'any'
        CHECK (severity_condition IN ('any', 'critical', 'below_critical')),
    added_at           timestamptz NOT NULL,
    PRIMARY KEY (team_id, destination_id)
);

-- The Teams that name a Destination (DestinationReadFields.teams).
CREATE INDEX team_destinations_destination_idx ON team_destinations (org_id, destination_id);

ALTER TABLE deliveries
    ADD COLUMN entries jsonb NOT NULL DEFAULT '[]',
    ADD CONSTRAINT deliveries_entries_check CHECK (jsonb_typeof(entries) = 'array') NOT VALID;

ALTER TABLE timeline_entries
    ADD COLUMN destination_id bigint,  -- destinations.id of destination_joined
    ADD CONSTRAINT timeline_entries_event_v3_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'ack_timeout', 'unclaimed',
        'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value', 'destination_joined')) NOT VALID,
    ADD CONSTRAINT timeline_entries_event_kind_v3_check CHECK (
        (event IS NULL AND kind = 'system')
        OR (kind = 'status' AND event IN ('created', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                                          'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed',
                                          'unsnoozed', 'auto_unacknowledged'))
        OR (kind = 'alerts' AND event IN ('alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                          'annotations_changed', 'severity_raised', 'team_joined'))
        OR (kind = 'timers' AND event IN ('ack_timeout', 'unclaimed', 'reminder', 'reminder_answered', 'notices_missed'))
        OR (kind = 'system' AND event IN ('moved_to_default_route', 'unknown_team_value', 'destination_joined')))
        NOT VALID,
    ADD CONSTRAINT timeline_entries_system_event_v3_check CHECK (system_event IN (
        'moved_to_default_route', 'muster_unavailable', 'fallback_template_used', 'template_value_missing',
        'unknown_team_value', 'destination_joined')) NOT VALID,
    ADD CONSTRAINT timeline_entries_system_v3_check CHECK (
        (kind = 'system') = (system_event IS NOT NULL)
        AND (system_event IS NOT DISTINCT FROM 'moved_to_default_route')
            = (event IS NOT DISTINCT FROM 'moved_to_default_route')
        AND (system_event IS NOT DISTINCT FROM 'unknown_team_value')
            = (event IS NOT DISTINCT FROM 'unknown_team_value')
        AND (system_event IS NOT DISTINCT FROM 'destination_joined')
            = (event IS NOT DISTINCT FROM 'destination_joined')) NOT VALID;

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_v3_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value', 'destination_joined')) NOT VALID;

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v6_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations'))
        NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE route_destinations VALIDATE CONSTRAINT route_destinations_severity_condition_check;
ALTER TABLE deliveries VALIDATE CONSTRAINT deliveries_entries_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_v3_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_v3_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_event_v3_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_v3_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_v3_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v6_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_event_v2_check,
    DROP CONSTRAINT timeline_entries_event_kind_v2_check,
    DROP CONSTRAINT timeline_entries_system_event_v2_check,
    DROP CONSTRAINT timeline_entries_system_v2_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_v2_check;

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v5_check;

COMMIT;
