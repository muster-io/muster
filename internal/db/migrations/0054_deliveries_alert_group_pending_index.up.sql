-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the pending deliveries of Alert Groups by Alert Group, which each batch of the walks of a change of
-- configuration reads after its cursor to list the resolved Alert Groups whose final edit still waits
-- (ListOpenGroups…After, ListEvaluatedGroupsAfter, C-12.FR-12, C-26.FR-9). Without it PostgreSQL walked
-- deliveries_alert_group_key, every delivery ever made, from the cursor and filtered on the state, or the primary key
-- of alert_groups, so a batch read the whole history. The queries' conditions imply the predicate with constants only,
-- no parameters, so that both custom and generic plans can use it. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX deliveries_alert_group_pending_idx ON deliveries (org_id, alert_group_id)
    WHERE state = 'pending' AND alert_group_id IS NOT NULL;

COMMIT;
