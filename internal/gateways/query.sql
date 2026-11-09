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

-- The calls through Notification gateways (C-30.FR-4): a row per call to a person through a gateway, the Alert Groups
-- it is about, claimed with a lease by any replica's gateway sender (sender.go, calls.go).

-- GetPage reads what a Page names: the Alert Group, with whether it is critical, which puts its call first; the
-- Notification gateway, not deleted; and the User, not deleted. No row when one of them is not so.
-- name: GetPage :one
SELECT a.number, (a.severity_level = 'critical')::boolean AS critical, c.public_id AS connection_public_id,
       u.public_id AS user_public_id
FROM alert_groups a, connections c, users u
WHERE a.org_id = @org_id AND a.id = @alert_group_id
  AND c.org_id = @org_id AND c.id = @connection_id AND c.type = 'bridge' AND c.deleted_at IS NULL
  AND c.bridge_info->'roles' @> '["gateway"]'::jsonb
  AND u.org_id = @org_id AND u.id = @user_id AND u.status <> 'deleted';

-- GetPageGroupID reads the internal id of the Alert Group public_id, for the hook of muster dev.
-- name: GetPageGroupID :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND public_id = @public_id;

-- LockCallPerson takes, until the transaction ends, the lock of the calls of a User through a Notification gateway
-- (class db.GatewayCallLockClass): a Page joining or creating the waiting call, the start of a first attempt and the
-- end of a call serialize. A collision of the hash only serializes two people.
-- name: LockCallPerson :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashtext(format('%s/%s/%s', @org_id::bigint, @connection_id::bigint,
                                                                @user_id::bigint)));

-- PreviousCall reads the latest call of a User through a gateway that went — whose first attempt was made, or tried —
-- with its state and its end, null while it is being sent.
-- name: PreviousCall :one
SELECT state, ended_at
FROM gateway_calls
WHERE org_id = @org_id AND connection_id = @connection_id AND user_id = @user_id AND first_attempt_at IS NOT NULL
ORDER BY id DESC
LIMIT 1;

-- LockWaitingCall locks the call of a User through a gateway that waits for its first attempt.
-- name: LockWaitingCall :one
SELECT id, public_id
FROM gateway_calls
WHERE org_id = @org_id AND connection_id = @connection_id AND user_id = @user_id AND state = 'waiting'
FOR NO KEY UPDATE;

-- InsertWaitingCall creates the call that waits for its first attempt, due at not_before, never while it is null.
-- name: InsertWaitingCall :one
INSERT INTO gateway_calls (org_id, public_id, connection_id, user_id, state, critical, not_before, next_attempt_at,
                           created_at)
VALUES (@org_id, @public_id, @connection_id, @user_id, 'waiting', @critical, sqlc.narg('not_before')::timestamptz,
        sqlc.narg('not_before')::timestamptz, @now::timestamptz)
RETURNING id;

-- MarkCallCritical makes a waiting call critical once a critical Alert Group joins it.
-- name: MarkCallCritical :exec
UPDATE gateway_calls
SET critical = true
WHERE org_id = @org_id AND id = @id AND state = 'waiting';

-- AddCallItem adds an Alert Group to a waiting call; one it is about already stays as it is, and one dropped from it
-- while it waited — a preparation that dropped it and then found no limiter token — is queued again.
-- name: AddCallItem :exec
INSERT INTO gateway_call_items (org_id, call_id, alert_group_id, queued_at)
VALUES (@org_id, @call_id, @alert_group_id, @now::timestamptz)
ON CONFLICT (call_id, alert_group_id) DO UPDATE SET dropped_at = NULL, queued_at = EXCLUDED.queued_at
WHERE gateway_call_items.org_id = @org_id AND gateway_call_items.dropped_at IS NOT NULL;

-- NotifyCalls wakes the gateway senders of every replica once the transaction commits.
-- name: NotifyCalls :exec
SELECT pg_notify(@channel::text, '');

-- ClaimDueCalls leases the due calls that wait for their first attempt or are being retried, whose lease is free or
-- ran out, critical first, then by when they are due. The choice is a materialized CTE, run once, as in
-- ClaimDueDeliveries.
-- name: ClaimDueCalls :many
WITH due AS MATERIALIZED (
    SELECT x.id
    FROM gateway_calls x
    WHERE x.org_id = @org_id AND x.state IN ('waiting', 'sending') AND x.next_attempt_at <= @due::timestamptz
      AND (x.lease_until IS NULL OR x.lease_until <= @now::timestamptz)
    ORDER BY x.critical DESC, x.next_attempt_at, x.id
    LIMIT @lim
    FOR NO KEY UPDATE OF x SKIP LOCKED
)
UPDATE gateway_calls c
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz
FROM due
WHERE c.org_id = @org_id AND c.id = due.id
RETURNING c.id, c.public_id, c.connection_id, c.user_id, c.critical,
          c.next_attempt_at::timestamptz AS next_attempt_at;

-- GetLeasedCall reads, and locks, a call whose lease this replica still holds at the real time now, with its gateway
-- and whether it is still a Notification gateway, not deleted, and whether the call came due while the gateway was
-- Broken — it is Broken, or the call's due time lies before the end of its last Broken state, which another replica
-- ended after this one claimed the call (P-68); this holds within the replicas' clock skew, so a Page made on a
-- replica whose clock is behind by δ, within δ after the end, may be ended too, accepted as NTP keeps δ to
-- milliseconds and MusterClockSkew fires above 2 s — its User and, from its first attempt, its contact while that is
-- still confirmed and its signed action data, null for a call that a replica of an earlier version started. No row
-- when the lease ran out or went to another replica, or the call ended.
-- name: GetLeasedCall :one
SELECT g.id, g.public_id, g.connection_id, g.user_id, g.state, g.critical, g.language, g.text, g.attempts,
       g.first_failed_at, g.action_data, c.public_id AS connection_public_id, c.name AS connection_name,
       (c.deleted_at IS NULL AND c.type = 'bridge' AND coalesce(c.bridge_info->'roles' @> '["gateway"]'::jsonb, false))::boolean
           AS gateway,
       (c.gateway_broken_since IS NOT NULL OR coalesce(g.next_attempt_at < c.gateway_recovered_at, false))::boolean
           AS gateway_broken,
       u.public_id AS user_public_id, u.name AS user_name, u.login AS user_login, u.language AS user_language,
       k.contact AS contact
FROM gateway_calls g
JOIN connections c ON c.org_id = g.org_id AND c.id = g.connection_id
JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
LEFT JOIN gateway_contacts k ON k.org_id = g.org_id AND k.id = g.contact_id AND k.confirmed_at IS NOT NULL
WHERE g.org_id = @org_id AND g.id = @id AND g.state IN ('waiting', 'sending') AND g.lease_owner = @owner::text
  AND g.lease_until > @now::timestamptz
FOR NO KEY UPDATE OF g;

-- ListCallGroups lists the Alert Groups a call is about, without those dropped before its first attempt, by number.
-- name: ListCallGroups :many
SELECT a.id, a.public_id, a.number, a.title, a.severity_level, a.status
FROM gateway_call_items i
JOIN alert_groups a ON a.org_id = i.org_id AND a.id = i.alert_group_id
WHERE i.org_id = @org_id AND i.call_id = @call_id AND i.dropped_at IS NULL
ORDER BY a.number, a.id;

-- DropCallItems drops from a call, before its first attempt, the Alert Groups that stopped.
-- name: DropCallItems :exec
UPDATE gateway_call_items
SET dropped_at = @now::timestamptz
WHERE org_id = @org_id AND call_id = @call_id AND alert_group_id = ANY(@alert_group_ids::bigint[])
  AND dropped_at IS NULL;

-- CallContact reads the confirmed contact of a User at a Notification gateway that is not deleted, the one a call's
-- first attempt goes to.
-- name: CallContact :one
SELECT g.id, g.contact
FROM gateway_contacts g
JOIN connections c ON c.org_id = g.org_id AND c.id = g.connection_id
WHERE g.org_id = @org_id AND g.user_id = @user_id AND g.connection_id = @connection_id AND g.confirmed_at IS NOT NULL
  AND c.deleted_at IS NULL AND c.bridge_info->'roles' @> '["gateway"]'::jsonb;

-- StartCall makes the first attempt of a waiting call: it keeps its contact, its text, its language and its action
-- data, signed by the key button_key_id, from now on, takes no more Alert Groups and is due now, its lease renewed for
-- the attempt.
-- name: StartCall :execrows
UPDATE gateway_calls
SET state = 'sending', contact_id = sqlc.narg('contact_id')::bigint, language = @language::text, text = @text::text,
    action_data = sqlc.narg('action_data')::text, button_key_id = sqlc.narg('button_key_id')::text,
    first_attempt_at = @now::timestamptz, next_attempt_at = @now::timestamptz, not_before = coalesce(not_before, @now),
    lease_until = @lease_until::timestamptz
WHERE org_id = @org_id AND id = @id AND state = 'waiting' AND lease_owner = @owner::text;

-- RenewCallLease renews the lease of a call being retried for its next attempt.
-- name: RenewCallLease :execrows
UPDATE gateway_calls
SET lease_until = @lease_until::timestamptz
WHERE org_id = @org_id AND id = @id AND state = 'sending' AND lease_owner = @owner::text;

-- RescheduleCall makes a call due again at a later time without counting an attempt — no limiter token was free, or
-- the gateway asked to wait — and frees its lease. A gateway's RetryAfter passes failed_at, which starts the time
-- budget of delivery.transient_budget when no failure started it yet.
-- name: RescheduleCall :execrows
UPDATE gateway_calls
SET next_attempt_at = @at::timestamptz, lease_owner = NULL, lease_until = NULL,
    first_failed_at = coalesce(first_failed_at, sqlc.narg('failed_at')::timestamptz),
    last_error_class = coalesce(sqlc.narg('error_class')::text, last_error_class),
    last_error = coalesce(sqlc.narg('error')::text, last_error)
WHERE org_id = @org_id AND id = @id AND state IN ('waiting', 'sending') AND lease_owner = @owner::text;

-- RecordCallRetry counts a failed attempt of the budget and makes the call due again at a later time.
-- name: RecordCallRetry :execrows
UPDATE gateway_calls
SET attempts = attempts + 1, first_failed_at = coalesce(first_failed_at, @now::timestamptz),
    next_attempt_at = @at::timestamptz, lease_owner = NULL, lease_until = NULL, last_error_class = @error_class::text,
    last_error = sqlc.narg('error')::text
WHERE org_id = @org_id AND id = @id AND state = 'sending' AND lease_owner = @owner::text;

-- EndCall ends a call — accepted, Not delivered with its reason, or dropped — counting the attempt that ended it, if
-- one did, and clears its text, which names its Alert Groups, and its action data, which are never sent again; the
-- language and the id of the key that signed the data stay. It returns whether the call went, which starts the combine
-- window of the next one.
-- name: EndCall :one
UPDATE gateway_calls
SET state = @state::text, text = NULL, action_data = NULL, reason = sqlc.narg('reason')::text, notification_id = sqlc.narg('notification_id')::text,
    attempts = attempts + @attempted::bigint, ended_at = @now::timestamptz, next_attempt_at = NULL,
    lease_owner = NULL, lease_until = NULL,
    first_failed_at = CASE WHEN sqlc.narg('error_class')::text IS NULL THEN first_failed_at
                           ELSE coalesce(first_failed_at, @now::timestamptz) END,
    last_error_class = coalesce(sqlc.narg('error_class')::text, last_error_class),
    last_error = coalesce(sqlc.narg('error')::text, last_error)
WHERE org_id = @org_id AND id = @id AND state IN ('waiting', 'sending') AND lease_owner = @owner::text
RETURNING (first_attempt_at IS NOT NULL)::boolean AS went;

-- ReleaseWaitingCall sets when the call that waits behind a call being sent may go, once that call ended.
-- name: ReleaseWaitingCall :exec
UPDATE gateway_calls
SET not_before = @at::timestamptz, next_attempt_at = @at::timestamptz
WHERE org_id = @org_id AND connection_id = @connection_id AND user_id = @user_id AND state = 'waiting'
  AND not_before IS NULL;

-- NextCallWork is when the next call can be claimed: the earliest due time of a call without a live lease (business
-- clock), and the earliest end of a live lease (real clock), the zero time for none.
-- name: NextCallWork :one
SELECT coalesce(min(next_attempt_at) FILTER (WHERE lease_until IS NULL OR lease_until <= @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS free_at,
       coalesce(min(lease_until) FILTER (WHERE lease_until > @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS lease_end
FROM gateway_calls
WHERE org_id = @org_id AND state IN ('waiting', 'sending') AND next_attempt_at IS NOT NULL;

-- PruneEndedCalls deletes up to batch_size calls that ended at or before @now and are about no Alert Group any more:
-- the retention of Alert Groups deleted every one of theirs, and its cascade their items. The batch is chosen once, in
-- a materialized CTE, as PruneCodes does; a call that another transaction holds is taken at the next run.
-- name: PruneEndedCalls :execrows
WITH doomed AS MATERIALIZED (
    SELECT c.id FROM gateway_calls c
    WHERE c.org_id = @org_id AND c.state IN ('accepted', 'not_delivered', 'dropped')
      AND c.ended_at <= @now::timestamptz
      AND NOT EXISTS (SELECT 1 FROM gateway_call_items i WHERE i.org_id = c.org_id AND i.call_id = c.id)
    ORDER BY c.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
)
DELETE FROM gateway_calls g
USING doomed d
WHERE g.org_id = @org_id AND g.id = d.id;

-- GetAcknowledgedCall reads the call public_id of a Notification gateway's Connection, whatever its state, with the
-- User it was for — Role, state, name and profile language — for the Acknowledge of a notification_acknowledged event
-- (C-30.FR-6). No row for a call of another Connection or Organization, or none.
-- name: GetAcknowledgedCall :one
SELECT g.id, g.public_id, g.state, u.id AS user_id, u.public_id AS user_public_id, u.role AS user_role,
       u.status AS user_status, u.language AS user_language
FROM gateway_calls g
JOIN users u ON u.org_id = g.org_id AND u.id = g.user_id
WHERE g.org_id = @org_id AND g.connection_id = @connection_id AND g.public_id = @public_id;

-- The Broken state of Notification gateways (C-30.FR-8, broken.go): a call answered with a Fatal error, or one that
-- used up delivery.transient_budget, makes its gateway's Connection Broken; any replica probes it through info every
-- delivery.broken_probe_interval, claimed with a lease, and a successful probe or Connection check ends the state.

-- BreakGateway makes the Connection of a Notification gateway Broken with reason, probed first at @next_probe. No row
-- when it is Broken already, deleted or not a Bridge Connection.
-- name: BreakGateway :one
UPDATE connections
SET gateway_broken_since = @now::timestamptz, gateway_broken_reason = @reason::text,
    gateway_next_probe_at = @next_probe::timestamptz
WHERE org_id = @org_id AND id = @id AND type = 'bridge' AND deleted_at IS NULL AND gateway_broken_since IS NULL
RETURNING public_id, name;

-- UpdateGatewayBrokenReason keeps a Broken gateway Broken with the error of its last failure as the reason; it
-- returns whether the gateway was Broken.
-- name: UpdateGatewayBrokenReason :execrows
UPDATE connections
SET gateway_broken_reason = @reason::text
WHERE org_id = @org_id AND id = @id AND gateway_broken_since IS NOT NULL;

-- EndGatewayBroken ends the Broken state of a gateway at @recovered_at and frees the lease of its probe, and tells
-- when it became Broken and whether the Connection is deleted. A probe passes its owner: then only while this replica still holds
-- the probe's lease at the real time @now. No row when it is not Broken, or the probe's lease is not held.
-- name: EndGatewayBroken :one
UPDATE connections c
SET gateway_broken_since = NULL, gateway_broken_reason = NULL, gateway_next_probe_at = NULL,
    gateway_probe_lease_owner = NULL, gateway_probe_lease_until = NULL,
    gateway_recovered_at = @recovered_at::timestamptz
FROM (SELECT x.id, x.gateway_broken_since
      FROM connections x
      WHERE x.org_id = @org_id AND x.id = @id AND x.gateway_broken_since IS NOT NULL
        AND (sqlc.narg('owner')::text IS NULL
             OR (x.gateway_probe_lease_owner = sqlc.narg('owner')::text
                 AND x.gateway_probe_lease_until > @now::timestamptz))
      FOR NO KEY UPDATE OF x) AS old
WHERE c.org_id = @org_id AND c.id = old.id
RETURNING c.public_id, c.name, old.gateway_broken_since::timestamptz AS was_broken_since,
          (c.deleted_at IS NOT NULL)::boolean AS deleted;

-- ClaimGatewayProbes leases the Broken gateways that are not deleted whose probe is due and whose lease is free or ran
-- out, and moves their next probe to @next_probe, so that a probe that a replica dies in is made again after
-- delivery.broken_probe_interval. The choice is a materialized CTE, run once, as in ClaimDueCalls.
-- name: ClaimGatewayProbes :many
WITH due AS MATERIALIZED (
    SELECT x.id
    FROM connections x
    WHERE x.org_id = @org_id AND x.gateway_broken_since IS NOT NULL AND x.deleted_at IS NULL
      AND x.gateway_next_probe_at <= @due::timestamptz
      AND (x.gateway_probe_lease_until IS NULL OR x.gateway_probe_lease_until <= @now::timestamptz)
    ORDER BY x.gateway_next_probe_at, x.id
    LIMIT @lim
    FOR NO KEY UPDATE OF x SKIP LOCKED
)
UPDATE connections c
SET gateway_probe_lease_owner = @owner::text, gateway_probe_lease_until = @lease_until::timestamptz,
    gateway_next_probe_at = @next_probe::timestamptz
FROM due
WHERE c.org_id = @org_id AND c.id = due.id
RETURNING c.id, c.public_id, c.name;

-- FailGatewayProbe records the error of a failed probe as the reason of the Broken gateway and frees its lease, while
-- this replica still holds it at the real time @now; no row changes when the lease ran out or went to another
-- replica, or the state ended.
-- name: FailGatewayProbe :execrows
UPDATE connections
SET gateway_broken_reason = @reason::text, gateway_probe_lease_owner = NULL, gateway_probe_lease_until = NULL
WHERE org_id = @org_id AND id = @id AND gateway_broken_since IS NOT NULL AND gateway_probe_lease_owner = @owner::text
  AND gateway_probe_lease_until > @now::timestamptz;

-- NextGatewayProbe is when the next probe of a Broken gateway that is not deleted is due (business clock), and the
-- earliest end of a live lease of one (real clock), the zero time for none.
-- name: NextGatewayProbe :one
SELECT coalesce(min(gateway_next_probe_at)
                    FILTER (WHERE gateway_probe_lease_until IS NULL OR gateway_probe_lease_until <= @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS free_at,
       coalesce(min(gateway_probe_lease_until) FILTER (WHERE gateway_probe_lease_until > @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS lease_end
FROM connections
WHERE org_id = @org_id AND gateway_broken_since IS NOT NULL AND deleted_at IS NULL;

-- ListGatewayHealth lists the Notification gateways that are not deleted, and the Broken ones whose stored info no
-- longer declares the role gateway, each with whether it is Broken, for muster_notification_gateway_broken.
-- name: ListGatewayHealth :many
SELECT public_id, (gateway_broken_since IS NOT NULL)::boolean AS broken
FROM connections
WHERE org_id = @org_id AND type = 'bridge' AND deleted_at IS NULL
  AND (coalesce(bridge_info->'roles' @> '["gateway"]'::jsonb, false) OR gateway_broken_since IS NOT NULL)
ORDER BY id;
