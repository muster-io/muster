-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0019_request_log_socket_envelopes.up.sql. The CHECK of 0013 is restored under its name NOT VALID, which holds
-- every new row from then on, and the records it refuses — those with an answer but no status, the envelopes of Slack
-- Socket Mode — are deleted in the same transaction and lost: troubleshooting data. The CHECK is validated in a second
-- transaction. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE request_log
    DROP CONSTRAINT request_log_answer_check,
    ADD CONSTRAINT request_log_answer_check CHECK ((status IS NULL) = (response_headers IS NULL)
        AND (status IS NULL) = (response_body IS NULL)) NOT VALID;

DELETE FROM request_log WHERE status IS NULL AND response_body IS NOT NULL;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE request_log VALIDATE CONSTRAINT request_log_answer_check;

COMMIT;
