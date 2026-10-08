-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0004_thread_replies_pending_index.up.sql.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP INDEX thread_replies_pending_idx;

COMMIT;
