-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0047 adds the runs of Escalation chains on Alert Groups (C-32.FR-4 to FR-7, FR-14 to FR-16, FR-18):
-- escalation_runs, one run of a chain on an Alert Group from a start of its chains, with the sources that named the
-- chain, the steps the chain had when the run started (P-73), where it is, the passes of its repeats, its state and
-- what stopped it; escalation_run_steps, the steps a run made or skipped, for "How it went"; the timer kind
-- escalation_step, whose timer names its run in timers.escalation_run_id instead of an Alert Group, one per run
-- (timers_escalation_run_key); the columns of the escalation entries of the Timeline that name the run, the chain and
-- the step; alert_groups.chain_generation, which each start or start-over of the chains of an Alert Group raises; and
-- deliveries.published_by_configuration, a first Publication that a change of configuration released, which starts
-- no chain.
-- The timers of runs name the run so that the unique index timers_alert_group_key, one timer per Alert Group and kind,
-- and the UpsertTimer statement whose ON CONFLICT infers it stay as they are: several chains run on one Alert Group,
-- each with its timer, and replicas of 0.1 or of an earlier development image keep setting their ack timeout,
-- Reminder and Snooze timers with the statement they have. A run cascades from its Alert Group and its timer from the
-- run, so deleting an Alert Group still deletes every timer about it.
-- Expand only: the new tables are empty, the new columns are nullable or have defaults that an earlier binary never
-- needs to write, and an earlier binary never claims the new kind, since it claims only the kinds it has handlers for.
-- The CHECKs of the timers' kind and subject are replaced by _v2 CHECKs added NOT VALID, validated in a second
-- transaction under a lock that lets writes through, and the old ones are dropped in a third, as 0010 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE escalation_runs (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    public_id      text NOT NULL CHECK (public_id ~ '^ER[0-9A-HJKMNP-TV-Z]{12}$'),
    alert_group_id bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    chain_id       bigint REFERENCES escalation_chains (id) ON DELETE SET NULL,
    chain_name     text NOT NULL,
    -- the owning Team the chain had when the run started, whose Working hours its steps use; Teams are only
    -- soft-deleted, so the foreign key never acts
    owning_team_id bigint REFERENCES teams (id) ON DELETE SET NULL,
    sources        jsonb NOT NULL,                -- the Route and the Teams that named the chain (P-70)
    steps          jsonb NOT NULL,                -- the chain's steps when the run started (P-73)
    step_index     integer NOT NULL DEFAULT 0 CHECK (step_index >= 0),  -- the current step, from 0
    repeats        jsonb NOT NULL DEFAULT '{}',   -- the passes of each repeat step, by its number
    state          text NOT NULL CHECK (state IN ('running', 'ran_out', 'stopped')),
    generation     bigint NOT NULL CHECK (generation > 0),  -- the start of the Alert Group's chains it belongs to
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz,
    stop_command   text CHECK (stop_command IN ('acknowledge', 'snooze', 'resolve', 'started_over')),
    stop_event_seq bigint,                        -- the lifecycle event of the change that stopped it
    UNIQUE (org_id, public_id),
    CONSTRAINT escalation_runs_ended_check CHECK ((state = 'running') = (ended_at IS NULL)),
    CONSTRAINT escalation_runs_stop_check CHECK ((stop_command IS NOT NULL) = (state = 'stopped'))
);

-- The runs of the current start of an Alert Group, for its read and for whether every one ran out; it also serves the
-- cascade of the deletion of an Alert Group, by alert_group_id alone.
CREATE INDEX escalation_runs_group_idx ON escalation_runs (alert_group_id, generation);
-- "How it went": the recent runs of a chain; it also serves the SET NULL of the deletion of a chain.
CREATE INDEX escalation_runs_chain_idx ON escalation_runs (chain_id, started_at DESC, id DESC);
-- A chain runs once per start of an Alert Group's chains, however many sources name it (P-70).
CREATE UNIQUE INDEX escalation_runs_chain_key ON escalation_runs (alert_group_id, generation, chain_id)
    WHERE chain_id IS NOT NULL;

CREATE TABLE escalation_run_steps (
    org_id  bigint NOT NULL REFERENCES organizations (id),
    run_id  bigint NOT NULL REFERENCES escalation_runs (id) ON DELETE CASCADE,
    seq     integer NOT NULL CHECK (seq > 0),
    number  integer NOT NULL CHECK (number BETWEEN 1 AND 20),
    kind    text NOT NULL CHECK (kind IN ('page_on_call', 'page_people', 'page_team', 'call', 'wait', 'webhook',
        'repeat')),
    at      timestamptz NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('made', 'skipped')),
    reason  text,                                 -- why a skipped step was skipped (EscalationSkipReason)
    PRIMARY KEY (run_id, seq),
    CONSTRAINT escalation_run_steps_reason_check CHECK ((outcome = 'skipped') = (reason IS NOT NULL))
);

-- The retention of the details: the steps made before the cutoff, the only ones whose Alert Group can have resolved
-- before it, so that it reads the steps still present instead of every Alert Group resolved long ago.
CREATE INDEX escalation_run_steps_at_idx ON escalation_run_steps (org_id, at);

ALTER TABLE timers
    ADD COLUMN escalation_run_id bigint REFERENCES escalation_runs (id) ON DELETE CASCADE,
    ADD CONSTRAINT timers_kind_v2_check CHECK (kind IN (
        'ack_timeout', 'reminder', 'snooze_end', 'reopen_window_end', 'grace_period_end', 'storm_calm_check',
        'escalation_step')) NOT VALID,
    ADD CONSTRAINT timers_subject_v2_check CHECK (
        (kind = 'storm_calm_check' AND storm_id IS NOT NULL AND alert_group_id IS NULL AND escalation_run_id IS NULL)
        OR (kind = 'escalation_step' AND escalation_run_id IS NOT NULL AND alert_group_id IS NULL
            AND storm_id IS NULL)
        OR (kind NOT IN ('storm_calm_check', 'escalation_step') AND alert_group_id IS NOT NULL AND storm_id IS NULL
            AND escalation_run_id IS NULL)) NOT VALID;

-- One timer per run; it also serves the cascade of the deletion of a run. Only the timers of runs name a run
-- (timers_subject_v2_check), so the predicate is the run's.
CREATE UNIQUE INDEX timers_escalation_run_key ON timers (escalation_run_id) WHERE escalation_run_id IS NOT NULL;

ALTER TABLE timeline_entries
    ADD COLUMN escalation_run_id bigint,          -- no foreign key: entries outlive the runs of a pruned Alert Group
    ADD COLUMN escalation_chain_id bigint,
    ADD COLUMN step_number integer;

ALTER TABLE alert_groups
    ADD COLUMN chain_generation bigint NOT NULL DEFAULT 0;

-- A delivery whose first Publication a change of configuration released, such as a raised Minimum severity that made
-- a held Alert Group Urgent: that Publication starts no Escalation chain (ADR-0018). An earlier binary never sets it,
-- so its releases start chains as Publications do.
ALTER TABLE deliveries
    ADD COLUMN published_by_configuration boolean NOT NULL DEFAULT false;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE timers VALIDATE CONSTRAINT timers_kind_v2_check;
ALTER TABLE timers VALIDATE CONSTRAINT timers_subject_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE timers DROP CONSTRAINT timers_kind_check;
ALTER TABLE timers DROP CONSTRAINT timers_subject_check;

COMMIT;
