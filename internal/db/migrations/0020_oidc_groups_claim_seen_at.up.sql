-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Records when a sign-in or a link last found the groups claim in the identity provider's tokens, so that a check of
-- discovery that does not list the claim — an optional list, which some identity providers leave without the claims
-- of their mappers — no longer raises the warning `groups_claim_missing` once a sign-in has seen it (C-03.FR-6).
-- Expand only: a nullable column without a default changes no existing row.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE oidc_settings ADD COLUMN groups_claim_seen_at timestamptz;

COMMIT;
