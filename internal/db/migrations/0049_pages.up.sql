-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0049 adds the Pages by Mention of Escalation chains and the runs of Notification rules (C-32.FR-3, FR-8 to
-- FR-10, FR-19, C-31.FR-3): notification_rule_runs, one run of a person's normal or important rule for a step that
-- Paged them, with the actions the rule had when it started and where it is; pages, one Page of a step or of an action
-- of a rule, with what its Thread replies say and its outcome, and page_users, the people it names with each one's
-- outcome; the timer kind notification_rule_action, the next action of a rule run after a wait, whose subject is its
-- rule run in timers.notification_rule_run_id, one per rule run (timers_notification_rule_run_key); the Thread replies
-- of Pages, thread_replies.page_id with the people a reply mentions (paged_user_ids) and the source of the chain whose
-- Destination it is in (page_source), and the event and Mention paged of thread_replies; and the Team mention or token
-- and the importance of a Page in the Timeline (page_everyone, page_importance; team_id names the Team of a page_team
-- step). Migration 0034 already gave the Timeline the event and Mention paged, paged_user_ids and the kind escalation,
-- and webhook_events the event paged.
-- The timers of rule runs name the run, as those of the runs of chains do (0047), so that timers_alert_group_key and
-- the UpsertTimer statement stay as they are: one step may Page several people, each with a rule run that waits. A
-- rule run cascades from its Alert Group and from the run of its chain, and its timer from it; a Page cascades from its
-- Alert Group, and its Thread replies and people from it.
-- Expand only: the new tables are empty, the new columns are nullable, an earlier binary writes neither the event paged
-- in thread_replies nor the new timer kind, which it never claims. The CHECKs of thread_replies and of the timers are
-- replaced by _v2 and _v3 CHECKs added NOT VALID, validated in a second transaction under a lock that lets writes
-- through, and the old ones are dropped in a third, as 0047 does. The foreign keys of the new columns of those two
-- tables are added NOT VALID and validated with the CHECKs, so that the scan of their validation runs in the second
-- transaction; the indexes on those columns are still built in the first.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE notification_rule_runs (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id            bigint NOT NULL REFERENCES organizations (id),
    alert_group_id    bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    escalation_run_id bigint NOT NULL REFERENCES escalation_runs (id) ON DELETE CASCADE,
    step_number       integer NOT NULL CHECK (step_number BETWEEN 1 AND 20),
    user_id           bigint NOT NULL REFERENCES users (id),
    importance        text NOT NULL CHECK (importance IN ('normal', 'important')),
    actions           jsonb NOT NULL,                       -- the rule when the run started
    position          integer NOT NULL DEFAULT 0 CHECK (position >= 0),  -- the next action, from 0
    state             text NOT NULL CHECK (state IN ('running', 'done', 'stopped')),
    started_at        timestamptz NOT NULL,
    ended_at          timestamptz,
    CONSTRAINT notification_rule_runs_ended_check CHECK ((state = 'running') = (ended_at IS NULL))
);

-- The rule runs of a run of a chain, which its stop stops; it also serves the cascade of the run's deletion.
CREATE INDEX notification_rule_runs_run_idx ON notification_rule_runs (escalation_run_id);
-- The cascade of the deletion of an Alert Group.
CREATE INDEX notification_rule_runs_group_idx ON notification_rule_runs (alert_group_id);

CREATE TABLE pages (
    id                       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    alert_group_id           bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    escalation_run_id        bigint REFERENCES escalation_runs (id) ON DELETE CASCADE,
    -- the rule run of a later Mention of a rule; null for the first Mentions of a step, which may name several people
    notification_rule_run_id bigint REFERENCES notification_rule_runs (id) ON DELETE CASCADE,
    step_number              integer NOT NULL CHECK (step_number BETWEEN 1 AND 20),
    kind                     text NOT NULL CHECK (kind IN ('mention', 'call')),
    team_id                  bigint REFERENCES teams (id),   -- the Team of a page_team step
    everyone                 text CHECK (everyone IN ('channel', 'here', 'all', 'everyone')),
    importance               text CHECK (importance IN ('normal', 'important')),  -- null for a page_team step
    schedule_id              bigint REFERENCES schedules (id) ON DELETE SET NULL,  -- of a page_on_call step
    -- the Team of "Chain of the Team": the run's owning Team, else the first Team that named the chain
    chain_team_id            bigint REFERENCES teams (id),
    again_seconds            integer CHECK (again_seconds > 0),  -- the wait until the rule's next Mention
    again                    boolean NOT NULL DEFAULT false,     -- a later Mention of a rule
    state                    text NOT NULL CHECK (state IN ('pending', 'delivered', 'not_delivered')),
    reason                   text CHECK (reason IN ('no_root_message', 'no_account_link', 'not_delivered')),
    created_at               timestamptz NOT NULL,
    ended_at                 timestamptz,
    CONSTRAINT pages_ended_check CHECK ((state = 'pending') = (ended_at IS NULL)),
    CONSTRAINT pages_state_reason_check CHECK (reason IS NULL OR state = 'not_delivered'),
    CONSTRAINT pages_target_check CHECK (team_id IS NULL OR everyone IS NULL)
);

-- The Pages of a run, for "How it went"; it also serves the cascade of the run's deletion.
CREATE INDEX pages_run_idx ON pages (escalation_run_id);
-- The cascade of the deletion of an Alert Group, and of a rule run.
CREATE INDEX pages_group_idx ON pages (alert_group_id);
CREATE INDEX pages_rule_run_idx ON pages (notification_rule_run_id) WHERE notification_rule_run_id IS NOT NULL;
-- The ON DELETE SET NULL of a Schedule's deletion.
CREATE INDEX pages_schedule_idx ON pages (schedule_id) WHERE schedule_id IS NOT NULL;
-- The Pages whose outcome is still open, which the delivery worker settles.
CREATE INDEX pages_pending_idx ON pages (org_id, id) WHERE state = 'pending';

CREATE TABLE page_users (
    org_id  bigint NOT NULL REFERENCES organizations (id),
    page_id bigint NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users (id),
    outcome text NOT NULL CHECK (outcome IN ('pending', 'delivered', 'not_delivered')),
    reason  text CHECK (reason IN ('no_root_message', 'no_account_link', 'not_delivered')),
    PRIMARY KEY (page_id, user_id),
    CONSTRAINT page_users_outcome_reason_check CHECK ((outcome = 'not_delivered') = (reason IS NOT NULL))
);

ALTER TABLE thread_replies
    ADD COLUMN page_id bigint,
    ADD COLUMN paged_user_ids bigint[],          -- users.id of the people the reply mentions
    ADD COLUMN page_source text CHECK (page_source IN ('route', 'team')),
    ADD CONSTRAINT thread_replies_page_id_fkey FOREIGN KEY (page_id) REFERENCES pages (id) ON DELETE CASCADE
        NOT VALID,
    ADD CONSTRAINT thread_replies_event_v2_check CHECK (event IN (
        'alerts_added', 'alert_replaced', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved', 'takeover',
        'unacknowledged', 'ack_timeout', 'reminder', 'auto_unacknowledged', 'notices_missed', 'paged')) NOT VALID,
    ADD CONSTRAINT thread_replies_mentions_v2_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent', 'paged']::text[]) NOT VALID;

-- The Thread replies of a Page, whose outcomes make the Page's; it also serves the cascade of a Page's deletion.
CREATE INDEX thread_replies_page_idx ON thread_replies (page_id) WHERE page_id IS NOT NULL;

ALTER TABLE timers
    ADD COLUMN notification_rule_run_id bigint,
    ADD CONSTRAINT timers_notification_rule_run_id_fkey FOREIGN KEY (notification_rule_run_id)
        REFERENCES notification_rule_runs (id) ON DELETE CASCADE NOT VALID,
    ADD CONSTRAINT timers_kind_v3_check CHECK (kind IN (
        'ack_timeout', 'reminder', 'snooze_end', 'reopen_window_end', 'grace_period_end', 'storm_calm_check',
        'escalation_step', 'notification_rule_action')) NOT VALID,
    ADD CONSTRAINT timers_subject_v3_check CHECK (
        (kind = 'storm_calm_check' AND storm_id IS NOT NULL AND alert_group_id IS NULL AND escalation_run_id IS NULL
            AND notification_rule_run_id IS NULL)
        OR (kind = 'escalation_step' AND escalation_run_id IS NOT NULL AND alert_group_id IS NULL
            AND storm_id IS NULL AND notification_rule_run_id IS NULL)
        OR (kind = 'notification_rule_action' AND notification_rule_run_id IS NOT NULL AND alert_group_id IS NULL
            AND storm_id IS NULL AND escalation_run_id IS NULL)
        OR (kind NOT IN ('storm_calm_check', 'escalation_step', 'notification_rule_action')
            AND alert_group_id IS NOT NULL AND storm_id IS NULL AND escalation_run_id IS NULL
            AND notification_rule_run_id IS NULL)) NOT VALID;

-- One timer per rule run; it also serves the cascade of the deletion of a rule run. Only the timers of rule runs name
-- one (timers_subject_v3_check), so the predicate is the run's, which the cascade's DELETE implies.
CREATE UNIQUE INDEX timers_notification_rule_run_key ON timers (notification_rule_run_id)
    WHERE notification_rule_run_id IS NOT NULL;

ALTER TABLE timeline_entries
    ADD COLUMN page_everyone text,                -- the token of a page_team step for everyone in the chat
    ADD COLUMN page_importance text;              -- normal or important, of a Page of people

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE thread_replies VALIDATE CONSTRAINT thread_replies_page_id_fkey;
ALTER TABLE thread_replies VALIDATE CONSTRAINT thread_replies_event_v2_check;
ALTER TABLE thread_replies VALIDATE CONSTRAINT thread_replies_mentions_v2_check;
ALTER TABLE timers VALIDATE CONSTRAINT timers_notification_rule_run_id_fkey;
ALTER TABLE timers VALIDATE CONSTRAINT timers_kind_v3_check;
ALTER TABLE timers VALIDATE CONSTRAINT timers_subject_v3_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE thread_replies DROP CONSTRAINT thread_replies_event_check;
ALTER TABLE thread_replies DROP CONSTRAINT thread_replies_mentions_check;
ALTER TABLE timers DROP CONSTRAINT timers_kind_v2_check;
ALTER TABLE timers DROP CONSTRAINT timers_subject_v2_check;

COMMIT;
