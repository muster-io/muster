-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0024 adds the Schedules of C-27: schedules, each of a Team, with a name unique in the Team ignoring case, a
-- time zone and at most one main Schedule per Team; rotation_layers, the Rotation layers of a Schedule from the lowest,
-- with their Shift length, handover and limits, a window whose end is earlier than its start crossing midnight;
-- rotation_layer_users, the people of each Rotation layer in turn; schedule_overrides, the Overrides on top, made by a
-- User or a Service account; and the Permissions schedules:read, for every Role, schedules:write, for Admins, and
-- schedule-overrides:write, for Admins and Responders. Expand only: the new tables are empty and an earlier binary
-- reads and writes none of them.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE schedules (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint NOT NULL REFERENCES organizations (id),
    public_id  text NOT NULL CHECK (public_id ~ '^SC[0-9A-HJKMNP-TV-Z]{12}$'),
    team_id    bigint NOT NULL REFERENCES teams (id),
    name       text NOT NULL CHECK (name <> ''),
    time_zone  text NOT NULL CHECK (time_zone <> ''),  -- schedule.time_zone, stored at creation (P-77)
    main       boolean NOT NULL DEFAULT false,
    version    bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (org_id, public_id)
);

CREATE UNIQUE INDEX schedules_name_key ON schedules (team_id, lower(name));
-- One main Schedule per Team (C-27.FR-1); a Team has none after its main Schedule is deleted (P-66).
CREATE UNIQUE INDEX schedules_main_key ON schedules (team_id) WHERE main;

CREATE TABLE rotation_layers (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        bigint NOT NULL REFERENCES organizations (id),
    public_id     text NOT NULL CHECK (public_id ~ '^RY[0-9A-HJKMNP-TV-Z]{12}$'),
    schedule_id   bigint NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    position      integer NOT NULL CHECK (position > 0),  -- 1 for the lowest
    name          text NOT NULL CHECK (name <> ''),
    shift_length  text NOT NULL CHECK (shift_length IN ('day', 'week', 'hours')),
    shift_seconds integer,
    handover_time text NOT NULL CHECK (handover_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    handover_day  text CHECK (handover_day IN ('monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday',
        'sunday')),
    start_date    date NOT NULL,
    limit_days    text[],
    limit_start   text CHECK (limit_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    limit_end     text CHECK (limit_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    UNIQUE (org_id, public_id),
    -- A save renumbers the Rotation layers it keeps; the positions are unique once it commits.
    CONSTRAINT rotation_layers_position_key UNIQUE (schedule_id, position) DEFERRABLE INITIALLY DEFERRED,
    -- shift_seconds is set exactly with hours: whole hours from one hour.
    CONSTRAINT rotation_layers_shift_seconds_check CHECK ((shift_length = 'hours') = (shift_seconds IS NOT NULL)
        AND shift_seconds >= 3600 AND shift_seconds % 3600 = 0),
    -- handover_day is set exactly with week.
    CONSTRAINT rotation_layers_handover_day_set_check CHECK ((shift_length = 'week') = (handover_day IS NOT NULL)),
    CONSTRAINT rotation_layers_limit_days_check CHECK (cardinality(limit_days) > 0
        AND limit_days <@ ARRAY['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']),
    -- The time of day of the limits is set with both its ends, and only with days.
    CONSTRAINT rotation_layers_limit_times_check CHECK ((limit_start IS NULL) = (limit_end IS NULL)
        AND (limit_start IS NULL OR limit_days IS NOT NULL)),
    -- An end earlier than the start crosses midnight; an end equal to the start is no window.
    CONSTRAINT rotation_layers_limit_order_check CHECK (limit_end <> limit_start)
);

CREATE TABLE rotation_layer_users (
    org_id            bigint NOT NULL REFERENCES organizations (id),
    rotation_layer_id bigint NOT NULL REFERENCES rotation_layers (id) ON DELETE CASCADE,
    position          integer NOT NULL CHECK (position > 0),
    user_id           bigint NOT NULL REFERENCES users (id),
    PRIMARY KEY (rotation_layer_id, position)
);

CREATE INDEX rotation_layer_users_user_idx ON rotation_layer_users (org_id, user_id);

CREATE TABLE schedule_overrides (
    id                            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                        bigint NOT NULL REFERENCES organizations (id),
    public_id                     text NOT NULL CHECK (public_id ~ '^VR[0-9A-HJKMNP-TV-Z]{12}$'),
    schedule_id                   bigint NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    user_id                       bigint NOT NULL REFERENCES users (id),
    replaces_user_id              bigint REFERENCES users (id),
    starts_at                     timestamptz NOT NULL,
    ends_at                       timestamptz NOT NULL,
    created_by_user_id            bigint REFERENCES users (id),
    created_by_service_account_id bigint REFERENCES service_accounts (id),
    version                       bigint NOT NULL DEFAULT 1,
    created_at                    timestamptz NOT NULL,
    updated_at                    timestamptz NOT NULL,
    UNIQUE (org_id, public_id),
    CONSTRAINT schedule_overrides_span_check CHECK (ends_at > starts_at),
    -- Made by exactly one User or Service account.
    CONSTRAINT schedule_overrides_created_by_check CHECK (
        (created_by_user_id IS NULL) <> (created_by_service_account_id IS NULL))
);

-- The Overrides of a Schedule in a period.
CREATE INDEX schedule_overrides_period_idx ON schedule_overrides (org_id, schedule_id, ends_at);

INSERT INTO permissions (name) VALUES ('schedules:read'), ('schedules:write'), ('schedule-overrides:write');

INSERT INTO role_permissions (role, permission) VALUES
    ('admin', 'schedules:read'), ('admin', 'schedules:write'), ('admin', 'schedule-overrides:write'),
    ('responder', 'schedules:read'), ('responder', 'schedule-overrides:write'),
    ('viewer', 'schedules:read');

COMMIT;
