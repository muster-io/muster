-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: GetUser :one
SELECT u.id, u.public_id, u.login, u.name, u.email, u.role, u.source, u.status, (u.password_hash IS NOT NULL)::boolean AS has_password,
       (u.oidc_subject IS NOT NULL)::boolean AS has_oidc_identity,
       (u.oidc_offline_token_ciphertext IS NOT NULL)::boolean AS has_offline_token,
       u.time_zone, u.language, u.last_sign_in_at, u.created_at, u.version,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enabled
FROM users u
WHERE u.org_id = @org_id AND u.id = @id;

-- UpdateProfile changes what the user edits in the profile: the display name, the time zone and the language.
-- name: UpdateProfile :execrows
UPDATE users
SET name = @name, time_zone = sqlc.narg('time_zone'), language = sqlc.narg('language'), updated_at = @updated_at,
    version = version + 1
WHERE org_id = @org_id AND id = @id;

-- CountAdmins counts the Admins that are not deleted; a disabled Admin still counts.
-- name: CountAdmins :one
SELECT count(*)
FROM users
WHERE org_id = @org_id AND role = 'admin' AND status <> 'deleted';

-- name: CreateUser :one
INSERT INTO users (
    org_id, public_id, login, name, email, role, source, status, password_hash, password_changed_at, created_at,
    updated_at
)
VALUES (
    @org_id, @public_id, @login, @name, sqlc.narg('email'), @role, @source, 'active', sqlc.narg('password_hash'),
    sqlc.narg('password_changed_at'), @created_at, @created_at
)
RETURNING id;
