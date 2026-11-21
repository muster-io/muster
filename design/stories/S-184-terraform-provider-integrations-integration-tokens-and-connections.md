---
id: S-184
title: "The Terraform resources of Integrations, Integration tokens and the Connections of each type, and the data source muster_connection (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-182]
covers: [C-33.FR-5, C-33.FR-6, C-33.FR-7, C-33.FR-8, C-33.FR-9, C-33.FR-10, C-33.FR-13, C-33.FR-17, C-33.FR-19, C-33.AC-5, C-33.AC-6, C-33.AC-7, C-33.AC-8, C-33.AC-9, C-33.AC-13]
files_touched:
  - internal/provider/provider.go
  - internal/provider/resource_integration.go
  - internal/provider/resource_integration_test.go
  - internal/provider/resource_integration_token.go
  - internal/provider/resource_integration_token_test.go
  - internal/provider/connection_common.go
  - internal/provider/resource_mattermost_connection.go
  - internal/provider/resource_mattermost_connection_test.go
  - internal/provider/resource_telegram_connection.go
  - internal/provider/resource_telegram_connection_test.go
  - internal/provider/resource_bridge_connection.go
  - internal/provider/resource_bridge_connection_test.go
  - internal/provider/resource_slack_connection.go
  - internal/provider/resource_slack_connection_test.go
  - internal/provider/data_source_connection.go
  - internal/provider/data_source_connection_test.go
  - examples/resources/muster_integration/resource.tf
  - examples/resources/muster_integration/import.sh
  - examples/resources/muster_integration_token/resource.tf
  - examples/resources/muster_mattermost_connection/resource.tf
  - examples/resources/muster_mattermost_connection/import.sh
  - examples/resources/muster_telegram_connection/resource.tf
  - examples/resources/muster_telegram_connection/import.sh
  - examples/resources/muster_bridge_connection/resource.tf
  - examples/resources/muster_bridge_connection/import.sh
  - examples/resources/muster_slack_connection/resource.tf
  - examples/resources/muster_slack_connection/import.sh
  - examples/data-sources/muster_connection/data-source.tf
acceptance:
  - "[C-33.FR-5, C-33.FR-13, C-33.FR-19] `muster_integration` creates, updates and deletes an Integration with its name, description, Static labels, duplicate window and Heartbeat settings, each write with `managed_by` `terraform`; a second plan shows no change; `ingest_url` is computed; the acceptance tests of this story pass on the four CLI versions of `acceptance.yml` (`TestAccIntegration_basic`)."
  - "[C-33.FR-10, C-33.AC-9] `muster_integration_token` gives `value` and `alertmanager_snippet`, sensitive, to another resource after its create; a change of `name` replaces it; after an import both are null and the plan shows no change; destroying it revokes the token, and an ingestion request with its value then answers `401` (`TestAccIntegrationToken_basic`, `_import`)."
  - "[C-33.FR-8, C-33.AC-6] With a saved plan that renames an Integration, a change of the Integration through the API before `terraform apply` makes the apply fail with \"Integration \\\"…\\\" changed in Muster since Terraform last read it.\" and change nothing; a new plan and apply succeed (`TestAccIntegration_concurrentChange`)."
  - "[C-33.FR-7, C-33.AC-5] A Static label added through the API shows in the next plan and is removed by the apply; `warnings`, `snapshot_count`, `open_alert_group_count` and `last_snapshot_at` are not attributes (`TestAccIntegration_drift`)."
  - "[C-33.FR-5, C-33.FR-10, C-33.AC-8] `muster_mattermost_connection` on the second fake Mattermost server with `bot_token` keeps it in the state, hidden in the plan; with `bot_token_wo` and its version, the state file does not contain the token, raising the version records `secret_changed` in the Audit log and the Connection check passes with the new token, and an apply without raising it sends nothing; setting both fails validation; on Terraform 1.10 and OpenTofu 1.10 `bot_token_wo` fails validation naming 1.11; with `bot_token`, a token cleared through the API is set again by the next apply (`TestAccMattermostConnection_*`)."
  - "[C-33.FR-5, C-33.FR-10] `muster_telegram_connection`, `muster_bridge_connection` and `muster_slack_connection` create, update, import and delete Connections on the fake Telegram server, the fake bridge and the fake Slack server with the published second bots of `muster dev`, each Secret — `bot_token`, `push_secret`, `app_token`, `slack_signing_secret`, the proxy `password` — in both ways (`TestAccTelegramConnection_*`, `TestAccBridgeConnection_*`, `TestAccSlackConnection_*`)."
  - "[C-33.FR-6] `data.muster_connection` finds the demo Connection \"Example Gateway\" by `name` and by `id`, answering its `id`, `type` and the bridge roles; a name that matches nothing is an error that names it (`TestAccDataSourceConnection`)."
  - "[C-33.FR-9, C-33.AC-7] Each resource of this story imported by `public_id` plans without changes but the Secrets that the configuration holds (`TestAcc*_import`)."
  - "[C-33.FR-17, C-33.AC-13] Deleting through the API the Integration of a `muster_integration` makes the next plan create it again; a Connection used by a Destination made through the API refuses `terraform destroy` with a diagnostic carrying the code `in_use` (`TestAccMattermostConnection_inUse`)."
verify: "make ci testacc"
operator_attention: false
issue: null
---

# S-184. The Terraform resources of Integrations, Integration tokens and the Connections of each type, and the data source muster_connection (BE)

## Scope

**IN**

- `muster_integration` and `muster_integration_token`.
- One resource per Connection type — `muster_mattermost_connection`, `muster_telegram_connection`,
  `muster_bridge_connection`, `muster_slack_connection` — sharing the name, limiter and proxy attributes.
- `data.muster_connection` of any type.
- The `412` acceptance test of C-33.FR-8, on Integrations.

**OUT**

- Destinations, Lookup tables and Link rules (S-185); Teams and their Team mentions (S-186).
- The checks of Connections (`checkConnection`, `checkConnectionDraft`), the channel and group lists: operations, not
  configuration.

## Contracts

- **`muster_integration`** (Integration operations): `name`, `description`, `connection_mode`, `static_labels`,
  `duplicate_window_seconds`, `heartbeat` (`enabled`, `timeout_seconds`); computed `id`, `ingest_url`, `etag`. The
  built-in "Muster" Integration is refused at import with a diagnostic that it cannot be managed (C-33.FR-14).
- **`muster_integration_token`** (`createIntegrationToken`, `listIntegrationTokens`, `revokeIntegrationToken`):
  `integration_id`, `name` (both force a replacement); computed `id`, `created_at`, and the sensitive `value`,
  `alertmanager_snippet` and `heartbeat_snippet`, set only by the create (C-33.FR-10). Read finds the token in
  `listIntegrationTokens` and removes the resource when it is gone. Import by `<integration id>/<token id>`; it has no
  `etag`, since tokens are not updated.
- **Connections** (`createConnection`, `getConnection`, `updateConnection`, `deleteConnection`): the fields of each
  type's input schema — Mattermost `server_url`; Telegram `bot_api_base_url`, `update_mode`; Bridge `base_url`,
  `event_delivery`; Slack `event_mode` — with `name`, `limiter` and `proxy` from `connection_common.go`; `type` is set
  by the resource. Secrets as pairs: `bot_token` (every type), `push_secret` (Bridge), `app_token` and
  `slack_signing_secret` (Slack), `proxy.password`. Computed `id`, `etag`; not `health`, `info`, the check results, the
  callback addresses or the Broken state. A type change is impossible: each resource reads only its type, and an
  import of another type's id fails naming it.
- **`data.muster_connection`**: `name` or `id`, exactly one; answers `id`, `name`, `type`, `roles` (Bridge `info`
  roles, empty for other types).
- **Tests**: against the fakes of `muster dev` — the second Mattermost server `127.0.0.1:18066`, the fake Telegram
  server, the fake bridge and the fake Slack server — with the published second bots (`muster-dev-mattermost-token-b`,
  `234567:dev-telegram-token-b`, `muster-dev-bridge-token-b`, `xoxb-muster-dev-slack-token-b` with its app-level token
  and signing secret), for which `muster dev` creates no Connection (AGENTS.md).

## Steps

1. Write `muster_integration` with its acceptance tests, the `412` test among them. Check: `make testacc
   TESTARGS='-run TestAccIntegration'`.
2. Write `muster_integration_token`. Check: its tests, ingestion with the value.
3. Write `connection_common.go` and the four Connection resources with their Secret tests. Check: their tests on the
   four CLI versions.
4. Write the data source, the examples and the documentation. Check: `make docs-check`, `make ci testacc`.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` and the provider installed as in S-182:

```sh
cat > main.tf <<'EOF'
resource "muster_integration" "am" { name = "alertmanager-prod" }
resource "muster_integration_token" "am" {
  integration_id = muster_integration.am.id
  name           = "prod"
}
resource "muster_mattermost_connection" "mm2" {
  name                 = "Second Mattermost"
  server_url           = "http://127.0.0.1:18066"
  bot_token_wo         = "muster-dev-mattermost-token-b"
  bot_token_wo_version = 1
}
data "muster_connection" "gw" { name = "Example Gateway" }
output "token" {
  value     = muster_integration_token.am.value
  sensitive = true
}
output "integration_id" { value = muster_integration.am.id }
output "gw_roles" { value = data.muster_connection.gw.roles }
EOF
terraform apply -auto-approve
terraform output gw_roles                                            # ["gateway"]
grep -c muster-dev-mattermost-token-b terraform.tfstate               # 0
TOK=$(terraform output -raw token); INGEST=http://127.0.0.1:8081/api/v1/ingest
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" -X POST -d '{}' $INGEST         # 202
A=(-H "Authorization: Bearer $MUSTER_TOKEN" -H 'Content-Type: application/json'); API=$MUSTER_ENDPOINT/api/v1
ID=$(terraform output -raw integration_id)
curl -s "${A[@]}" $API/integrations/$ID | jq -r .managed_by                                                 # terraform

# 412: a saved plan, a change through the API, then the saved plan
sed -i.bak 's/"alertmanager-prod"/"alertmanager-production"/' main.tf && terraform plan -out=p.plan > /dev/null
E=$(curl -s "${A[@]}" $API/integrations/$ID | jq -r .etag)
curl -s -o /dev/null "${A[@]}" -H "If-Match: $E" -X PUT \
  -d "$(curl -s "${A[@]}" $API/integrations/$ID | jq -c '{name, description: "changed by hand"}')" $API/integrations/$ID
terraform apply p.plan 2>&1 | grep -o 'changed in Muster since Terraform last read it'
# changed in Muster since Terraform last read it
curl -s "${A[@]}" $API/integrations/$ID | jq -r .name                                                       # alertmanager-prod
terraform apply -auto-approve > /dev/null && curl -s "${A[@]}" $API/integrations/$ID | jq -c '{name, description}'
# {"name":"alertmanager-production","description":""}

terraform destroy -auto-approve > /dev/null
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" -X POST -d '{}' $INGEST         # 401
```

`TestAccIntegration_concurrentChange` runs the same `412` path in the acceptance suite; the pull request carries its
output and that of the suite on the four CLI versions.

## Open questions

None.

## Notes

- Suggested commit: `feat: manage integrations, integration tokens and connections`.
- One resource per Connection type, not one with a block per type: each type has required fields and Secrets of its
  own, and a schema per type lets Terraform check them before any request; the data source covers every type, because a
  reference needs only the id.
- An Integration token's value is the one thing a pipeline needs from Muster for Alertmanager; with
  `muster_integration_token`, the same apply that creates the Integration can write Alertmanager's configuration.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-5 | partial | these resources |
| C-33.FR-6 | partial | `muster_connection`; `muster_user` and `muster_team` in S-186 |
| C-33.FR-7 | partial | these resources |
| C-33.FR-8 | partial | the `412` acceptance test |
| C-33.FR-9 | partial | these resources |
| C-33.FR-10 | partial | the Secrets of Connections and the Integration token's value |
| C-33.FR-13 | partial | these resources |
| C-33.FR-17 | partial | `in_use` and an object deleted in Muster |
| C-33.FR-19 | partial | these resources' acceptance tests |
| C-33.AC-5 | partial | Integrations |
| C-33.AC-6 | full | |
| C-33.AC-7 | partial | these resources |
| C-33.AC-8 | full | |
| C-33.AC-9 | full | |
| C-33.AC-13 | partial | the refusal `in_use`; the server's bound in S-187 |
