---
id: S-185
title: "The Terraform resources of the Destinations of each type with the Secrets of outgoing webhooks, Lookup tables and Link rules (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-184]
covers: [C-33.FR-5, C-33.FR-7, C-33.FR-9, C-33.FR-10, C-33.FR-13, C-33.FR-14, C-33.FR-19, C-33.AC-7]
files_touched:
  - internal/provider/provider.go
  - internal/provider/destination_common.go
  - internal/provider/destination_common_test.go
  - internal/provider/resource_mattermost_destination.go
  - internal/provider/resource_mattermost_destination_test.go
  - internal/provider/resource_telegram_destination.go
  - internal/provider/resource_telegram_destination_test.go
  - internal/provider/resource_bridge_destination.go
  - internal/provider/resource_bridge_destination_test.go
  - internal/provider/resource_slack_destination.go
  - internal/provider/resource_slack_destination_test.go
  - internal/provider/resource_webhook_destination.go
  - internal/provider/resource_webhook_destination_test.go
  - internal/provider/resource_lookup_table.go
  - internal/provider/resource_lookup_table_test.go
  - internal/provider/resource_link_rule.go
  - internal/provider/resource_link_rule_test.go
  - examples/resources/muster_mattermost_destination/resource.tf
  - examples/resources/muster_mattermost_destination/import.sh
  - examples/resources/muster_telegram_destination/resource.tf
  - examples/resources/muster_telegram_destination/import.sh
  - examples/resources/muster_bridge_destination/resource.tf
  - examples/resources/muster_bridge_destination/import.sh
  - examples/resources/muster_slack_destination/resource.tf
  - examples/resources/muster_slack_destination/import.sh
  - examples/resources/muster_webhook_destination/resource.tf
  - examples/resources/muster_webhook_destination/import.sh
  - examples/resources/muster_lookup_table/resource.tf
  - examples/resources/muster_lookup_table/import.sh
  - examples/resources/muster_link_rule/resource.tf
  - examples/resources/muster_link_rule/import.sh
acceptance:
  - "[C-33.FR-5, C-33.FR-7, C-33.FR-13, C-33.FR-19] `muster_mattermost_destination` on a `muster_mattermost_connection` of S-184 and the channel `ch-alerts` creates, updates and deletes a Destination with its Mention entries — everyone in the chat for `ack_timeout`, a User for `storm_started` — its button set and its limiter, each write with `managed_by` `terraform`; a second plan shows no change; the acceptance tests of this story pass on the four CLI versions; `mentions`, `health`, `mention_warnings`, `routes` and `teams` are not attributes; the Destinations of Telegram, Bridges and Slack do the same on the fake servers' chats (`TestAccMattermostDestination_*`, `TestAccTelegramDestination_*`, `TestAccBridgeDestination_*`, `TestAccSlackDestination_*`)."
  - "[C-33.FR-5, C-33.FR-10] `muster_webhook_destination` in the template mode with request templates for `create` and `update` applies; its `signing_secret` is sensitive, set by the create and unknown after an import; `secrets` sets the named Secrets `api_key` and `region` with `setDestinationSecret`, removing `region` from the map deletes it, and a Secret named in Muster but not in the map is deleted by the next apply; `secrets_wo` with `secrets_wo_version` sends them only when the version changes and keeps them out of the state file (`TestAccWebhookDestination_*`)."
  - "[C-33.FR-7] A template of a webhook Destination that fails the server's dry run is refused with a diagnostic at `template.create.body`, carrying `template_syntax` with its line and column (`TestAccWebhookDestination_badTemplate`)."
  - "[C-33.FR-5] `muster_lookup_table` with two columns and three entries and `muster_link_rule` with Matchers, a scope and a URL template that reads the table apply, change and delete (`TestAccLookupTable_basic`, `TestAccLinkRule_basic`)."
  - "[C-33.FR-14] The built-in Link rule \"Explore\" imported into `muster_link_rule` takes a changed URL template; its destroy leaves it in Muster, clears its `managed_by` and warns that it stays (`TestAccLinkRule_builtin`)."
  - "[C-33.FR-9, C-33.AC-7] Each resource of this story imported by `public_id` plans without changes but the Secrets that the configuration holds (`TestAcc*_import`)."
verify: "make ci testacc"
operator_attention: false
issue: 703
---

# S-185. The Terraform resources of the Destinations of each type with the Secrets of outgoing webhooks, Lookup tables and Link rules (BE)

## Scope

**IN**

- One resource per Destination type — `muster_mattermost_destination`, `muster_telegram_destination`,
  `muster_bridge_destination`, `muster_slack_destination`, `muster_webhook_destination` — sharing the Mention entries,
  the owning Team, the button set and the limiter in `destination_common.go`.
- The named Secrets of an outgoing webhook Destination and its first Signing secret.
- `muster_lookup_table` and `muster_link_rule`, the built-in "Explore" included.

**OUT**

- The Destinations of Routes and Teams, which are the Destination entries of those resources (S-186, S-187).
- Tests, checks and previews of Destinations, and the rotation of a Signing secret (`generateSigningSecret`,
  `retirePreviousSigningSecret`): operations, not configuration; the maintainer left the rotation out of the first
  release (C-33.FR-5).

## Contracts

- **Destinations** (`createDestination`, `getDestination`, `updateDestination`, `deleteDestination`): the fields of
  each type's input schema except `type`, which the resource sets, and the deprecated `mentions` — Mattermost
  `connection_id`, `team_id` (the Mattermost team's id, the API's name), `channel_id`; Telegram, Bridge and Slack
  `connection_id`, `channel_id`; the outgoing webhook `mode`, `events` (`url` and `headers`), `template` (`create`,
  `update`, `open_thread`, `reply_in_thread` request templates) and `proxy` with its `password` pair — and, shared,
  `name`, `mention_entries` (each `target` with its `kind` and the fields of `MentionTargetInput`, and `events`),
  `owning_team_id`, `buttons` (not on outgoing webhooks, whose input has none) and `limiter`. Computed `id`, `etag`.
- **Named Secrets** (`listDestinationSecrets`, `setDestinationSecret`, `deleteDestinationSecret`): `secrets`, a
  sensitive map from name to value, or `secrets_wo`, a write-only map with `secrets_wo_version`; the resource is
  authoritative over the names — a name that Muster has and the configuration does not is deleted. Reads use the names
  and `set` of `listDestinationSecrets`; the ETag of that list goes as `If-Match` on its writes.
- **Signing secret**: `signing_secret`, sensitive and computed, from `DestinationCreated.signing_secret`; null after
  an import.
- **`muster_lookup_table`**: `name`, `description`, `columns`, `entries`; **`muster_link_rule`**: `name`, `matchers`,
  `scope`, `url_template`. The built-in rule — `builtin` in its read — is imported, never created; its destroy uses the
  forget path of `singleton.go` (S-183).

## Steps

1. Write `destination_common.go` with its unit tests for the Mention entries' mapping. Check: its test.
2. Write the Mattermost Destination, then the Telegram, Bridge and Slack ones. Check: their acceptance tests.
3. Write the outgoing webhook Destination with its Secrets and Signing secret. Check: its tests on the four CLI
   versions.
4. Write the Lookup table and the Link rule with "Explore". Check: their tests.
5. Add the examples and the documentation. Check: `make docs-check`, `make ci testacc`.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` and the provider installed as in S-182:

```sh
cat > main.tf <<'EOF'
data "muster_connection" "mm" { name = "Example Mattermost" }
resource "muster_mattermost_destination" "alerts" {
  name          = "tf-alerts"
  connection_id = data.muster_connection.mm.id
  team_id       = "team-1"
  channel_id    = "ch-alerts"
  mention_entries = [{ target = { kind = "everyone", everyone = "channel" }, events = ["ack_timeout"] }]
}
resource "muster_webhook_destination" "hook" {
  name   = "tf-hook"
  mode   = "events"
  events = { url = "http://127.0.0.1:19999/hook" }
  secrets = { api_key = "k1" }
}
EOF
terraform apply -auto-approve && terraform plan -detailed-exitcode; echo "exit=$?"                      # exit=0
A=(-H "Authorization: Bearer $MUSTER_TOKEN"); API=$MUSTER_ENDPOINT/api/v1
H=$(terraform state show -no-color muster_webhook_destination.hook | awk '$1=="id"{print $3}' | tr -d '"')
curl -s "${A[@]}" $API/destinations/$H/secrets | jq -c '[.items[] | {name, set}]'      # [{"name":"api_key","set":true}]
curl -s "${A[@]}" -X PUT -H 'Content-Type: application/json' -d '{"value":"x"}' $API/destinations/$H/secrets/extra -o /dev/null
terraform plan -detailed-exitcode > /dev/null; echo "exit=$?"                                         # exit=2
terraform apply -auto-approve > /dev/null
curl -s "${A[@]}" $API/destinations/$H/secrets | jq -c '[.items[].name]'                               # ["api_key"]
terraform destroy -auto-approve > /dev/null
```

The Mattermost team and channel ids are those of the fake Mattermost server; the test reads them from its fixtures.
The pull request carries this output and the acceptance run.

## Open questions

None. The rotation of a webhook Destination's Signing secret, which keeps the previous one valid until it is retired,
is left out of the first release, as the maintainer decided; the resource's documentation points to the rotation in
the web UI and the API.

## Notes

- Suggested commit: `feat: manage destinations, lookup tables and link rules`.
- `team_id` of a Mattermost Destination is the Mattermost team, not a Muster Team; the resource's documentation says
  so, and `owning_team_id` is the Muster Team.
- Mention entries that name Teams or the On-call of Teams take the ids of `muster_team` from S-186; this story's tests
  use everyone in the chat and Users.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-5 | partial | these resources |
| C-33.FR-7 | partial | these resources |
| C-33.FR-9 | partial | these resources |
| C-33.FR-10 | partial | the named Secrets, the webhook proxy password and the Signing secret |
| C-33.FR-13 | partial | these resources |
| C-33.FR-14 | partial | "Explore" |
| C-33.FR-19 | partial | these resources' acceptance tests |
| C-33.AC-7 | partial | these resources |
