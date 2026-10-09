-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Outgoing webhooks (C-15): the Signing secrets and the named Secrets of an outgoing webhook Destination, what an
-- events-mode request needs of its Destination, and what the version 1 body reads of an Alert Group. Secret values are
-- read only to send a request; the API reads their status.

-- GetWebhookDestination reads a Destination that is not deleted, of any type, with the status of its Signing secrets.
-- name: GetWebhookDestination :one
SELECT id, public_id, name, type, version, (signing_secret_ciphertext IS NOT NULL)::boolean AS signing_secret_set,
       signing_secret_updated_at, previous_signing_secret_since
FROM destinations
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL;

-- LockWebhookDestination locks a Destination that is not deleted, of any type, for a change of its secrets, with its
-- Signing secrets.
-- name: LockWebhookDestination :one
SELECT id, public_id, name, type, version, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at,
       previous_signing_secret_since
FROM destinations
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- BumpDestinationVersion gives a Destination whose Secrets changed its next version, the ETag of its Secrets.
-- name: BumpDestinationVersion :one
UPDATE destinations
SET version = version + 1, updated_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id
RETURNING version;

-- ListSecrets lists the names of the Secrets of a Destination with the time of their last change, by name.
-- name: ListSecrets :many
SELECT name, value_updated_at
FROM destination_secrets
WHERE org_id = @org_id AND destination_id = @destination_id
ORDER BY name;

-- UpsertSecret creates or replaces a Secret of a Destination.
-- name: UpsertSecret :exec
INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id, value_updated_at)
VALUES (@destination_id, @org_id, @name, @value_ciphertext, @value_key_id, @now::timestamptz)
ON CONFLICT (destination_id, name)
DO UPDATE SET value_ciphertext = excluded.value_ciphertext, value_key_id = excluded.value_key_id,
              value_updated_at = excluded.value_updated_at;

-- DeleteSecret deletes a Secret of a Destination; no row when it does not exist.
-- name: DeleteSecret :execrows
DELETE FROM destination_secrets
WHERE org_id = @org_id AND destination_id = @destination_id AND name = @name;

-- RotateSigningSecret makes the current Signing secret the previous one, which still signs since @now — none when it
-- had none — and stores the new one, and gives the Destination its next version.
-- name: RotateSigningSecret :one
UPDATE destinations
SET previous_signing_secret_ciphertext = sqlc.narg('previous_ciphertext')::bytea,
    previous_signing_secret_key_id = sqlc.narg('previous_key_id')::text,
    previous_signing_secret_since = CASE WHEN sqlc.narg('previous_ciphertext')::bytea IS NULL THEN NULL
                                         ELSE @now::timestamptz END,
    signing_secret_ciphertext = @ciphertext,
    signing_secret_key_id = @key_id, signing_secret_updated_at = @now::timestamptz, updated_at = @now::timestamptz,
    version = version + 1
WHERE org_id = @org_id AND id = @id
RETURNING version;

-- RetirePreviousSigningSecret wipes the previous Signing secret, which signs no more. No row when there is none.
-- name: RetirePreviousSigningSecret :one
UPDATE destinations
SET previous_signing_secret_ciphertext = NULL, previous_signing_secret_key_id = NULL,
    previous_signing_secret_since = NULL, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND previous_signing_secret_ciphertext IS NOT NULL
RETURNING version;

-- GetTarget reads what a request of an outgoing webhook needs, in either mode, deleted or not: a call in flight when it
-- was deleted still signs with its secrets, which delivery wipes once no call of it holds a lease.
-- name: GetTarget :one
SELECT id, public_id, webhook_mode, webhook_events_config, webhook_template_config, proxy, proxy_password_ciphertext,
       proxy_password_key_id,
       signing_secret_ciphertext, signing_secret_key_id, previous_signing_secret_ciphertext,
       previous_signing_secret_key_id
FROM destinations
WHERE org_id = @org_id AND id = @id AND type = 'webhook';

-- GetTemplateConfig reads the request templates of an outgoing webhook, deleted or not, through the transaction of a
-- change, to render the Desired state of its template mode; null in the events mode.
-- name: GetTemplateConfig :one
SELECT webhook_template_config
FROM destinations
WHERE org_id = @org_id AND id = @id AND type = 'webhook';

-- ListSecretValues reads the encrypted values of the Secrets of a Destination, by name.
-- name: ListSecretValues :many
SELECT name, value_ciphertext, value_key_id
FROM destination_secrets
WHERE org_id = @org_id AND destination_id = @destination_id
ORDER BY name;

-- GetBodyUser reads a User as the body of an event names them.
-- name: GetBodyUser :one
SELECT public_id, name, login
FROM users
WHERE org_id = @org_id AND id = @id;

-- GetBodyServiceAccount reads a Service account as the body of an event names it.
-- name: GetBodyServiceAccount :one
SELECT public_id, name
FROM service_accounts
WHERE org_id = @org_id AND id = @id;

-- ListBodyAlerts lists the Alerts of an Alert Group for the body of an event, one per fingerprint with its latest
-- membership, Alerts that moved to another Alert Group left out, by fingerprint.
-- name: ListBodyAlerts :many
SELECT DISTINCT ON (a.fingerprint) a.fingerprint, a.labels, m.annotations, m.starts_at, m.ended_at, m.resolve_reason,
       a.generator_url, m.state
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id AND m.state IN ('firing', 'resolved')
ORDER BY a.fingerprint, m.id DESC;
