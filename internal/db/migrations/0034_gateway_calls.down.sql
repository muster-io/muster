-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0034_gateway_calls.up.sql. The calls through Notification gateways are lost, and so are the Timeline
-- entries of the kind escalation and the outgoing webhook events of its events, which no earlier version knows: their
-- rows are deleted, with the Mention paged of any other entry. The Timeline and the Audit log are history, and the
-- Audit log is append-only, so their rows with the Transport gateway are kept, as 0018's down keeps those of slack: the
-- old transport CHECKs are restored on the parents NOT VALID, which adds them to every partition and holds every new
-- row, and validated in the second transaction only when no such row exists. The other old CHECKs are restored NOT
-- VALID under their names, on the parent of timeline_entries, which adds them to every partition, and validated in a
-- second transaction; the new ones are dropped in a third. Up again works from either state.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE gateway_call_items;

DROP TABLE gateway_calls;

DELETE FROM webhook_events
WHERE event IN ('paged', 'page_not_delivered', 'escalation_step_skipped', 'assignees_changed', 'escalation_webhook');

DELETE FROM timeline_entries WHERE kind = 'escalation';

UPDATE timeline_entries SET mentions = array_remove(mentions, 'paged') WHERE 'paged' = ANY(mentions);

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_v3_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value', 'destination_joined')) NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_v3_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'cli', 'system')) NOT VALID;

ALTER TABLE timeline_entries
    DROP COLUMN paged_user_ids,
    DROP COLUMN gateway_connection_id,
    ADD CONSTRAINT timeline_entries_kind_check CHECK (kind IN ('status', 'alerts', 'timers', 'system')) NOT VALID,
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
    ADD CONSTRAINT timeline_entries_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[]) NOT VALID,
    ADD CONSTRAINT timeline_entries_transport_v3_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'cli', 'system')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_kind_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_v3_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_v3_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_mentions_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_v3_check;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM timeline_entries WHERE transport = 'gateway') THEN
        ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_v3_check;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM audit_log WHERE transport = 'gateway') THEN
        ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_v3_check;
    END IF;
END
$$;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_kind_v4_check,
    DROP CONSTRAINT timeline_entries_event_v4_check,
    DROP CONSTRAINT timeline_entries_event_kind_v4_check,
    DROP CONSTRAINT timeline_entries_mentions_v4_check,
    DROP CONSTRAINT timeline_entries_transport_v4_check;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_v4_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_v4_check;

COMMIT;
