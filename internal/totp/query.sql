-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- GetTOTPUser reads what TOTP needs of a user: the login names the account in the authenticator app.
-- name: GetTOTPUser :one
SELECT id, public_id, login, name, status
FROM users
WHERE org_id = @org_id AND id = @id;

-- name: GetTOTPUserByPublicID :one
SELECT id, public_id, login, name, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id;

-- GetTOTPUserByLogin finds an account by its login, compared lowercased.
-- name: GetTOTPUserByLogin :one
SELECT id, public_id, login, name, status
FROM users
WHERE org_id = @org_id AND lower(login) = lower(@login);

-- name: GetTOTP :one
SELECT seed_ciphertext, seed_key_id, pending_seed_ciphertext, pending_seed_key_id, enrolled_at, last_used_step
FROM user_totp
WHERE org_id = @org_id AND user_id = @user_id;

-- SetPendingSeed stores the seed of a begun enrolment, replacing a pending one; it changes nothing and returns no row
-- while TOTP is enrolled.
-- name: SetPendingSeed :execrows
INSERT INTO user_totp (user_id, org_id, pending_seed_ciphertext, pending_seed_key_id, pending_seed_updated_at)
VALUES (@user_id, @org_id, @ciphertext, @key_id::text, @now::timestamptz)
ON CONFLICT (user_id) DO UPDATE
SET pending_seed_ciphertext = EXCLUDED.pending_seed_ciphertext, pending_seed_key_id = EXCLUDED.pending_seed_key_id,
    pending_seed_updated_at = EXCLUDED.pending_seed_updated_at
WHERE user_totp.org_id = @org_id AND user_totp.seed_ciphertext IS NULL;

-- ActivateSeed confirms an enrolment: the pending seed that was checked, written again as the seed, becomes the
-- user's TOTP, and the step of the confirming code is used. No row changes when the pending seed was replaced or
-- TOTP is enrolled meanwhile.
-- name: ActivateSeed :execrows
UPDATE user_totp
SET seed_ciphertext = @ciphertext, seed_key_id = @key_id::text, seed_updated_at = @now::timestamptz,
    enrolled_at = @now::timestamptz,
    pending_seed_ciphertext = NULL, pending_seed_key_id = NULL, pending_seed_updated_at = NULL,
    last_used_step = @step::bigint
WHERE org_id = @org_id AND user_id = @user_id AND seed_ciphertext IS NULL
  AND pending_seed_ciphertext = @pending_ciphertext;

-- UseStep records the time step of an accepted code; no row changes for a step already used or an older one, which
-- refuses a replayed code.
-- name: UseStep :execrows
UPDATE user_totp
SET last_used_step = @step::bigint
WHERE org_id = @org_id AND user_id = @user_id AND seed_ciphertext IS NOT NULL
  AND (last_used_step IS NULL OR last_used_step < @step::bigint);

-- name: DeleteTOTP :execrows
DELETE FROM user_totp
WHERE org_id = @org_id AND user_id = @user_id;

-- name: ListUnusedRecoveryCodes :many
SELECT id, code_hash
FROM user_recovery_codes
WHERE org_id = @org_id AND user_id = @user_id AND used_at IS NULL
ORDER BY id;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*)
FROM user_recovery_codes
WHERE org_id = @org_id AND user_id = @user_id AND used_at IS NULL;

-- UseRecoveryCode marks a code used; no row changes when another request used it first.
-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes
SET used_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND used_at IS NULL;

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (org_id, user_id, code_hash, created_at)
VALUES (@org_id, @user_id, @code_hash, @now);

-- DeleteUnusedRecoveryCodes removes the codes not used yet, before new ones are issued.
-- name: DeleteUnusedRecoveryCodes :execrows
DELETE FROM user_recovery_codes
WHERE org_id = @org_id AND user_id = @user_id AND used_at IS NULL;

-- DeleteRecoveryCodes removes every code of a user, with the TOTP they belong to.
-- name: DeleteRecoveryCodes :execrows
DELETE FROM user_recovery_codes
WHERE org_id = @org_id AND user_id = @user_id;

-- ActivateSession makes a session that waited for the enrolment active, once the enrolment is confirmed.
-- name: ActivateSession :execrows
UPDATE sessions
SET state = 'active'
WHERE org_id = @org_id AND id = @id AND state = 'totp_enrolment_required' AND ended_at IS NULL;

-- EndSessionsOfUser ends every session of a user whose TOTP an Admin reset.
-- name: EndSessionsOfUser :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = 'totp_reset'
WHERE org_id = @org_id AND user_id = @user_id AND ended_at IS NULL;
