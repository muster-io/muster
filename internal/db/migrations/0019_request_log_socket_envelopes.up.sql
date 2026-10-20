-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Lets an inbound record of the Request log carry an answer without a status (C-23.FR-2, C-24.FR-14): Muster's
-- confirmation of an envelope received over a Slack Socket Mode connection, whose answer has a body but neither a
-- status nor headers. An answer with a status keeps its headers and body, a record without a status has no headers,
-- and only an inbound one may have a body without a status. Expand only: request_log_answer_check is replaced under
-- its name, without a scan, under the exclusive lock, and validated in a second transaction, under a lock that lets
-- writes through; every row an earlier binary writes satisfies it.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE request_log
    DROP CONSTRAINT request_log_answer_check,
    ADD CONSTRAINT request_log_answer_check CHECK ((status IS NULL) = (response_headers IS NULL)
        AND (status IS NULL OR response_body IS NOT NULL)
        AND (status IS NOT NULL OR response_body IS NULL OR direction = 'inbound')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE request_log VALIDATE CONSTRAINT request_log_answer_check;

COMMIT;
