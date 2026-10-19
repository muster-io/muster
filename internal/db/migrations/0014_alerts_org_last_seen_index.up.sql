-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the Alerts of an Organization by last_seen_at, which the values seen of the severity label
-- (ListSeverityValues) look up for every Integration at once: alerts_last_seen_idx leads with integration_id, so it
-- cannot range over last_seen_at alone, and PostgreSQL read every Alert of the Organization, the resolved ones that
-- retention.alert_details keeps included. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX alerts_org_last_seen_idx ON alerts (org_id, last_seen_at);

COMMIT;
