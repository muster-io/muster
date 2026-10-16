-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Account links (C-18): the messenger accounts linked to Users, one per identity space and User, and the link
-- requests started from the web session — a hashed Telegram deep-link token or a hashed Mattermost code.

-- LookupAccountLink reads the User that the messenger account external_id of the identity space is linked to, with
-- the link's id and last use; a deleted User has no links (C-18.FR-8).
-- name: LookupAccountLink :one
SELECT u.id, u.public_id, u.login, u.name, u.role, u.status, al.id AS link_id, al.last_used_at
FROM account_links al
JOIN users u ON u.org_id = al.org_id AND u.id = al.user_id
WHERE al.org_id = @org_id AND al.identity_space = @identity_space AND al.external_id = @external_id
  AND u.status <> 'deleted';

-- TouchAccountLink sets the last use of a link unless it was set after stale_before, so that a link is written at
-- most once per account_link touch interval.
-- name: TouchAccountLink :exec
UPDATE account_links
SET last_used_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND (last_used_at IS NULL OR last_used_at < @stale_before::timestamptz);

-- GetLinkConnection reads a Connection that is not deleted, for starting a link through it.
-- name: GetLinkConnection :one
SELECT id, public_id, type, name, bot_user_id, bot_username
FROM connections
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL;

-- ListLinkableConnections lists the Connections that are not deleted, by messenger and name (C-18.FR-10).
-- Bridge Connections are not linkable until their Account links exist (S-076): LinkableConnection.messenger has no
-- bridge.
-- name: ListLinkableConnections :many
SELECT public_id, name, type
FROM connections
WHERE org_id = @org_id AND deleted_at IS NULL AND type <> 'bridge'
ORDER BY type, lower(name), id;

-- LockLinkUser locks the User a link is made for, so that the links of one User change one at a time.
-- name: LockLinkUser :one
SELECT id, public_id, login, name, language, status
FROM users
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- GetUserByPublicID reads a User named by an Admin, deleted ones included.
-- name: GetUserByPublicID :one
SELECT id, public_id, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id;

-- ListAccountLinksOfUser lists the links of a User with the Connection each was made through.
-- name: ListAccountLinksOfUser :many
SELECT al.public_id, al.messenger, al.external_id, al.username, al.last_used_at, al.created_at,
       c.public_id AS connection_public_id, c.name AS connection_name
FROM account_links al
JOIN connections c ON c.org_id = al.org_id AND c.id = al.connection_id
WHERE al.org_id = @org_id AND al.user_id = @user_id
ORDER BY al.messenger, al.created_at, al.id;

-- GetAccountLinkOfUser locks the link public_id of a User, for unlinking it.
-- name: GetAccountLinkOfUser :one
SELECT al.id, al.public_id, al.messenger, al.external_id, al.username,
       c.public_id AS connection_public_id, c.name AS connection_name
FROM account_links al
JOIN connections c ON c.org_id = al.org_id AND c.id = al.connection_id
WHERE al.org_id = @org_id AND al.user_id = @user_id AND al.public_id = @public_id
FOR UPDATE OF al;

-- GetAccountLinkBySpace locks the link of the account external_id of the identity space, whoever it belongs to.
-- name: GetAccountLinkBySpace :one
SELECT id, public_id, user_id
FROM account_links
WHERE org_id = @org_id AND identity_space = @identity_space AND external_id = @external_id
FOR UPDATE;

-- GetUserLinkInSpace locks the link a User has in the identity space, which a new link replaces or, for the same
-- account, keeps.
-- name: GetUserLinkInSpace :one
SELECT al.id, al.public_id, al.messenger, al.external_id, al.username, al.created_at, al.last_used_at,
       c.public_id AS connection_public_id, c.name AS connection_name
FROM account_links al
JOIN connections c ON c.org_id = al.org_id AND c.id = al.connection_id
WHERE al.org_id = @org_id AND al.user_id = @user_id AND al.identity_space = @identity_space
FOR UPDATE OF al;

-- name: DeleteAccountLink :execrows
DELETE FROM account_links
WHERE org_id = @org_id AND id = @id;

-- UpdateAccountLink keeps a link whose account is linked again by its User, with the username and the Connection of
-- the new link.
-- name: UpdateAccountLink :exec
UPDATE account_links
SET username = @username, connection_id = @connection_id
WHERE org_id = @org_id AND id = @id;

-- name: InsertAccountLink :one
INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username, created_at)
VALUES (@org_id, @public_id, @user_id, @messenger, @connection_id, @external_id, @username, @created_at::timestamptz)
RETURNING id;

-- name: InsertLinkRequest :one
INSERT INTO account_link_requests (org_id, public_id, user_id, messenger, connection_id, secret_hash,
                                   target_external_id, target_username, created_at, expires_at)
VALUES (@org_id, @public_id, @user_id, @messenger, @connection_id, @secret_hash, @target_external_id,
        @target_username, @created_at::timestamptz, @expires_at::timestamptz)
RETURNING id;

-- GetTelegramRequestByHash locks the Telegram link request of a token, which `/start` names by its hash alone, with
-- its User and the Connection it is bound to.
-- name: GetTelegramRequestByHash :one
SELECT r.id, r.user_id, r.connection_id, r.expires_at, r.consumed_at, r.voided_at,
       u.public_id AS user_public_id, c.public_id AS connection_public_id
FROM account_link_requests r
JOIN users u ON u.org_id = r.org_id AND u.id = r.user_id
JOIN connections c ON c.org_id = r.org_id AND c.id = r.connection_id
WHERE r.org_id = @org_id AND r.messenger = 'telegram' AND r.secret_hash = @secret_hash
FOR UPDATE OF r;

-- GetLinkRequest reads a link request by its public_id, with its Connection.
-- name: GetLinkRequest :one
SELECT r.id, r.user_id, r.messenger, r.connection_id, r.target_external_id, r.target_username,
       c.public_id AS connection_public_id, c.name AS connection_name
FROM account_link_requests r
JOIN connections c ON c.org_id = r.org_id AND c.id = r.connection_id
WHERE r.org_id = @org_id AND r.public_id = @public_id;

-- LockLinkRequest locks a link request for checking its code.
-- name: LockLinkRequest :one
SELECT id, secret_hash, attempts, expires_at, consumed_at, voided_at
FROM account_link_requests
WHERE org_id = @org_id AND id = @id
FOR UPDATE;

-- name: ConsumeLinkRequest :execrows
UPDATE account_link_requests
SET consumed_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND consumed_at IS NULL AND voided_at IS NULL;

-- RecordWrongCode counts a wrong code; the attempt that reaches max_attempts voids the request (C-18.AC-2).
-- name: RecordWrongCode :one
UPDATE account_link_requests
SET attempts = attempts + 1,
    voided_at = CASE WHEN attempts + 1 >= @max_attempts::bigint THEN @now::timestamptz ELSE voided_at END
WHERE org_id = @org_id AND id = @id
RETURNING attempts;

-- VoidPendingMattermostRequests voids the pending Mattermost requests of a User for a Connection, which a new one
-- replaces.
-- name: VoidPendingMattermostRequests :execrows
UPDATE account_link_requests
SET voided_at = @now::timestamptz
WHERE org_id = @org_id AND user_id = @user_id AND connection_id = @connection_id AND messenger = 'mattermost'
  AND consumed_at IS NULL AND voided_at IS NULL;

-- name: VoidLinkRequest :exec
UPDATE account_link_requests
SET voided_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND consumed_at IS NULL AND voided_at IS NULL;

-- LockCodeTarget takes, until the transaction ends, the lock of the Mattermost code requests for one account of a
-- Connection (class db.AccountLinkCodeTargetLockClass), so that the count of its requests and the new one of
-- concurrent requests serialize. A collision of the hash only serializes two accounts.
-- name: LockCodeTarget :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashtext(format('%s/%s/%s', @org_id::bigint, @connection_id::bigint,
                                                                @target_external_id::text)));

-- CountUserCodeRequests counts the Mattermost code requests of a User made after since, with the oldest of them.
-- name: CountUserCodeRequests :one
SELECT count(*)::bigint AS requests, coalesce(min(created_at), @since::timestamptz)::timestamptz AS oldest
FROM account_link_requests
WHERE org_id = @org_id AND user_id = @user_id AND messenger = 'mattermost' AND created_at > @since::timestamptz;

-- CountTargetCodeRequests counts the Mattermost code requests for one account of a Connection made after since, by
-- any User, with the oldest of them.
-- name: CountTargetCodeRequests :one
SELECT count(*)::bigint AS requests, coalesce(min(created_at), @since::timestamptz)::timestamptz AS oldest
FROM account_link_requests
WHERE org_id = @org_id AND connection_id = @connection_id AND target_external_id = @target_external_id
  AND messenger = 'mattermost' AND created_at > @since::timestamptz;

-- DeleteUserAccountLinks deletes every link of a User and returns them with their Connections (C-03.FR-13).
-- name: DeleteUserAccountLinks :many
WITH deleted AS (
    DELETE FROM account_links
    WHERE org_id = @org_id AND user_id = @user_id
    RETURNING id, public_id, messenger, connection_id, external_id, username
)
SELECT d.public_id, d.messenger, d.external_id, d.username, c.public_id AS connection_public_id,
       c.name AS connection_name
FROM deleted d
JOIN connections c ON c.org_id = @org_id AND c.id = d.connection_id
ORDER BY d.id;

-- name: VoidUserLinkRequests :execrows
UPDATE account_link_requests
SET voided_at = @now::timestamptz
WHERE org_id = @org_id AND user_id = @user_id AND consumed_at IS NULL AND voided_at IS NULL;

-- PruneLinkRequests deletes up to batch_size link requests that expired before @before, consumed and voided ones
-- included. A request being used is skipped and taken at the next run.
-- name: PruneLinkRequests :execrows
DELETE FROM account_link_requests r
WHERE r.org_id = @org_id AND r.id IN (
    SELECT o.id FROM account_link_requests o
    WHERE o.org_id = @org_id AND o.expires_at < @before::timestamptz
    ORDER BY o.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
);
