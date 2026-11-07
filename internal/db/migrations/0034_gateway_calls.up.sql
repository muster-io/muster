-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0034 adds the queue of calls through Notification gateways (C-30.FR-4): gateway_calls, one row per call to
-- a person through a gateway, with its public_id, its state, its lease, its retries and, from its first attempt, the
-- contact, the text and the language it keeps; and gateway_call_items, the Alert Groups each call is about, an Alert
-- Group dropped before the first attempt keeping its row with dropped_at. At most one call waits per person and
-- gateway, which the later Pages join. not_before, next_attempt_at, ended_at and the combine window are times of the
-- business clock; lease_until is on the real clock, as every row lease.
-- The Timeline gains the kind escalation (C-32.FR-19) with every event of that kind in the L2 lifecycle event table —
-- paged, page_not_delivered, escalation_step_skipped, assignees_changed, escalation_webhook — and the symbolic Mention
-- paged, through _v4 CHECKs of its kinds, events, event kinds and Mentions; and the columns paged_user_ids, the people
-- a Page names, and gateway_connection_id, the Notification gateway of a call. The transport CHECKs of the Timeline and
-- the Audit log add gateway (C-30.FR-6), and webhook_events_event_v4_check the five escalation events, so that the
-- later stories of C-30 and C-32 need no migration for them.
-- Expand only: a replica of an earlier version that still runs during a rolling update writes none of the new tables
-- and columns, which are nullable, and every row it writes satisfies the widened CHECKs. timeline_entries and audit_log
-- are partitioned, so each new CHECK is added to the parent NOT VALID, which adds it to every partition; every new
-- CHECK is validated in a second transaction under a lock that lets writes through, and the old ones are dropped in a
-- third, as 0018 and 0032 do.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE gateway_calls (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id           bigint NOT NULL REFERENCES organizations (id),
    public_id        text NOT NULL CHECK (public_id ~ '^CA[0-9A-HJKMNP-TV-Z]{12}$'),
    connection_id    bigint NOT NULL REFERENCES connections (id),  -- the Notification gateway
    user_id          bigint NOT NULL REFERENCES users (id),        -- the person called
    -- The confirmed contact the first attempt went to; null before it, and once the contact is removed.
    contact_id       bigint REFERENCES gateway_contacts (id) ON DELETE SET NULL,
    state            text NOT NULL CHECK (state IN ('waiting', 'sending', 'accepted', 'not_delivered', 'dropped')),
    critical         boolean NOT NULL DEFAULT false,               -- about a critical Alert Group: claimed first
    language         text CHECK (language IN ('en', 'ru')),        -- fixed at the first attempt, with the text
    text             text,
    -- The earliest first attempt: the end of the previous call plus gateway.combine_window; null while it is sent.
    not_before       timestamptz,
    next_attempt_at  timestamptz,
    lease_owner      text,
    lease_until      timestamptz,
    attempts         bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    first_attempt_at timestamptz,
    first_failed_at  timestamptz,
    last_error_class text CHECK (last_error_class IN ('retry_after', 'transient', 'fatal', 'unknown')),
    last_error       text,
    notification_id  text,                                          -- the gateway's id of an accepted call
    reason           text,                                          -- why it ended Not delivered
    ended_at         timestamptz,
    created_at       timestamptz NOT NULL,
    UNIQUE (org_id, public_id),
    CONSTRAINT gateway_calls_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT gateway_calls_ended_check CHECK ((state IN ('waiting', 'sending')) = (ended_at IS NULL)),
    CONSTRAINT gateway_calls_reason_check CHECK ((state = 'not_delivered') = (reason IS NOT NULL)),
    CONSTRAINT gateway_calls_text_check CHECK ((first_attempt_at IS NULL) = (text IS NULL)
                                               AND (text IS NULL) = (language IS NULL)),
    CONSTRAINT gateway_calls_sending_check CHECK (state <> 'sending'
                                                  OR (first_attempt_at IS NOT NULL AND next_attempt_at IS NOT NULL))
);

-- At most one call waits per person and gateway: the Pages that come due join it.
CREATE UNIQUE INDEX gateway_calls_waiting_key ON gateway_calls (connection_id, user_id) WHERE state = 'waiting';
-- The claim of the due calls.
CREATE INDEX gateway_calls_due_idx ON gateway_calls (org_id, next_attempt_at) WHERE state IN ('waiting', 'sending');
-- The previous call of a person through a gateway, whose end the combine window counts from.
CREATE INDEX gateway_calls_person_idx ON gateway_calls (org_id, connection_id, user_id, id);
-- The foreign keys: a removed contact sets theirs to null; users and connections are referenced by the calls.
CREATE INDEX gateway_calls_contact_idx ON gateway_calls (contact_id);
CREATE INDEX gateway_calls_user_idx ON gateway_calls (org_id, user_id);

CREATE TABLE gateway_call_items (
    org_id         bigint NOT NULL REFERENCES organizations (id),
    call_id        bigint NOT NULL REFERENCES gateway_calls (id) ON DELETE CASCADE,
    alert_group_id bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    queued_at      timestamptz NOT NULL,
    dropped_at     timestamptz,  -- stopped before the first attempt: the call is not about it
    PRIMARY KEY (call_id, alert_group_id)
);

-- The foreign key action: the retention of an Alert Group deletes its rows.
CREATE INDEX gateway_call_items_alert_group_idx ON gateway_call_items (alert_group_id);

ALTER TABLE timeline_entries
    ADD COLUMN paged_user_ids bigint[],         -- users.id of the people a Page names (C-32.FR-19)
    ADD COLUMN gateway_connection_id bigint,    -- connections.id of the Notification gateway of a call
    ADD CONSTRAINT timeline_entries_kind_v4_check
        CHECK (kind IN ('status', 'alerts', 'timers', 'system', 'escalation')) NOT VALID,
    ADD CONSTRAINT timeline_entries_event_v4_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'ack_timeout', 'unclaimed',
        'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value', 'destination_joined', 'paged', 'page_not_delivered', 'escalation_step_skipped',
        'assignees_changed', 'escalation_webhook')) NOT VALID,
    ADD CONSTRAINT timeline_entries_event_kind_v4_check CHECK (
        (event IS NULL AND kind = 'system')
        OR (kind = 'status' AND event IN ('created', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                                          'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed',
                                          'unsnoozed', 'auto_unacknowledged'))
        OR (kind = 'alerts' AND event IN ('alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                          'annotations_changed', 'severity_raised', 'team_joined'))
        OR (kind = 'timers' AND event IN ('ack_timeout', 'unclaimed', 'reminder', 'reminder_answered', 'notices_missed'))
        OR (kind = 'system' AND event IN ('moved_to_default_route', 'unknown_team_value', 'destination_joined'))
        OR (kind = 'escalation' AND event IN ('paged', 'page_not_delivered', 'escalation_step_skipped',
                                              'assignees_changed', 'escalation_webhook')))
        NOT VALID,
    ADD CONSTRAINT timeline_entries_mentions_v4_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent', 'paged']::text[]) NOT VALID,
    ADD CONSTRAINT timeline_entries_transport_v4_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'gateway', 'cli', 'system'))
        NOT VALID;

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_transport_v4_check
        CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'bridge', 'slack', 'gateway', 'cli', 'system'))
        NOT VALID;

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_v4_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value', 'destination_joined', 'paged', 'page_not_delivered', 'escalation_step_skipped',
        'assignees_changed', 'escalation_webhook')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_kind_v4_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_v4_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_v4_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_mentions_v4_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_transport_v4_check;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_transport_v4_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_v4_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_kind_check,
    DROP CONSTRAINT timeline_entries_event_v3_check,
    DROP CONSTRAINT timeline_entries_event_kind_v3_check,
    DROP CONSTRAINT timeline_entries_mentions_check,
    DROP CONSTRAINT timeline_entries_transport_v3_check;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_transport_v3_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_v3_check;

COMMIT;
