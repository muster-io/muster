-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the claim of every entry of the Role mapping and the Team entries of the identity provider (C-03.FR-5 to FR-7
-- revised for L2, C-25.FR-4). oidc_settings gains role_mappings, the entries {claim, group, role} that 0.2.0 reads,
-- filled here from group_mappings, whose entries read the groups claim; claims_seen, the claims a sign-in or a link
-- carried, each with when it first did; claims_missing, the claims the last check found not advertised; and
-- claims_supported, what discovery listed at the last check. The state of the groups claim carries over from
-- groups_claim_seen_at and groups_claim_missing_since, which 0.2.0 keeps writing in step. team_idp_entries holds the
-- entries of each Team, replaced as a whole on update; team_users gains by_idp and idp_matches, the entries that
-- matched the person at their last sign-in or re-check, and every row is in its Team by hand, through the identity
-- provider or both.
-- Expand only: a replica of 0.1 that still runs during a rolling update reads and writes group_mappings, to which
-- 0.2.0 writes only the entries such a replica reads the same way — the groups claim and an exact group — so that it
-- never grants a Role through an entry of another claim or a subtree; it reads none of the new columns and has no
-- Teams. Every new column has a default, the CHECK of team_users is added NOT VALID and validated in a second
-- transaction under a lock that lets writes through, as 0022 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE oidc_settings
    ADD COLUMN role_mappings jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN claims_seen jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN claims_missing text[] NOT NULL DEFAULT '{}',
    ADD COLUMN claims_supported text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT oidc_settings_role_mappings_check CHECK (jsonb_typeof(role_mappings) = 'array') NOT VALID,
    ADD CONSTRAINT oidc_settings_claims_seen_check CHECK (jsonb_typeof(claims_seen) = 'object') NOT VALID;

-- The entries of L1 read the groups claim; a groups claim seen and a warning raised stay, by the claim's name.
UPDATE oidc_settings
SET role_mappings = group_mappings,
    claims_seen = CASE
        WHEN groups_claim_seen_at IS NULL THEN '{}'::jsonb
        ELSE jsonb_build_object(groups_claim, groups_claim_seen_at)
    END,
    claims_missing = CASE WHEN groups_claim_missing_since IS NULL THEN '{}'::text[] ELSE ARRAY[groups_claim] END;

CREATE TABLE team_idp_entries (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint NOT NULL REFERENCES organizations (id),
    team_id    bigint NOT NULL REFERENCES teams (id),
    claim      text CHECK (claim <> ''),  -- null reads the groups claim of the OIDC settings
    group_name text NOT NULL CHECK (group_name <> ''),
    position   integer NOT NULL,
    UNIQUE (team_id, position)
);

-- The entries of every Team, which each sign-in and re-check reads.
CREATE INDEX team_idp_entries_org_idx ON team_idp_entries (org_id, team_id, position);

ALTER TABLE team_users
    ADD COLUMN by_idp boolean NOT NULL DEFAULT false,
    ADD COLUMN idp_matches jsonb NOT NULL DEFAULT '[]',
    ADD CONSTRAINT team_users_source_check CHECK (by_hand OR by_idp) NOT VALID,
    ADD CONSTRAINT team_users_idp_matches_check CHECK (jsonb_typeof(idp_matches) = 'array') NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE oidc_settings VALIDATE CONSTRAINT oidc_settings_role_mappings_check;
ALTER TABLE oidc_settings VALIDATE CONSTRAINT oidc_settings_claims_seen_check;
ALTER TABLE team_users VALIDATE CONSTRAINT team_users_source_check;
ALTER TABLE team_users VALIDATE CONSTRAINT team_users_idp_matches_check;

COMMIT;
