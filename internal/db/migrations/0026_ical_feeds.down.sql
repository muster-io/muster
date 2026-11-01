-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0026_ical_feeds.up.sql. Every iCal feed is lost; the Audit log entries about the feeds stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP TABLE ical_feeds;

COMMIT;
