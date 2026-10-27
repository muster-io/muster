-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0022 adds the Teams of C-25: teams, with a name unique ignoring case and a slug unique among the Teams
-- that are not deleted, a description and their own Working hours, set exactly when own_working_hours; team_users, the
-- people in each Team; the owning Team of Routes and Destinations, owning_team_id, with partial indexes for the
-- filters by Team; the Organization's Working hours, working_days and workday_end beside workday_start
-- (organization.working_hours); and the Permissions teams:read, for every Role, and teams:write, for Admins. Expand
-- only: the new tables are empty, the new columns are nullable or have defaults, and an earlier binary writes none of
-- them. The CHECKs of the Organization's Working hours are added NOT VALID. Before they are validated in a second
-- transaction, under a lock that lets writes through, an Organization whose working day starts at 18:00 or later gets
-- the end 23:59 and, where the start is 23:59 — the latest HH:MM, after which no end exists — the start 23:58, with
-- a new version.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE teams (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id            bigint NOT NULL REFERENCES organizations (id),
    public_id         text NOT NULL CHECK (public_id ~ '^TM[0-9A-HJKMNP-TV-Z]{12}$'),
    name              text NOT NULL CHECK (name <> ''),
    slug              text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),  -- team.slug_format
    description       text NOT NULL DEFAULT '',
    own_working_hours boolean NOT NULL DEFAULT false,
    working_days      text[],
    working_start     text,
    working_end       text,
    working_time_zone text,
    version           bigint NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    deleted_at        timestamptz,
    UNIQUE (org_id, public_id),
    -- Its own Working hours are set exactly when the Team has them.
    CONSTRAINT teams_working_hours_check CHECK (
        own_working_hours = (working_days IS NOT NULL) AND own_working_hours = (working_start IS NOT NULL)
        AND own_working_hours = (working_end IS NOT NULL) AND own_working_hours = (working_time_zone IS NOT NULL)),
    CONSTRAINT teams_working_days_check CHECK (cardinality(working_days) > 0
        AND working_days <@ ARRAY['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']),
    CONSTRAINT teams_working_start_check CHECK (working_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    CONSTRAINT teams_working_end_check CHECK (working_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    -- Working hours end later than they start on the same day (P-63).
    CONSTRAINT teams_working_order_check CHECK (working_end > working_start)
);

CREATE UNIQUE INDEX teams_name_key ON teams (org_id, lower(name)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX teams_slug_key ON teams (org_id, slug) WHERE deleted_at IS NULL;

CREATE TABLE team_users (
    org_id     bigint NOT NULL REFERENCES organizations (id),
    team_id    bigint NOT NULL REFERENCES teams (id),
    user_id    bigint NOT NULL REFERENCES users (id),
    by_hand    boolean NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

-- "My Teams": the Teams of a User.
CREATE INDEX team_users_user_idx ON team_users (org_id, user_id);

ALTER TABLE routes ADD COLUMN owning_team_id bigint REFERENCES teams (id);
CREATE INDEX routes_owning_team_idx ON routes (org_id, owning_team_id) WHERE owning_team_id IS NOT NULL;

ALTER TABLE destinations ADD COLUMN owning_team_id bigint REFERENCES teams (id);
CREATE INDEX destinations_owning_team_idx ON destinations (org_id, owning_team_id) WHERE owning_team_id IS NOT NULL;

ALTER TABLE organizations
    ADD COLUMN working_days text[] NOT NULL DEFAULT '{monday,tuesday,wednesday,thursday,friday}',
    ADD COLUMN workday_end text NOT NULL DEFAULT '18:00',
    ADD CONSTRAINT organizations_working_days_check CHECK (cardinality(working_days) > 0
        AND working_days <@ ARRAY['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'])
        NOT VALID,
    ADD CONSTRAINT organizations_workday_end_check
        CHECK (workday_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$') NOT VALID,
    ADD CONSTRAINT organizations_workday_order_check CHECK (workday_end > workday_start) NOT VALID;

-- A working day that starts at 18:00 or later ends at 23:59, the latest end on the same day (P-63).
UPDATE organizations
SET workday_end = '23:59', workday_start = least(workday_start, '23:58'), version = version + 1
WHERE workday_start >= '18:00';

INSERT INTO permissions (name) VALUES ('teams:read'), ('teams:write');

INSERT INTO role_permissions (role, permission) VALUES
    ('admin', 'teams:read'), ('admin', 'teams:write'), ('responder', 'teams:read'), ('viewer', 'teams:read');

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations VALIDATE CONSTRAINT organizations_working_days_check;
ALTER TABLE organizations VALIDATE CONSTRAINT organizations_workday_end_check;
ALTER TABLE organizations VALIDATE CONSTRAINT organizations_workday_order_check;

COMMIT;
