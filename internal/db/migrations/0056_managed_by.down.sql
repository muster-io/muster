-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0056_managed_by.up.sql. Only the marks are lost: the objects stay as they were, and the next write of a tool
-- that manages one marks it again on 0056. Up again works.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE integrations DROP COLUMN managed_by;
ALTER TABLE routes DROP COLUMN managed_by;
ALTER TABLE teams DROP COLUMN managed_by;
ALTER TABLE schedules DROP COLUMN managed_by;
ALTER TABLE escalation_chains DROP COLUMN managed_by;
ALTER TABLE lookup_tables DROP COLUMN managed_by;
ALTER TABLE link_rules DROP COLUMN managed_by;
ALTER TABLE connections DROP COLUMN managed_by;
ALTER TABLE destinations DROP COLUMN managed_by;
ALTER TABLE organizations DROP COLUMN managed_by;
ALTER TABLE outbound_policies DROP COLUMN managed_by;
ALTER TABLE oidc_settings DROP COLUMN managed_by;

COMMIT;
