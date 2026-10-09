-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Makes deliveries.published_at the mark of a Root message that exists, in place of message_id: an outgoing webhook in
-- the template mode may publish with no message id to extract (C-15.FR-4). A Root message forgotten so that it is
-- published again kept the time of the first Publication; from now on it is cleared with the message id, and this
-- clears it on the rows forgotten before. Data only, no change of the columns: a row without a message id that still
-- has published_at waits for its republication, which the earlier binary also read as unpublished.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE deliveries SET published_at = NULL WHERE message_id IS NULL AND published_at IS NOT NULL;

COMMIT;
