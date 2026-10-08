-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the pending Thread replies of each delivery, which the claim of due replies (ClaimDueReplies) and the
-- worker's next wake (NextDeliveryWork) look up for every candidate: "an earlier reply of its delivery is pending".
-- Without it PostgreSQL scanned every pending reply for each candidate, so a backlog of replies slowed each claim to
-- seconds and delivery fell further behind. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX thread_replies_pending_idx ON thread_replies (delivery_id, id) WHERE state = 'pending';

COMMIT;
