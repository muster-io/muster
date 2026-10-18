-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Request log (C-23): request_log, one row per HTTP request Muster sent for a Connection or an outgoing
-- webhook Destination — from S-089 also per request that reached a Connection's callbacks — with its answer, masked,
-- kept to the newest request_log.max_records of each Connection, each outgoing webhook Destination and the records
-- without an owner, and to request_log.max_age, by the batched deletes of the Leader task request_log_pruning. Not
-- partitioned: its size is bounded per owner, and the count bound needs deletes whatever the partitions. The foreign
-- keys to connections and destinations take FOR KEY SHARE, which a save of the Connection or the Destination (FOR NO
-- KEY UPDATE) does not wait for; alert_group_id has no foreign key and is kept by the single writer, as a record goes
-- long before its Alert Group. Expand only: an earlier binary never reads or writes the table.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE request_log (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                  bigint NOT NULL REFERENCES organizations (id),
    public_id               text NOT NULL CHECK (public_id ~ '^RQ[0-9A-HJKMNP-TV-Z]{12}$'),
    at                      timestamptz NOT NULL,  -- business clock
    direction               text NOT NULL CHECK (direction IN ('outbound', 'inbound')),
    operation               text NOT NULL CHECK (char_length(operation) BETWEEN 1 AND 64),
    check_kind              text CHECK (check_kind IN ('connection_check', 'draft_check', 'destination_check', 'test',
                                                       'probe')),
    check_run               text CHECK (check_run ~ '^CR[0-9A-HJKMNP-TV-Z]{12}$'),
    method                  text NOT NULL CHECK (char_length(method) BETWEEN 1 AND 16),
    url                     text NOT NULL,         -- as errors show it: no user information, Secrets masked
    request_headers         jsonb NOT NULL CHECK (jsonb_typeof(request_headers) = 'object'),
    request_body            text NOT NULL,         -- masked, untrusted, cut at request_log.body_max
    request_body_bytes      bigint NOT NULL CHECK (request_body_bytes >= 0),
    request_body_truncated  boolean NOT NULL,
    status                  integer CHECK (status BETWEEN 100 AND 999),  -- null without an answer
    response_headers        jsonb CHECK (jsonb_typeof(response_headers) = 'object'),
    response_body           text,                  -- masked, untrusted, cut at request_log.body_max
    response_body_bytes     bigint NOT NULL CHECK (response_body_bytes >= 0),
    response_body_truncated boolean NOT NULL,
    latency_ms              integer NOT NULL CHECK (latency_ms >= 0),
    via                     text NOT NULL CHECK (via IN ('direct', 'proxy')),
    attempt                 integer NOT NULL CHECK (attempt >= 1),
    error_class             text CHECK (error_class IN ('retry_after', 'transient', 'fatal', 'unknown', 'blocked',
                                                        'redirect')),
    error_code              text CHECK (error_code IN ('dns_failed', 'connect_failed', 'tls_failed', 'timeout',
                                                       'proxy_refused', 'blocked', 'request_failed')),
    error                   text,                  -- masked, untrusted
    connection_id           bigint REFERENCES connections (id) ON DELETE CASCADE,
    destination_id          bigint REFERENCES destinations (id) ON DELETE CASCADE,
    alert_group_id          bigint,                -- alert_groups.id, kept by the single writer
    UNIQUE (org_id, public_id),
    -- An answer has its status, headers and body; a request that got none has none of them.
    CONSTRAINT request_log_answer_check CHECK ((status IS NULL) = (response_headers IS NULL)
        AND (status IS NULL) = (response_body IS NULL)),
    -- The code names a failure without an answer.
    CONSTRAINT request_log_error_code_check CHECK (error_code IS NULL OR status IS NULL)
);

-- The list and the count bound of a Connection; of a Destination: an outgoing webhook's own records and the records of
-- a Connection that name a messenger Destination; and the age bound.
CREATE INDEX request_log_connection_idx ON request_log (org_id, connection_id, at DESC, id DESC)
    WHERE connection_id IS NOT NULL;
CREATE INDEX request_log_destination_idx ON request_log (org_id, destination_id, at DESC, id DESC)
    WHERE destination_id IS NOT NULL;
CREATE INDEX request_log_at_idx ON request_log (org_id, at);

COMMIT;
