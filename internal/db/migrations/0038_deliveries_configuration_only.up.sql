-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0038 adds deliveries.configuration_only, set while the only work a pending delivery has is an edit of its
-- published Root message that a change of configuration made — a re-render of the open Root messages after a change of
-- a button set, of the message settings or of a Route's "Labels in the message", the evaluation of the Teams, a raised
-- Minimum severity or the re-render after a key rotation — and cleared by every change an Alert, a Command or a timer
-- with a lifecycle event makes, by a Publication, by a final edit and once the edit is delivered. The claim of due
-- deliveries takes these edits after every other due delivery of the same urgency, always last among the Root messages
-- of one urgency, so that a re-render of many open Root messages, which waits for the messengers' limiters, never
-- delays a Publication or an edit of a Root message that an Alert or a Command made; the edits wait as long as those
-- use up the limiter, and none is dropped. Thread replies are not ranked. deliveries_due_idx is replaced by
-- deliveries_due_v2_idx, which orders the pending deliveries as the claim does: Urgent first, then the edits that are
-- not configuration only, each by next_attempt_at. Every existing delivery starts at false, so a re-render still
-- pending at the upgrade keeps its place.
-- Expand only: the column has a default. A replica of an earlier version that still runs during a rolling update claims
-- in its own order, which the new index no longer matches: PostgreSQL may read the due pending deliveries through it,
-- or through a bitmap scan, and sort them all. It writes none of the column: an edit, a final edit or a republication
-- it queues for a delivery that a re-render marked keeps the mark and waits behind the other pending deliveries of its
-- urgency until it is delivered, and a delivery by a replica of this version clears it. A mark left on a delivered row
-- is read only by a replica of an earlier version: on this version the next change of the Desired state sets the mark
-- afresh.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE deliveries ADD COLUMN configuration_only boolean NOT NULL DEFAULT false;

-- Reconciliation claim: due work, Urgent first, then the edits that a change of configuration alone made last.
CREATE INDEX deliveries_due_v2_idx ON deliveries (org_id, urgent DESC, configuration_only, next_attempt_at)
    WHERE state = 'pending';
DROP INDEX deliveries_due_idx;

COMMIT;
