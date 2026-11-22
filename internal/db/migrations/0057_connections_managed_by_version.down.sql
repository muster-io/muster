-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0057_connections_managed_by_version.up.sql. Only the counter is lost: the marks stay, and the ETag of a
-- Connection whose mark alone was saved goes back to its version, so a client holding an ETag read before gets one
-- 412 and reads again. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections DROP COLUMN managed_by_version;

COMMIT;
