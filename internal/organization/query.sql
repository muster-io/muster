-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: GetOrganization :one
SELECT id, public_id, name, time_zone, severity_label, severity_mapping, severity_styles, critical_is_urgent,
       instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
       retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds
FROM organizations
ORDER BY id
LIMIT 1;

-- name: CreateOrganization :one
INSERT INTO organizations (
    public_id, name, time_zone, severity_label, severity_mapping, severity_styles, critical_is_urgent,
    instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
    retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds,
    created_at, updated_at
)
VALUES (
    @public_id, @name, @time_zone, @severity_label, @severity_mapping, @severity_styles, @critical_is_urgent,
    @instance_labels, @retention_stored_snapshots_days, @retention_alert_details_days,
    @retention_alert_group_summaries_days, @retention_audit_log_days, @totp_required, @oidc_token_grace_seconds,
    @created_at, @created_at
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

-- GetSettings reads the organization resource, with the state of its Secrets but not their values.
-- name: GetSettings :one
SELECT id, public_id, name, time_zone, severity_label, severity_mapping, severity_styles, critical_is_urgent,
       instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
       retention_alert_group_summaries_days, retention_audit_log_days, totp_required, oidc_token_grace_seconds,
       (outgoing_heartbeat_url_ciphertext IS NOT NULL)::boolean AS outgoing_heartbeat_url_set,
       outgoing_heartbeat_url_updated_at, outgoing_heartbeat_proxy,
       (outgoing_heartbeat_proxy_password_ciphertext IS NOT NULL)::boolean AS outgoing_heartbeat_proxy_password_set,
       outgoing_heartbeat_proxy_password_updated_at, version
FROM organizations
WHERE id = @org_id;

-- LockSettings locks the organization row until the transaction ends and returns its version.
-- name: LockSettings :one
SELECT version
FROM organizations
WHERE id = @org_id
FOR UPDATE;

-- SetTOTPPolicy changes the TOTP policy and moves the version of the resource.
-- name: SetTOTPPolicy :execrows
UPDATE organizations
SET totp_required = @totp_required, updated_at = @now, version = version + 1
WHERE id = @org_id;
