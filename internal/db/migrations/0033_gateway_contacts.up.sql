-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0033 adds the contacts of people at Notification gateways (C-30.FR-3): gateway_contacts, one contact per
-- person and Bridge Connection in the role gateway, in plain text since a contact is personal data rather than a
-- Secret, with at most one confirmed contact per person and gateway; and gateway_contact_codes, the confirmation codes
-- of gateway.contact_code, of which only the SHA-256 is stored, each tied to the web session that asked for it (ADR-0013)
-- and to the SHA-256 of the contact text, checked only once the gateway accepted it, so that the hourly limits of gateway.contact_code_requests per User and per
-- contact still count the codes of a contact that was removed. Deleting a User erases both (C-03.FR-13); the Leader
-- prunes expired codes with the expired Account link requests.
-- Expand only: the new tables are empty and an earlier binary reads and writes nothing of them.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE gateway_contacts (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id        bigint NOT NULL REFERENCES organizations (id),
    public_id     text NOT NULL CHECK (public_id ~ '^GC[0-9A-HJKMNP-TV-Z]{12}$'),
    user_id       bigint NOT NULL REFERENCES users (id),
    connection_id bigint NOT NULL REFERENCES connections (id),
    contact       text NOT NULL CHECK (char_length(contact) BETWEEN 1 AND 200),
    confirmed_at  timestamptz,
    created_at    timestamptz NOT NULL,
    UNIQUE (org_id, public_id)
);

-- One confirmed contact per person and gateway: a new one replaces it in the transaction of its confirmation.
CREATE UNIQUE INDEX gateway_contacts_confirmed_key ON gateway_contacts (user_id, connection_id)
    WHERE confirmed_at IS NOT NULL;
CREATE INDEX gateway_contacts_connection_idx ON gateway_contacts (org_id, connection_id);
CREATE INDEX gateway_contacts_user_idx ON gateway_contacts (org_id, user_id);

CREATE TABLE gateway_contact_codes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    user_id      bigint NOT NULL REFERENCES users (id),
    contact_id   bigint REFERENCES gateway_contacts (id) ON DELETE SET NULL,  -- null once the contact is removed
    contact_hash bytea NOT NULL CHECK (octet_length(contact_hash) = 32),       -- SHA-256 of the trimmed contact text
    session_id   bigint NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,   -- the web session that asked
    code_hash    bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    attempts     bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),              -- gateway.contact_code
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    sent_at      timestamptz,  -- the gateway accepted its notification; only a sent code is checked
    used_at      timestamptz,
    voided_at    timestamptz
);

CREATE INDEX gateway_contact_codes_contact_idx ON gateway_contact_codes (org_id, contact_id, created_at);
-- The hourly limits of gateway.contact_code_requests: per User, and per contact text whoever asked.
CREATE INDEX gateway_contact_codes_user_idx ON gateway_contact_codes (org_id, user_id, created_at);
CREATE INDEX gateway_contact_codes_hash_idx ON gateway_contact_codes (org_id, contact_hash, created_at);
CREATE INDEX gateway_contact_codes_expires_idx ON gateway_contact_codes (org_id, expires_at);

COMMIT;
