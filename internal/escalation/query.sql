-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Escalation chains (C-32): the chains with their steps and the people of their steps, what the steps name, the
-- Routes and Teams that name a chain and the Destinations a chain writes in.

-- GetOrganizationWorkingHours reads the Organization's Working hours, which the steps of a chain without an owning
-- Team, or whose Team has none of its own, use.
-- name: GetOrganizationWorkingHours :one
SELECT working_days, workday_start, workday_end, time_zone
FROM organizations
WHERE id = @org_id;

-- GetTeamWorkingHours reads the own Working hours of a Team, deleted ones included.
-- name: GetTeamWorkingHours :one
SELECT own_working_hours, working_days, working_start, working_end, working_time_zone
FROM teams
WHERE org_id = @org_id AND id = @id;

-- ListChains reads a page of the chains by name, after a cursor, with their owning Teams; @q matches the name ignoring
-- case, and @team_ids, when given, keeps the chains those Teams own.
-- name: ListChains :many
SELECT c.id, c.public_id, c.name, c.description, c.created_at, c.version, c.owning_team_id,
       t.public_id AS team_public_id, t.name AS team_name, t.slug AS team_slug,
       (t.deleted_at IS NOT NULL)::boolean AS team_deleted, lower(c.name)::text AS sort_name
FROM escalation_chains c
LEFT JOIN teams t ON t.org_id = c.org_id AND t.id = c.owning_team_id
WHERE c.org_id = @org_id
  AND (sqlc.narg('q')::text IS NULL OR strpos(lower(c.name), lower(sqlc.narg('q')::text)) > 0)
  AND (sqlc.narg('team_ids')::bigint[] IS NULL OR c.owning_team_id = ANY(sqlc.narg('team_ids')::bigint[]))
  AND (sqlc.narg('after_id')::bigint IS NULL
       OR (lower(c.name), c.id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::bigint))
ORDER BY lower(c.name), c.id
LIMIT @page_size;

-- GetChain reads a chain with its owning Team.
-- name: GetChain :one
SELECT c.id, c.public_id, c.name, c.description, c.created_at, c.version, c.owning_team_id,
       t.public_id AS team_public_id, t.name AS team_name, t.slug AS team_slug,
       (t.deleted_at IS NOT NULL)::boolean AS team_deleted
FROM escalation_chains c
LEFT JOIN teams t ON t.org_id = c.org_id AND t.id = c.owning_team_id
WHERE c.org_id = @org_id AND c.public_id = @public_id;

-- LockChain locks a chain for a change, FOR NO KEY UPDATE: the saves of Routes and Teams that name it take it FOR KEY
-- SHARE and do not wait for a change of it.
-- name: LockChain :one
SELECT id
FROM escalation_chains
WHERE org_id = @org_id AND public_id = @public_id
FOR NO KEY UPDATE;

-- LockChainForDeletion locks a chain FOR UPDATE before its deletion, which conflicts with the FOR KEY SHARE of a save
-- of a Route or a Team that names it: either that save committed and its entry refuses the deletion, or it waits and
-- then finds the chain gone.
-- name: LockChainForDeletion :one
SELECT id
FROM escalation_chains
WHERE org_id = @org_id AND public_id = @public_id
FOR UPDATE;

-- LockOwningTeam reads a Team that is not deleted as the owner of a chain FOR SHARE, before the chain's row: the
-- deletion of the Team, which locks it first FOR NO KEY UPDATE and then reads the chains it owns, either sees the chain
-- or makes this save wait and then find the Team deleted.
-- name: LockOwningTeam :one
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR SHARE;

-- InsertChain creates a chain.
-- name: InsertChain :one
INSERT INTO escalation_chains (org_id, public_id, name, description, owning_team_id, created_at, updated_at)
VALUES (@org_id, @public_id, @name, @description, sqlc.narg('owning_team_id')::bigint, @now, @now)
RETURNING id;

-- UpdateChain replaces the fields of a chain and bumps its version.
-- name: UpdateChain :exec
UPDATE escalation_chains
SET name = @name, description = @description, owning_team_id = sqlc.narg('owning_team_id')::bigint,
    updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteChain deletes a chain; its steps, their people and the entries of deleted Routes go with it.
-- name: DeleteChain :exec
DELETE FROM escalation_chains
WHERE org_id = @org_id AND id = @id;

-- ListSteps lists the steps of the chains in their order with what they name: the Schedule with its Team, the Team,
-- the gateway's Connection with whether it is a Notification gateway now — not deleted, its info declaring the role
-- gateway — and the Destination with its type, mode and whether it is deleted. The condition of a live gateway mirrors
-- internal/gateways/query.sql ListGateways; change both together.
-- name: ListSteps :many
SELECT s.id, s.chain_id, s.position, s.kind, s.importance, s.everyone, s.call_target, s.wait_seconds, s.repeat_from,
       s.repeat_times, s.working_hours_only, s.severity_condition,
       sc.public_id AS schedule_public_id, sc.name AS schedule_name, st.public_id AS schedule_team_public_id,
       st.name AS schedule_team_name, st.slug AS schedule_team_slug,
       (st.deleted_at IS NOT NULL)::boolean AS schedule_team_deleted,
       t.public_id AS team_public_id, t.name AS team_name, t.slug AS team_slug,
       (t.deleted_at IS NOT NULL)::boolean AS team_deleted,
       g.public_id AS gateway_public_id, g.name AS gateway_name,
       (g.deleted_at IS NULL AND coalesce(g.bridge_info -> 'roles' @> '["gateway"]'::jsonb, false))::boolean
           AS gateway_live,
       d.public_id AS destination_public_id, d.name AS destination_name, d.type AS destination_type,
       d.webhook_mode AS destination_mode, (d.deleted_at IS NOT NULL)::boolean AS destination_deleted
FROM escalation_steps s
LEFT JOIN schedules sc ON sc.org_id = s.org_id AND sc.id = s.schedule_id
LEFT JOIN teams st ON st.org_id = sc.org_id AND st.id = sc.team_id
LEFT JOIN teams t ON t.org_id = s.org_id AND t.id = s.team_id
LEFT JOIN connections g ON g.org_id = s.org_id AND g.id = s.gateway_connection_id
LEFT JOIN destinations d ON d.org_id = s.org_id AND d.id = s.destination_id
WHERE s.org_id = @org_id AND s.chain_id = ANY(@chain_ids::bigint[])
ORDER BY s.chain_id, s.position;

-- ListStepUsers lists the people of the steps in their order, deleted ones included.
-- name: ListStepUsers :many
SELECT su.step_id, u.public_id, u.name, u.login, u.role, u.status
FROM escalation_step_users su
JOIN users u ON u.org_id = su.org_id AND u.id = su.user_id
WHERE su.org_id = @org_id AND su.step_id = ANY(@step_ids::bigint[])
ORDER BY su.step_id, su.position;

-- DeleteSteps deletes the steps of a chain with their people, before a save writes them again.
-- name: DeleteSteps :exec
DELETE FROM escalation_steps
WHERE org_id = @org_id AND chain_id = @chain_id;

-- InsertStep writes one step of a chain.
-- name: InsertStep :one
INSERT INTO escalation_steps (
    org_id, chain_id, position, kind, importance, schedule_id, team_id, everyone, call_target, gateway_connection_id,
    wait_seconds, destination_id, repeat_from, repeat_times, working_hours_only, severity_condition
)
VALUES (
    @org_id, @chain_id, @position, @kind, sqlc.narg('importance')::text, sqlc.narg('schedule_id')::bigint,
    sqlc.narg('team_id')::bigint, sqlc.narg('everyone')::text, sqlc.narg('call_target')::text,
    sqlc.narg('gateway_connection_id')::bigint, sqlc.narg('wait_seconds')::integer,
    sqlc.narg('destination_id')::bigint, sqlc.narg('repeat_from')::integer, sqlc.narg('repeat_times')::integer,
    @working_hours_only, @severity_condition
)
RETURNING id;

-- InsertStepUsers writes the people of a step in their order.
-- name: InsertStepUsers :exec
INSERT INTO escalation_step_users (org_id, step_id, position, user_id)
SELECT @org_id, @step_id, u.position, u.user_id
FROM unnest(@user_ids::bigint[]) WITH ORDINALITY AS u (user_id, position);

-- ResolveSchedules reads the Schedules among the public_ids with their Teams, FOR KEY SHARE in id order: the deletion
-- of a Schedule locks it FOR UPDATE and then reads the steps that name it, so either it sees this save's step or this
-- save waits and then finds the Schedule gone. KEY SHARE, unlike SHARE, does not conflict with the NO KEY UPDATE of a
-- change of a Schedule, so a save naming two Schedules never deadlocks with making one of them main.
-- name: ResolveSchedules :many
SELECT sc.id, sc.public_id, sc.name, t.public_id AS team_public_id, t.name AS team_name, t.slug AS team_slug,
       (t.deleted_at IS NOT NULL)::boolean AS team_deleted
FROM schedules sc
JOIN teams t ON t.org_id = sc.org_id AND t.id = sc.team_id
WHERE sc.org_id = @org_id AND sc.public_id = ANY(@public_ids::text[])
ORDER BY sc.id
FOR KEY SHARE OF sc;

-- ResolveUsers reads the Users among the public_ids with their Role and status, deleted ones included.
-- name: ResolveUsers :many
SELECT id, public_id, name, login, role, status
FROM users
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ResolveTeams reads the Teams among the public_ids, deleted ones included.
-- name: ResolveTeams :many
SELECT id, public_id, name, slug, (deleted_at IS NOT NULL)::boolean AS deleted
FROM teams
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ListGateways lists every Bridge Connection, deleted ones included, which a call step may name, with whether it is a
-- Notification gateway now — not deleted, its info declaring the role gateway — by name. The condition mirrors
-- internal/gateways/query.sql ListGateways; change both together.
-- name: ListGateways :many
SELECT id, public_id, name,
       (deleted_at IS NULL AND coalesce(bridge_info -> 'roles' @> '["gateway"]'::jsonb, false))::boolean AS live
FROM connections
WHERE org_id = @org_id AND type = 'bridge'
ORDER BY lower(name), id;

-- ResolveDestinations reads the Destinations among the public_ids with their type and mode, deleted ones included.
-- name: ResolveDestinations :many
SELECT id, public_id, name, type, webhook_mode, (deleted_at IS NOT NULL)::boolean AS deleted
FROM destinations
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ListRouteReferrers lists the Routes that are not deleted and name the chains, with their Severity conditions, by
-- name.
-- name: ListRouteReferrers :many
SELECT rc.chain_id, r.id, r.public_id, r.name, rc.severity_condition
FROM route_escalation_chains rc
JOIN routes r ON r.org_id = rc.org_id AND r.id = rc.route_id
WHERE rc.org_id = @org_id AND rc.chain_id = ANY(@chain_ids::bigint[]) AND r.deleted_at IS NULL
ORDER BY rc.chain_id, lower(r.name), r.id;

-- ListTeamReferrers lists the Teams that are not deleted and name the chains, with their Severity conditions, by
-- name.
-- name: ListTeamReferrers :many
SELECT tc.chain_id, t.id, t.public_id, t.name, t.slug, tc.severity_condition
FROM team_escalation_chains tc
JOIN teams t ON t.org_id = tc.org_id AND t.id = tc.team_id
WHERE tc.org_id = @org_id AND tc.chain_id = ANY(@chain_ids::bigint[]) AND t.deleted_at IS NULL
ORDER BY tc.chain_id, lower(t.name), t.id;

-- ListChainDestinations lists, for each chain, the Destinations that are not deleted where it writes (C-32.FR-8):
-- those of the Routes and of the Teams that are not deleted and name it, each once, by name, with their health and
-- what their Connection can mention, none for an outgoing webhook or a deleted Connection. What a Connection can mention
-- mirrors internal/mentions/query.sql GetMentionConnection; change both together.
-- name: ListChainDestinations :many
SELECT DISTINCT ON (x.chain_id, lower(d.name), d.id)
       x.chain_id, d.id, d.public_id, d.name, d.type, d.health, d.broken_since, d.broken_reason,
       coalesce(c.slack_usergroups_read, false)::boolean AS usergroups_read,
       coalesce(c.bridge_info -> 'capabilities' -> 'group_mentions' = 'true'::jsonb, false)::boolean AS group_mentions,
       (c.bridge_info -> 'roles' IS NULL OR c.bridge_info -> 'roles' @> '["messenger"]'::jsonb)::boolean AS messenger,
       (CASE jsonb_typeof(c.bridge_info -> 'capabilities' -> 'everyone_mentions')
            WHEN 'array' THEN ARRAY(SELECT jsonb_array_elements_text(c.bridge_info -> 'capabilities' -> 'everyone_mentions'))
            ELSE '{}'::text[] END)::text[] AS everyone_mentions
FROM (SELECT rc.chain_id, rd.destination_id
      FROM route_escalation_chains rc
      JOIN routes r ON r.org_id = rc.org_id AND r.id = rc.route_id
      JOIN route_destinations rd ON rd.org_id = rc.org_id AND rd.route_id = rc.route_id
      WHERE rc.org_id = @org_id AND rc.chain_id = ANY(@chain_ids::bigint[]) AND r.deleted_at IS NULL
      UNION
      SELECT tc.chain_id, td.destination_id
      FROM team_escalation_chains tc
      JOIN teams t ON t.org_id = tc.org_id AND t.id = tc.team_id
      JOIN team_destinations td ON td.org_id = tc.org_id AND td.team_id = tc.team_id
      WHERE tc.org_id = @org_id AND tc.chain_id = ANY(@chain_ids::bigint[]) AND t.deleted_at IS NULL) x
JOIN destinations d ON d.org_id = @org_id AND d.id = x.destination_id
LEFT JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id AND c.deleted_at IS NULL
WHERE d.deleted_at IS NULL
ORDER BY x.chain_id, lower(d.name), d.id;

-- ListUserTeams lists the Teams that are not deleted that a User is in, for the filter "My Teams".
-- name: ListUserTeams :many
SELECT t.id
FROM team_users tu
JOIN teams t ON t.org_id = tu.org_id AND t.id = tu.team_id
WHERE tu.org_id = @org_id AND tu.user_id = @user_id AND t.deleted_at IS NULL;

-- GetTeamID reads the id of a Team that is not deleted, for the filter by Team.
-- name: GetTeamID :one
SELECT id
FROM teams
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL;

-- ListChainInfo lists every chain for muster_escalation_chain_info.
-- name: ListChainInfo :many
SELECT public_id, name
FROM escalation_chains
WHERE org_id = @org_id;

-- The runs of Escalation chains on Alert Groups (C-32.FR-4 to FR-7, FR-14 to FR-16, FR-18). Every statement below runs
-- in the dispatcher's transaction, after the Alert Group's lock (Lock order of runs.go).

-- ListChainSources lists the chains that may run on an Alert Group, each by the source that names it: its Route's,
-- and, where the Route delivers to the Teams and runs their chains, those of the Teams of the Alert Group that are not
-- deleted, each with its Severity condition.
-- name: ListChainSources :many
SELECT x.kind, x.team_id, x.chain_id, x.severity_condition
FROM (SELECT 'route'::text AS kind, 0::bigint AS team_id, rc.chain_id, rc.severity_condition
      FROM route_escalation_chains rc
      WHERE rc.org_id = @org_id AND rc.route_id = @route_id
      UNION ALL
      SELECT 'team'::text, tc.team_id, tc.chain_id, tc.severity_condition
      FROM team_escalation_chains tc
      JOIN teams t ON t.org_id = tc.org_id AND t.id = tc.team_id
      JOIN routes r ON r.org_id = tc.org_id AND r.id = @route_id
      WHERE tc.org_id = @org_id AND t.deleted_at IS NULL AND r.deliver_to_teams AND r.run_team_chains
        AND tc.team_id IN (SELECT gt.team_id
                           FROM alert_group_teams gt
                           WHERE gt.org_id = @org_id AND gt.alert_group_id = @alert_group_id)) x
ORDER BY x.chain_id, x.kind, x.team_id;

-- ShareChains reads the chains a start runs FOR KEY SHARE in id order, before any run is written: the deletion of a
-- chain locks it FOR UPDATE first and waits, or has deleted it and the start finds it gone.
-- name: ShareChains :many
SELECT id, public_id, name, owning_team_id
FROM escalation_chains
WHERE org_id = @org_id AND id = ANY(@ids::bigint[])
ORDER BY id
FOR KEY SHARE;

-- SnapshotSteps reads the steps of the chains with the people of each, in one statement, so that a run takes the steps
-- of one version of its chain (P-73).
-- name: SnapshotSteps :many
SELECT s.chain_id, s.position, s.kind, s.importance, s.schedule_id, sc.public_id AS schedule_public_id, s.team_id,
       s.everyone, s.call_target, s.gateway_connection_id, s.wait_seconds, s.destination_id, s.repeat_from,
       s.repeat_times, s.working_hours_only, s.severity_condition,
       ARRAY(SELECT su.user_id
             FROM escalation_step_users su
             WHERE su.org_id = s.org_id AND su.step_id = s.id
             ORDER BY su.position)::bigint[] AS user_ids
FROM escalation_steps s
LEFT JOIN schedules sc ON sc.org_id = s.org_id AND sc.id = s.schedule_id
WHERE s.org_id = @org_id AND s.chain_id = ANY(@chain_ids::bigint[])
ORDER BY s.chain_id, s.position;

-- ListGenerationRuns lists the runs of an Alert Group in a start of its chains.
-- name: ListGenerationRuns :many
SELECT id, public_id, chain_id, state, sources
FROM escalation_runs
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND generation = @generation
ORDER BY id;

-- InsertRun starts a run of a chain on an Alert Group at its first step, with the chain's name and owning Team.
-- name: InsertRun :one
INSERT INTO escalation_runs (org_id, public_id, alert_group_id, chain_id, chain_name, owning_team_id, sources, steps,
                             state, generation, started_at)
VALUES (@org_id, @public_id, @alert_group_id, @chain_id, @chain_name, sqlc.narg('owning_team_id')::bigint, @sources,
        @steps, 'running', @generation, @now)
RETURNING id;

-- SetRunSources writes the sources of a run that another source now names too (P-70).
-- name: SetRunSources :exec
UPDATE escalation_runs
SET sources = @sources
WHERE org_id = @org_id AND id = @id;

-- StopRuns stops the running runs of an Alert Group with what stopped them; runs that ran out stay as they are.
-- name: StopRuns :many
UPDATE escalation_runs r
SET state = 'stopped', ended_at = @now, stop_command = @stop_command, stop_event_seq = sqlc.narg('stop_event_seq')
WHERE r.org_id = @org_id AND r.alert_group_id = @alert_group_id AND r.state = 'running'
RETURNING r.id, r.public_id,
          coalesce((SELECT c.public_id
                    FROM escalation_chains c
                    WHERE c.org_id = r.org_id AND c.id = r.chain_id), '')::text AS chain_public_id;

-- DeleteRunTimers deletes the timers of the runs.
-- name: DeleteRunTimers :exec
DELETE FROM timers
WHERE org_id = @org_id AND escalation_run_id = ANY(@run_ids::bigint[]) AND kind = 'escalation_step';

-- UpsertRunTimer sets the timer of a run at its deadline, a new lease to claim. The subject is the run: the Alert Group
-- and the Storm are null, and the conflict is on timers_escalation_run_key, never timers_alert_group_key.
-- name: UpsertRunTimer :exec
INSERT INTO timers (org_id, escalation_run_id, kind, deadline, created_at, updated_at)
VALUES (@org_id, @escalation_run_id::bigint, 'escalation_step', @deadline, @now, @now)
ON CONFLICT (escalation_run_id) WHERE escalation_run_id IS NOT NULL
DO UPDATE SET deadline = excluded.deadline, lease_owner = NULL, lease_until = NULL, attempts = 0,
              updated_at = excluded.updated_at;

-- NotifyRunTimers wakes the timer workers of every replica once the transaction commits.
-- name: NotifyRunTimers :exec
SELECT pg_notify(@channel::text, '');

-- GetRunGroup reads the Alert Group of a run, which the step of its timer locks first.
-- name: GetRunGroup :one
SELECT alert_group_id
FROM escalation_runs
WHERE org_id = @org_id AND id = @id;

-- LockRun locks a run for its step, after its Alert Group, with the deadline of its timer — none, the zero time
-- 0001-01-01, once it was stopped or replaced — and the number of steps it made and skipped so far.
-- name: LockRun :one
SELECT r.id, r.public_id, r.alert_group_id, r.chain_id, r.chain_name, r.steps, r.step_index, r.repeats, r.state,
       r.generation, c.public_id AS chain_public_id, r.owning_team_id,
       (SELECT coalesce(max(s.seq), 0)
        FROM escalation_run_steps s
        WHERE s.org_id = r.org_id AND s.run_id = r.id)::bigint AS steps_made,
       coalesce((SELECT t.deadline
                 FROM timers t
                 WHERE t.org_id = r.org_id AND t.escalation_run_id = r.id AND t.kind = 'escalation_step'),
                '0001-01-01 00:00:00+00')::timestamptz AS deadline
FROM escalation_runs r
LEFT JOIN escalation_chains c ON c.org_id = r.org_id AND c.id = r.chain_id
WHERE r.org_id = @org_id AND r.id = @id
FOR NO KEY UPDATE OF r;

-- SaveRun writes where a run is after its steps.
-- name: SaveRun :exec
UPDATE escalation_runs
SET step_index = @step_index, repeats = @repeats, state = @state, ended_at = sqlc.narg('ended_at')
WHERE org_id = @org_id AND id = @id;

-- InsertRunStep records a step a run made or skipped, numbered after the run's last one.
-- name: InsertRunStep :exec
INSERT INTO escalation_run_steps (org_id, run_id, seq, number, kind, at, outcome, reason)
VALUES (@org_id, @run_id,
        (SELECT coalesce(max(x.seq), 0) + 1 FROM escalation_run_steps x WHERE x.org_id = @org_id AND x.run_id = @run_id),
        @number, @kind, @at, @outcome, sqlc.narg('reason'));

-- CountGenerationRuns counts the runs of an Alert Group in a start of its chains: all of them, those that ran out and
-- those stopped.
-- name: CountGenerationRuns :one
SELECT count(*)::bigint AS total, count(*) FILTER (WHERE state = 'ran_out')::bigint AS ran_out,
       count(*) FILTER (WHERE state = 'stopped')::bigint AS stopped
FROM escalation_runs
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND generation = @generation;

-- ListPeopleStates reads the Role and status of the people a step names, deleted ones included.
-- name: ListPeopleStates :many
SELECT id, public_id, name, login, role, status
FROM users
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- ListGroupRuns reads the runs of Alert Groups, by public_id, in the current start of their chains
-- (alert_groups.chain_generation), with their chains, owning Teams and the deadline of their timers, the zero time
-- 0001-01-01 for none.
-- name: ListGroupRuns :many
SELECT g.public_id AS group_public_id, r.id, r.public_id, r.chain_id, r.chain_name, r.sources, r.steps, r.step_index, r.state, r.started_at,
       c.public_id AS chain_public_id, c.name AS current_chain_name, t.public_id AS team_public_id,
       t.name AS team_name, t.slug AS team_slug, (t.deleted_at IS NOT NULL)::boolean AS team_deleted,
       coalesce((SELECT x.deadline
                 FROM timers x
                 WHERE x.org_id = r.org_id AND x.escalation_run_id = r.id AND x.kind = 'escalation_step'),
                '0001-01-01 00:00:00+00')::timestamptz AS deadline
FROM alert_groups g
JOIN escalation_runs r ON r.org_id = g.org_id AND r.alert_group_id = g.id AND r.generation = g.chain_generation
LEFT JOIN escalation_chains c ON c.org_id = r.org_id AND c.id = r.chain_id
LEFT JOIN teams t ON t.org_id = r.org_id AND t.id = c.owning_team_id
WHERE g.org_id = @org_id AND g.public_id = ANY(@public_ids::text[])
ORDER BY g.id, r.id;

-- ListTeamRefs names Teams by id, deleted ones included.
-- name: ListTeamRefs :many
SELECT id, public_id, name, slug, (deleted_at IS NOT NULL)::boolean AS deleted
FROM teams
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- GetChainID reads the id of a chain by public_id.
-- name: GetChainID :one
SELECT id
FROM escalation_chains
WHERE org_id = @org_id AND public_id = @public_id;

-- ListChainRuns reads a page of the runs of a chain, newest first, after a position, with their Alert Groups — with
-- when they resolved and retention.alert_details, which decide whether their details are removed — and what stopped
-- them: the Command and its actor, from the lifecycle event that stopped them.
-- name: ListChainRuns :many
SELECT r.id, r.public_id, r.state, r.started_at, r.ended_at, r.stop_command, g.public_id AS group_public_id,
       g.number AS group_number, g.title AS group_title, g.severity_level AS group_severity,
       g.resolved_at AS group_resolved_at, o.retention_alert_details_days,
       coalesce(e.actor_kind, '')::text AS actor_kind, u.public_id AS user_public_id, u.name AS user_name,
       u.login AS user_login, coalesce(u.status = 'deleted', false)::boolean AS user_deleted,
       sa.public_id AS service_account_public_id, sa.name AS service_account_name,
       coalesce(sa.status = 'deleted', false)::boolean AS service_account_deleted
FROM escalation_runs r
JOIN alert_groups g ON g.org_id = r.org_id AND g.id = r.alert_group_id
JOIN organizations o ON o.id = r.org_id
LEFT JOIN LATERAL (SELECT te.actor_kind, te.actor_user_id, te.actor_service_account_id
                   FROM timeline_entries te
                   WHERE te.org_id = r.org_id AND te.alert_group_id = r.alert_group_id
                     AND te.event_seq = r.stop_event_seq
                   LIMIT 1) e ON true
LEFT JOIN users u ON u.org_id = r.org_id AND u.id = e.actor_user_id
LEFT JOIN service_accounts sa ON sa.org_id = r.org_id AND sa.id = e.actor_service_account_id
WHERE r.org_id = @org_id AND r.chain_id = @chain_id
  AND (sqlc.narg('after_id')::bigint IS NULL
       OR (r.started_at, r.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY r.started_at DESC, r.id DESC
LIMIT @page_size;

-- ListRunSteps reads the steps the runs made or skipped, in their order.
-- name: ListRunSteps :many
SELECT run_id, seq, number, kind, at, outcome, reason
FROM escalation_run_steps
WHERE org_id = @org_id AND run_id = ANY(@run_ids::bigint[])
ORDER BY run_id, seq;
