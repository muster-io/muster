-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds connections.managed_by_version, the count of the saves of a Connection that changed its mark managed_by and
-- nothing else (C-33.FR-11). Such a save writes managed_by and moves this counter, not version: the Leader's Slack
-- sockets, its Telegram and Bridge pollers, the cached clients, the copy of a bridge's info and the writes guarded by
-- version — a cursor of events, a hello, what a check learned — all follow version, and a mark changes no behaviour, so
-- they go on as they were. The ETag of a Connection is version plus managed_by_version: each save moves one of them
-- and neither goes back, so a mark alone still gives a new ETag. Every row starts at 0, which keeps the ETags of today.
-- Expand only: a replica without this migration that still runs during a rolling update neither reads nor writes the
-- column; its saves of the mark alone move version as before, and its ETag and its check of If-Match are version alone.
-- Once a Connection's mark alone was saved on a replica with this migration, a client that read it on one kind of
-- replica gets 412 on the other until the rollout ends, and reads it again; and a replica without it may take an ETag
-- that a later save made stale when that save moved version by as much as the counter. One transaction: a NOT NULL
-- column with a constant default is a change of the catalog only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE connections ADD COLUMN managed_by_version bigint NOT NULL DEFAULT 0;

COMMIT;
