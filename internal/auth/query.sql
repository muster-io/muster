-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- ListRolePermissions reads the allocation of Permissions to Roles, reference data without org_id.
-- name: ListRolePermissions :many
SELECT role, permission
FROM role_permissions
ORDER BY role, permission;

-- GetSignInUser finds the account of a login, compared lowercased.
-- name: GetSignInUser :one
SELECT id, public_id, name, role, status, password_hash
FROM users
WHERE org_id = @org_id AND lower(login) = lower(@login);

-- name: GetUserPassword :one
SELECT login, password_hash, (oidc_subject IS NOT NULL)::boolean AS has_oidc_identity
FROM users
WHERE org_id = @org_id AND id = @id;

-- name: MarkSignedIn :exec
UPDATE users
SET last_sign_in_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id;

-- SetPassword replaces the password of a local account.
-- name: SetPassword :execrows
UPDATE users
SET password_hash = @password_hash, password_changed_at = @now::timestamptz, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND oidc_subject IS NULL;

-- name: CreateSession :one
INSERT INTO sessions (
    org_id, public_id, user_id, token_hash, state, method, address, user_agent, created_at, last_used_at,
    idle_expires_at, expires_at
)
VALUES (
    @org_id, @public_id, @user_id, @token_hash, @state, @method, sqlc.narg('address'), sqlc.narg('user_agent'),
    @created_at, @created_at, @idle_expires_at, @expires_at
)
RETURNING id;

-- GetSessionByToken finds a session by the hash of its cookie value, with its user.
-- name: GetSessionByToken :one
SELECT s.id, s.public_id, s.user_id, s.state, s.method, s.address, s.user_agent, s.created_at, s.last_used_at,
       s.idle_expires_at, s.expires_at, s.ended_at, s.end_reason, u.public_id AS user_public_id, u.name AS user_name,
       u.role AS user_role, u.status AS user_status
FROM sessions s
JOIN users u ON u.org_id = s.org_id AND u.id = s.user_id
WHERE s.org_id = @org_id AND u.org_id = @org_id AND s.token_hash = @token_hash;

-- TouchSession records the use of a session, at most once a minute, and moves its idle expiry.
-- name: TouchSession :exec
UPDATE sessions
SET last_used_at = @now, idle_expires_at = @idle_expires_at
WHERE org_id = @org_id AND id = @id AND ended_at IS NULL;

-- name: EndSession :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = @end_reason
WHERE org_id = @org_id AND id = @id AND ended_at IS NULL;

-- name: EndUserSessions :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = @end_reason
WHERE org_id = @org_id AND user_id = @user_id AND ended_at IS NULL;

-- name: EndOtherUserSessions :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = @end_reason
WHERE org_id = @org_id AND user_id = @user_id AND id <> @keep_id AND ended_at IS NULL;

-- ListUserSessions lists the sessions of a user that have neither ended nor expired, the latest used first.
-- name: ListUserSessions :many
SELECT id, public_id, method, address, user_agent, created_at, last_used_at
FROM sessions
WHERE org_id = @org_id AND user_id = @user_id AND ended_at IS NULL AND idle_expires_at > @now AND expires_at > @now
ORDER BY last_used_at DESC, id DESC;

-- name: GetThrottles :many
SELECT subject_kind, consecutive_failures, blocked_until
FROM sign_in_throttles
WHERE org_id = @org_id
  AND ((subject_kind = 'account' AND subject = @account) OR (subject_kind = 'address' AND subject = @address));

-- RecordSignInFailure counts one more consecutive failure of a subject and returns the count.
-- name: RecordSignInFailure :one
INSERT INTO sign_in_throttles (org_id, subject_kind, subject, consecutive_failures, last_failure_at)
VALUES (@org_id, @subject_kind, @subject, 1, @now)
ON CONFLICT (org_id, subject_kind, subject) DO UPDATE
SET consecutive_failures = sign_in_throttles.consecutive_failures + 1, last_failure_at = EXCLUDED.last_failure_at
RETURNING consecutive_failures;

-- name: BlockSignIn :exec
UPDATE sign_in_throttles
SET blocked_until = @blocked_until
WHERE org_id = @org_id AND subject_kind = @subject_kind AND subject = @subject;

-- ResetSignInThrottles forgets the failures of an account and of a source address after a successful sign-in.
-- name: ResetSignInThrottles :exec
DELETE FROM sign_in_throttles
WHERE org_id = @org_id
  AND ((subject_kind = 'account' AND subject = @account) OR (subject_kind = 'address' AND subject = @address));
