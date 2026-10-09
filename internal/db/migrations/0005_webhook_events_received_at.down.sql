-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0005_webhook_events_received_at.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE webhook_events DROP COLUMN received_at;

COMMIT;
