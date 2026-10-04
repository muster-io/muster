CREATE TABLE alerts (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL
);

CREATE TABLE alert_groups (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL,
    number bigint NOT NULL,
    state text NOT NULL
);

CREATE TABLE alert_group_alerts (
    org_id bigint NOT NULL,
    group_id bigint NOT NULL,
    alert_id bigint NOT NULL,
    last_seen_at timestamptz
);

CREATE TABLE timeline_entries (
    org_id bigint NOT NULL,
    group_id bigint NOT NULL,
    kind text NOT NULL
);

CREATE TABLE notes (
    id bigint PRIMARY KEY,
    org_id bigint NOT NULL,
    group_id bigint NOT NULL
);

CREATE TABLE alert_group_counters (
    org_id bigint PRIMARY KEY,
    next bigint NOT NULL
);

CREATE TABLE delivery_events (
    org_id bigint NOT NULL,
    group_id bigint NOT NULL,
    kind text NOT NULL
);
