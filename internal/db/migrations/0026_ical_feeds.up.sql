-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0026 adds the personal iCal feeds of C-27.FR-10: ical_feeds, one per User at most, with the SHA-256 of
-- its token (ical.token, `mstr_cal_` and 32 random bytes), which the feed is looked up by, and when it was issued.
-- Issuing a feed again replaces the row; revoking it and deleting its User delete it. Expand only: the new table is
-- empty and an earlier binary reads and writes nothing of it.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE ical_feeds (
    org_id     bigint NOT NULL REFERENCES organizations (id),
    user_id    bigint PRIMARY KEY REFERENCES users (id),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    issued_at  timestamptz NOT NULL
);

COMMIT;
