-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0049_pages.up.sql. The Pages, the people they named and their outcomes, the runs of Notification rules and
-- their notification_rule_action timers, and the Thread replies of Pages are lost; the Timeline keeps its paged and
-- page_not_delivered entries without the Team mention or token and the importance of a Page. The CHECKs of
-- thread_replies are those of 0001 again and those of the timers those of 0047.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM timers WHERE kind = 'notification_rule_action';
DELETE FROM thread_replies WHERE event = 'paged' OR page_id IS NOT NULL;

ALTER TABLE timers
    DROP CONSTRAINT timers_subject_v3_check,
    DROP CONSTRAINT timers_kind_v3_check,
    DROP COLUMN notification_rule_run_id,
    ADD CONSTRAINT timers_kind_v2_check CHECK (kind IN (
        'ack_timeout', 'reminder', 'snooze_end', 'reopen_window_end', 'grace_period_end', 'storm_calm_check',
        'escalation_step')),
    ADD CONSTRAINT timers_subject_v2_check CHECK (
        (kind = 'storm_calm_check' AND storm_id IS NOT NULL AND alert_group_id IS NULL AND escalation_run_id IS NULL)
        OR (kind = 'escalation_step' AND escalation_run_id IS NOT NULL AND alert_group_id IS NULL
            AND storm_id IS NULL)
        OR (kind NOT IN ('storm_calm_check', 'escalation_step') AND alert_group_id IS NOT NULL AND storm_id IS NULL
            AND escalation_run_id IS NULL));

ALTER TABLE thread_replies
    DROP CONSTRAINT thread_replies_event_v2_check,
    DROP CONSTRAINT thread_replies_mentions_v2_check,
    DROP COLUMN page_source,
    DROP COLUMN paged_user_ids,
    DROP COLUMN page_id,
    ADD CONSTRAINT thread_replies_event_check CHECK (event IN (
        'alerts_added', 'alert_replaced', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'takeover',
        'unacknowledged', 'ack_timeout', 'reminder', 'auto_unacknowledged', 'notices_missed')),
    ADD CONSTRAINT thread_replies_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[]);

ALTER TABLE timeline_entries
    DROP COLUMN page_importance,
    DROP COLUMN page_everyone;

DROP TABLE page_users;
DROP TABLE pages;
DROP TABLE notification_rule_runs;

COMMIT;
