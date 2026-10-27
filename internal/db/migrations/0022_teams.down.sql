-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0022_teams.up.sql. The Teams, the people in them, the owning Team of Routes and Destinations and the
-- Organization's Working hours other than the start of its working day are lost; Personal access tokens lose the
-- Permissions teams:read and teams:write. The Audit log entries about Teams stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DELETE FROM api_token_permissions WHERE permission IN ('teams:read', 'teams:write');
DELETE FROM role_permissions WHERE permission IN ('teams:read', 'teams:write');
DELETE FROM permissions WHERE name IN ('teams:read', 'teams:write');

ALTER TABLE organizations
    DROP COLUMN working_days,
    DROP COLUMN workday_end;

ALTER TABLE destinations DROP COLUMN owning_team_id;
ALTER TABLE routes DROP COLUMN owning_team_id;

DROP TABLE team_users;
DROP TABLE teams;

COMMIT;
