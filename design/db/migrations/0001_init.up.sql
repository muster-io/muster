-- Muster L1 — initial schema.
--
-- PostgreSQL 14 or newer. golang-migrate format; this directory is also the schema that sqlc reads.
-- design/db/schema.md explains every table group, its invariants, indexes and retention.
--
-- Conventions
--   * Internal keys are `bigint GENERATED ALWAYS AS IDENTITY`. They never leave the server: URLs, the API and signed
--     buttons use the opaque `public_id`, generated in Go: a two-letter type prefix and 12 random characters of
--     Crockford base32 (60 bits), stored in canonical upper case, for example `AGK7M3QX9P2RTA`. A CHECK on every
--     public_id column pins its prefix and alphabet (the prefix table is in api/README.md).
--   * Every organization-scoped table has `org_id` and every query filters by it (architecture lint). The few
--     installation-level tables (Keyring, replicas, Leader state, reference data) have no `org_id` and say so.
--   * Time is always `timestamptz` and always comes from Go (virtual clock in tests). The only `now()` default is
--     `audit_log.recorded_at`, a database-side receipt time kept for tamper evidence.
--   * Closed value sets are `text` with a CHECK, not ENUM types, so a later migration can widen them with
--     expand/contract (replace the CHECK) instead of `ALTER TYPE ... ADD VALUE`.
--   * Secrets are stored as `<name>_ciphertext bytea` (AES-256-GCM) plus `<name>_key_id text` (the Keyring key that
--     encrypted them) plus `<name>_updated_at`. Never plaintext. The view `encrypted_values` lists every one of them.
--   * Tokens Muster issues are stored only as SHA-256 hashes (`*_hash bytea`, 32 bytes).
--   * Configuration rows carry `version bigint` for ETag / If-Match optimistic locking.
--   * Partitioned tables (Stored Snapshots and their bodies daily; Timeline entries, delivery events and the Audit log
--     monthly) are created here without partitions. Partitions are runtime work: at startup and then on the Leader,
--     Muster creates them ahead of time and drops whole partitions for retention. Partitioned tables reference only
--     `organizations` by foreign key; their other references are kept by their single writer (see schema.md).

BEGIN;

-- Every migration bounds its locks and run time (squawk enforces it); later migrations keep the pattern.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE EXTENSION IF NOT EXISTS pg_trgm;  -- trigram search over Alert Group titles and summaries

-- ============================================================================================================
-- Reference data (installation-level, no org_id)
-- ============================================================================================================

-- The fixed L1 Roles. Custom Roles are a later feature; these rows are reference data, not configuration.
CREATE TABLE roles (
    name text PRIMARY KEY CHECK (name IN ('admin', 'responder', 'viewer'))
);

-- The closed set of Permissions (`<resource>:<verb>`), the `Permission` schema of the API specification.
CREATE TABLE permissions (
    name text PRIMARY KEY CHECK (name ~ '^[a-z-]+:[a-z]+$')
);

-- The default allocation of Permissions to Roles (provisional in the PRD; a change is a new migration).
CREATE TABLE role_permissions (
    role       text NOT NULL REFERENCES roles (name),
    permission text NOT NULL REFERENCES permissions (name),
    PRIMARY KEY (role, permission)
);

INSERT INTO roles (name) VALUES ('admin'), ('responder'), ('viewer');

INSERT INTO permissions (name) VALUES
    ('alert-groups:read'), ('alert-groups:acknowledge'), ('alert-groups:resolve'), ('alert-groups:snooze'),
    ('alert-groups:note'), ('alerts:read'),
    ('integrations:read'), ('integrations:write'), ('stored-snapshots:read'),
    ('routes:read'), ('routes:write'),
    ('connections:read'), ('connections:write'),
    ('destinations:read'), ('destinations:write'), ('destinations:test'),
    ('link-rules:read'), ('link-rules:write'), ('lookup-tables:read'), ('lookup-tables:write'),
    ('templates:preview'),
    ('users:read'), ('users:write'), ('oidc:read'), ('oidc:write'),
    ('service-accounts:read'), ('service-accounts:write'),
    ('organization:read'), ('organization:write'),
    ('audit-log:read'), ('system-status:read');

INSERT INTO role_permissions (role, permission)
SELECT 'admin', name FROM permissions;

INSERT INTO role_permissions (role, permission) VALUES
    ('responder', 'alert-groups:read'), ('responder', 'alerts:read'),
    ('responder', 'alert-groups:acknowledge'), ('responder', 'alert-groups:resolve'),
    ('responder', 'alert-groups:snooze'), ('responder', 'alert-groups:note'),
    ('responder', 'integrations:read'), ('responder', 'routes:read'), ('responder', 'connections:read'),
    ('responder', 'destinations:read'), ('responder', 'link-rules:read'), ('responder', 'lookup-tables:read'),
    ('viewer', 'alert-groups:read'), ('viewer', 'alerts:read'),
    ('viewer', 'integrations:read'), ('viewer', 'routes:read'), ('viewer', 'connections:read'),
    ('viewer', 'destinations:read'), ('viewer', 'link-rules:read'), ('viewer', 'lookup-tables:read');

-- ============================================================================================================
-- Platform state (installation-level, no org_id)
-- ============================================================================================================

-- Which master key is active and the key canary. The key material itself is never stored: the Keyring comes from
-- MUSTER_SECRET_KEYS. Written once at first start under the migration lock, then only by `secrets rotate-key`.
CREATE TABLE keyring_state (
    singleton         boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    active_key_id     text NOT NULL CHECK (char_length(active_key_id) BETWEEN 1 AND 64),
    activated_at      timestamptz NOT NULL,
    canary_ciphertext bytea NOT NULL,
    canary_key_id     text NOT NULL,
    -- Activation re-encrypts the canary in the same transaction, so removing an old key never breaks startup.
    CONSTRAINT keyring_state_canary_active_check CHECK (canary_key_id = active_key_id)
);

-- Live-replica records: every replica refreshes its row every replica.key_record_refresh; a replica is live while
-- refreshed_at is younger than replica.live_expiry. Key activation is refused unless every live replica holds the key.
CREATE TABLE replicas (
    replica_id   text PRIMARY KEY CHECK (char_length(replica_id) BETWEEN 1 AND 128),
    hostname     text,
    version      text NOT NULL,
    key_ids      text[] NOT NULL,
    started_at   timestamptz NOT NULL,
    refreshed_at timestamptz NOT NULL
);

CREATE INDEX replicas_refreshed_idx ON replicas (refreshed_at);

-- The Leader's "alive" mark and the recovery window after downtime, written by the Leader; and the offset of the
-- development clock, written only in development mode (`muster dev`), so that all its replicas share one clock.
CREATE TABLE runtime_state (
    singleton                boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    leader_replica_id        text,
    leader_since             timestamptz,
    alive_at                 timestamptz,  -- refreshed every leader.alive_mark_interval
    recovery_until           timestamptz,  -- the "recovering after downtime" notice is active until then
    dev_clock_offset_seconds bigint NOT NULL DEFAULT 0,  -- always 0 outside development mode
    updated_at               timestamptz NOT NULL
);

-- Periods during which no Leader wrote an alive mark (Muster was down). Recorded by the next Leader.
CREATE TABLE downtime_periods (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    started_at  timestamptz NOT NULL,  -- the last alive mark before the outage
    ended_at    timestamptz NOT NULL,  -- when the next Leader took over
    recorded_at timestamptz NOT NULL,
    CONSTRAINT downtime_periods_order_check CHECK (ended_at > started_at)
);

CREATE INDEX downtime_periods_ended_idx ON downtime_periods (ended_at DESC);

-- ============================================================================================================
-- Organization and its settings
-- ============================================================================================================

-- The single Organization of L1 is created by Go at first start (with its Default route, its built-in Integration,
-- its counter row and the built-in Link rule), with the values of defaults.md. The table holds the Organization
-- settings resource; the outbound address policy has its own ETag and lives in outbound_policies.
CREATE TABLE organizations (
    id                                           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id                                    text NOT NULL UNIQUE CHECK (public_id ~ '^RG[0-9A-HJKMNP-TV-Z]{12}$'),
    name                                         text NOT NULL CHECK (name <> ''),
    time_zone                                    text NOT NULL,          -- IANA, for times in messages
    severity_label                               text NOT NULL,
    severity_mapping                             jsonb NOT NULL,         -- [{"value": "P1", "level": "critical"}, ...]
    severity_styles                              jsonb NOT NULL,         -- [{"level": ..., "emoji": ..., "color": ...}]
    critical_is_urgent                           boolean NOT NULL,
    instance_labels                              text[] NOT NULL,
    retention_stored_snapshots_days              bigint NOT NULL CHECK (retention_stored_snapshots_days > 0),
    retention_alert_details_days                 bigint NOT NULL CHECK (retention_alert_details_days > 0),
    retention_alert_group_summaries_days         bigint NOT NULL CHECK (retention_alert_group_summaries_days > 0),
    retention_audit_log_days                     bigint NOT NULL CHECK (retention_audit_log_days > 0),
    totp_required                                text NOT NULL CHECK (totp_required IN ('nobody', 'local_users', 'everyone')),
    -- auth.oidc_token_grace: a Personal access token of an OIDC account works while its last IdP contact is this recent.
    oidc_token_grace_seconds                     bigint NOT NULL CHECK (oidc_token_grace_seconds > 0),
    -- Outgoing heartbeat (sent only by the Leader). The URL is a Secret because such URLs carry tokens.
    outgoing_heartbeat_url_ciphertext            bytea,
    outgoing_heartbeat_url_key_id                text,
    outgoing_heartbeat_url_updated_at            timestamptz,
    outgoing_heartbeat_proxy                     jsonb NOT NULL DEFAULT '{"enabled": false}',
    outgoing_heartbeat_proxy_password_ciphertext bytea,
    outgoing_heartbeat_proxy_password_key_id     text,
    outgoing_heartbeat_proxy_password_updated_at timestamptz,
    outgoing_heartbeat_last_attempt_at           timestamptz,
    outgoing_heartbeat_last_ok                   boolean,
    outgoing_heartbeat_last_error                text,
    route_order_version                          bigint NOT NULL DEFAULT 1,  -- ETag of the Route list and its order
    version                                      bigint NOT NULL DEFAULT 1,
    created_at                                   timestamptz NOT NULL,
    updated_at                                   timestamptz NOT NULL,
    CONSTRAINT organizations_severity_mapping_check CHECK (jsonb_typeof(severity_mapping) = 'array'),
    CONSTRAINT organizations_severity_styles_check CHECK (jsonb_typeof(severity_styles) = 'array'),
    CONSTRAINT organizations_ohb_proxy_check CHECK (jsonb_typeof(outgoing_heartbeat_proxy) = 'object'),
    -- Details (Alerts, Timeline, delivery events) are always removed before the summary rows that they belong to.
    CONSTRAINT organizations_retention_order_check
        CHECK (retention_alert_group_summaries_days >= retention_alert_details_days),
    CONSTRAINT organizations_ohb_url_secret_check
        CHECK ((outgoing_heartbeat_url_ciphertext IS NULL) = (outgoing_heartbeat_url_key_id IS NULL)),
    CONSTRAINT organizations_ohb_proxy_secret_check
        CHECK ((outgoing_heartbeat_proxy_password_ciphertext IS NULL) = (outgoing_heartbeat_proxy_password_key_id IS NULL))
);

-- The outbound address policy (standard or strict) with its allowed and denied networks, host names and domains.
CREATE TABLE outbound_policies (
    org_id     bigint PRIMARY KEY REFERENCES organizations (id),
    policy     text NOT NULL CHECK (policy IN ('standard', 'strict')),
    allowed    text[] NOT NULL,  -- CIDR networks, host names, domains
    denied     text[] NOT NULL,  -- a denied entry always wins
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL
);

-- `#N` without gaps: the creating transaction increments last_number with UPDATE ... RETURNING. The row lock
-- serializes Alert Group creation per Organization and a rollback returns the number, so no gap appears.
CREATE TABLE alert_group_counters (
    org_id      bigint PRIMARY KEY REFERENCES organizations (id),
    last_number bigint NOT NULL DEFAULT 0 CHECK (last_number >= 0)
);

-- ============================================================================================================
-- Users, sign-in and sessions
-- ============================================================================================================

-- A User signs in either with a password or through an OIDC identity (issuer and subject), never both: the IdP is the
-- only source of truth for an OIDC account. `source` records how the account was created. Accounts are never merged
-- automatically by login or email: an OIDC sign-in whose login is taken is refused (login_taken). Linking an identity
-- from the profile wipes the password; an Admin converting the account back to local, or `muster admin reset-password`,
-- removes the identity. A local account waits for its first password with neither. When the IdP grants
-- `offline_access`, the user's offline token (a Secret) is kept here for the background re-checks; it never leaves the
-- server and is wiped when the user is disabled, deleted or converted to local.
CREATE TABLE users (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES organizations (id),
    public_id           text NOT NULL CHECK (public_id ~ '^SR[0-9A-HJKMNP-TV-Z]{12}$'),
    login               text NOT NULL CHECK (login <> ''),
    name                text NOT NULL CHECK (name <> ''),  -- display name; `deleted-user-<id>` after deletion
    email               text,
    role                text NOT NULL REFERENCES roles (name),
    source              text NOT NULL CHECK (source IN ('local', 'oidc', 'bootstrap')),
    status              text NOT NULL CHECK (status IN ('active', 'disabled', 'deleted')),
    password_hash       text,                              -- argon2id PHC string; never together with an OIDC identity
    password_changed_at timestamptz,
    oidc_issuer         text,                              -- the OIDC identity: created through OIDC, or linked
    oidc_subject        text,
    oidc_last_contact_at timestamptz,                      -- last successful OIDC sign-in or background re-check
    oidc_offline_token_ciphertext bytea,                   -- the IdP's offline (refresh) token, a Secret
    oidc_offline_token_key_id     text,
    oidc_offline_token_updated_at timestamptz,
    oidc_refused_at     timestamptz,                       -- the IdP refused a re-check; cleared by the next OIDC sign-in
    time_zone           text,                              -- null follows the browser
    language            text CHECK (language IN ('en', 'ru')),
    last_sign_in_at     timestamptz,
    deleted_at          timestamptz,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    version             bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    CONSTRAINT users_deleted_check CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
    CONSTRAINT users_oidc_identity_pair_check CHECK ((oidc_issuer IS NULL) = (oidc_subject IS NULL)),
    -- One credential at most: a password or an OIDC identity.
    CONSTRAINT users_one_credential_check CHECK (password_hash IS NULL OR oidc_subject IS NULL),
    CONSTRAINT users_offline_token_secret_check
        CHECK ((oidc_offline_token_ciphertext IS NULL) = (oidc_offline_token_key_id IS NULL)),
    -- The offline token belongs to an active account with an OIDC identity; disable, delete and conversion wipe it.
    CONSTRAINT users_offline_token_check CHECK (
        oidc_offline_token_ciphertext IS NULL OR (oidc_subject IS NOT NULL AND status = 'active')),
    CONSTRAINT users_deleted_pseudonymized_check CHECK (status <> 'deleted' OR email IS NULL)
);

-- Deleting a user pseudonymizes the login too, so the unique login stays free for reuse.
CREATE UNIQUE INDEX users_login_key ON users (org_id, lower(login));
-- One OIDC identity belongs to one account: linking an identity that another user holds is refused.
CREATE UNIQUE INDEX users_oidc_subject_key ON users (org_id, oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

-- TOTP (RFC 6238). The seed is a Secret. A pending seed exists between "enrol" and "confirm"; replay protection
-- accepts each time step once.
CREATE TABLE user_totp (
    user_id                 bigint PRIMARY KEY REFERENCES users (id),
    org_id                  bigint NOT NULL REFERENCES organizations (id),
    seed_ciphertext         bytea,
    seed_key_id             text,
    seed_updated_at         timestamptz,
    pending_seed_ciphertext bytea,
    pending_seed_key_id     text,
    pending_seed_updated_at timestamptz,
    enrolled_at             timestamptz,
    last_used_step          bigint,
    CONSTRAINT user_totp_seed_secret_check CHECK ((seed_ciphertext IS NULL) = (seed_key_id IS NULL)),
    CONSTRAINT user_totp_pending_secret_check CHECK ((pending_seed_ciphertext IS NULL) = (pending_seed_key_id IS NULL)),
    CONSTRAINT user_totp_enrolled_check CHECK ((seed_ciphertext IS NULL) = (enrolled_at IS NULL)),
    CONSTRAINT user_totp_any_seed_check CHECK (seed_ciphertext IS NOT NULL OR pending_seed_ciphertext IS NOT NULL)
);

-- Single-use recovery codes. They are short, so they are hashed with argon2id (PHC string), not a fast hash.
CREATE TABLE user_recovery_codes (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint NOT NULL REFERENCES organizations (id),
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  text NOT NULL,
    created_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE INDEX user_recovery_codes_user_idx ON user_recovery_codes (org_id, user_id) WHERE used_at IS NULL;

-- Browser sessions. The cookie value is hashed; the CSRF token is derived from it with a Keyring sub-key and not
-- stored. A limited session (totp_required, totp_enrolment_required) can only finish the second step or sign out.
-- OIDC accounts are re-checked at the IdP per user, in the background (oidc_checks), not per session. An OIDC session of
-- a user without an offline token lives at most auth.oidc_fallback_session_lifetime from sign-in.
CREATE TABLE sessions (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES organizations (id),
    public_id       text NOT NULL CHECK (public_id ~ '^SN[0-9A-HJKMNP-TV-Z]{12}$'),
    user_id         bigint NOT NULL REFERENCES users (id),
    token_hash      bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    state           text NOT NULL CHECK (state IN ('active', 'totp_required', 'totp_enrolment_required')),
    method          text NOT NULL CHECK (method IN ('local', 'oidc')),
    idp_mfa         boolean NOT NULL DEFAULT false,  -- the IdP asserted multi-factor authentication (`amr`)
    address         inet,
    user_agent      text,
    created_at      timestamptz NOT NULL,
    last_used_at    timestamptz NOT NULL,            -- refreshed at most once a minute
    idle_expires_at timestamptz NOT NULL,            -- auth.session_idle_timeout
    expires_at      timestamptz NOT NULL,            -- auth.session_lifetime
    ended_at        timestamptz,
    end_reason      text CHECK (end_reason IN (
                        'sign_out', 'sign_out_everywhere', 'password_changed', 'role_changed',
                        'user_disabled', 'user_deleted', 'totp_reset', 'expired', 'oidc_linked',
                        'converted_to_local', 'idp_refused')),
    UNIQUE (org_id, public_id),
    CONSTRAINT sessions_end_check CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);

CREATE INDEX sessions_user_idx ON sessions (org_id, user_id) WHERE ended_at IS NULL;
CREATE INDEX sessions_expires_idx ON sessions (org_id, expires_at);

-- Background re-checks of OIDC users at the IdP: one row per user who holds an offline token, due every
-- auth.oidc_recheck_interval. Any replica claims due rows with FOR UPDATE SKIP LOCKED and a lease, like timers, so one
-- check per user runs at a time. A user with neither a live session nor a usable Personal access token is skipped
-- without calling the IdP. The row goes when the offline token is wiped.
CREATE TABLE oidc_checks (
    user_id      bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    deadline     timestamptz NOT NULL,
    lease_owner  text,
    lease_until  timestamptz,
    last_outcome text CHECK (last_outcome IN ('ok', 'refused', 'unavailable', 'skipped')),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    CONSTRAINT oidc_checks_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

-- Claim: due rows in deadline order; the lease is checked on the few rows found.
CREATE INDEX oidc_checks_due_idx ON oidc_checks (org_id, deadline);

-- Sign-in throttling per account and per source address (auth.signin_throttle), shared by all replicas.
CREATE TABLE sign_in_throttles (
    org_id               bigint NOT NULL REFERENCES organizations (id),
    subject_kind         text NOT NULL CHECK (subject_kind IN ('account', 'address')),
    subject              text NOT NULL,  -- login (lower-cased) or source address
    consecutive_failures bigint NOT NULL CHECK (consecutive_failures >= 0),
    last_failure_at      timestamptz NOT NULL,
    blocked_until        timestamptz,
    PRIMARY KEY (org_id, subject_kind, subject)
);

-- Single-use password setup links. The token travels in the URL fragment and is stored only as a hash.
CREATE TABLE password_setups (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id             bigint NOT NULL REFERENCES organizations (id),
    user_id            bigint NOT NULL REFERENCES users (id),
    token_hash         bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_by_user_id bigint REFERENCES users (id),
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,       -- auth.password_setup_link_ttl
    used_at            timestamptz,                -- `link_used`
    superseded_at      timestamptz                 -- replaced by a newer link: also `link_used`
);

CREATE INDEX password_setups_user_idx ON password_setups (org_id, user_id) WHERE used_at IS NULL AND superseded_at IS NULL;

-- In-flight OIDC redirects (PKCE S256, state, nonce), shared by all replicas because the callback may reach another
-- one. Short-lived; the PKCE verifier is a Secret. A sign-in creates or finds the account; a link adds the identity to
-- the account of the web session that started it, and its callback is accepted only from that session.
CREATE TABLE oidc_auth_requests (
    state_hash               bytea PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    purpose                  text NOT NULL CHECK (purpose IN ('sign_in', 'link')),
    link_user_id             bigint REFERENCES users (id),
    link_session_id          bigint REFERENCES sessions (id) ON DELETE CASCADE,
    nonce                    text NOT NULL,
    code_verifier_ciphertext bytea NOT NULL,
    code_verifier_key_id     text NOT NULL,
    return_to                text CHECK (return_to ~ '^/([^/\\]|$)'),  -- a relative path, never `//host` or `/\host`
    created_at               timestamptz NOT NULL,
    expires_at               timestamptz NOT NULL,
    CONSTRAINT oidc_auth_requests_link_check CHECK (
        (purpose = 'link') = (link_user_id IS NOT NULL)
        AND (link_user_id IS NULL) = (link_session_id IS NULL)
        AND (purpose = 'sign_in' OR return_to IS NULL))
);

CREATE INDEX oidc_auth_requests_expires_idx ON oidc_auth_requests (org_id, expires_at);

-- OIDC settings; absent until an Admin configures OIDC.
CREATE TABLE oidc_settings (
    org_id                     bigint PRIMARY KEY REFERENCES organizations (id),
    enabled                    boolean NOT NULL,
    display_name               text,
    issuer_url                 text NOT NULL,
    client_id                  text NOT NULL,
    client_secret_ciphertext   bytea,
    client_secret_key_id       text,
    client_secret_updated_at   timestamptz,
    client_secret_expires_on   date,                 -- MusterOIDCSecretExpiring, oidc.secret_expiry_lead
    scopes                     text[] NOT NULL,
    groups_claim               text NOT NULL,
    group_mappings             jsonb NOT NULL,       -- [{"group": "oncall", "role": "responder"}, ...]
    unmatched_role             text NOT NULL CHECK (unmatched_role IN ('none', 'viewer', 'responder')),
    sync_role                  boolean NOT NULL,
    skip_totp_with_idp_mfa     boolean NOT NULL,
    proxy                      jsonb NOT NULL DEFAULT '{"enabled": false}',
    proxy_password_ciphertext  bytea,
    proxy_password_key_id      text,
    proxy_password_updated_at  timestamptz,
    groups_claim_missing_since timestamptz,          -- warning `groups_claim_missing` from the last check
    version                    bigint NOT NULL DEFAULT 1,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT oidc_settings_group_mappings_check CHECK (jsonb_typeof(group_mappings) = 'array'),
    CONSTRAINT oidc_settings_proxy_check CHECK (jsonb_typeof(proxy) = 'object'),
    CONSTRAINT oidc_settings_client_secret_check CHECK ((client_secret_ciphertext IS NULL) = (client_secret_key_id IS NULL)),
    CONSTRAINT oidc_settings_proxy_secret_check CHECK ((proxy_password_ciphertext IS NULL) = (proxy_password_key_id IS NULL))
);

-- ============================================================================================================
-- Service accounts and API tokens
-- ============================================================================================================

CREATE TABLE service_accounts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id     bigint NOT NULL REFERENCES organizations (id),
    public_id  text NOT NULL CHECK (public_id ~ '^SA[0-9A-HJKMNP-TV-Z]{12}$'),
    name       text NOT NULL CHECK (name <> ''),
    role       text NOT NULL REFERENCES roles (name),
    status     text NOT NULL CHECK (status IN ('active', 'disabled', 'deleted')),
    deleted_at timestamptz,  -- kept so that the Audit log and Timeline show it as "(deactivated)"
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    CONSTRAINT service_accounts_deleted_check CHECK ((status = 'deleted') = (deleted_at IS NOT NULL))
);

CREATE UNIQUE INDEX service_accounts_name_key ON service_accounts (org_id, lower(name)) WHERE status <> 'deleted';

-- Personal access tokens (`mstr_pat_`) and Service account tokens (`mstr_sat_`). The prefix tells the
-- authenticator which kind it holds; the stored value is the SHA-256 of the whole token. Integration tokens
-- (`mstr_int_`) live in integration_tokens because they authenticate a different listener.
CREATE TABLE api_tokens (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id             bigint NOT NULL REFERENCES organizations (id),
    public_id          text NOT NULL,
    kind               text NOT NULL CHECK (kind IN ('personal', 'service_account')),
    user_id            bigint REFERENCES users (id),
    service_account_id bigint REFERENCES service_accounts (id),
    name               text NOT NULL CHECK (name <> ''),
    token_hash         bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at         timestamptz,              -- null: never expires (the UI warns)
    created_at         timestamptz NOT NULL,
    last_used_at       timestamptz,              -- refreshed at most once a minute
    last_used_address  inet,
    revoked_at         timestamptz,
    revoked_reason     text CHECK (revoked_reason IN ('revoked', 'owner_deleted')),
    UNIQUE (org_id, public_id),
    -- The public_id prefix names the token kind: PT for a Personal access token, ST for a Service account token.
    CONSTRAINT api_tokens_public_id_check CHECK (
        (kind = 'personal' AND public_id ~ '^PT[0-9A-HJKMNP-TV-Z]{12}$')
        OR (kind = 'service_account' AND public_id ~ '^ST[0-9A-HJKMNP-TV-Z]{12}$')),
    CONSTRAINT api_tokens_owner_check CHECK (
        (kind = 'personal' AND user_id IS NOT NULL AND service_account_id IS NULL)
        OR (kind = 'service_account' AND service_account_id IS NOT NULL AND user_id IS NULL)),
    CONSTRAINT api_tokens_revoked_check CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);

CREATE INDEX api_tokens_user_idx ON api_tokens (org_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX api_tokens_service_account_idx ON api_tokens (org_id, service_account_id) WHERE service_account_id IS NOT NULL;

-- The Permissions a Personal access token is narrowed to (always a subset of its owner's at creation).
CREATE TABLE api_token_permissions (
    api_token_id bigint NOT NULL REFERENCES api_tokens (id) ON DELETE CASCADE,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    permission   text NOT NULL REFERENCES permissions (name),
    PRIMARY KEY (api_token_id, permission)
);

-- ============================================================================================================
-- Connections, Destinations and Account links
-- ============================================================================================================

-- A bot on a Mattermost server or a Telegram bot. Soft-deleted, because history points at it. It cannot be deleted
-- while a Destination that is not deleted uses it; deleting it abandons the final edits still pending for its deleted
-- Destinations (they end as not_delivered).
CREATE TABLE connections (
    id                                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                            bigint NOT NULL REFERENCES organizations (id),
    public_id                         text NOT NULL CHECK (public_id ~ '^CN[0-9A-HJKMNP-TV-Z]{12}$'),
    type                              text NOT NULL CHECK (type IN ('mattermost', 'telegram')),
    name                              text NOT NULL CHECK (name <> ''),
    mattermost_server_url             text,
    telegram_bot_api_base_url         text,
    telegram_update_mode              text CHECK (telegram_update_mode IN ('long_polling', 'webhook')),
    telegram_update_offset            bigint,  -- getUpdates offset, kept by the Leader's poller across handovers
    telegram_webhook_secret_ciphertext bytea,  -- secret token header expected in webhook mode
    telegram_webhook_secret_key_id    text,
    telegram_webhook_secret_updated_at timestamptz,
    bot_token_ciphertext              bytea,   -- wiped when the Connection is deleted
    bot_token_key_id                  text,
    bot_token_updated_at              timestamptz NOT NULL,
    bot_user_id                       text,    -- learned by the connection check
    bot_username                      text,
    proxy                             jsonb NOT NULL DEFAULT '{"enabled": false}',
    proxy_password_ciphertext         bytea,
    proxy_password_key_id             text,
    proxy_password_updated_at         timestamptz,
    limiter_limit                     bigint NOT NULL CHECK (limiter_limit > 0),
    limiter_per_seconds               bigint NOT NULL CHECK (limiter_per_seconds > 0),
    deleted_at                        timestamptz,
    created_at                        timestamptz NOT NULL,
    updated_at                        timestamptz NOT NULL,
    version                           bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    UNIQUE (id, type),  -- target of type-checked foreign keys from destinations and account links
    CONSTRAINT connections_type_fields_check CHECK (
        (type = 'mattermost' AND mattermost_server_url IS NOT NULL
            AND telegram_bot_api_base_url IS NULL AND telegram_update_mode IS NULL)
        OR (type = 'telegram' AND telegram_bot_api_base_url IS NOT NULL AND telegram_update_mode IS NOT NULL
            AND mattermost_server_url IS NULL)),
    CONSTRAINT connections_proxy_check CHECK (jsonb_typeof(proxy) = 'object'),
    CONSTRAINT connections_proxy_secret_check CHECK ((proxy_password_ciphertext IS NULL) = (proxy_password_key_id IS NULL)),
    CONSTRAINT connections_tg_webhook_secret_check
        CHECK ((telegram_webhook_secret_ciphertext IS NULL) = (telegram_webhook_secret_key_id IS NULL)),
    CONSTRAINT connections_bot_token_check CHECK (
        (bot_token_ciphertext IS NULL) = (bot_token_key_id IS NULL)
        AND (deleted_at IS NOT NULL OR bot_token_ciphertext IS NOT NULL))
);

CREATE UNIQUE INDEX connections_name_key ON connections (org_id, name) WHERE deleted_at IS NULL;

-- A place where Alert Groups are delivered: a Mattermost channel, a Telegram channel (configured by the channel alone;
-- its linked discussion group is found by the Destination check), or an outgoing webhook. Soft-deleted: open Root
-- messages get a final edit after deletion, then the Destination's secrets are wiped; the row stays for history. An
-- outgoing webhook in events mode has no Root message: its queued events end as not_delivered and its secrets are
-- wiped at once.
CREATE TABLE destinations (
    id                                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                                bigint NOT NULL REFERENCES organizations (id),
    public_id                             text NOT NULL CHECK (public_id ~ '^DS[0-9A-HJKMNP-TV-Z]{12}$'),
    type                                  text NOT NULL CHECK (type IN ('mattermost', 'telegram', 'webhook')),
    name                                  text NOT NULL CHECK (name <> ''),
    connection_id                         bigint,  -- messengers only
    mattermost_team_id                    text,
    mattermost_channel_id                 text,
    mattermost_team_name                  text,
    mattermost_channel_name               text,
    telegram_channel_id                   text,    -- chat id or @username as entered: the only Telegram setting
    telegram_channel_chat_id              bigint,  -- numeric ids learned by the Destination check
    telegram_discussion_chat_id           bigint,  -- getChat(channel).linked_chat_id: the channel's discussion group
    telegram_channel_title                text,
    telegram_discussion_group_title       text,
    webhook_mode                          text CHECK (webhook_mode IN ('events', 'template', 'both')),
    webhook_events_config                 jsonb,   -- {"url": <template>, "headers": [...]}
    webhook_template_config               jsonb,   -- {"create": ..., "update": ..., "open_thread": ..., "reply_in_thread": ...}
    proxy                                 jsonb NOT NULL DEFAULT '{"enabled": false}',  -- outgoing webhooks; messengers use the Connection's
    proxy_password_ciphertext             bytea,
    proxy_password_key_id                 text,
    proxy_password_updated_at             timestamptz,
    signing_secret_ciphertext             bytea,   -- outgoing webhooks: generated at creation
    signing_secret_key_id                 text,
    signing_secret_updated_at             timestamptz,
    previous_signing_secret_ciphertext    bytea,   -- still signs until the Admin retires it
    previous_signing_secret_key_id        text,
    previous_signing_secret_since         timestamptz,
    mentions                              jsonb NOT NULL,  -- MentionSettings, keyed by the Mention names of Loud events
    limiter_limit                         bigint NOT NULL CHECK (limiter_limit > 0),
    limiter_per_seconds                   bigint NOT NULL CHECK (limiter_per_seconds > 0),
    -- Health. Broken after a Fatal error or an exhausted Transient budget; probed every delivery.broken_probe_interval.
    health                                text NOT NULL CHECK (health IN ('healthy', 'broken')),
    broken_since                          timestamptz,
    broken_cause                          text CHECK (broken_cause IN ('fatal', 'unavailable')),
    broken_reason                         text,    -- masked, untrusted provider text
    next_probe_at                         timestamptz,
    -- Template error state (MusterTemplateError) of an outgoing webhook request template.
    template_error_since                  timestamptz,
    template_error                        text,
    deleted_at                            timestamptz,
    created_at                            timestamptz NOT NULL,
    updated_at                            timestamptz NOT NULL,
    version                               bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    -- A Mattermost Destination posts through a Mattermost Connection, a Telegram one through a Telegram Connection.
    FOREIGN KEY (connection_id, type) REFERENCES connections (id, type),
    CONSTRAINT destinations_type_fields_check CHECK (
        (type = 'mattermost' AND connection_id IS NOT NULL
            AND mattermost_team_id IS NOT NULL AND mattermost_channel_id IS NOT NULL AND webhook_mode IS NULL)
        OR (type = 'telegram' AND connection_id IS NOT NULL
            AND telegram_channel_id IS NOT NULL AND webhook_mode IS NULL)
        -- The Signing secret exists from creation until a deleted Destination's secrets are wiped.
        OR (type = 'webhook' AND connection_id IS NULL AND webhook_mode IS NOT NULL
            AND (signing_secret_ciphertext IS NOT NULL OR deleted_at IS NOT NULL)
            AND (webhook_mode = 'template' OR webhook_events_config IS NOT NULL)
            AND (webhook_mode = 'events' OR webhook_template_config IS NOT NULL))),
    CONSTRAINT destinations_broken_state_check CHECK (
        (health = 'healthy' AND broken_since IS NULL AND broken_cause IS NULL)
        OR (health = 'broken' AND broken_since IS NOT NULL AND broken_cause IS NOT NULL AND next_probe_at IS NOT NULL)),
    CONSTRAINT destinations_mentions_check CHECK (jsonb_typeof(mentions) = 'object'),
    CONSTRAINT destinations_proxy_check CHECK (jsonb_typeof(proxy) = 'object'),
    CONSTRAINT destinations_proxy_secret_check CHECK ((proxy_password_ciphertext IS NULL) = (proxy_password_key_id IS NULL)),
    CONSTRAINT destinations_signing_secret_check CHECK ((signing_secret_ciphertext IS NULL) = (signing_secret_key_id IS NULL)),
    CONSTRAINT destinations_previous_signing_secret_check CHECK (
        (previous_signing_secret_ciphertext IS NULL) = (previous_signing_secret_key_id IS NULL)
        AND (previous_signing_secret_ciphertext IS NULL) = (previous_signing_secret_since IS NULL))
);

CREATE UNIQUE INDEX destinations_name_key ON destinations (org_id, name) WHERE deleted_at IS NULL;
CREATE INDEX destinations_connection_idx ON destinations (connection_id) WHERE connection_id IS NOT NULL;
CREATE INDEX destinations_probe_idx ON destinations (org_id, next_probe_at) WHERE health = 'broken';

-- Named Secrets of an outgoing webhook Destination, referenced as {{ .Secrets.<name> }}.
CREATE TABLE destination_secrets (
    destination_id   bigint NOT NULL REFERENCES destinations (id) ON DELETE CASCADE,
    org_id           bigint NOT NULL REFERENCES organizations (id),
    name             text NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    value_ciphertext bytea NOT NULL,
    value_key_id     text NOT NULL,
    value_updated_at timestamptz NOT NULL,
    PRIMARY KEY (destination_id, name)
);

-- The tie between a User and a messenger account. Identity space: all of Telegram, or one Mattermost Connection.
--   * one messenger account belongs to one User:      UNIQUE (org_id, identity_space, external_id)
--   * one account per identity space for each User:   UNIQUE (org_id, user_id, identity_space)
-- A new link of the same User in the same space replaces the old one (delete + insert in one transaction, audited).
CREATE TABLE account_links (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    public_id      text NOT NULL CHECK (public_id ~ '^AK[0-9A-HJKMNP-TV-Z]{12}$'),
    user_id        bigint NOT NULL REFERENCES users (id),
    messenger      text NOT NULL CHECK (messenger IN ('telegram', 'mattermost')),
    connection_id  bigint NOT NULL,  -- the Connection the link was made through
    identity_space text NOT NULL GENERATED ALWAYS AS (
                       CASE WHEN messenger = 'telegram' THEN 'telegram'
                            ELSE 'mattermost:' || connection_id::text END) STORED,
    external_id    text NOT NULL CHECK (external_id <> ''),  -- Telegram user id or Mattermost user id
    username       text,
    created_at     timestamptz NOT NULL,
    last_used_at   timestamptz,
    UNIQUE (org_id, public_id),
    FOREIGN KEY (connection_id, messenger) REFERENCES connections (id, type),
    CONSTRAINT account_links_account_key UNIQUE (org_id, identity_space, external_id),
    CONSTRAINT account_links_user_space_key UNIQUE (org_id, user_id, identity_space)
);

-- Pending links started from the web session: a Telegram deep-link token or a Mattermost code, hashed.
CREATE TABLE account_link_requests (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id             bigint NOT NULL REFERENCES organizations (id),
    public_id          text NOT NULL CHECK (public_id ~ '^AR[0-9A-HJKMNP-TV-Z]{12}$'),
    user_id            bigint NOT NULL REFERENCES users (id),
    messenger          text NOT NULL CHECK (messenger IN ('telegram', 'mattermost')),
    connection_id      bigint NOT NULL,
    secret_hash        bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    target_external_id text,  -- Mattermost: the user the code was sent to
    target_username    text,
    attempts           bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),  -- account_link.mattermost_code
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    consumed_at        timestamptz,
    voided_at          timestamptz,
    UNIQUE (org_id, public_id),
    FOREIGN KEY (connection_id, messenger) REFERENCES connections (id, type),
    CONSTRAINT account_link_requests_target_check CHECK (messenger <> 'mattermost' OR target_external_id IS NOT NULL)
);

-- `/start <token>` arrives without any other context, so the Telegram token is looked up by its hash alone.
CREATE UNIQUE INDEX account_link_requests_token_key ON account_link_requests (secret_hash) WHERE messenger = 'telegram';
CREATE INDEX account_link_requests_user_idx ON account_link_requests (org_id, user_id, created_at);
CREATE INDEX account_link_requests_target_idx ON account_link_requests (org_id, connection_id, target_external_id, created_at)
    WHERE messenger = 'mattermost';

-- ============================================================================================================
-- Integrations and ingestion
-- ============================================================================================================

-- One Integration per Alertmanager cluster; the built-in "Muster" Integration carries Internal alerts.
-- Soft-deleted: its tokens stop working at once, its history stays until retention.
CREATE TABLE integrations (
    id                        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                    bigint NOT NULL REFERENCES organizations (id),
    public_id                 text NOT NULL CHECK (public_id ~ '^NT[0-9A-HJKMNP-TV-Z]{12}$'),
    name                      text NOT NULL CHECK (name <> ''),
    description               text NOT NULL DEFAULT '',
    builtin                   boolean NOT NULL DEFAULT false,
    connection_mode           text NOT NULL CHECK (connection_mode IN ('webhook_only')),
    static_labels             jsonb NOT NULL,
    duplicate_window_seconds  bigint NOT NULL CHECK (duplicate_window_seconds > 0),
    heartbeat_enabled         boolean NOT NULL,
    heartbeat_timeout_seconds bigint NOT NULL CHECK (heartbeat_timeout_seconds > 0),
    -- Runtime state (not part of the ETag).
    heartbeat_state           text NOT NULL CHECK (heartbeat_state IN ('not_configured', 'waiting', 'live', 'lost')),
    heartbeat_last_signal_at  timestamptz,
    heartbeat_lost_since      timestamptz,
    -- The liveness clock: milliseconds of time during which the path was alive. It advances only between Heartbeat
    -- signals that arrive within the timeout, so time with a lost Heartbeat, and time while Muster was down (no
    -- signal could be received), never counts towards stale_after.
    liveness_clock_ms         bigint NOT NULL DEFAULT 0 CHECK (liveness_clock_ms >= 0),
    snapshot_count            bigint NOT NULL DEFAULT 0,  -- maintained by processing, so ingestion stays insert-only
    last_snapshot_at          timestamptz,
    deleted_at                timestamptz,
    created_at                timestamptz NOT NULL,
    updated_at                timestamptz NOT NULL,
    version                   bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    CONSTRAINT integrations_static_labels_check CHECK (jsonb_typeof(static_labels) = 'object'),
    CONSTRAINT integrations_heartbeat_check CHECK (heartbeat_enabled = (heartbeat_state <> 'not_configured')),
    CONSTRAINT integrations_heartbeat_lost_check CHECK ((heartbeat_state = 'lost') = (heartbeat_lost_since IS NOT NULL)),
    CONSTRAINT integrations_builtin_check CHECK (NOT builtin OR (deleted_at IS NULL AND NOT heartbeat_enabled))
);

CREATE UNIQUE INDEX integrations_name_key ON integrations (org_id, name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX integrations_builtin_key ON integrations (org_id) WHERE builtin;
CREATE INDEX integrations_heartbeat_idx ON integrations (org_id) WHERE heartbeat_state IN ('live', 'lost') AND deleted_at IS NULL;

-- Integration tokens (`mstr_int_`): ingestion and Heartbeat only. A request carries nothing but the token, so the
-- lookup is by hash alone and yields the Organization.
CREATE TABLE integration_tokens (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    public_id      text NOT NULL CHECK (public_id ~ '^NK[0-9A-HJKMNP-TV-Z]{12}$'),
    integration_id bigint NOT NULL REFERENCES integrations (id),
    name           text,
    token_hash     bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at     timestamptz NOT NULL,
    last_used_at   timestamptz,  -- refreshed at most once a minute, never on every request
    revoked_at     timestamptz,
    UNIQUE (org_id, public_id)
);

CREATE INDEX integration_tokens_integration_idx ON integration_tokens (org_id, integration_id) WHERE revoked_at IS NULL;

-- The ingestion queue is the Stored Snapshots table itself; this row makes processing exclusive per Integration:
-- a worker leases the Integration, then processes its pending Snapshots in (received_at, id) order.
CREATE TABLE ingest_claims (
    integration_id bigint PRIMARY KEY REFERENCES integrations (id),
    org_id         bigint NOT NULL REFERENCES organizations (id),
    lease_owner    text,
    lease_until    timestamptz,
    CONSTRAINT ingest_claims_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

-- Stored Snapshots: every webhook body exactly as received, plus its processing state. Daily partitions by
-- received_at (UTC days), kept retention.stored_snapshots days, removed by dropping whole partitions.
-- The body lives in snapshot_bodies, de-duplicated by SHA-256 within a day: Alertmanager repeats and HA copies are
-- usually byte-identical.
--
-- Example partition (created ahead of time by Muster, never by a migration):
--   CREATE TABLE stored_snapshots_p20261003 PARTITION OF stored_snapshots
--       FOR VALUES FROM ('2026-10-03 00:00:00+00') TO ('2026-10-04 00:00:00+00');
-- Retention (with snapshot_bodies_p20260919 dropped in the same run):
--   ALTER TABLE stored_snapshots DETACH PARTITION stored_snapshots_p20260919 CONCURRENTLY;
--   DROP TABLE stored_snapshots_p20260919;
CREATE TABLE stored_snapshots (
    id               bigint GENERATED ALWAYS AS IDENTITY,
    org_id           bigint NOT NULL REFERENCES organizations (id),
    public_id        text NOT NULL CHECK (public_id ~ '^SS[0-9A-HJKMNP-TV-Z]{12}$'),
    integration_id   bigint NOT NULL,  -- integrations.id
    source           text NOT NULL CHECK (source IN ('webhook', 'internal')),  -- internal: a raise or resolve of an Internal alert
    received_at      timestamptz NOT NULL,
    body_day         date NOT NULL,
    body_sha256      bytea NOT NULL CHECK (octet_length(body_sha256) = 32),
    size_bytes       bigint NOT NULL CHECK (size_bytes >= 0),
    content_type     text,
    state            text NOT NULL CHECK (state IN ('pending', 'processed', 'failed')),
    processed_at     timestamptz,
    processing_error text,
    -- Read from the payload during processing; null when it could not be read.
    group_key        text,
    alert_count      bigint,
    truncated_alerts bigint,
    route_ids        bigint[] NOT NULL DEFAULT '{}',  -- Routes that took its Alerts: template dry runs per Route
    PRIMARY KEY (id, received_at),
    CONSTRAINT stored_snapshots_body_day_check CHECK (body_day = (received_at AT TIME ZONE 'UTC')::date),
    CONSTRAINT stored_snapshots_processing_check CHECK (
        (state = 'pending' AND processed_at IS NULL AND processing_error IS NULL)
        OR (state = 'processed' AND processed_at IS NOT NULL AND processing_error IS NULL)
        OR (state = 'failed' AND processed_at IS NOT NULL AND processing_error IS NOT NULL))
) PARTITION BY RANGE (received_at);

-- Processing order per Integration; small, because only pending rows are indexed.
CREATE INDEX stored_snapshots_pending_idx ON stored_snapshots (org_id, integration_id, received_at, id) WHERE state = 'pending';
-- The Stored Snapshot list of an Integration, newest first, with cursor (received_at, id); last Snapshot time.
CREATE INDEX stored_snapshots_list_idx ON stored_snapshots (org_id, integration_id, received_at DESC, id DESC);
CREATE INDEX stored_snapshots_public_id_idx ON stored_snapshots (org_id, public_id);
CREATE INDEX stored_snapshots_routes_idx ON stored_snapshots USING gin (route_ids);

-- Bodies of Stored Snapshots, one row per distinct body per Organization and UTC day. Daily partitions by body_day,
-- created and dropped together with the stored_snapshots partition of the same day.
--   CREATE TABLE snapshot_bodies_p20261003 PARTITION OF snapshot_bodies
--       FOR VALUES FROM ('2026-10-03') TO ('2026-10-04');
CREATE TABLE snapshot_bodies (
    org_id      bigint NOT NULL REFERENCES organizations (id),
    body_sha256 bytea NOT NULL CHECK (octet_length(body_sha256) = 32),
    body_day    date NOT NULL,
    body        bytea NOT NULL,  -- exactly the bytes received; any content, up to ingest.body_limit
    PRIMARY KEY (org_id, body_sha256, body_day)
) PARTITION BY RANGE (body_day);

-- ============================================================================================================
-- Snapshot semantics: Alertmanager routes, Alertmanager groups, Alerts and their presence
-- ============================================================================================================

-- An Alertmanager route, taken from the route part of groupKey, with its learned repeat interval.
CREATE TABLE alertmanager_routes (
    id                         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                     bigint NOT NULL REFERENCES organizations (id),
    integration_id             bigint NOT NULL REFERENCES integrations (id),
    route_path                 text NOT NULL,
    route_path_sha256          bytea NOT NULL CHECK (octet_length(route_path_sha256) = 32),
    learned_repeat_interval_ms bigint CHECK (learned_repeat_interval_ms > 0),  -- null: stale_after is processing.stale_after_unlearned
    repeat_observations        bigint NOT NULL DEFAULT 0,
    recent_repeat_gaps_ms      bigint[] NOT NULL DEFAULT '{}',  -- a short ring of observed gaps; the interval is their median
    first_seen_at              timestamptz NOT NULL,
    last_seen_at               timestamptz NOT NULL,
    UNIQUE (integration_id, route_path_sha256)
);

-- An Alertmanager group (one groupKey) of an Integration: duplicate window, truncation and repeat bookkeeping.
CREATE TABLE alertmanager_groups (
    id                     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                 bigint NOT NULL REFERENCES organizations (id),
    integration_id         bigint NOT NULL REFERENCES integrations (id),
    alertmanager_route_id  bigint NOT NULL REFERENCES alertmanager_routes (id),
    group_key              text NOT NULL,
    group_key_sha256       bytea NOT NULL CHECK (octet_length(group_key_sha256) = 32),
    first_seen_at          timestamptz NOT NULL,
    last_snapshot_at       timestamptz NOT NULL,
    last_snapshot_clock_ms bigint NOT NULL,  -- liveness clock at the last Snapshot: truncation expires like staleness
    -- Duplicate window: Snapshots within integrations.duplicate_window_seconds of window_started_at form one counted
    -- Snapshot (window). A window that contained a truncated Snapshot never counts as evidence of absence.
    window_seq             bigint NOT NULL DEFAULT 0,
    window_started_at      timestamptz,
    window_truncated       boolean NOT NULL DEFAULT false,
    -- Truncation of this groupKey (MusterSnapshotTruncated while any groupKey of the Integration is truncated).
    truncated              boolean NOT NULL DEFAULT false,
    truncated_since        timestamptz,
    -- Repeat learning: gaps between Snapshots marked as repeats, or between identical Snapshots (fallback).
    last_repeat_at         timestamptz,
    last_content_sha256    bytea,  -- fingerprints and statuses of the last Snapshot
    last_content_at        timestamptz,
    UNIQUE (integration_id, group_key_sha256),
    CONSTRAINT alertmanager_groups_truncated_check CHECK (truncated = (truncated_since IS NOT NULL))
);

CREATE INDEX alertmanager_groups_route_idx ON alertmanager_groups (alertmanager_route_id);
CREATE INDEX alertmanager_groups_truncated_idx ON alertmanager_groups (org_id, integration_id) WHERE truncated;

-- ============================================================================================================
-- Routes
-- ============================================================================================================

-- Ordered Routes; the first matching Route wins and the Default route, always present, is evaluated last.
-- Soft-deleted: Alert Group summary rows keep pointing at a deleted Route for two years.
CREATE TABLE routes (
    id                                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                             bigint NOT NULL REFERENCES organizations (id),
    public_id                          text NOT NULL CHECK (public_id ~ '^RT[0-9A-HJKMNP-TV-Z]{12}$'),
    name                               text NOT NULL CHECK (name <> ''),
    description                        text NOT NULL DEFAULT '',
    position                           bigint NOT NULL,     -- evaluation order; ties broken by id; ignored for the Default route
    is_default                         boolean NOT NULL DEFAULT false,
    urgent                             boolean NOT NULL,
    group_key                          text[] NOT NULL,     -- label names
    -- Policy (each field arrives with its capability; Route profiles only pre-fill these and are not stored).
    reopen_window_seconds              bigint NOT NULL CHECK (reopen_window_seconds >= 0),
    grace_period_seconds               bigint NOT NULL CHECK (grace_period_seconds >= 0),
    urgent_rise_removes_ack            boolean NOT NULL,
    snooze_durations_seconds           bigint[] NOT NULL,
    thread_batching_window_seconds     bigint NOT NULL CHECK (thread_batching_window_seconds >= 0),
    storm_threshold                    bigint NOT NULL CHECK (storm_threshold > 0),
    language                           text NOT NULL CHECK (language IN ('en', 'ru')),
    template_root_message              text,                -- null: built-in text
    template_line                      text,
    template_ack_timeout_notice        text,
    ack_timeout_enabled                boolean NOT NULL,
    ack_timeout_first_interval_seconds bigint NOT NULL CHECK (ack_timeout_first_interval_seconds > 0),
    reminders_enabled                  boolean NOT NULL,
    reminders_first_interval_seconds   bigint NOT NULL CHECK (reminders_first_interval_seconds > 0),
    reminders_cap_seconds              bigint NOT NULL,
    auto_unacknowledge                 boolean NOT NULL,
    -- Template error state (MusterTemplateError); the Fallback template is used meanwhile.
    template_error_since               timestamptz,
    template_error                     text,
    template_error_template            text CHECK (template_error_template IN ('root_message', 'line', 'ack_timeout_notice')),
    deleted_at                         timestamptz,
    created_at                         timestamptz NOT NULL,
    updated_at                         timestamptz NOT NULL,
    version                            bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    CONSTRAINT routes_default_check CHECK (NOT is_default OR deleted_at IS NULL),
    CONSTRAINT routes_reminders_cap_check CHECK (reminders_cap_seconds >= reminders_first_interval_seconds),
    CONSTRAINT routes_template_error_check CHECK ((template_error_since IS NULL) = (template_error IS NULL))
);

CREATE UNIQUE INDEX routes_name_key ON routes (org_id, name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX routes_default_key ON routes (org_id) WHERE is_default;
CREATE INDEX routes_order_idx ON routes (org_id, is_default, position, id) WHERE deleted_at IS NULL;

-- Matchers of a Route, combined with AND. The Default route has none (enforced by the routes package).
CREATE TABLE route_matchers (
    route_id bigint NOT NULL REFERENCES routes (id) ON DELETE CASCADE,
    org_id   bigint NOT NULL REFERENCES organizations (id),
    position bigint NOT NULL CHECK (position >= 0),
    label    text NOT NULL CHECK (label <> ''),
    op       text NOT NULL CHECK (op IN ('=', '!=', '=~', '!~')),
    value    text NOT NULL,  -- literal, or RE2 for =~ and !~
    PRIMARY KEY (route_id, position)
);

-- The Destinations of a Route. added_at drives the Quiet publication of open Alert Groups into a new Destination.
CREATE TABLE route_destinations (
    route_id       bigint NOT NULL REFERENCES routes (id) ON DELETE CASCADE,
    destination_id bigint NOT NULL REFERENCES destinations (id),
    org_id         bigint NOT NULL REFERENCES organizations (id),
    added_at       timestamptz NOT NULL,
    PRIMARY KEY (route_id, destination_id)
);

CREATE INDEX route_destinations_destination_idx ON route_destinations (destination_id);

-- Route suggestions dismissed by a user (remembered per user).
CREATE TABLE route_suggestion_dismissals (
    user_id      bigint NOT NULL REFERENCES users (id),
    suggestion   text NOT NULL CHECK (suggestion IN ('heartbeat_lost', 'internal_alerts')),
    org_id       bigint NOT NULL REFERENCES organizations (id),
    dismissed_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, suggestion)
);

-- ============================================================================================================
-- Alerts (written by snapshot processing and routing)
-- ============================================================================================================

-- One row per Integration and fingerprint: the Alert's identity and current state. A new firing of a resolved
-- fingerprint reuses the row and increments `episode`. Rows of fingerprints resolved longer ago than
-- retention.alert_details are deleted in batches by the Leader (see schema.md: this is current state, not history).
CREATE TABLE alerts (
    id                     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                 bigint NOT NULL REFERENCES organizations (id),
    integration_id         bigint NOT NULL REFERENCES integrations (id),
    fingerprint            text NOT NULL CHECK (fingerprint <> ''),
    labels                 jsonb NOT NULL,  -- with Static labels applied
    annotations            jsonb NOT NULL,
    generator_url          text,
    static_label_conflicts text[] NOT NULL DEFAULT '{}',  -- Static labels the Alert already carried; its own value won
    status                 text NOT NULL CHECK (status IN ('firing', 'resolved')),
    starts_at              timestamptz NOT NULL,  -- source clock: shown and used to order events of the same source
    ends_at                timestamptz,
    episode                bigint NOT NULL DEFAULT 1 CHECK (episode > 0),
    fired_at               timestamptz NOT NULL,  -- Muster's clock: when the current firing began
    first_seen_at          timestamptz NOT NULL,
    last_seen_at           timestamptz NOT NULL,
    resolved_at            timestamptz,
    resolve_reason         text CHECK (resolve_reason IN ('resolved', 'gone', 'stale', 'integration_deleted')),
    resolve_reason_text    text,
    -- Routing of the current firing.
    route_id               bigint REFERENCES routes (id),
    severity_level         text CHECK (severity_level IN ('critical', 'warning', 'info')),
    severity_raw           text,  -- the value as received when it has no mapping
    updated_at             timestamptz NOT NULL,
    UNIQUE (integration_id, fingerprint),
    CONSTRAINT alerts_labels_check CHECK (jsonb_typeof(labels) = 'object'),
    CONSTRAINT alerts_annotations_check CHECK (jsonb_typeof(annotations) = 'object'),
    CONSTRAINT alerts_resolved_check CHECK (
        (status = 'firing' AND resolved_at IS NULL AND resolve_reason IS NULL)
        OR (status = 'resolved' AND resolved_at IS NOT NULL AND resolve_reason IS NOT NULL))
);

-- The Alerts view of an Integration (sort by last_seen_at or starts_at, cursor on (value, id)).
CREATE INDEX alerts_last_seen_idx ON alerts (org_id, integration_id, last_seen_at DESC, id DESC);
CREATE INDEX alerts_starts_idx ON alerts (org_id, integration_id, starts_at DESC, id DESC);
CREATE INDEX alerts_labels_idx ON alerts USING gin (labels jsonb_path_ops);
CREATE INDEX alerts_retention_idx ON alerts (org_id, resolved_at) WHERE status = 'resolved';
CREATE INDEX alerts_route_idx ON alerts (route_id) WHERE route_id IS NOT NULL;

-- Presence of an Alert in one Alertmanager group (groupKey): Gone and Stale are decided per pair, and an Alert
-- resolves by absence only when it is Gone or Stale in every groupKey that listed it.
CREATE TABLE alert_presences (
    alert_id              bigint NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    alertmanager_group_id bigint NOT NULL REFERENCES alertmanager_groups (id) ON DELETE CASCADE,
    org_id                bigint NOT NULL REFERENCES organizations (id),
    integration_id        bigint NOT NULL REFERENCES integrations (id),
    state                 text NOT NULL CHECK (state IN ('listed', 'missed', 'gone', 'stale')),
    first_listed_at       timestamptz NOT NULL,
    last_seen_at          timestamptz NOT NULL,
    last_seen_clock_ms    bigint NOT NULL,  -- integrations.liveness_clock_ms when last listed: Stale when the clock
                                            -- has advanced more than stale_after since
    last_listed_window    bigint NOT NULL,  -- alertmanager_groups.window_seq of the last window that listed it
    missed_since          timestamptz,      -- receipt time of the first window that missed it: Gone once a later
                                            -- window misses it processing.gone_min_absence after this
    PRIMARY KEY (alert_id, alertmanager_group_id),
    CONSTRAINT alert_presences_missed_check CHECK ((state = 'missed') = (missed_since IS NOT NULL))
);

-- The unlisted Alerts of a groupKey (Gone), and the Stale scan of an Integration.
CREATE INDEX alert_presences_group_active_idx ON alert_presences (alertmanager_group_id) WHERE state IN ('listed', 'missed');
CREATE INDEX alert_presences_stale_idx ON alert_presences (org_id, integration_id, last_seen_clock_ms)
    WHERE state IN ('listed', 'missed');

-- ============================================================================================================
-- Alert Groups (written only by the `groups` package: alert_groups, alert_group_alerts, timeline_entries, notes,
-- alert_group_counters)
-- ============================================================================================================

-- One row per Alert Group. This row is the summary kept for retention.alert_group_summaries (2 years): list, search,
-- statistics and the summary page work from it alone after its details were removed after retention.alert_details.
-- Its Notes are kept with it and deleted with it.
CREATE TABLE alert_groups (
    id                             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                         bigint NOT NULL REFERENCES organizations (id),
    public_id                      text NOT NULL CHECK (public_id ~ '^AG[0-9A-HJKMNP-TV-Z]{12}$'),
    number                         bigint NOT NULL CHECK (number > 0),  -- #N, from alert_group_counters
    route_id                       bigint NOT NULL REFERENCES routes (id),
    -- Set when its Route was deleted and it moved to the Default route: it takes no new Alerts and never reopens.
    moved_from_route_id            bigint REFERENCES routes (id),
    group_key_labels               text[] NOT NULL,   -- the Route's Group key when the Alert Group was created
    group_key_values               jsonb NOT NULL,    -- label -> value ("" for a missing label): group labels
    group_key_sha256               bytea NOT NULL CHECK (octet_length(group_key_sha256) = 32),
    title                          text NOT NULL,
    title_from_group_key           boolean NOT NULL DEFAULT false,  -- the one allowed title switch has happened
    summary                        text,
    common_labels                  jsonb NOT NULL,    -- labels whose value all its Alerts share: label filters, columns
    common_annotations             jsonb NOT NULL,
    integration_ids                bigint[] NOT NULL, -- Integrations of its Alerts
    status                         text NOT NULL CHECK (status IN ('firing', 'acknowledged', 'snoozed', 'resolved')),
    severity_level                 text NOT NULL CHECK (severity_level IN ('critical', 'warning', 'info')),
    urgent                         boolean NOT NULL,
    -- Owner (always a User) while acknowledged.
    owner_user_id                  bigint REFERENCES users (id),
    acknowledged_at                timestamptz,       -- the current acknowledgement; Reminders count from it
    first_acknowledged_at          timestamptz,       -- time to acknowledge: set once, never changed afterwards
    -- Snooze.
    snooze_until                   timestamptz,
    snooze_no_end                  boolean NOT NULL DEFAULT false,
    snoozed_while_urgent           boolean NOT NULL DEFAULT false,  -- a rise to Urgent does not end such a Snooze
    -- Who set the current Snooze ("Snoozed by"): a User or a Service account, set by Snooze, cleared when it ends.
    snoozed_by_user_id             bigint REFERENCES users (id),
    snoozed_by_service_account_id  bigint REFERENCES service_accounts (id),
    -- Counters kept on the summary row.
    firing_alert_count             bigint NOT NULL DEFAULT 0 CHECK (firing_alert_count >= 0),
    resolved_alert_count           bigint NOT NULL DEFAULT 0 CHECK (resolved_alert_count >= 0),
    reopen_count                   bigint NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
    -- Resolution: by a person (a User or a Service account) or by the system with a reason.
    resolved_at                    timestamptz,
    resolved_by_kind               text CHECK (resolved_by_kind IN ('user', 'system')),
    resolved_by_user_id            bigint REFERENCES users (id),
    resolved_by_service_account_id bigint REFERENCES service_accounts (id),
    resolve_reason                 text CHECK (resolve_reason IN ('resolved', 'gone', 'stale', 'integration_deleted')),
    resolve_reason_text            text,
    -- Reopen window after a system resolve: the status to return to.
    reopen_deadline                timestamptz,       -- cleared when the Reopen window ends (timer)
    prior_status                   text CHECK (prior_status IN ('firing', 'acknowledged', 'snoozed')),
    prior_owner_user_id            bigint REFERENCES users (id),
    prior_snooze_until             timestamptz,
    prior_snooze_no_end            boolean,
    prior_snoozed_while_urgent     boolean,
    prior_snoozed_by_user_id       bigint REFERENCES users (id),
    prior_snoozed_by_service_account_id bigint REFERENCES service_accounts (id),
    -- Grace period after a manual resolve.
    grace_deadline                 timestamptz,       -- cleared when the Grace period ends (timer)
    firing_again_after_id          bigint REFERENCES alert_groups (id),  -- "firing again after a manual resolve of #N"
    -- Publication and timers.
    first_published_at             timestamptz,       -- the ack timeout starts here
    ack_timeout_started_at         timestamptz,
    ack_timeout_notices_sent       bigint NOT NULL DEFAULT 0 CHECK (ack_timeout_notices_sent >= 0),
    unclaimed                      boolean NOT NULL DEFAULT false,
    reminders_sent                 bigint NOT NULL DEFAULT 0,
    reminders_unanswered           bigint NOT NULL DEFAULT 0,  -- auto-unacknowledge after timers.auto_unacknowledge_after
    last_reminder_at               timestamptz,
    owner_answered_at              timestamptz,       -- "Still on it", any command or a Note by the Owner
    event_seq                      bigint NOT NULL DEFAULT 0,  -- last lifecycle event number; webhook `sequence`
    created_at                     timestamptz NOT NULL,       -- started_at
    last_changed_at                timestamptz NOT NULL,
    UNIQUE (org_id, public_id),
    UNIQUE (org_id, number),
    CONSTRAINT alert_groups_group_key_values_check CHECK (jsonb_typeof(group_key_values) = 'object'),
    CONSTRAINT alert_groups_common_labels_check CHECK (jsonb_typeof(common_labels) = 'object'),
    CONSTRAINT alert_groups_common_annotations_check CHECK (jsonb_typeof(common_annotations) = 'object'),
    CONSTRAINT alert_groups_owner_check CHECK (
        (status = 'acknowledged') = (owner_user_id IS NOT NULL)
        AND (owner_user_id IS NULL) = (acknowledged_at IS NULL)),
    CONSTRAINT alert_groups_snooze_check CHECK (
        CASE WHEN status = 'snoozed' THEN (snooze_until IS NULL) = snooze_no_end
             ELSE snooze_until IS NULL AND NOT snooze_no_end AND NOT snoozed_while_urgent END),
    CONSTRAINT alert_groups_snoozer_check CHECK (
        num_nonnulls(snoozed_by_user_id, snoozed_by_service_account_id) = CASE WHEN status = 'snoozed' THEN 1 ELSE 0 END),
    CONSTRAINT alert_groups_resolution_check CHECK (
        (status <> 'resolved' AND resolved_at IS NULL AND resolved_by_kind IS NULL
            AND resolved_by_user_id IS NULL AND resolved_by_service_account_id IS NULL AND resolve_reason IS NULL)
        OR (status = 'resolved' AND resolved_by_kind = 'user' AND resolved_at IS NOT NULL
            AND num_nonnulls(resolved_by_user_id, resolved_by_service_account_id) = 1 AND resolve_reason IS NULL)
        OR (status = 'resolved' AND resolved_by_kind = 'system' AND resolved_at IS NOT NULL
            AND resolved_by_user_id IS NULL AND resolved_by_service_account_id IS NULL AND resolve_reason IS NOT NULL)),
    CONSTRAINT alert_groups_reopen_check CHECK (
        reopen_deadline IS NULL OR (resolved_by_kind = 'system' AND prior_status IS NOT NULL)),
    CONSTRAINT alert_groups_grace_check CHECK (grace_deadline IS NULL OR resolved_by_kind = 'user'),
    CONSTRAINT alert_groups_moved_check CHECK (moved_from_route_id IS NULL OR reopen_deadline IS NULL),
    CONSTRAINT alert_groups_unclaimed_check CHECK (NOT unclaimed OR status = 'firing')
);

-- Grouping: at most one open Alert Group per Route and Group key values (snoozed and acknowledged count as open).
-- Alert Groups moved to the Default route are left out: they take no new Alerts, so they never compete for a key.
CREATE UNIQUE INDEX alert_groups_open_key ON alert_groups (route_id, group_key_sha256)
    WHERE status <> 'resolved' AND moved_from_route_id IS NULL;
-- Reopen: system-resolved Alert Groups whose Reopen window is still open.
CREATE INDEX alert_groups_reopen_idx ON alert_groups (route_id, group_key_sha256) WHERE reopen_deadline IS NOT NULL;
-- Previous Alert Groups with the same Route and key (the Alert Group page).
CREATE INDEX alert_groups_related_idx ON alert_groups (org_id, route_id, group_key_sha256, created_at DESC, id DESC);
-- Lists with cursor pagination on (created_at, id) or (last_changed_at, id), either direction.
CREATE INDEX alert_groups_started_idx ON alert_groups (org_id, created_at DESC, id DESC);
CREATE INDEX alert_groups_changed_idx ON alert_groups (org_id, last_changed_at DESC, id DESC);
CREATE INDEX alert_groups_open_idx ON alert_groups (org_id, created_at DESC, id DESC) WHERE status <> 'resolved';
-- Time-range overlap (not resolved before `from`), resolved-by filters and summary retention.
CREATE INDEX alert_groups_resolved_idx ON alert_groups (org_id, resolved_at) WHERE status = 'resolved';
-- Filters and statistics per Route.
CREATE INDEX alert_groups_route_idx ON alert_groups (org_id, route_id, created_at DESC);
CREATE INDEX alert_groups_owner_idx ON alert_groups (org_id, owner_user_id) WHERE owner_user_id IS NOT NULL;
-- Search by text in the title or `summary` (case-insensitive, parts of words), label filters, Integration filter.
CREATE INDEX alert_groups_title_trgm_idx ON alert_groups USING gin (title gin_trgm_ops);
CREATE INDEX alert_groups_summary_trgm_idx ON alert_groups USING gin (summary gin_trgm_ops);
CREATE INDEX alert_groups_labels_idx ON alert_groups USING gin (common_labels jsonb_path_ops);
CREATE INDEX alert_groups_integrations_idx ON alert_groups USING gin (integration_ids);
CREATE INDEX alert_groups_firing_again_idx ON alert_groups (firing_again_after_id) WHERE firing_again_after_id IS NOT NULL;

-- Alerts inside Alert Groups: one row per firing (episode) of an Alert in an Alert Group. Kept
-- retention.alert_details after it ended; deleted in batches by the Leader.
CREATE TABLE alert_group_alerts (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                  bigint NOT NULL REFERENCES organizations (id),
    alert_group_id          bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    alert_id                bigint NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    episode                 bigint NOT NULL CHECK (episode > 0),  -- alerts.episode at joining
    -- firing: lives in this Alert Group (also while it is resolved by a person, as a tail within the Grace period);
    -- resolved: resolved here; moved: left while still firing, into moved_to_alert_group_id (Grace period ended).
    state                   text NOT NULL CHECK (state IN ('firing', 'resolved', 'moved')),
    joined_at               timestamptz NOT NULL,
    starts_at               timestamptz NOT NULL,  -- source clock, updated by a Continuation
    annotations             jsonb NOT NULL,        -- as last seen during this membership
    ended_at                timestamptz,
    resolve_reason          text CHECK (resolve_reason IN ('resolved', 'gone', 'stale', 'integration_deleted')),
    resolve_reason_text     text,
    moved_to_alert_group_id bigint REFERENCES alert_groups (id),
    UNIQUE (alert_group_id, alert_id, episode),
    CONSTRAINT alert_group_alerts_membership_check CHECK (
        (state = 'firing' AND ended_at IS NULL AND resolve_reason IS NULL AND moved_to_alert_group_id IS NULL)
        OR (state = 'resolved' AND ended_at IS NOT NULL AND resolve_reason IS NOT NULL AND moved_to_alert_group_id IS NULL)
        OR (state = 'moved' AND ended_at IS NOT NULL AND resolve_reason IS NULL AND moved_to_alert_group_id IS NOT NULL))
);

-- An Alert lives in at most one Alert Group at a time.
CREATE UNIQUE INDEX alert_group_alerts_firing_key ON alert_group_alerts (alert_id) WHERE state = 'firing';
CREATE INDEX alert_group_alerts_group_idx ON alert_group_alerts (alert_group_id, state);
CREATE INDEX alert_group_alerts_retention_idx ON alert_group_alerts (org_id, ended_at) WHERE state <> 'firing';
CREATE INDEX alert_group_alerts_moved_idx ON alert_group_alerts (moved_to_alert_group_id) WHERE moved_to_alert_group_id IS NOT NULL;

-- The Timeline: lifecycle events (with their loudness and symbolic Mentions) and system entries. Notes and delivery
-- events are not stored here: Notes live in `notes`, which outlives the details, and delivery events in
-- delivery_events; the Timeline view merges both by time. Monthly partitions by `at`, kept retention.alert_details,
-- removed by dropping whole partitions.
--   CREATE TABLE timeline_entries_p202610 PARTITION OF timeline_entries
--       FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');
CREATE TABLE timeline_entries (
    id                       bigint GENERATED ALWAYS AS IDENTITY,
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    public_id                text NOT NULL CHECK (public_id ~ '^TE[0-9A-HJKMNP-TV-Z]{12}$'),
    alert_group_id           bigint NOT NULL,  -- alert_groups.id
    at                       timestamptz NOT NULL,
    kind                     text NOT NULL CHECK (kind IN ('status', 'alerts', 'timers', 'system')),
    -- Every lifecycle event except `note_added`, which is recorded by its Note in `notes`.
    event                    text CHECK (event IN (
                                 'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                 'annotations_changed', 'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended',
                                 'resolved', 'moved_to_default_route', 'acknowledged', 'takeover', 'unacknowledged',
                                 'unresolved', 'snoozed', 'unsnoozed', 'ack_timeout', 'unclaimed',
                                 'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed')),
    event_seq                bigint,           -- position among the Alert Group's lifecycle events
    system_event             text CHECK (system_event IN (
                                 'moved_to_default_route', 'muster_unavailable', 'fallback_template_used',
                                 'template_value_missing')),
    loudness                 text CHECK (loudness IN ('loud', 'quiet')),
    mentions                 text[] NOT NULL DEFAULT '{}',
    -- Actor and Transport.
    actor_kind               text NOT NULL CHECK (actor_kind IN ('user', 'service_account', 'system')),
    actor_user_id            bigint,
    actor_service_account_id bigint,
    api_token_id             bigint,
    token_name               text,
    transport                text NOT NULL CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'cli', 'system')),
    reason                   text,             -- reason of an automatic transition
    -- Variant fields.
    from_status              text CHECK (from_status IN ('firing', 'acknowledged', 'snoozed', 'resolved')),
    to_status                text CHECK (to_status IN ('firing', 'acknowledged', 'snoozed', 'resolved')),
    owner_user_id            bigint,           -- the Owner after the entry
    previous_owner_user_id   bigint,           -- Takeover, rise to Urgent, auto-unacknowledge
    snooze_until             timestamptz,
    fingerprints             text[],
    replaced_label           text,
    label_conflicts          text[],
    notice_number            bigint,
    missed_count             bigint,
    period_from              timestamptz,
    period_to                timestamptz,
    detail                   text,
    PRIMARY KEY (id, at),
    CONSTRAINT timeline_entries_lifecycle_check CHECK (
        (event IS NULL AND event_seq IS NULL AND loudness IS NULL)
        OR (event IS NOT NULL AND event_seq IS NOT NULL AND loudness IS NOT NULL)),
    -- The Timeline kind of each lifecycle event (x-event-kinds in the API specification).
    CONSTRAINT timeline_entries_event_kind_check CHECK (
        (event IS NULL AND kind = 'system')
        OR (kind = 'status' AND event IN ('created', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                                          'acknowledged', 'takeover', 'unacknowledged', 'unresolved', 'snoozed',
                                          'unsnoozed', 'auto_unacknowledged'))
        OR (kind = 'alerts' AND event IN ('alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                                          'annotations_changed', 'severity_raised'))
        OR (kind = 'timers' AND event IN ('ack_timeout', 'unclaimed', 'reminder', 'reminder_answered', 'notices_missed'))
        OR (kind = 'system' AND event = 'moved_to_default_route')),
    CONSTRAINT timeline_entries_system_check CHECK (
        (kind = 'system') = (system_event IS NOT NULL)
        AND (system_event IS NOT DISTINCT FROM 'moved_to_default_route')
            = (event IS NOT DISTINCT FROM 'moved_to_default_route')),
    CONSTRAINT timeline_entries_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[]),
    CONSTRAINT timeline_entries_actor_check CHECK (
        (actor_kind = 'user' AND actor_user_id IS NOT NULL AND actor_service_account_id IS NULL)
        OR (actor_kind = 'service_account' AND actor_service_account_id IS NOT NULL AND actor_user_id IS NULL)
        OR (actor_kind = 'system' AND actor_user_id IS NULL AND actor_service_account_id IS NULL
            AND transport = 'system'))
) PARTITION BY RANGE (at);

-- The Timeline of one Alert Group in time order (cursor on (at, id)).
CREATE INDEX timeline_entries_group_idx ON timeline_entries (org_id, alert_group_id, at, id);

-- Notes: text people add to a Timeline. Kept as long as the summary row of their Alert Group
-- (retention.alert_group_summaries), not with its details: not partitioned, so dropping a Timeline month never takes
-- a Note, and deleted with the summary row (ON DELETE CASCADE). Each Note records the lifecycle event `note_added`
-- (Quiet, no Mentions); the Timeline view merges Notes by time as the kind `notes`.
CREATE TABLE notes (
    id                       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    public_id                text NOT NULL CHECK (public_id ~ '^NE[0-9A-HJKMNP-TV-Z]{12}$'),
    alert_group_id           bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    event_seq                bigint NOT NULL CHECK (event_seq > 0),  -- position of its `note_added` among the lifecycle events
    body                     text NOT NULL CHECK (body <> ''),       -- at most alert_group.note_max_length characters
    -- Author (a User or a Service account) and Transport; Notes are added from the UI and the API only.
    actor_kind               text NOT NULL CHECK (actor_kind IN ('user', 'service_account')),
    actor_user_id            bigint REFERENCES users (id),
    actor_service_account_id bigint REFERENCES service_accounts (id),
    api_token_id             bigint,           -- api_tokens.id of the token used, if any
    token_name               text,
    transport                text NOT NULL CHECK (transport IN ('ui', 'api')),
    created_at               timestamptz NOT NULL,
    UNIQUE (org_id, public_id),
    CONSTRAINT notes_actor_check CHECK (
        (actor_kind = 'user' AND actor_user_id IS NOT NULL AND actor_service_account_id IS NULL)
        OR (actor_kind = 'service_account' AND actor_service_account_id IS NOT NULL AND actor_user_id IS NULL))
);

-- The Notes of one Alert Group in time order (the Notes list and the Timeline merge, cursor on (created_at, id)); it
-- also serves the cascade when a summary row is deleted.
CREATE INDEX notes_group_idx ON notes (alert_group_id, created_at, id);

-- ============================================================================================================
-- Timers (claimed by any replica)
-- ============================================================================================================

-- Storms: per Route, more than route.storm_threshold new Alert Groups per minute. Delivery-owned.
CREATE TABLE storms (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id            bigint NOT NULL REFERENCES organizations (id),
    route_id          bigint NOT NULL REFERENCES routes (id),
    started_at        timestamptz NOT NULL,
    calm_since        timestamptz,  -- the rate fell below the threshold; the Storm ends after delivery.storm_calm_period
    ended_at          timestamptz,
    alert_group_count bigint NOT NULL DEFAULT 0 CHECK (alert_group_count >= 0),
    urgent_count      bigint NOT NULL DEFAULT 0 CHECK (urgent_count >= 0),
    CONSTRAINT storms_order_check CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE UNIQUE INDEX storms_active_key ON storms (route_id) WHERE ended_at IS NULL;
CREATE INDEX storms_org_idx ON storms (org_id, started_at DESC);

-- Timer rows with deadlines: ack timeout notices, Reminders, Snooze ends, Reopen window and Grace period ends, and
-- the calm check of a Storm. A worker claims due rows with FOR UPDATE SKIP LOCKED in a short transaction and leases
-- them; a row whose lease ran out is claimed again by any replica. One row per subject and kind; rescheduling is an
-- update of `deadline`. Overdue rows after downtime fire once, collapsed.
CREATE TABLE timers (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    alert_group_id bigint REFERENCES alert_groups (id) ON DELETE CASCADE,
    storm_id       bigint REFERENCES storms (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN (
                       'ack_timeout', 'reminder', 'snooze_end', 'reopen_window_end', 'grace_period_end',
                       'storm_calm_check')),
    deadline       timestamptz NOT NULL,
    notice_number  bigint,        -- the next ack timeout notice or Reminder
    lease_owner    text,
    lease_until    timestamptz,
    attempts       bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CONSTRAINT timers_subject_check CHECK (
        (kind = 'storm_calm_check' AND storm_id IS NOT NULL AND alert_group_id IS NULL)
        OR (kind <> 'storm_calm_check' AND alert_group_id IS NOT NULL AND storm_id IS NULL)),
    CONSTRAINT timers_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

CREATE UNIQUE INDEX timers_alert_group_key ON timers (alert_group_id, kind) WHERE alert_group_id IS NOT NULL;
CREATE UNIQUE INDEX timers_storm_key ON timers (storm_id, kind) WHERE storm_id IS NOT NULL;
-- Claim: due rows in deadline order; the lease is checked on the few rows found.
CREATE INDEX timers_due_idx ON timers (org_id, deadline);

-- ============================================================================================================
-- Delivery (written only by the `delivery` package, never by `groups`, except through its enqueue functions)
-- ============================================================================================================

-- Desired state per Alert Group x Destination (or Storm summary x Destination), reconciled by the delivery worker:
-- one send or edit brings the actual message to the latest desired version. Claimed with FOR UPDATE SKIP LOCKED and
-- a lease; the HTTP call is made outside the transaction.
CREATE TABLE deliveries (
    id                       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    destination_id           bigint NOT NULL REFERENCES destinations (id),
    alert_group_id           bigint REFERENCES alert_groups (id) ON DELETE CASCADE,
    storm_id                 bigint REFERENCES storms (id) ON DELETE CASCADE,  -- a Storm summary
    held_by_storm_id         bigint REFERENCES storms (id),  -- an Alert Group a Storm summary stands for
    -- pending: work to do (also while the Destination is Broken: "waiting"); delivered: actual = desired;
    -- not_delivered: unknown response or failed template, terminal until the desired state changes;
    -- withheld: never published (resolved while held by a Storm or while the Destination was Broken);
    -- deleted_in_messenger: deleted twice, left alone; retired: final edit done after leaving the Route or after the
    -- Destination was deleted.
    state                    text NOT NULL CHECK (state IN (
                                 'pending', 'delivered', 'not_delivered', 'withheld', 'deleted_in_messenger', 'retired')),
    urgent                   boolean NOT NULL DEFAULT false,  -- Urgent first in the queue, never past a limiter
    -- Desired state.
    desired_version          bigint NOT NULL DEFAULT 0 CHECK (desired_version >= 0),
    desired_text             text,
    desired_payload          jsonb,       -- buttons, colour, or the rendered webhook request
    desired_hash             bytea,
    desired_button_key_id    text,        -- key that signed the buttons of the desired state
    desired_retire           boolean NOT NULL DEFAULT false,  -- next call is the final edit ("no longer updated here")
    -- Receipt time (stored_snapshots.received_at) of the oldest Snapshot whose change the desired state carries and the
    -- actual message does not show yet; null when only Commands or timers changed it. The adapter call observes
    -- muster_delivery_latency_seconds from it, and a delivered version clears it.
    desired_received_at      timestamptz,
    publication_loud         boolean,     -- decided when the first Publication becomes due
    late_note                boolean NOT NULL DEFAULT false,  -- "Delivered late" on the first Publication
    -- Actual message.
    actual_version           bigint,
    actual_hash              bytea,
    actual_button_key_id     text,        -- Keyring: an open Root message still depends on this key
    message_id               text,        -- post id, channel message id, or the extracted id of a webhook
    message_url              text,
    response_values          jsonb NOT NULL DEFAULT '{}',  -- outgoing webhook template mode: extracted values
    thread_opened            boolean NOT NULL DEFAULT false,  -- outgoing webhook "open thread" has run
    -- Publication bookkeeping. Recorded before the API call; a retry after it publishes again (possible duplicate).
    publication_started_at   timestamptz,
    published_at             timestamptz,
    last_delivered_at        timestamptz,
    publications             bigint NOT NULL DEFAULT 0 CHECK (publications >= 0),
    possible_duplicate       boolean NOT NULL DEFAULT false,
    republished_after_delete boolean NOT NULL DEFAULT false,  -- a deleted Root message is republished once
    -- Thread.
    thread_state             text NOT NULL DEFAULT 'none' CHECK (thread_state IN (
                                 'none', 'waiting_for_copy', 'attached', 'unattached')),
    thread_anchor_id         text,        -- Telegram: the automatic copy of the post in the discussion group
    thread_chain_last_id     text,        -- Telegram: last reply of an unattached chain
    thread_batch_until       timestamptz, -- end of the open Thread batching window
    -- Retry, attempt budget and lease.
    attempts                 bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),  -- Transient attempts in the budget
    first_failed_at          timestamptz, -- start of the time budget
    next_attempt_at          timestamptz NOT NULL,
    lease_owner              text,
    lease_until              timestamptz,
    last_error_class         text CHECK (last_error_class IN (
                                 'retry_after', 'transient', 'fatal', 'unknown', 'template_error', 'blocked')),
    last_error               text,        -- masked, untrusted provider text
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    CONSTRAINT deliveries_subject_check CHECK (num_nonnulls(alert_group_id, storm_id) = 1),
    CONSTRAINT deliveries_held_check CHECK (held_by_storm_id IS NULL OR alert_group_id IS NOT NULL),
    CONSTRAINT deliveries_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT deliveries_not_delivered_check CHECK (state <> 'not_delivered' OR last_error IS NOT NULL),
    CONSTRAINT deliveries_response_values_check CHECK (jsonb_typeof(response_values) = 'object')
);

CREATE UNIQUE INDEX deliveries_alert_group_key ON deliveries (alert_group_id, destination_id) WHERE alert_group_id IS NOT NULL;
CREATE UNIQUE INDEX deliveries_storm_key ON deliveries (storm_id, destination_id) WHERE storm_id IS NOT NULL;
-- Reconciliation claim: due work, Urgent first.
CREATE INDEX deliveries_due_idx ON deliveries (org_id, urgent DESC, next_attempt_at) WHERE state = 'pending';
-- Queue depth per Destination, and the oldest waiting delivery as the Broken probe.
CREATE INDEX deliveries_destination_pending_idx ON deliveries (destination_id, next_attempt_at) WHERE state = 'pending';
-- Incoming Telegram updates and button presses name a message; find its delivery.
CREATE INDEX deliveries_message_idx ON deliveries (destination_id, message_id) WHERE message_id IS NOT NULL;
CREATE INDEX deliveries_anchor_idx ON deliveries (destination_id, thread_anchor_id) WHERE thread_anchor_id IS NOT NULL;
CREATE INDEX deliveries_held_idx ON deliveries (held_by_storm_id) WHERE held_by_storm_id IS NOT NULL;
CREATE INDEX deliveries_button_key_idx ON deliveries (org_id, actual_button_key_id) WHERE actual_button_key_id IS NOT NULL;

-- Thread replies: follow-ups under a Root message (new Alerts batched by the Thread batching window, notices,
-- Reminders, Takeovers, Reopens, ...). A `collecting` row is the open batch of new Alerts; it becomes due at the end
-- of the window. Rows that came due while the Destination was Broken are `dropped`.
CREATE TABLE thread_replies (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id           bigint NOT NULL REFERENCES organizations (id),
    delivery_id      bigint NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    alert_group_id   bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    destination_id   bigint NOT NULL REFERENCES destinations (id),
    event            text NOT NULL CHECK (event IN (
                         'alerts_added', 'alert_replaced', 'urgency_raised', 'reopened', 'snooze_ended', 'resolved',
                         'takeover', 'unacknowledged', 'ack_timeout', 'reminder', 'auto_unacknowledged',
                         'notices_missed')),  -- unacknowledged: only the release of a disabled or deleted Owner
    event_seqs       bigint[] NOT NULL,          -- the lifecycle events this reply carries (several when batched)
    loudness         text NOT NULL CHECK (loudness IN ('loud', 'quiet')),
    mentions         text[] NOT NULL DEFAULT '{}',
    fingerprints     text[] NOT NULL DEFAULT '{}',  -- new Alerts collected in the batching window
    state            text NOT NULL CHECK (state IN ('collecting', 'pending', 'sent', 'dropped', 'not_delivered')),
    attempts         bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    first_failed_at  timestamptz,
    next_attempt_at  timestamptz NOT NULL,       -- end of the batching window, or the Telegram copy wait
    lease_owner      text,
    lease_until      timestamptz,
    last_error_class text CHECK (last_error_class IN (
                         'retry_after', 'transient', 'fatal', 'unknown', 'template_error', 'blocked')),
    last_error       text,
    sent_at          timestamptz,
    message_id       text,                       -- for presses on its buttons (Reminders)
    button_key_id    text,
    created_at       timestamptz NOT NULL,
    CONSTRAINT thread_replies_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT thread_replies_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[])
);

CREATE UNIQUE INDEX thread_replies_collecting_key ON thread_replies (delivery_id) WHERE state = 'collecting';
CREATE INDEX thread_replies_due_idx ON thread_replies (org_id, next_attempt_at) WHERE state IN ('collecting', 'pending');
CREATE INDEX thread_replies_delivery_idx ON thread_replies (delivery_id, id);
CREATE INDEX thread_replies_retention_idx ON thread_replies (org_id, created_at) WHERE state IN ('sent', 'dropped', 'not_delivered');

-- Outgoing webhook events mode: one request per lifecycle event, in order per Alert Group, at least once, never
-- collapsed. Only the head event (lowest pending sequence) of an Alert Group and Destination is attempted; the next
-- waits until it is delivered or Not delivered. The body (version 1) is rendered when the event is queued, with
-- the state of the Alert Group at that moment.
CREATE TABLE webhook_events (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id           bigint NOT NULL REFERENCES organizations (id),
    destination_id   bigint NOT NULL REFERENCES destinations (id),
    alert_group_id   bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    sequence         bigint NOT NULL CHECK (sequence > 0),  -- alert_groups.event_seq of the event; gaps are normal
    webhook_id       text NOT NULL UNIQUE,                  -- kept across retries; receivers drop duplicates by it
    event            text NOT NULL CHECK (event IN (
                         'created', 'alerts_added', 'alert_replaced', 'alert_resolved', 'alert_continued',
                         'annotations_changed', 'severity_raised', 'urgency_raised', 'reopened', 'snooze_ended',
                         'resolved', 'moved_to_default_route', 'acknowledged', 'takeover', 'unacknowledged',
                         'unresolved', 'snoozed', 'unsnoozed', 'note_added', 'ack_timeout', 'unclaimed',
                         'reminder', 'reminder_answered', 'auto_unacknowledged', 'notices_missed')),
    notify           boolean NOT NULL,                      -- the event is Loud
    occurred_at      timestamptz NOT NULL,
    body             jsonb NOT NULL,
    state            text NOT NULL CHECK (state IN ('pending', 'delivered', 'not_delivered')),
    attempts         bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    first_failed_at  timestamptz,
    next_attempt_at  timestamptz NOT NULL,
    lease_owner      text,
    lease_until      timestamptz,
    last_error_class text CHECK (last_error_class IN (
                         'retry_after', 'transient', 'fatal', 'unknown', 'template_error', 'blocked')),
    last_error       text,
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL,
    UNIQUE (destination_id, alert_group_id, sequence),
    CONSTRAINT webhook_events_lease_check CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
    CONSTRAINT webhook_events_body_check CHECK (jsonb_typeof(body) = 'object')
);

-- Head of line per Alert Group and Destination, and due events.
CREATE INDEX webhook_events_head_idx ON webhook_events (org_id, destination_id, alert_group_id, sequence) WHERE state = 'pending';
CREATE INDEX webhook_events_due_idx ON webhook_events (org_id, next_attempt_at) WHERE state = 'pending';
CREATE INDEX webhook_events_retention_idx ON webhook_events (org_id, created_at) WHERE state <> 'pending';

-- Delivery events: what happened while delivering to a Destination. Not lifecycle events; never sent anywhere.
-- Monthly partitions by occurred_at, kept retention.alert_details.
--   CREATE TABLE delivery_events_p202610 PARTITION OF delivery_events
--       FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');
CREATE TABLE delivery_events (
    id             bigint GENERATED ALWAYS AS IDENTITY,
    org_id         bigint NOT NULL REFERENCES organizations (id),
    public_id      text NOT NULL CHECK (public_id ~ '^DE[0-9A-HJKMNP-TV-Z]{12}$'),
    occurred_at    timestamptz NOT NULL,
    destination_id bigint NOT NULL,  -- destinations.id
    alert_group_id bigint,           -- alert_groups.id, when the event concerns one
    storm_id       bigint,           -- storms.id, for Storm summaries
    kind           text NOT NULL CHECK (kind IN (
                       'publication', 'possible_duplicate', 'not_delivered', 'delivered_late', 'deleted_in_messenger',
                       'republished', 'thread_not_attached', 'markup_rejected', 'destination_broken',
                       'destination_recovered', 'storm_summary', 'final_edit')),
    loudness       text NOT NULL CHECK (loudness IN ('loud', 'quiet')),
    mentions       text[] NOT NULL DEFAULT '{}',
    error_class    text CHECK (error_class IN (
                       'retry_after', 'transient', 'fatal', 'unknown', 'template_error', 'blocked')),
    error          text,             -- masked, untrusted provider text
    detail         jsonb,
    PRIMARY KEY (id, occurred_at),
    CONSTRAINT delivery_events_mentions_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent']::text[])
) PARTITION BY RANGE (occurred_at);

-- Merged into the Timeline of an Alert Group by time; history of a Destination.
CREATE INDEX delivery_events_group_idx ON delivery_events (org_id, alert_group_id, occurred_at, id) WHERE alert_group_id IS NOT NULL;
CREATE INDEX delivery_events_destination_idx ON delivery_events (org_id, destination_id, occurred_at DESC);

-- Telegram Thread mapping buffer: the automatic copy of a channel post in its discussion group, which may arrive
-- before or after the response to the send call, on any replica. Learned from the copy or from the first comment.
CREATE TABLE telegram_post_copies (
    connection_id      bigint NOT NULL REFERENCES connections (id),
    channel_chat_id    bigint NOT NULL,
    channel_message_id bigint NOT NULL,
    org_id             bigint NOT NULL REFERENCES organizations (id),
    discussion_chat_id bigint NOT NULL,
    copy_message_id    bigint NOT NULL,
    learned_from       text NOT NULL CHECK (learned_from IN ('automatic_forward', 'comment')),
    received_at        timestamptz NOT NULL,
    PRIMARY KEY (connection_id, channel_chat_id, channel_message_id)
);

CREATE INDEX telegram_post_copies_received_idx ON telegram_post_copies (org_id, received_at);

-- Token buckets of the limiters per Destination and per Connection, shared by all replicas. The interactive path
-- takes its token here too, ahead of waiting deliveries.
CREATE TABLE rate_limit_buckets (
    subject_kind text NOT NULL CHECK (subject_kind IN ('destination', 'connection')),
    subject_id   bigint NOT NULL,  -- destinations.id or connections.id
    org_id       bigint NOT NULL REFERENCES organizations (id),
    tokens       double precision NOT NULL,
    refilled_at  timestamptz NOT NULL,
    PRIMARY KEY (subject_kind, subject_id)
);

-- ============================================================================================================
-- Link rules and Lookup tables
-- ============================================================================================================

CREATE TABLE lookup_tables (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    public_id    text NOT NULL CHECK (public_id ~ '^TB[0-9A-HJKMNP-TV-Z]{12}$'),
    name         text NOT NULL CHECK (name <> ''),  -- `lookup "<name>" <key> "<column>"`
    description  text NOT NULL DEFAULT '',
    column_names text[] NOT NULL CHECK (cardinality(column_names) >= 1),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    version      bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    UNIQUE (org_id, name)
);

-- One row per key; `cells` maps every column name to its value (the keys equal column_names, checked on write).
CREATE TABLE lookup_table_entries (
    lookup_table_id bigint NOT NULL REFERENCES lookup_tables (id) ON DELETE CASCADE,
    key             text NOT NULL CHECK (key <> ''),
    org_id          bigint NOT NULL REFERENCES organizations (id),
    cells           jsonb NOT NULL CHECK (jsonb_typeof(cells) = 'object'),
    PRIMARY KEY (lookup_table_id, key)
);

CREATE TABLE link_rules (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    public_id    text NOT NULL CHECK (public_id ~ '^KR[0-9A-HJKMNP-TV-Z]{12}$'),
    name         text NOT NULL CHECK (name <> ''),
    builtin      boolean NOT NULL DEFAULT false,  -- the "Explore" rule cannot be deleted
    scope_type   text NOT NULL CHECK (scope_type IN ('alert_group', 'label_value')),
    scope_label  text,
    url_template text NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    version      bigint NOT NULL DEFAULT 1,
    UNIQUE (org_id, public_id),
    UNIQUE (org_id, name),
    CONSTRAINT link_rules_scope_check CHECK ((scope_type = 'label_value') = (scope_label IS NOT NULL))
);

CREATE UNIQUE INDEX link_rules_builtin_key ON link_rules (org_id) WHERE builtin;

CREATE TABLE link_rule_matchers (
    link_rule_id bigint NOT NULL REFERENCES link_rules (id) ON DELETE CASCADE,
    org_id       bigint NOT NULL REFERENCES organizations (id),
    position     bigint NOT NULL CHECK (position >= 0),
    label        text NOT NULL CHECK (label <> ''),
    op           text NOT NULL CHECK (op IN ('=', '!=', '=~', '!~')),
    value        text NOT NULL,
    PRIMARY KEY (link_rule_id, position)
);

-- ============================================================================================================
-- Audit log (append-only)
-- ============================================================================================================

-- Who changed or did what: configuration changes with a before/after diff (Secrets only marked as changed), security
-- events and every command of people and automation. Monthly partitions by `at`, kept retention.audit_log.
-- Append-only: UPDATE, DELETE and TRUNCATE are refused by triggers; retention drops whole partitions.
--   CREATE TABLE audit_log_p202610 PARTITION OF audit_log
--       FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');
CREATE TABLE audit_log (
    id                       bigint GENERATED ALWAYS AS IDENTITY,
    org_id                   bigint NOT NULL REFERENCES organizations (id),
    public_id                text NOT NULL CHECK (public_id ~ '^AE[0-9A-HJKMNP-TV-Z]{12}$'),
    at                       timestamptz NOT NULL,                -- Muster's clock
    recorded_at              timestamptz NOT NULL DEFAULT now(),  -- database clock (deliberate now(): tamper evidence)
    actor_kind               text NOT NULL CHECK (actor_kind IN ('user', 'service_account', 'system', 'bootstrap', 'cli')),
    actor_user_id            bigint,  -- users.id; the current name is shown, rows are never rewritten
    actor_service_account_id bigint,  -- service_accounts.id
    actor_name               text,    -- `--actor` of a CLI command
    api_token_id             bigint,  -- api_tokens.id of the Personal access token or Service account token used
    token_name               text,
    transport                text NOT NULL CHECK (transport IN ('ui', 'api', 'mattermost', 'telegram', 'cli', 'system')),
    action                   text NOT NULL CHECK (action ~ '^[a-z_]+(\.[a-z_]+)+$'),  -- e.g. alert_group.takeover
    resource_type            text,
    resource_public_id       text,
    resource_name            text,
    diff                     jsonb NOT NULL DEFAULT '[]',  -- [{"pointer": ..., "before": ..., "after": ..., "secret_changed": ...}]
    details                  jsonb NOT NULL DEFAULT '{}',  -- never contains Secrets
    source_address           inet,
    PRIMARY KEY (id, at),
    CONSTRAINT audit_log_diff_check CHECK (jsonb_typeof(diff) = 'array'),
    CONSTRAINT audit_log_details_check CHECK (jsonb_typeof(details) = 'object'),
    CONSTRAINT audit_log_actor_check CHECK (
        (actor_kind = 'user' AND actor_user_id IS NOT NULL AND actor_service_account_id IS NULL)
        OR (actor_kind = 'service_account' AND actor_service_account_id IS NOT NULL AND actor_user_id IS NULL)
        OR (actor_kind = 'cli' AND actor_name IS NOT NULL AND transport = 'cli')
        OR (actor_kind IN ('system', 'bootstrap') AND actor_user_id IS NULL AND actor_service_account_id IS NULL))
) PARTITION BY RANGE (at);

CREATE INDEX audit_log_at_idx ON audit_log (org_id, at DESC, id DESC);
CREATE INDEX audit_log_user_idx ON audit_log (org_id, actor_user_id, at DESC) WHERE actor_user_id IS NOT NULL;
CREATE INDEX audit_log_service_account_idx ON audit_log (org_id, actor_service_account_id, at DESC)
    WHERE actor_service_account_id IS NOT NULL;
CREATE INDEX audit_log_action_idx ON audit_log (org_id, action, at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (org_id, resource_type, resource_public_id, at DESC);

CREATE FUNCTION audit_log_reject_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

-- Row triggers on a partitioned table are cloned to every partition, also to partitions created later.
CREATE TRIGGER audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- ============================================================================================================
-- Every encrypted value, with the key that encrypted it
-- ============================================================================================================

-- Used by `muster secrets rotate-key` (what to re-encrypt), the Keyring page and `muster doctor` (which keys are
-- still needed). A test asserts that every `*_ciphertext` column of the schema appears here.
CREATE VIEW encrypted_values AS
          SELECT 'organizations.outgoing_heartbeat_url' AS field, id AS org_id, id::text AS row_key,
                 outgoing_heartbeat_url_key_id AS key_id
            FROM organizations WHERE outgoing_heartbeat_url_ciphertext IS NOT NULL
UNION ALL SELECT 'organizations.outgoing_heartbeat_proxy_password', id, id::text, outgoing_heartbeat_proxy_password_key_id
            FROM organizations WHERE outgoing_heartbeat_proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_settings.client_secret', org_id, org_id::text, client_secret_key_id
            FROM oidc_settings WHERE client_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_settings.proxy_password', org_id, org_id::text, proxy_password_key_id
            FROM oidc_settings WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'oidc_auth_requests.code_verifier', org_id, encode(state_hash, 'hex'), code_verifier_key_id
            FROM oidc_auth_requests
UNION ALL SELECT 'users.oidc_offline_token', org_id, id::text, oidc_offline_token_key_id
            FROM users WHERE oidc_offline_token_ciphertext IS NOT NULL
UNION ALL SELECT 'user_totp.seed', org_id, user_id::text, seed_key_id
            FROM user_totp WHERE seed_ciphertext IS NOT NULL
UNION ALL SELECT 'user_totp.pending_seed', org_id, user_id::text, pending_seed_key_id
            FROM user_totp WHERE pending_seed_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.bot_token', org_id, id::text, bot_token_key_id
            FROM connections
UNION ALL SELECT 'connections.proxy_password', org_id, id::text, proxy_password_key_id
            FROM connections WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'connections.telegram_webhook_secret', org_id, id::text, telegram_webhook_secret_key_id
            FROM connections WHERE telegram_webhook_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.proxy_password', org_id, id::text, proxy_password_key_id
            FROM destinations WHERE proxy_password_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.signing_secret', org_id, id::text, signing_secret_key_id
            FROM destinations WHERE signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destinations.previous_signing_secret', org_id, id::text, previous_signing_secret_key_id
            FROM destinations WHERE previous_signing_secret_ciphertext IS NOT NULL
UNION ALL SELECT 'destination_secrets.value', org_id, destination_id::text || '/' || name, value_key_id
            FROM destination_secrets;

COMMIT;
