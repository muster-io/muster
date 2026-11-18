-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0050_assignees.up.sql. The Assignees of firing Alert Groups are lost, the Timeline keeps its
-- assignees_changed entries without the Assignees after each change, and the Pages and rule runs lose the run step
-- that made them.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE notification_rule_runs DROP COLUMN run_step_seq;

ALTER TABLE pages DROP COLUMN run_step_seq;

ALTER TABLE timeline_entries DROP COLUMN assignees;

DROP TABLE alert_group_assignees;

COMMIT;
