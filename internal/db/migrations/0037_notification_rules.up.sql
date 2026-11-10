-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0037 adds the Notification rules of C-31: organizations.notification_rules, the Organization's default
-- rules (organization.notification_rules, C-31.FR-2) — normal: a Mention; important: a Mention, a wait of 5 minutes and
-- a Mention — which every existing Organization takes as its default; and notification_rules, the rules of a User whose
-- state is "Call", "Don't call" or custom, one row per User, which holds the normal and the important sequence computed
-- when the User chose the state (journal D569). A User without a row follows the Organization's current rules, the
-- state "As in the Organization". gateway_connection_id is the Notification gateway of the state call, set exactly
-- with it; Connections are never hard-deleted, so the action of a deleted gateway stays in the rule. version comes
-- from a sequence, so that a row deleted by "Reset to the Organization's rules" and written again never repeats the
-- ETag of an earlier one. Deleting the User deletes the row in the transaction of the deletion.
-- Expand only: the column has a default, which a replica of an earlier version that creates an Organization gets,
-- and the new table is empty; an earlier binary reads and writes nothing of either.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE organizations
    ADD COLUMN notification_rules jsonb NOT NULL DEFAULT
        '{"normal": [{"kind": "mention"}], "important": [{"kind": "mention"}, {"kind": "wait", "wait_seconds": 300}, {"kind": "mention"}]}'::jsonb,
    -- IS TRUE: a missing rule makes jsonb_typeof null, which a CHECK would let pass.
    ADD CONSTRAINT organizations_notification_rules_check CHECK ((
        jsonb_typeof(notification_rules) = 'object'
        AND jsonb_typeof(notification_rules -> 'normal') = 'array'
        AND jsonb_typeof(notification_rules -> 'important') = 'array') IS TRUE);

CREATE SEQUENCE notification_rules_version_seq;

CREATE TABLE notification_rules (
    org_id                bigint NOT NULL REFERENCES organizations (id),
    user_id               bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    state                 text NOT NULL CHECK (state IN ('call', 'no_call', 'custom')),
    gateway_connection_id bigint REFERENCES connections (id),  -- the gateway of the state call
    normal                jsonb NOT NULL CHECK (jsonb_typeof(normal) = 'array'),
    important             jsonb NOT NULL CHECK (jsonb_typeof(important) = 'array'),
    version               bigint NOT NULL DEFAULT nextval('notification_rules_version_seq'),
    updated_at            timestamptz NOT NULL,
    CONSTRAINT notification_rules_gateway_check CHECK ((state = 'call') = (gateway_connection_id IS NOT NULL))
);

ALTER SEQUENCE notification_rules_version_seq OWNED BY notification_rules.version;

-- The rules that call through a gateway, for its foreign key.
CREATE INDEX notification_rules_gateway_idx ON notification_rules (org_id, gateway_connection_id);

COMMIT;
