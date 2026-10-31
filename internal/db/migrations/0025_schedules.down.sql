-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0025_schedules.up.sql. The Schedules, their Rotation layers with their people and their Overrides are lost;
-- Personal access tokens lose the Permissions schedules:read, schedules:write and schedule-overrides:write. The Audit
-- log entries about Schedules and Overrides stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DELETE FROM api_token_permissions
WHERE permission IN ('schedules:read', 'schedules:write', 'schedule-overrides:write');
DELETE FROM role_permissions WHERE permission IN ('schedules:read', 'schedules:write', 'schedule-overrides:write');
DELETE FROM permissions WHERE name IN ('schedules:read', 'schedules:write', 'schedule-overrides:write');

DROP TABLE schedule_overrides;
DROP TABLE rotation_layer_users;
DROP TABLE rotation_layers;
DROP TABLE schedules;

COMMIT;
