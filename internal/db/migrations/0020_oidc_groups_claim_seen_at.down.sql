-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0020_oidc_groups_claim_seen_at.up.sql. A check of discovery that does not list the groups claim raises
-- `groups_claim_missing` again until a sign-in finds the claim.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE oidc_settings DROP COLUMN groups_claim_seen_at;

COMMIT;
