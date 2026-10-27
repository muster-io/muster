-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: GetOrganization :one
SELECT id, public_id, name, time_zone, workday_start, week_start, severity_label, severity_mapping, severity_styles,
       instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
       retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds,
       working_days, workday_end
FROM organizations
ORDER BY id
LIMIT 1;

-- name: CreateOrganization :one
INSERT INTO organizations (
    public_id, name, time_zone, workday_start, week_start, severity_label, severity_mapping, severity_styles,
    critical_is_urgent, instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
    retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds,
    working_days, workday_end, created_at, updated_at
)
VALUES (
    @public_id, @name, @time_zone, @workday_start, @week_start, @severity_label, @severity_mapping, @severity_styles,
    @critical_is_urgent, @instance_labels, @retention_stored_snapshots_days, @retention_alert_details_days,
    @retention_alert_group_summaries_days, @retention_audit_log_days, @totp_required, @oidc_token_grace_seconds,
    @working_days::text[], @workday_end, @created_at, @created_at
)
RETURNING id;

-- CreateOutboundPolicy writes the Organization's outbound address policy; it changes nothing when one exists.
-- name: CreateOutboundPolicy :execrows
INSERT INTO outbound_policies (org_id, policy, allowed, denied, updated_at)
VALUES (@org_id, @policy, @allowed, @denied, @updated_at)
ON CONFLICT (org_id) DO NOTHING;

-- name: GetOutboundPolicy :one
SELECT policy, allowed, denied
FROM outbound_policies
WHERE org_id = @org_id;

-- GetOutboundPolicySettings reads the outbound address policy as the API shows it, with its version, the ETag.
-- name: GetOutboundPolicySettings :one
SELECT policy, allowed, denied, version
FROM outbound_policies
WHERE org_id = @org_id;

-- LockOutboundPolicy locks the outbound address policy until the transaction ends and returns its version.
-- name: LockOutboundPolicy :one
SELECT version
FROM outbound_policies
WHERE org_id = @org_id
FOR UPDATE;

-- UpdateOutboundPolicy replaces the outbound address policy and its lists and moves its version.
-- name: UpdateOutboundPolicy :execrows
UPDATE outbound_policies
SET policy = @policy, allowed = @allowed, denied = @denied, version = version + 1, updated_at = @updated_at
WHERE org_id = @org_id;

-- ListSeverityValues counts the Alerts last seen at @since or later by their value of the label @label, the most
-- frequent first; Alerts without the label or with an empty value are left out. At most @max_values values.
-- name: ListSeverityValues :many
SELECT (labels ->> @label::text)::text AS value, count(*)::bigint AS alerts
FROM alerts
WHERE org_id = @org_id AND last_seen_at >= @since::timestamptz AND labels ->> @label::text <> ''
GROUP BY 1
ORDER BY alerts DESC, value
LIMIT @max_values;

-- GetSettings reads the organization resource, with the state of its Secrets but not their values. critical_is_urgent
-- is not read: it is true from 0.2.0 (C-26.FR-10).
-- name: GetSettings :one
SELECT id, public_id, name, time_zone, workday_start, week_start, severity_label, severity_mapping, severity_styles,
       instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
       retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds,
       (outgoing_heartbeat_url_ciphertext IS NOT NULL)::boolean AS outgoing_heartbeat_url_set,
       outgoing_heartbeat_url_updated_at, outgoing_heartbeat_proxy,
       (outgoing_heartbeat_proxy_password_ciphertext IS NOT NULL)::boolean AS outgoing_heartbeat_proxy_password_set,
       outgoing_heartbeat_proxy_password_updated_at, version, working_days, workday_end, buttons
FROM organizations
WHERE id = @org_id;

-- LockSettings locks the organization row until the transaction ends and returns its version.
-- name: LockSettings :one
SELECT version
FROM organizations
WHERE id = @org_id
FOR UPDATE;

-- GetOutgoingHeartbeatSecrets reads the stored Secrets of the outgoing heartbeat, for an update that keeps them.
-- name: GetOutgoingHeartbeatSecrets :one
SELECT outgoing_heartbeat_url_ciphertext, outgoing_heartbeat_url_key_id, outgoing_heartbeat_url_updated_at,
       outgoing_heartbeat_proxy_password_ciphertext, outgoing_heartbeat_proxy_password_key_id,
       outgoing_heartbeat_proxy_password_updated_at
FROM organizations
WHERE id = @org_id;

-- UpdateSettings writes the editable fields of the organization resource and moves its version. A new or cleared
-- URL of the outgoing heartbeat starts without a result, so that the last result never belongs to another URL. A
-- null button set keeps the stored one.
-- name: UpdateSettings :execrows
UPDATE organizations
SET name                                         = @name,
    time_zone                                    = @time_zone,
    workday_start                                = @workday_start,
    week_start                                   = @week_start,
    working_days                                 = @working_days::text[],
    workday_end                                  = @workday_end,
    severity_label                               = @severity_label,
    severity_mapping                             = @severity_mapping,
    severity_styles                              = @severity_styles,
    instance_labels                              = @instance_labels,
    retention_stored_snapshots_days              = @retention_stored_snapshots_days,
    retention_alert_details_days                 = @retention_alert_details_days,
    retention_alert_group_summaries_days         = @retention_alert_group_summaries_days,
    retention_audit_log_days                     = @retention_audit_log_days,
    oidc_token_grace_seconds                     = @oidc_token_grace_seconds,
    totp_required                                = @totp_required,
    outgoing_heartbeat_url_ciphertext            = @url_ciphertext,
    outgoing_heartbeat_url_key_id                = @url_key_id,
    outgoing_heartbeat_url_updated_at            = @url_updated_at,
    outgoing_heartbeat_proxy                     = @proxy,
    outgoing_heartbeat_proxy_password_ciphertext = @proxy_password_ciphertext,
    outgoing_heartbeat_proxy_password_key_id     = @proxy_password_key_id,
    outgoing_heartbeat_proxy_password_updated_at = @proxy_password_updated_at,
    buttons                                      = coalesce(sqlc.narg('buttons')::jsonb, buttons),
    outgoing_heartbeat_last_attempt_at = CASE WHEN @url_changed::boolean THEN NULL
                                              ELSE outgoing_heartbeat_last_attempt_at END,
    outgoing_heartbeat_last_ok         = CASE WHEN @url_changed::boolean THEN NULL ELSE outgoing_heartbeat_last_ok END,
    outgoing_heartbeat_last_error      = CASE WHEN @url_changed::boolean THEN NULL
                                              ELSE outgoing_heartbeat_last_error END,
    updated_at = @now,
    version = version + 1
WHERE id = @org_id;

-- ClaimOutgoingHeartbeat takes the send of the outgoing heartbeat that is due: with a URL set and no attempt since
-- @due_before, it records the attempt at @now and returns what the send needs. A second claim in the same interval
-- returns no row, so that two Leaders at once send once. A last attempt after @now, left by a clock that moved
-- backward, is due too, so that the heartbeat does not stop until the clock catches up. It is runtime state: the
-- version and updated_at of the resource stay.
-- name: ClaimOutgoingHeartbeat :one
UPDATE organizations
SET outgoing_heartbeat_last_attempt_at = @now::timestamptz
WHERE id = @org_id
  AND outgoing_heartbeat_url_ciphertext IS NOT NULL
  AND (outgoing_heartbeat_last_attempt_at IS NULL OR outgoing_heartbeat_last_attempt_at <= @due_before::timestamptz
       OR outgoing_heartbeat_last_attempt_at > @now::timestamptz)
RETURNING outgoing_heartbeat_url_ciphertext, outgoing_heartbeat_url_key_id, outgoing_heartbeat_url_updated_at,
          outgoing_heartbeat_proxy, outgoing_heartbeat_proxy_password_ciphertext,
          outgoing_heartbeat_proxy_password_key_id, outgoing_heartbeat_proxy_password_updated_at;

-- RecordOutgoingHeartbeat stores the result of the attempt claimed at @attempt_at and returns the result before it;
-- the result of an attempt that a newer claim or a changed URL replaced is not stored and returns no row. It leaves
-- the version and updated_at of the resource.
-- name: RecordOutgoingHeartbeat :one
UPDATE organizations o
SET outgoing_heartbeat_last_ok = @ok::boolean, outgoing_heartbeat_last_error = sqlc.narg(last_error)
FROM (
    SELECT x.id, x.outgoing_heartbeat_last_ok AS previous
    FROM organizations x
    WHERE x.id = @org_id
    FOR UPDATE
) p
WHERE o.id = p.id AND o.outgoing_heartbeat_last_attempt_at = @attempt_at::timestamptz
RETURNING p.previous;

-- InsertOrganizationButtonsUpdate requests that a change of the Organization's button set reach the open Root messages
-- of the Destinations that follow it (C-28.FR-9): the open_group_updates worker of any replica claims the row.
-- name: InsertOrganizationButtonsUpdate :exec
INSERT INTO open_group_updates (org_id, kind, requested_at)
VALUES (@org_id, @kind, @requested_at::timestamptz);

-- NotifyOpenGroupUpdates wakes the open Alert Group update workers of every replica once the transaction commits.
-- name: NotifyOpenGroupUpdates :exec
SELECT pg_notify(@channel::text, '');
