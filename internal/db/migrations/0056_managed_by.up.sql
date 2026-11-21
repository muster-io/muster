-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds managed_by, the name of the tool that keeps an object as code, such as terraform, or null (C-33.FR-11), to the
-- twelve kinds of configuration a tool may manage: Integrations, Routes, Teams, Schedules, Escalation chains, Lookup
-- tables, Link rules, Connections, Destinations, the Organization's settings, the Outbound address policy and the OIDC
-- settings. It is a mark only: nothing but the reads and writes of those objects reads it. The domain packages check
-- its format (internal/managedby), so the columns have no CHECK, and a later change of the format needs no migration;
-- no query looks them up, so they have no index. Expand only: a replica of a development image without this migration
-- that still runs during a rolling update neither reads nor writes them, and a write it makes keeps the mark. One
-- transaction: the columns are nullable without a default, a change of the catalog only.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE integrations ADD COLUMN managed_by text;
ALTER TABLE routes ADD COLUMN managed_by text;
ALTER TABLE teams ADD COLUMN managed_by text;
ALTER TABLE schedules ADD COLUMN managed_by text;
ALTER TABLE escalation_chains ADD COLUMN managed_by text;
ALTER TABLE lookup_tables ADD COLUMN managed_by text;
ALTER TABLE link_rules ADD COLUMN managed_by text;
ALTER TABLE connections ADD COLUMN managed_by text;
ALTER TABLE destinations ADD COLUMN managed_by text;
ALTER TABLE organizations ADD COLUMN managed_by text;
ALTER TABLE outbound_policies ADD COLUMN managed_by text;
ALTER TABLE oidc_settings ADD COLUMN managed_by text;

COMMIT;
