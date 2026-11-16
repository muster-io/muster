-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0046_escalation_chains.up.sql. The Escalation chains, their steps with their people, and the chains that
-- Routes and Teams name are lost; Personal access tokens lose the Permissions escalation-chains:read and
-- escalation-chains:write. The Audit log entries about chains stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DELETE FROM api_token_permissions WHERE permission IN ('escalation-chains:read', 'escalation-chains:write');
DELETE FROM role_permissions WHERE permission IN ('escalation-chains:read', 'escalation-chains:write');
DELETE FROM permissions WHERE name IN ('escalation-chains:read', 'escalation-chains:write');

DROP TABLE team_escalation_chains;
DROP TABLE route_escalation_chains;
DROP TABLE escalation_step_users;
DROP TABLE escalation_steps;
DROP TABLE escalation_chains;

COMMIT;
