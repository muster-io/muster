-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the Alert Groups that the evaluation on a save of a Team or of the Team label classifies again, the open
-- ones and those still in their Grace period, which each batch lists by id after its cursor behind the exclusive
-- evaluation lock (ListEvaluatedGroupsAfter, C-25.FR-13). Without it PostgreSQL walked the primary key from the cursor
-- and filtered out every resolved Alert Group, so a batch read the whole history while Snapshots of the Organization
-- waited. The query's condition implies the predicate with constants only, no parameters, so that both custom and
-- generic plans can use it. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX alert_groups_evaluated_idx ON alert_groups (org_id, id)
    WHERE status <> 'resolved' OR grace_deadline IS NOT NULL;

COMMIT;
