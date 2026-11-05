-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0032_destination_entries_and_teams.up.sql. The Severity conditions, the options of Routes for Teams, the
-- Destinations of Teams and the entries of deliveries are lost, and so are the Timeline entries and the outgoing
-- webhook events of destination_joined and the open_group_updates rows of team_destinations, which no earlier version
-- knows: their rows are deleted, and the Root messages that destination_joined published stay as deliveries of the
-- Alert Groups. The old CHECKs are restored NOT VALID under their names, on the parent of timeline_entries, which adds
-- them to every partition, and validated in a second transaction; the new ones are dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM open_group_updates WHERE kind = 'team_destinations';

DELETE FROM webhook_events WHERE event = 'destination_joined';

DELETE FROM timeline_entries WHERE event = 'destination_joined';

ALTER TABLE open_group_updates
    ADD CONSTRAINT open_group_updates_kind_v5_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name')) NOT VALID;

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_v2_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value')) NOT VALID;

ALTER TABLE timeline_entries
    DROP COLUMN destination_id,
    ADD CONSTRAINT timeline_entries_event_v2_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'ack_timeout', 'unclaimed',
        'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value')) NOT VALID,
    ADD CONSTRAINT timeline_entries_event_kind_v2_check CHECK (
        (event IS NULL AND kind = 'system')
        OR (kind = 'status' AND event IN ('created', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                                          'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed',
                                          'unsnoozed', 'auto_unacknowledged'))
        OR (kind = 'alerts' AND event IN ('alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                          'annotations_changed', 'severity_raised', 'team_joined'))
        OR (kind = 'timers' AND event IN ('ack_timeout', 'unclaimed', 'reminder', 'reminder_answered', 'notices_missed'))
        OR (kind = 'system' AND event IN ('moved_to_default_route', 'unknown_team_value'))) NOT VALID,
    ADD CONSTRAINT timeline_entries_system_event_v2_check CHECK (system_event IN (
        'moved_to_default_route', 'muster_unavailable', 'fallback_template_used', 'template_value_missing',
        'unknown_team_value')) NOT VALID,
    ADD CONSTRAINT timeline_entries_system_v2_check CHECK (
        (kind = 'system') = (system_event IS NOT NULL)
        AND (system_event IS NOT DISTINCT FROM 'moved_to_default_route')
            = (event IS NOT DISTINCT FROM 'moved_to_default_route')
        AND (system_event IS NOT DISTINCT FROM 'unknown_team_value')
            = (event IS NOT DISTINCT FROM 'unknown_team_value')) NOT VALID;

ALTER TABLE deliveries
    DROP CONSTRAINT deliveries_entries_check,
    DROP COLUMN entries;

DROP TABLE team_destinations;

ALTER TABLE routes
    DROP COLUMN deliver_to_teams,
    DROP COLUMN run_team_chains;

ALTER TABLE route_destinations
    DROP CONSTRAINT route_destinations_severity_condition_check,
    DROP COLUMN severity_condition;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_event_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_v2_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_v2_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v5_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_event_v3_check,
    DROP CONSTRAINT timeline_entries_event_kind_v3_check,
    DROP CONSTRAINT timeline_entries_system_event_v3_check,
    DROP CONSTRAINT timeline_entries_system_v3_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_v3_check;

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v6_check;

COMMIT;
