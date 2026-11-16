-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0046 adds the Escalation chains of C-32 as configuration: escalation_chains, each with a name unique in the
-- Organization ignoring case, a description and an optional owning Team; escalation_steps, the ordered steps of a
-- chain, each of one of seven kinds with exactly the fields of its kind and the limits of escalation.limits (P-84);
-- escalation_step_users, the people a page_people or call step names in their order; route_escalation_chains and
-- team_escalation_chains, the chains that Routes and Teams name, each with its Severity condition (C-32.FR-2); and the
-- Permissions escalation-chains:read, for every Role, and escalation-chains:write, for Admins. Expand only: the new
-- tables are empty and an earlier binary reads and writes none of them; no chain runs until a later version.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE escalation_chains (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    public_id      text NOT NULL CHECK (public_id ~ '^EC[0-9A-HJKMNP-TV-Z]{12}$'),
    name           text NOT NULL CHECK (name <> ''),
    description    text NOT NULL DEFAULT '',
    owning_team_id bigint REFERENCES teams (id),  -- the owning Team (C-25.FR-2), whose Working hours steps use
    version        bigint NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    UNIQUE (org_id, public_id)
);

CREATE UNIQUE INDEX escalation_chains_name_key ON escalation_chains (org_id, lower(name));
-- The chains a Team owns: the filter by Team and the refusal team_in_use (C-25.FR-10).
CREATE INDEX escalation_chains_owning_team_idx ON escalation_chains (org_id, owning_team_id);

CREATE TABLE escalation_steps (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                bigint NOT NULL REFERENCES organizations (id),
    chain_id              bigint NOT NULL REFERENCES escalation_chains (id) ON DELETE CASCADE,
    position              integer NOT NULL CHECK (position BETWEEN 1 AND 20),  -- the step's number
    kind                  text NOT NULL CHECK (kind IN ('page_on_call', 'page_people', 'page_team', 'call', 'wait',
        'webhook', 'repeat')),
    importance            text CHECK (importance IN ('normal', 'important')),
    schedule_id           bigint REFERENCES schedules (id),
    team_id               bigint REFERENCES teams (id),  -- page_team; null for the chain's owning Team
    everyone              text CHECK (everyone IN ('channel', 'here', 'all', 'everyone')),
    call_target           text CHECK (call_target IN ('on_call', 'people')),
    gateway_connection_id bigint REFERENCES connections (id),
    wait_seconds          integer CHECK (wait_seconds BETWEEN 60 AND 86400 AND wait_seconds % 60 = 0),
    destination_id        bigint REFERENCES destinations (id),
    repeat_from           integer,
    repeat_times          integer CHECK (repeat_times BETWEEN 1 AND 10),
    working_hours_only    boolean NOT NULL DEFAULT false,
    severity_condition    text NOT NULL DEFAULT 'any'
        CHECK (severity_condition IN ('any', 'critical', 'below_critical')),
    CONSTRAINT escalation_steps_position_key UNIQUE (chain_id, position),
    -- A repeat goes back to an earlier step.
    CONSTRAINT escalation_steps_repeat_from_check CHECK (repeat_from >= 1 AND repeat_from < position),
    -- Each kind has exactly its fields: importance with the Pages of people, a Schedule with page_on_call and a call
    -- of the On-call, a Team or the word for everyone with page_team only, a call target and a gateway with call, the
    -- wait with wait, the Destination with webhook and both repeat fields with repeat; wait and repeat Page nobody, so
    -- they have neither the limit to Working hours nor a Severity condition.
    CONSTRAINT escalation_steps_fields_check CHECK (
        (importance IS NOT NULL) = (kind IN ('page_on_call', 'page_people'))
        AND (schedule_id IS NOT NULL) = (kind = 'page_on_call' OR (kind = 'call' AND call_target = 'on_call'))
        AND (team_id IS NULL OR kind = 'page_team')
        AND (everyone IS NULL OR (kind = 'page_team' AND team_id IS NULL))
        AND (call_target IS NOT NULL) = (kind = 'call')
        AND (gateway_connection_id IS NOT NULL) = (kind = 'call')
        AND (wait_seconds IS NOT NULL) = (kind = 'wait')
        AND (destination_id IS NOT NULL) = (kind = 'webhook')
        AND (repeat_from IS NOT NULL) = (kind = 'repeat')
        AND (repeat_times IS NOT NULL) = (kind = 'repeat')
        AND (kind NOT IN ('wait', 'repeat') OR (NOT working_hours_only AND severity_condition = 'any')))
);

-- The steps that name a Schedule, which refuse its deletion (schedule_in_use) and back the foreign key of its DELETE.
CREATE INDEX escalation_steps_schedule_idx ON escalation_steps (schedule_id);
CREATE INDEX escalation_steps_team_idx ON escalation_steps (org_id, team_id);
CREATE INDEX escalation_steps_gateway_idx ON escalation_steps (org_id, gateway_connection_id);
CREATE INDEX escalation_steps_destination_idx ON escalation_steps (org_id, destination_id);

CREATE TABLE escalation_step_users (
    org_id   bigint NOT NULL REFERENCES organizations (id),
    step_id  bigint NOT NULL REFERENCES escalation_steps (id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position > 0),
    user_id  bigint NOT NULL REFERENCES users (id),
    PRIMARY KEY (step_id, position),
    -- A step names each person once.
    CONSTRAINT escalation_step_users_user_key UNIQUE (step_id, user_id)
);

-- The chains of the Routes and of the Teams. A chain that a Route or a Team that is not deleted names is never
-- deleted (chain_in_use); the rows of deleted Routes go with the chain.
CREATE TABLE route_escalation_chains (
    org_id             bigint NOT NULL REFERENCES organizations (id),
    route_id           bigint NOT NULL REFERENCES routes (id),
    chain_id           bigint NOT NULL REFERENCES escalation_chains (id) ON DELETE CASCADE,
    severity_condition text NOT NULL DEFAULT 'any'
        CHECK (severity_condition IN ('any', 'critical', 'below_critical')),
    added_at           timestamptz NOT NULL,
    PRIMARY KEY (route_id, chain_id)
);

CREATE INDEX route_escalation_chains_chain_idx ON route_escalation_chains (org_id, chain_id);

CREATE TABLE team_escalation_chains (
    org_id             bigint NOT NULL REFERENCES organizations (id),
    team_id            bigint NOT NULL REFERENCES teams (id),
    chain_id           bigint NOT NULL REFERENCES escalation_chains (id) ON DELETE CASCADE,
    severity_condition text NOT NULL DEFAULT 'any'
        CHECK (severity_condition IN ('any', 'critical', 'below_critical')),
    added_at           timestamptz NOT NULL,
    PRIMARY KEY (team_id, chain_id)
);

CREATE INDEX team_escalation_chains_chain_idx ON team_escalation_chains (org_id, chain_id);

INSERT INTO permissions (name) VALUES ('escalation-chains:read'), ('escalation-chains:write');

INSERT INTO role_permissions (role, permission) VALUES
    ('admin', 'escalation-chains:read'), ('admin', 'escalation-chains:write'),
    ('responder', 'escalation-chains:read'),
    ('viewer', 'escalation-chains:read');

COMMIT;
