-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Indexes the Thread replies of Pages still to be sent, which every round of the delivery worker looks up to drop
-- those of the Broken Destinations and settle their Pages (DropBrokenPageReplies, C-32.FR-10). Without it the round
-- read every reply of a Page ever sent through thread_replies_page_idx, or every pending reply through
-- thread_replies_due_idx, while any Destination was Broken. Expand only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX thread_replies_page_pending_idx ON thread_replies (org_id, id)
    WHERE page_id IS NOT NULL AND state IN ('collecting', 'pending');

COMMIT;
