-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0036 adds the Broken state of a Notification gateway (C-30.FR-8) to connections, kept apart from the
-- health of the bridge's Destinations: gateway_broken_since, when a call answered with a Fatal error or used up
-- delivery.transient_budget made it Broken, and gateway_broken_reason, the masked error; gateway_next_probe_at, when
-- any replica probes it through info again, every delivery.broken_probe_interval; and the lease of that probe,
-- gateway_probe_lease_owner and gateway_probe_lease_until; and gateway_recovered_at, when the last Broken state ended,
-- so that a call that came due before it is never sent, whichever replica prepares it. broken_since, next_probe_at
-- and recovered_at are on the business clock, the lease on the real clock, as every row lease. A partial index on the
-- calls that ended Not delivered serves the Leader's reconciliation, which makes a gateway Broken whose call failed
-- after its last Broken state ended when the replica that ended the call stopped before it made the gateway Broken. A CHECK keeps the three columns of the state set or null together,
-- another the lease's pair; a partial index backs the claim of the due probes.
-- Expand only: a replica of an earlier version that still runs during a rolling update writes none of the columns,
-- which are nullable, and sends the calls of a Broken gateway, which then fail as before; a Connection it creates or
-- changes is never Broken.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections
    ADD COLUMN gateway_broken_since timestamptz,
    ADD COLUMN gateway_broken_reason text,
    ADD COLUMN gateway_next_probe_at timestamptz,
    ADD COLUMN gateway_probe_lease_owner text,
    ADD COLUMN gateway_probe_lease_until timestamptz,
    ADD COLUMN gateway_recovered_at timestamptz,
    ADD CONSTRAINT connections_gateway_broken_check CHECK (
        (gateway_broken_since IS NULL) = (gateway_broken_reason IS NULL)
        AND (gateway_broken_since IS NULL) = (gateway_next_probe_at IS NULL)),
    ADD CONSTRAINT connections_gateway_probe_lease_check CHECK (
        (gateway_probe_lease_owner IS NULL) = (gateway_probe_lease_until IS NULL));

-- The claim of the due probes of Broken gateways.
CREATE INDEX connections_gateway_probe_idx ON connections (org_id, gateway_next_probe_at)
    WHERE gateway_broken_since IS NOT NULL;
-- The calls of a gateway that ended Not delivered lately, for the reconciliation of its Broken state.
CREATE INDEX gateway_calls_failed_idx ON gateway_calls (org_id, connection_id, ended_at)
    WHERE state = 'not_delivered';

COMMIT;
