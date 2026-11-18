-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0050 adds the Assignees of Alert Groups (C-32.FR-12): alert_group_assignees, for each run of an
-- Escalation chain on an Alert Group, the people its latest step Paging people by name reached, with the owning Team of
-- the chain when its run started, the step that made them and its place among the steps the run made, and since when;
-- and the Assignees after a change in the Timeline entry of assignees_changed (timeline_entries.assignees). Migration
-- 0034 already gave the Timeline the event assignees_changed of the kind escalation.
-- run_step_seq is the seq of the escalation_run_steps row of the step made — a repeated step included — on a Page of
-- its first Mentions (pages.run_step_seq) and on the rule runs it started (notification_rule_runs.run_step_seq), whose
-- later Mentions belong to that step; on an Assignee it is the step whose reach made the chain's Assignees. A reach by
-- a step with a greater seq replaces the chain's Assignees, one with the same seq adds to them, and one with a smaller
-- seq changes nothing. Pages and rule runs written before this migration have none: a reach by such a Page adds only to
-- a run without Assignees, or whose Assignees have none either.
-- A row of alert_group_assignees lives only while its Alert Group is firing: Acknowledge, Snooze and Resolve delete the
-- rows in their transaction, so the table holds the Assignees of firing Alert Groups alone. Its rows cascade from their
-- Alert Group and from their run, whose cascades scan few rows for that reason; a Team is only soft-deleted, so the
-- SET NULL of team_id never acts.
-- Expand only: the table is new and empty, and the new columns are nullable; an earlier binary writes none of them,
-- and its Pages and rule runs keep run_step_seq null. ADD COLUMN with a CHECK validates it under the ACCESS EXCLUSIVE
-- lock of the ALTER, scanning the table; pages and notification_rule_runs are young — 0049 added them, and their rows
-- go with their Alert Groups — so the scan is short, and a column that is null on every row passes it without a
-- rewrite, as in 0049.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE alert_group_assignees (
    org_id            bigint NOT NULL REFERENCES organizations (id),
    alert_group_id    bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    escalation_run_id bigint NOT NULL REFERENCES escalation_runs (id) ON DELETE CASCADE,
    step_number       integer NOT NULL CHECK (step_number BETWEEN 1 AND 20),
    run_step_seq      integer CHECK (run_step_seq > 0),  -- null for a Page written before this migration
    user_id           bigint NOT NULL REFERENCES users (id),
    -- the owning Team of the chain when its run started; Teams are only soft-deleted, so the foreign key never acts
    team_id           bigint REFERENCES teams (id) ON DELETE SET NULL,
    since             timestamptz NOT NULL,
    PRIMARY KEY (alert_group_id, escalation_run_id, user_id)
);

-- The filter Assignee of the Alert Group list and counts ("I am an Assignee").
CREATE INDEX alert_group_assignees_user_idx ON alert_group_assignees (org_id, user_id, alert_group_id);

ALTER TABLE timeline_entries
    ADD COLUMN assignees jsonb;   -- the Assignees after an assignees_changed, by internal ids

ALTER TABLE pages
    ADD COLUMN run_step_seq integer CHECK (run_step_seq > 0);  -- the run step of the first Mentions of a step

ALTER TABLE notification_rule_runs
    ADD COLUMN run_step_seq integer CHECK (run_step_seq > 0);  -- the run step that started the rule run

COMMIT;
