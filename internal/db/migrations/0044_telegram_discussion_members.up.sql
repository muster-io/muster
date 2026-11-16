-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds what the last Destination check of a Telegram Destination saw of the members of its discussion group
-- (C-27.FR-8): destinations.telegram_absent_user_ids, the users.id of the Muster Users of the Teams that name the
-- Destination whom getChatMember answered as outside the group (left, kicked, or 400 PARTICIPANT_ID_INVALID, F-128) —
-- Muster User ids, not Telegram user ids — and destinations.telegram_members_checked_at, when that check ran, on the
-- business clock. Both are null until a check on a replica with this migration sets them, and stay null for the other
-- types. They are what a check found, not configuration: writing them bumps no version and records no Audit log entry.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update reads and writes neither; a check it runs leaves them as the last check on a newer replica left them,
-- so that the warnings of the Schedules may be as old as that check, which its time shows. One transaction: the
-- columns are nullable without a default, a change of the catalog only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE destinations
    ADD COLUMN telegram_absent_user_ids bigint[],
    ADD COLUMN telegram_members_checked_at timestamptz;

COMMIT;
