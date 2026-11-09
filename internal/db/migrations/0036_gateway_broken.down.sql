-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0036_gateway_broken.up.sql. A Broken Notification gateway forgets its state and its calls are sent again;
-- an open MusterNotificationGatewayBroken stays open, as no version before 0036 resolves it.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX connections_gateway_probe_idx;

ALTER TABLE connections
    DROP CONSTRAINT connections_gateway_probe_lease_check,
    DROP CONSTRAINT connections_gateway_broken_check,
    DROP COLUMN gateway_recovered_at,
    DROP COLUMN gateway_probe_lease_until,
    DROP COLUMN gateway_probe_lease_owner,
    DROP COLUMN gateway_next_probe_at,
    DROP COLUMN gateway_broken_reason,
    DROP COLUMN gateway_broken_since;

COMMIT;
