-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- ListRolePermissions reads the allocation of Permissions to Roles, reference data without org_id.
-- name: ListRolePermissions :many
SELECT role, permission
FROM role_permissions
ORDER BY role, permission;

-- GetSignInUser finds the account of a login, compared lowercased, with whether it has TOTP and the Organization's
-- TOTP policy.
-- name: GetSignInUser :one
SELECT u.id, u.public_id, u.name, u.role, u.status, u.password_hash,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enrolled,
       o.totp_required
FROM users u
JOIN organizations o ON o.id = u.org_id
WHERE u.org_id = @org_id AND lower(u.login) = lower(@login);

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

-- CompleteSecondFactor makes a session that waited for the TOTP code active.
-- name: CompleteSecondFactor :execrows
UPDATE sessions
SET state = 'active'
WHERE org_id = @org_id AND id = @id AND state = 'totp_required' AND ended_at IS NULL;

-- LiveSessions returns which of the sessions are still usable at @now: active, neither ended nor expired, of an active
-- user. Reading them does not count as a use.
-- name: LiveSessions :many
SELECT s.id
FROM sessions s
JOIN users u ON u.org_id = s.org_id AND u.id = s.user_id
WHERE s.org_id = @org_id AND u.org_id = @org_id AND s.id = ANY(@ids::bigint[]) AND s.state = 'active'
  AND s.ended_at IS NULL AND s.idle_expires_at > @now AND s.expires_at > @now AND u.status = 'active';

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

-- TryLockSignInSubject takes the attempt lock of an account until the transaction ends, without waiting; false means
-- that another attempt of the account is being evaluated. The two-key advisory locks do not overlap the one-key locks
-- of the migrations and the Leader.
-- name: TryLockSignInSubject :one
SELECT pg_try_advisory_xact_lock(@lock_class::int, hashtext(@subject::text))::boolean AS locked;

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

-- PruneSessions deletes up to batch_size sessions that could no longer be used before @before: ended, past their idle
-- timeout or past their lifetime. A session row being used is skipped and taken at the next run.
-- name: PruneSessions :execrows
DELETE FROM sessions s
WHERE s.org_id = @org_id AND s.id IN (
    SELECT e.id FROM sessions e
    WHERE e.org_id = @org_id AND LEAST(e.ended_at, e.idle_expires_at, e.expires_at) < @before::timestamptz
    ORDER BY e.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
);

-- PruneSignInThrottles deletes up to batch_size throttle rows whose last failure, and whose block, are older than
-- @before. A row being counted is skipped and taken at the next run.
-- name: PruneSignInThrottles :execrows
DELETE FROM sign_in_throttles t
WHERE t.org_id = @org_id AND (t.subject_kind, t.subject) IN (
    SELECT o.subject_kind, o.subject FROM sign_in_throttles o
    WHERE o.org_id = @org_id AND o.last_failure_at < @before::timestamptz
      AND (o.blocked_until IS NULL OR o.blocked_until < @before::timestamptz)
    ORDER BY o.subject_kind, o.subject
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
);
