-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Records, on each outgoing webhook event, the receipt time of the Stored Snapshot behind the change that queued it,
-- null for a change made by a Command or a timer, so that the call that delivers the event observes
-- muster_delivery_latency_seconds as a Root message does. Expand only: a nullable column without a default.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE webhook_events ADD COLUMN received_at timestamptz;

COMMIT;
