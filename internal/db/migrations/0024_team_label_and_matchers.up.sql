-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Teams of Alerts and Alert Groups (C-25.FR-5 to FR-7): the Organization's Team label,
-- organizations.team_label, `team` by default, and organizations.teams_version, which every change of a Team or of the
-- Team label raises so that the classification each replica caches is replaced in the Snapshot's own transaction;
-- team_matchers, the Team matchers of each Team in their order; alert_teams, the Teams the Router gave an Alert when it
-- started firing, with the rule, and alerts.unknown_team_value, the value of the Team label that named no Team then;
-- alert_group_teams, the Teams of an Alert Group, which only grow, with the index of the filter by Team, and
-- alert_groups.unknown_team_values, the values already recorded as unknown_team_value; and the Team fields of the
-- Timeline: team_id, team_rule and team_matcher of team_joined, team_value of unknown_team_value.
-- The CHECKs of the Timeline's events, event kinds, system events and system rule, and the CHECK of the events of
-- webhook_events, all still the versions of 0001, are replaced by _v2 ones that list every value of 0023 and the
-- lifecycle events team_joined (kind alerts) and unknown_team_value (kind system, with the same system_event).
-- Expand only: a replica of an earlier version that still runs during a rolling update writes neither the new columns,
-- which are nullable or have defaults, nor the new tables nor the new events, and every row it writes satisfies the
-- widened CHECKs. timeline_entries is partitioned, so each new CHECK is added to the parent NOT VALID, which adds it to
-- every partition, validated in a second transaction, which recurses to every partition, under a lock that lets writes
-- through, and the old CHECKs are dropped in a third, as 0018 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations
    ADD COLUMN team_label text NOT NULL DEFAULT 'team',
    ADD COLUMN teams_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT organizations_team_label_check CHECK (team_label ~ '^[a-zA-Z_][a-zA-Z0-9_]*$') NOT VALID;

CREATE TABLE team_matchers (
    org_id   bigint NOT NULL REFERENCES organizations (id),
    team_id  bigint NOT NULL REFERENCES teams (id),
    label    text NOT NULL CHECK (label <> ''),
    op       text NOT NULL CHECK (op IN ('=', '!=', '=~', '!~')),
    value    text NOT NULL,
    position bigint NOT NULL CHECK (position >= 0),
    PRIMARY KEY (team_id, position)
);

CREATE TABLE alert_teams (
    org_id   bigint NOT NULL REFERENCES organizations (id),
    alert_id bigint NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    team_id  bigint NOT NULL REFERENCES teams (id),
    rule     text NOT NULL CHECK (rule IN ('label', 'matcher')),
    matcher  text,  -- the Team matcher that picked the Alert, in the Alertmanager syntax
    PRIMARY KEY (alert_id, team_id),
    CONSTRAINT alert_teams_matcher_check CHECK ((rule = 'matcher') = (matcher IS NOT NULL))
);

ALTER TABLE alerts ADD COLUMN unknown_team_value text;

CREATE TABLE alert_group_teams (
    org_id         bigint NOT NULL REFERENCES organizations (id),
    alert_group_id bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    team_id        bigint NOT NULL REFERENCES teams (id),
    rule           text NOT NULL CHECK (rule IN ('label', 'matcher')),
    matcher        text,
    joined_at      timestamptz NOT NULL,
    PRIMARY KEY (alert_group_id, team_id),
    CONSTRAINT alert_group_teams_matcher_check CHECK ((rule = 'matcher') = (matcher IS NOT NULL))
);

-- The filter of the Alert Group list and counts by Team, "My Teams" included.
CREATE INDEX alert_group_teams_team_idx ON alert_group_teams (org_id, team_id, alert_group_id);

ALTER TABLE alert_groups ADD COLUMN unknown_team_values text[] NOT NULL DEFAULT '{}';

ALTER TABLE timeline_entries
    ADD COLUMN team_id bigint,       -- teams.id of team_joined
    ADD COLUMN team_rule text,
    ADD COLUMN team_matcher text,
    ADD COLUMN team_value text,      -- the value of unknown_team_value
    ADD CONSTRAINT timeline_entries_team_rule_check CHECK (team_rule IN ('label', 'matcher')) NOT VALID,
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

ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_event_v2_check CHECK (event IN (
        'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued', 'annotations_changed',
        'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'moved_to_default_route',
        'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout',
        'unclaimed', 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed', 'team_joined',
        'unknown_team_value')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations VALIDATE CONSTRAINT organizations_team_label_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_team_rule_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_event_kind_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_event_v2_check;
ALTER TABLE timeline_entries VALIDATE CONSTRAINT timeline_entries_system_v2_check;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_event_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timeline_entries
    DROP CONSTRAINT timeline_entries_event_check,
    DROP CONSTRAINT timeline_entries_event_kind_check,
    DROP CONSTRAINT timeline_entries_system_event_check,
    DROP CONSTRAINT timeline_entries_system_check;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_event_check;

COMMIT;
