-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the deliveries of Alert Groups that no worker has to send by their last change, which the retention of
-- deliveries walks (DeleteExpiredDeliveries in alert_group_retention, C-09.FR-16): the deliveries before its cutoff are
-- almost all deleted, so each run reads few more rows than it deletes. Walked from the Alert Groups resolved before the
-- cutoff, a run read every summary row kept for retention.alert_group_summaries, those whose deliveries were already
-- gone included. The query's conditions imply the predicate with constants only, no parameters, so that both custom
-- and generic plans can use it. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX deliveries_retention_idx ON deliveries (org_id, updated_at)
    WHERE state <> 'pending' AND alert_group_id IS NOT NULL;

COMMIT;
