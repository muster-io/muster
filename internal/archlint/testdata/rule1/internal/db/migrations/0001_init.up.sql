CREATE TABLE organizations (
    id bigint PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE roles (
    id bigint PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE integrations (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL REFERENCES organizations (id),
    token_hash bytea NOT NULL UNIQUE
);

CREATE TABLE alert_groups (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL REFERENCES organizations (id),
    state text NOT NULL
);

CREATE TABLE alerts (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL REFERENCES organizations (id),
    group_id bigint NOT NULL,
    integration_id bigint NOT NULL,
    state text NOT NULL
);

CREATE TABLE events (
    org_id bigint NOT NULL,
    at timestamptz NOT NULL,
    body text NOT NULL
) PARTITION BY RANGE (at);

CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');

CREATE TABLE destinations (
    id bigint PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE legacy (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL
);

CREATE TABLE drafts (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL
);

CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END;
$$;
