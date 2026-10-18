-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0013_request_log.up.sql. The records of the Request log are lost. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE request_log;

COMMIT;
