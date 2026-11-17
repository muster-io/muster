-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Notification rules (C-31): the rules of the Users in the states call, no_call and custom, the Organization's default
-- rules that the Users in the state organization follow, and the Notification gateways a call names.

-- LockRulesUser locks the User whose rules change, so that the rules of one User change one at a time and never after
-- the User's deletion, which locks the row first.
-- name: LockRulesUser :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- GetRulesUser reads a User an Admin names, deleted ones included.
-- name: GetRulesUser :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id;

-- GetRulesUserByID reads the signed-in User.
-- name: GetRulesUserByID :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND id = @id;

-- GetOrganizationRules reads the Organization's default rules (organization.notification_rules) with the version of
-- the organization resource, from which the ETag of the state organization comes.
-- name: GetOrganizationRules :one
SELECT notification_rules, version
FROM organizations
WHERE id = @org_id;

-- GetRules reads the rules of a User in the state call, no_call or custom; no row is the state organization.
-- name: GetRules :one
SELECT state, gateway_connection_id, normal, important, version
FROM notification_rules
WHERE org_id = @org_id AND user_id = @user_id;

-- ListRuleGateways lists every Bridge Connection, deleted ones included, which a call action may name: whether it is
-- a Notification gateway now — not deleted, its info declaring the role gateway — and whether the User user_id has a
-- confirmed contact there.
-- name: ListRuleGateways :many
SELECT c.id, c.public_id, c.name,
       (c.deleted_at IS NULL AND coalesce(c.bridge_info -> 'roles' @> '["gateway"]'::jsonb, false))::boolean AS live,
       EXISTS (SELECT 1
               FROM gateway_contacts g
               WHERE g.org_id = c.org_id AND g.connection_id = c.id AND g.user_id = @user_id
                 AND g.confirmed_at IS NOT NULL)::boolean AS contact
FROM connections c
WHERE c.org_id = @org_id AND c.type = 'bridge'
ORDER BY lower(c.name), c.id;

-- UpsertRules writes the rules of a User with a new version from the sequence.
-- name: UpsertRules :one
INSERT INTO notification_rules (org_id, user_id, state, gateway_connection_id, normal, important, updated_at)
VALUES (@org_id, @user_id, @state, sqlc.narg('gateway_connection_id'), @normal, @important, @now::timestamptz)
ON CONFLICT (user_id) DO UPDATE
SET state                 = excluded.state,
    gateway_connection_id = excluded.gateway_connection_id,
    normal                = excluded.normal,
    important             = excluded.important,
    updated_at            = excluded.updated_at,
    version               = nextval('notification_rules_version_seq')
WHERE notification_rules.org_id = @org_id
RETURNING version;

-- DeleteRules deletes the rules of a User, who then follows the Organization's rules; and those of a deleted User.
-- name: DeleteRules :execrows
DELETE FROM notification_rules
WHERE org_id = @org_id AND user_id = @user_id;

-- InsertRuleRun starts the run of a person's rule for a step that Paged them (C-31.FR-3), at its position and state
-- after the actions it made at once.
-- name: InsertRuleRun :one
INSERT INTO notification_rule_runs (org_id, alert_group_id, escalation_run_id, step_number, user_id, importance,
                                    actions, position, state, started_at, ended_at)
VALUES (@org_id, @alert_group_id, @escalation_run_id, @step_number, @user_id, @importance, @actions, @position, @state,
        @now::timestamptz, sqlc.narg('ended_at'))
RETURNING id;

-- UpsertRuleTimer sets the timer of a rule run at the end of its wait, a new lease to claim. The subject is the rule
-- run: the Alert Group, the Storm and the run of the chain are null, and the conflict is on
-- timers_notification_rule_run_key, never timers_alert_group_key (schema.md §4.10).
-- name: UpsertRuleTimer :exec
INSERT INTO timers (org_id, notification_rule_run_id, kind, deadline, created_at, updated_at)
VALUES (@org_id, @notification_rule_run_id::bigint, 'notification_rule_action', @deadline, @now, @now)
ON CONFLICT (notification_rule_run_id) WHERE notification_rule_run_id IS NOT NULL
DO UPDATE SET deadline = excluded.deadline, lease_owner = NULL, lease_until = NULL, attempts = 0,
              updated_at = excluded.updated_at;

-- DeleteRuleTimers deletes the timers of the rule runs.
-- name: DeleteRuleTimers :exec
DELETE FROM timers
WHERE org_id = @org_id AND notification_rule_run_id = ANY(@ids::bigint[]) AND kind = 'notification_rule_action';

-- NotifyRuleTimers wakes the timer workers of every replica once the transaction commits.
-- name: NotifyRuleTimers :exec
SELECT pg_notify(@channel::text, '');

-- StopRuleRuns stops the rule runs that still run on an Alert Group whose chains stopped or started over
-- (C-31.FR-3): those of runs that ran out included, whose rule may still repeat.
-- name: StopRuleRuns :many
UPDATE notification_rule_runs
SET state = 'stopped', ended_at = @now::timestamptz
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND state = 'running'
RETURNING id;

-- GetRuleRunGroup reads the Alert Group of a rule run, which the action of its timer locks first.
-- name: GetRuleRunGroup :one
SELECT alert_group_id
FROM notification_rule_runs
WHERE org_id = @org_id AND id = @id;

-- LockRuleRun locks a rule run for its next action, after its Alert Group, with the deadline of its timer — the zero
-- time 0001-01-01 once it was removed — and the run of its chain: its state, its chain, its sources, the owning Team it
-- had when it started and the Schedule of the step that Paged the person, 0 once that Schedule was deleted: the run's
-- steps are a snapshot, which a deleted Schedule outlives.
-- name: LockRuleRun :one
SELECT n.id, n.alert_group_id, n.escalation_run_id, n.step_number, n.user_id, n.importance, n.actions, n.position,
       n.state, r.state AS run_state, coalesce(r.chain_id, 0)::bigint AS chain_id, r.chain_name, r.sources,
       coalesce(r.owning_team_id, 0)::bigint AS owning_team_id, coalesce(c.public_id, '')::text AS chain_public_id,
       coalesce((SELECT s.id
                 FROM schedules s
                 WHERE s.org_id = n.org_id AND s.id = (r.steps -> (n.step_number - 1) ->> 'schedule_id')::bigint),
                0)::bigint AS schedule_id,
       coalesce((SELECT t.deadline
                 FROM timers t
                 WHERE t.org_id = n.org_id AND t.notification_rule_run_id = n.id
                   AND t.kind = 'notification_rule_action'),
                '0001-01-01 00:00:00+00')::timestamptz AS deadline
FROM notification_rule_runs n
JOIN escalation_runs r ON r.org_id = n.org_id AND r.id = n.escalation_run_id
LEFT JOIN escalation_chains c ON c.org_id = r.org_id AND c.id = r.chain_id
WHERE n.org_id = @org_id AND n.id = @id
FOR NO KEY UPDATE OF n;

-- SaveRuleRun writes where a rule run is after its actions.
-- name: SaveRuleRun :exec
UPDATE notification_rule_runs
SET position = @position, state = @state, ended_at = sqlc.narg('ended_at')
WHERE org_id = @org_id AND id = @id;
