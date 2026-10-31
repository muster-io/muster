-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0025_identity_provider_claims_and_team_entries.up.sql. The Role mapping is group_mappings again, which holds
-- the entries of the groups claim with an exact group: the entries of another claim or with a subtree are lost, as are
-- the claims seen other than the groups claim, the claims a check found missing or listed, and the Team entries. A
-- person in a Team only through the identity provider leaves it; one also added by hand stays, by hand.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DELETE FROM team_users WHERE NOT by_hand;

ALTER TABLE team_users
    DROP CONSTRAINT team_users_idp_matches_check,
    DROP CONSTRAINT team_users_source_check,
    DROP COLUMN idp_matches,
    DROP COLUMN by_idp;

DROP TABLE team_idp_entries;

ALTER TABLE oidc_settings
    DROP CONSTRAINT oidc_settings_claims_seen_check,
    DROP CONSTRAINT oidc_settings_role_mappings_check,
    DROP COLUMN claims_supported,
    DROP COLUMN claims_missing,
    DROP COLUMN claims_seen,
    DROP COLUMN role_mappings;

COMMIT;
