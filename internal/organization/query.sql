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
