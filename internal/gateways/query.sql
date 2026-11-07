-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Notification gateways (C-30): the Bridge Connections whose stored info declares the role gateway, the contacts of
-- people at them and the confirmation codes of the contacts, hashed.

-- ListGateways lists the Notification gateways that are not deleted, by name.
-- name: ListGateways :many
SELECT id, public_id, name
FROM connections
WHERE org_id = @org_id AND type = 'bridge' AND deleted_at IS NULL AND bridge_info->'roles' @> '["gateway"]'::jsonb
ORDER BY lower(name), id;

-- GetGateway reads the Notification gateway public_id, which is not deleted.
-- name: GetGateway :one
SELECT id, public_id, name
FROM connections
WHERE org_id = @org_id AND public_id = @public_id AND type = 'bridge' AND deleted_at IS NULL
  AND bridge_info->'roles' @> '["gateway"]'::jsonb;

-- ConfirmedContact reads the confirmed contact of a User at a Notification gateway that is not deleted, the one that
-- is called.
-- name: ConfirmedContact :one
SELECT g.contact
FROM gateway_contacts g
JOIN connections c ON c.org_id = g.org_id AND c.id = g.connection_id
WHERE g.org_id = @org_id AND g.user_id = @user_id AND g.connection_id = @connection_id AND g.confirmed_at IS NOT NULL
  AND c.deleted_at IS NULL AND c.bridge_info->'roles' @> '["gateway"]'::jsonb;

-- LockContactUser locks the User whose contacts change, so that the contacts and codes of one User change one at a
-- time.
-- name: LockContactUser :one
SELECT id, public_id, language, status
FROM users
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- GetUserByPublicID reads a User an Admin names, deleted ones included.
-- name: GetUserByPublicID :one
SELECT id, public_id, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id;

-- LockContactText takes, until the transaction ends, the lock of the code requests for one contact text (class
-- db.GatewayContactLockClass), so that the count of its requests and the new one of concurrent requests of several
-- Users serialize. A collision of the hash only serializes two texts.
-- name: LockContactText :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashtext(format('%s/%s', @org_id::bigint,
                                                                encode(@contact_hash::bytea, 'hex'))));

-- CountUserCodes counts the codes a User asked for after since, with the oldest of them.
-- name: CountUserCodes :one
SELECT count(*)::bigint AS requests, coalesce(min(created_at), @since::timestamptz)::timestamptz AS oldest
FROM gateway_contact_codes
WHERE org_id = @org_id AND user_id = @user_id AND created_at > @since::timestamptz;

-- CountContactCodes counts the codes for one contact text asked for after since, by any User, with the oldest of them.
-- name: CountContactCodes :one
SELECT count(*)::bigint AS requests, coalesce(min(created_at), @since::timestamptz)::timestamptz AS oldest
FROM gateway_contact_codes
WHERE org_id = @org_id AND contact_hash = @contact_hash AND created_at > @since::timestamptz;

-- ListContactsOfUser lists the contacts of a User with their gateway, by gateway and creation, each unconfirmed one
-- with the confirmed contact it replaces once confirmed.
-- name: ListContactsOfUser :many
SELECT g.id, g.public_id, g.contact, g.confirmed_at, g.created_at, c.public_id AS connection_public_id,
       c.name AS connection_name, r.public_id AS replaces
FROM gateway_contacts g
JOIN connections c ON c.org_id = g.org_id AND c.id = g.connection_id
LEFT JOIN gateway_contacts r ON r.org_id = g.org_id AND r.user_id = g.user_id AND r.connection_id = g.connection_id
    AND r.confirmed_at IS NOT NULL AND g.confirmed_at IS NULL
WHERE g.org_id = @org_id AND g.user_id = @user_id
ORDER BY lower(c.name), c.id, g.created_at, g.id;

-- LockContactOfUser locks the contact public_id of a User, with its gateway, whether that is still a Notification
-- gateway, and the confirmed contact it replaces once confirmed.
-- name: LockContactOfUser :one
SELECT g.id, g.public_id, g.connection_id, g.contact, g.confirmed_at, g.created_at,
       c.public_id AS connection_public_id, c.name AS connection_name,
       (c.deleted_at IS NULL AND c.type = 'bridge' AND coalesce(c.bridge_info->'roles' @> '["gateway"]'::jsonb, false))::boolean
           AS gateway,
       r.public_id AS replaces
FROM gateway_contacts g
JOIN connections c ON c.org_id = g.org_id AND c.id = g.connection_id
LEFT JOIN gateway_contacts r ON r.org_id = g.org_id AND r.user_id = g.user_id AND r.connection_id = g.connection_id
    AND r.confirmed_at IS NOT NULL AND g.confirmed_at IS NULL
WHERE g.org_id = @org_id AND g.user_id = @user_id AND g.public_id = @public_id
FOR UPDATE OF g;

-- name: InsertContact :one
INSERT INTO gateway_contacts (org_id, public_id, user_id, connection_id, contact, created_at)
VALUES (@org_id, @public_id, @user_id, @connection_id, @contact, @created_at::timestamptz)
RETURNING id;

-- name: DeleteContact :execrows
DELETE FROM gateway_contacts
WHERE org_id = @org_id AND id = @id;

-- DeleteUnconfirmedContacts deletes the unconfirmed contacts of a User at a gateway, which a new contact replaces; the
-- codes keep counting toward the limits without them.
-- name: DeleteUnconfirmedContacts :many
DELETE FROM gateway_contacts
WHERE org_id = @org_id AND user_id = @user_id AND connection_id = @connection_id AND confirmed_at IS NULL
RETURNING public_id, contact;

-- DeleteReplacedContacts deletes the confirmed contacts of a User at a gateway other than keep, which the
-- confirmation of keep replaces.
-- name: DeleteReplacedContacts :many
DELETE FROM gateway_contacts
WHERE org_id = @org_id AND user_id = @user_id AND connection_id = @connection_id AND confirmed_at IS NOT NULL
  AND id <> @keep
RETURNING public_id, contact;

-- name: ConfirmContact :exec
UPDATE gateway_contacts
SET confirmed_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id;

-- DeleteUserContacts deletes every contact of a User and returns them with their gateways (C-03.FR-13).
-- name: DeleteUserContacts :many
WITH deleted AS (
    DELETE FROM gateway_contacts
    WHERE org_id = @org_id AND user_id = @user_id
    RETURNING id, public_id, connection_id, contact, confirmed_at
)
SELECT d.public_id, d.contact, (d.confirmed_at IS NOT NULL)::boolean AS confirmed,
       c.public_id AS connection_public_id, c.name AS connection_name
FROM deleted d
JOIN connections c ON c.org_id = @org_id AND c.id = d.connection_id
ORDER BY d.id;

-- name: DeleteUserCodes :execrows
DELETE FROM gateway_contact_codes
WHERE org_id = @org_id AND user_id = @user_id;

-- MarkCodeSent records that the gateway accepted the notification of a code, which makes it the one checked.
-- name: MarkCodeSent :exec
UPDATE gateway_contact_codes
SET sent_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id;

-- VoidEarlierCodes voids the codes of a contact asked for before keep that are neither used nor void, which keep
-- replaces once the gateway accepted it.
-- name: VoidEarlierCodes :execrows
UPDATE gateway_contact_codes
SET voided_at = @now::timestamptz
WHERE org_id = @org_id AND contact_id = @contact_id AND id < @keep AND used_at IS NULL AND voided_at IS NULL;

-- name: InsertCode :one
INSERT INTO gateway_contact_codes (org_id, user_id, contact_id, contact_hash, session_id, code_hash, created_at,
                                   expires_at)
VALUES (@org_id, @user_id, @contact_id, @contact_hash, @session_id, @code_hash, @created_at::timestamptz,
        @expires_at::timestamptz)
RETURNING id;

-- VoidCode voids a code whose sending failed; it still counts toward the limits.
-- name: VoidCode :exec
UPDATE gateway_contact_codes
SET voided_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND used_at IS NULL AND voided_at IS NULL;

-- DeleteCode deletes a code that was never sent, since no limiter token was free: it does not count.
-- name: DeleteCode :exec
DELETE FROM gateway_contact_codes
WHERE org_id = @org_id AND id = @id;

-- LockNewestCode locks the newest code of a contact that the gateway accepted, for checking it, with whether the web
-- session that asked for it ended; a code whose sending failed, or that is still being sent, is never the one checked.
-- Newest is by id, as VoidEarlierCodes counts earlier, not by created_at, which replicas may set with skewed clocks.
-- name: LockNewestCode :one
SELECT k.id, k.session_id, k.code_hash, k.attempts, k.expires_at, k.used_at, k.voided_at,
       (s.ended_at IS NOT NULL)::boolean AS session_ended
FROM gateway_contact_codes k
JOIN sessions s ON s.org_id = k.org_id AND s.id = k.session_id
WHERE k.org_id = @org_id AND k.contact_id = @contact_id AND k.sent_at IS NOT NULL
ORDER BY k.id DESC
LIMIT 1
FOR UPDATE OF k;

-- RecordWrongCode counts a wrong code; the attempt that reaches max_attempts voids the code.
-- name: RecordWrongCode :one
UPDATE gateway_contact_codes
SET attempts = attempts + 1,
    voided_at = CASE WHEN attempts + 1 >= @max_attempts::bigint THEN @now::timestamptz ELSE voided_at END
WHERE org_id = @org_id AND id = @id
RETURNING attempts;

-- UseCode marks the code used; a used code is void.
-- name: UseCode :execrows
UPDATE gateway_contact_codes
SET used_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND used_at IS NULL AND voided_at IS NULL;

-- PruneCodes deletes up to batch_size codes that expired before @before, used and void ones included. A code being
-- checked is skipped and taken at the next run. The batch is chosen once, in a materialized CTE, as PruneLinkRequests
-- does.
-- name: PruneCodes :execrows
WITH doomed AS MATERIALIZED (
    SELECT o.id FROM gateway_contact_codes o
    WHERE o.org_id = @org_id AND o.expires_at < @before::timestamptz
    ORDER BY o.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
)
DELETE FROM gateway_contact_codes k
USING doomed d
WHERE k.org_id = @org_id AND k.id = d.id;
