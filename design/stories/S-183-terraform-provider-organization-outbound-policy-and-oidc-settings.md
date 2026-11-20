---
id: S-183
title: "The Terraform resources of the objects that exist once: the Organization's settings with the default Notification rules, the Outbound address policy and the OIDC settings with the Role mapping (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-182]
covers: [C-33.FR-5, C-33.FR-7, C-33.FR-9, C-33.FR-10, C-33.FR-13, C-33.FR-14, C-33.AC-2, C-33.AC-5, C-33.AC-7, C-33.AC-12]
files_touched:
  - internal/provider/provider.go
  - internal/provider/singleton.go
  - internal/provider/singleton_test.go
  - internal/provider/resource_organization.go
  - internal/provider/resource_organization_test.go
  - internal/provider/resource_outbound_policy.go
  - internal/provider/resource_outbound_policy_test.go
  - internal/provider/resource_oidc_settings.go
  - internal/provider/resource_oidc_settings_test.go
  - examples/resources/muster_organization/resource.tf
  - examples/resources/muster_organization/import.sh
  - examples/resources/muster_outbound_policy/resource.tf
  - examples/resources/muster_outbound_policy/import.sh
  - examples/resources/muster_oidc_settings/resource.tf
  - examples/resources/muster_oidc_settings/import.sh
acceptance:
  - "[C-33.FR-13, C-33.FR-14, C-33.AC-12] `muster_organization` that sets only `name` and `notification_rules` creates by adopting the Organization: every other setting keeps its value and is read into the state, a second plan shows no change, and `getOrganization` answers the new name, the rules and `managed_by` `terraform`; a User without rules of their own gets the default rules, and a User's own rules are untouched (`TestAccOrganization_adopt`)."
  - "[C-33.FR-14, C-33.AC-2] Destroying `muster_organization`, `muster_outbound_policy` and `muster_oidc_settings` changes none of their settings, clears their `managed_by` and warns that each object stays in Muster (`TestAccOrganization_destroy`, `TestAccOutboundPolicy_basic`, `TestAccOidcSettings_basic`)."
  - "[C-33.FR-7, C-33.AC-5] A change of the Organization's `time_zone` and of a Role mapping entry made through the API shows in the next plan and is undone by the apply; `critical_is_urgent`, `working_hours`, `id` and the Outbound address policy's `always_blocked` are not attributes (`TestAccOrganization_drift`, `TestAccOidcSettings_drift`)."
  - "[C-33.FR-10] `muster_oidc_settings` with `client_secret` keeps it in the state, hidden in the plan; with `client_secret_wo` and `client_secret_wo_version` = 1 the state file does not contain the secret, the Audit log shows `secret_changed` for `/client_secret` once, an apply with the same version records no change, and version 2 records one more; setting both fails validation; with `client_secret`, a secret cleared through the API is set again by the next apply. The URL of the Outgoing heartbeat and the proxy password of the OIDC settings behave the same (`TestAccOidcSettings_secret*`, `TestAccOrganization_heartbeatURL`)."
  - "[C-33.FR-10] On Terraform 1.10 and on OpenTofu 1.10, a configuration with `client_secret_wo` fails validation with an error that names Terraform 1.11 and OpenTofu 1.11, and the same configuration with `client_secret` applies (`TestAccOidcSettings_oldCLI`, run by the job that installs those versions)."
  - "[C-33.FR-9, C-33.AC-7] Each of the three resources imported with any id plans without changes but the Secrets that the configuration holds (`TestAcc*_import`)."
  - "[C-33.FR-5] The OIDC settings take the Role mapping as `group_mappings`, each entry with its `claim`, `group` and `role`, in order; an unknown Role is refused by the server with a diagnostic at that entry's `role` (`TestAccOidcSettings_mapping`)."
verify: "make ci testacc"
operator_attention: false
issue: null
---

# S-183. The Terraform resources of the objects that exist once: the Organization's settings with the default Notification rules, the Outbound address policy and the OIDC settings with the Role mapping (BE)

## Scope

**IN**

- `muster_organization`, `muster_outbound_policy` and `muster_oidc_settings`, with the adopt-and-forget lifecycle of
  C-33.FR-14 in a shared `singleton.go` that `muster_default_route` (S-187) reuses.
- The first Secrets end to end: the OIDC client secret, the OIDC proxy password and the URL of the Outgoing heartbeat,
  in both ways of C-33.FR-10.

**OUT**

- The other resources (S-184 to S-187); the guides (S-188).

## Contracts

- **`muster_organization`** (`getOrganization`, `updateOrganization`): every writable field of `OrganizationInput`
  but the deprecated `critical_is_urgent` — `name`, `time_zone`, `workday_start`, `workday_end`, `week_start`,
  `working_days`, `buttons`, `team_label`, `severity_label`, `severity_mapping`, `severity_styles`, `instance_labels`,
  `retention`, `totp_required`, `oidc_token_grace_seconds`, `notification_rules` (`normal`, `important`: the default
  Notification rules of C-31) and `outgoing_heartbeat` (`url` as a Secret pair, `proxy`) — each optional and computed,
  so that what the configuration leaves out keeps the server's value. Computed: `etag`. Not attributes: `id`,
  `working_hours` (derived), the read-only parts of the read.
- **`muster_outbound_policy`** (`getOutboundPolicy`, `updateOutboundPolicy`): `policy`, `allowed`, `denied`; computed
  `etag`; not `always_blocked`.
- **`muster_oidc_settings`** (`getOidcSettings`, `updateOidcSettings`): `enabled`, `display_name`, `issuer_url`,
  `client_id`, `client_secret` / `client_secret_wo` / `client_secret_wo_version`, `client_secret_expires_on`, `scopes`,
  `groups_claim`, `group_mappings` (the Role mapping: a list of `claim`, `group`, `role`), `unmatched_role`,
  `sync_role`, `skip_totp_with_idp_mfa`, `proxy` with its `password` pair; computed `etag`; not `warnings`,
  `claims_supported`, `client_secret_status`. The identity provider entries of Teams are `muster_team`'s (S-186).
- **Lifecycle** (`singleton.go`): Create reads the object, writes the configured attributes with its ETag and
  `managed_by` `terraform`, and stores the answer; Read, Update as any resource (`resourcekit`); Delete sends only
  `managed_by` null with the stored ETag and adds the warning "{Kind} stays in Muster; Terraform no longer manages
  it."; Import takes any id.
- **Secrets**: the pairs of `resourcekit` (S-182); a sensitive Secret is sent on create and when its value changes,
  and when the read's `*_status.set` is false while the state holds a value; a write-only one when its version changes
  or on create.

## Steps

1. Write `singleton.go` with its unit test against a test server. Check: `singleton_test.go`.
2. Write `muster_organization` with its acceptance tests. Check: `make testacc TESTARGS='-run TestAccOrganization'`.
3. Write `muster_outbound_policy` and `muster_oidc_settings`, with the Secret tests on both CLI families. Check:
   `make testacc`, the job of the old CLIs.
4. Add the examples and regenerate the documentation. Check: `make docs-check`, `make ci`.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` and the provider installed with `dev_overrides` as in
S-182:

```sh
cat > main.tf <<'EOF'
resource "muster_organization" "this" {
  name = "Example Org"
  notification_rules = {
    normal    = [{ kind = "mention" }]
    important = [{ kind = "mention" }, { kind = "wait", wait_seconds = 300 }, { kind = "mention" }]
  }
}
variable "oidc_secret" {
  type      = string
  ephemeral = true
}
resource "muster_oidc_settings" "this" {
  enabled                 = false
  issuer_url              = "http://127.0.0.1:18090"
  client_id               = "muster"
  client_secret_wo        = var.oidc_secret
  client_secret_wo_version = 1
  group_mappings          = [{ claim = "groups", group = "sre", role = "admin" }]
}
EOF
TF_VAR_oidc_secret=s1 terraform apply -auto-approve && terraform plan -detailed-exitcode; echo "exit=$?"     # exit=0
curl -s -H "Authorization: Bearer $MUSTER_TOKEN" $MUSTER_ENDPOINT/api/v1/organization |
  jq -c '{name, managed_by, tz: .time_zone}'      # {"name":"Example Org","managed_by":"terraform","tz":"UTC"}
grep -c s1 terraform.tfstate                                                                                 # 0
curl -s -H "Authorization: Bearer $MUSTER_TOKEN" "$MUSTER_ENDPOINT/api/v1/audit-log?limit=5" |
  jq -c '[.items[].diff[]? | select(.pointer=="/client_secret") | .secret_changed]'                          # [true]
terraform destroy -auto-approve 2>&1 | grep -c 'stays in Muster'                                            # 2
curl -s -H "Authorization: Bearer $MUSTER_TOKEN" $MUSTER_ENDPOINT/api/v1/organization | jq -c '{name, managed_by}'
# {"name":"Example Org","managed_by":null}
```

The actions of `notification_rules` are those of `NotificationRuleAction` in the specification. The pull request's
evidence table carries this output and the acceptance run on the four CLI versions and in `old-cli`.

## Open questions

None.

## Notes

- Suggested commit: `feat: manage the organization settings, outbound policy and OIDC settings`.
- The Organization's settings are one resource, though the web UI shows them on several pages: they share one ETag,
  and two resources writing one object would refuse each other's applies with `412`.
- The Role mapping is `group_mappings`, the API's name; a Team's identity provider entries stay on the Team.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-5 | partial | three resources; the others in S-184 to S-187 |
| C-33.FR-7 | partial | these resources |
| C-33.FR-9 | partial | these resources |
| C-33.FR-10 | partial | the OIDC client secret, proxy password and heartbeat URL; the Connections' in S-184, the Destinations' in S-185 |
| C-33.FR-13 | partial | these resources |
| C-33.FR-14 | partial | the three settings objects; the Default route and "Explore" in S-187 and S-185 |
| C-33.AC-2 | partial | the settings objects; the whole example in S-187 |
| C-33.AC-5 | partial | these resources; Routes in S-187 |
| C-33.AC-7 | partial | these resources |
| C-33.AC-12 | full | |
