-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0047_escalation_runs.up.sql. The runs of Escalation chains, the steps they made and their escalation_step
-- timers are lost, and the escalation entries of the Timeline lose the run, the chain and the step they named; the
-- CHECKs of the timers' kind and subject are those of 0001 again. Alert Groups whose chains ran keep no trace of it:
-- their ack timeout stays as the chains left it until the next start.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM timers WHERE kind = 'escalation_step';

ALTER TABLE timers
    DROP CONSTRAINT timers_subject_v2_check,
    DROP CONSTRAINT timers_kind_v2_check,
    DROP COLUMN escalation_run_id,
    ADD CONSTRAINT timers_kind_check CHECK (kind IN (
        'ack_timeout', 'reminder', 'snooze_end', 'reopen_window_end', 'grace_period_end', 'storm_calm_check')),
    ADD CONSTRAINT timers_subject_check CHECK (
        (kind = 'storm_calm_check' AND storm_id IS NOT NULL AND alert_group_id IS NULL)
        OR (kind <> 'storm_calm_check' AND alert_group_id IS NOT NULL AND storm_id IS NULL));

ALTER TABLE timeline_entries
    DROP COLUMN step_number,
    DROP COLUMN escalation_chain_id,
    DROP COLUMN escalation_run_id;

ALTER TABLE alert_groups
    DROP COLUMN chain_generation;

ALTER TABLE deliveries
    DROP COLUMN published_by_configuration;

DROP TABLE escalation_run_steps;
DROP TABLE escalation_runs;

COMMIT;
