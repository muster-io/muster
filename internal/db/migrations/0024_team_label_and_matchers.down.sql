-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0024_team_label_and_matchers.up.sql. The Team label, the Team matchers and the Teams of Alerts and Alert
-- Groups are lost, and so are the Timeline entries and the outgoing webhook events of team_joined and
-- unknown_team_value, which no earlier version knows: their rows are deleted. The old CHECKs are restored NOT VALID
-- under their names, on the parent of timeline_entries, which adds them to every partition, and validated in a second
-- transaction; the _v2 ones are dropped in a third. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM webhook_events WHERE event IN ('team_joined', 'unknown_team_value');

DELETE FROM timeline_entries WHERE event IN ('team_joined', 'unknown_team_value');

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed')) NOT VALID;

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_team_rule_check,
    DROP COLUMN team_id,
    DROP COLUMN team_rule,
    DROP COLUMN team_matcher,
    DROP COLUMN team_value,
    ADD CONSTRAINT timeline_entries_event_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'ack_timeout', 'unclaimed',
        'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed')) NOT VALID,
    ADD CONSTRAINT timeline_entries_event_kind_check CHECK (
        (event IS NULL AND kind = 'system')
        OR (kind = 'status' AND event IN ('created', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                                          'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed',
                                          'unsnoozed', 'auto_unacknowledged'))
        OR (kind = 'alerts' AND event IN ('alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                          'annotations_changed', 'severity_raised'))
        OR (kind = 'timers' AND event IN ('ack_timeout', 'unclaimed', 'reminder', 'reminder_answered', 'notices_missed'))
        OR (kind = 'system' AND event = 'moved_to_default_route')) NOT VALID,
    ADD CONSTRAINT timeline_entries_system_event_check CHECK (system_event IN (
        'moved_to_default_route', 'muster_unavailable', 'fallback_template_used', 'template_value_missing')) NOT VALID,
    ADD CONSTRAINT timeline_entries_system_check CHECK (
        (kind = 'system') = (system_event IS NOT NULL)
        AND (system_event IS NOT DISTINCT FROM 'moved_to_default_route')
            = (event IS NOT DISTINCT FROM 'moved_to_default_route')) NOT VALID;

ALTER TABLE alert_groups DROP COLUMN unknown_team_values;

DROP TABLE alert_group_teams;

ALTER TABLE alerts DROP COLUMN unknown_team_value;

DROP TABLE alert_teams;

DROP TABLE team_matchers;

ALTER TABLE organizations
    DROP CONSTRAINT organizations_team_label_check,
    DROP COLUMN team_label,
    DROP COLUMN teams_version;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_event_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_event_v2_check,
    DROP CONSTRAINT timeline_entries_event_kind_v2_check,
    DROP CONSTRAINT timeline_entries_system_event_v2_check,
    DROP CONSTRAINT timeline_entries_system_v2_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_v2_check;

COMMIT;
