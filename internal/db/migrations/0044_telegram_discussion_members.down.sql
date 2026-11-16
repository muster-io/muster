-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0044_telegram_discussion_members.up.sql. What the checks saw of the discussion groups is lost, and with it the
-- warnings not_in_discussion_group; each Telegram Destination's next check on 0044 finds it again. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations
    DROP COLUMN telegram_absent_user_ids,
    DROP COLUMN telegram_members_checked_at;

COMMIT;
