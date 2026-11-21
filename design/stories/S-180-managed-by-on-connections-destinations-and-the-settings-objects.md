---
id: S-180
title: The managed_by mark on Connections, Destinations, the Organization's settings, the Outbound address policy and the OIDC settings (BE)
capability: C-33
kind: be
layer: L2
depends_on: [S-179]
covers: [C-33.FR-11, C-33.AC-3]
files_touched:
  - api/openapi.yaml
  - internal/connections/query.sql
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/destinations/query.sql
  - internal/destinations/destinations.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/organization/query.sql
  - internal/organization/organization.go
  - internal/organization/outbound.go
  - internal/organization/organization_test.go
  - internal/oidc/query.sql
  - internal/oidc/settings.go
  - internal/oidc/settings_test.go
  - internal/api/connections.go
  - internal/api/destinations.go
  - internal/api/organization.go
  - internal/api/oidc.go
  - internal/api/middleware.go
  - internal/api/problem.go
  - internal/api/teams_test.go
  - internal/managedby/queries_test.go
  - api/README.md
  - test/e2e/managed_by_test.go
  - design/stories/S-180-managed-by-on-connections-destinations-and-the-settings-objects.md
acceptance:
  - "[C-33.FR-11, C-33.AC-3] For a Connection of each type (Mattermost, Telegram, Bridge, Slack), a Destination of each type (Mattermost, Telegram, Bridge, Slack, outgoing webhook), the Organization's settings, the Outbound address policy and the OIDC settings: a write with `managed_by` `terraform` reads it back; an update without the field keeps it; null clears it; `Terraform` is refused with `422` and `invalid_format` at `/managed_by` and changes nothing; the Audit log shows the change in the object's diff (`TestManagedBy`)."
  - "[C-33.FR-11] A Connection or Destination created without the field reads null; the objects that exist once read null until a write sets the field (`TestManagedBy`)."
  - "[C-33.FR-11] The mark changes nothing else: a marked Mattermost Destination delivers, its Connection is checked, and a marked Organization re-renders the open Root messages on a change of its time zone as before; a write of a Secret alone (`setDestinationSecret`) keeps the mark (`TestManagedBy`)."
verify: "make ci && make e2e-go"
operator_attention: false
issue: 698
---

# S-180. The managed_by mark on Connections, Destinations, the Organization's settings, the Outbound address policy and the OIDC settings (BE)

## Scope

**IN**

- The field `managed_by` of C-33.FR-11 on the inputs and reads of every Connection and Destination type, the
  Organization's settings, the Outbound address policy and the OIDC settings, through their queries, domain packages
  and handlers, on the columns that S-179's migration added.

**OUT**

- The migration, the value's validation and the other seven kinds (S-179); the pages (S-181).

## Contracts

- **API** (`api/openapi.yaml`): the property of S-179 — the same type, pattern, `maxLength`, `x-unprocessable` and
  description — on `MattermostConnectionInput`, `TelegramConnectionInput`, `BridgeConnectionInput`,
  `SlackConnectionInput` (through `MattermostConnectionBase`, `TelegramConnectionBase`, `BridgeConnectionBase` and
  `SlackConnectionBase`, which their reads extend too), the input schema of each Destination type
  (`MattermostDestinationInput`, `TelegramDestinationInput`, `BridgeDestinationInput`, `SlackDestinationInput`, and
  `WebhookDestinationInput` through `WebhookDestinationBase`), `OrganizationInput` (through `OrganizationBase`),
  `OutboundPolicy` and `OidcSettingsInput` (through `OidcSettingsBase`), so that the reads carry it; not in the reads'
  `required` lists, as in S-179. The description of the singletons adds that they read null until a write sets it.
- **Request validation** (`internal/api/middleware.go`): `x-unprocessable` is read through the variants of `oneOf` too,
  the bodies of `createConnection`, `updateConnection`, `createDestination` and `updateDestination`, and from the
  errors of the JSON Schema 2020-12 validator that kin-openapi uses for a body schema it can compile, such as
  `OutboundPolicy`, so that `Terraform` is the `422` `invalid_format` at `/managed_by` on each of them.
- **Tables**: `connections`, `destinations`, `organizations`, `outbound_policies` and `oidc_settings`, the columns of
  migration 0056 (S-179); no migration of its own.
- **Writes**: as in S-179 — create stores the value or null, an update keeps it when left out and clears it on null,
  the Audit log diff includes it; `internal/managedby.Validate` checks it. The operations of Secrets
  (`setDestinationSecret`, `deleteDestinationSecret`) and Signing secrets do not take the field and keep it. Nothing
  else reads the column.
- **Log events, metrics, settings**: none.

## Steps

1. Add the field to the schemas and run `make generate`. Check: `make generate-check`.
2. Carry it through the queries, domain packages and handlers of Connections, Destinations, the Organization's
   settings, the Outbound address policy and the OIDC settings. Check: the packages' tests.
3. Extend `TestManagedBy` to these kinds. Check: `make e2e-go` with `-run TestManagedBy`, then `make ci`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-023: the Admin's Personal access token PAT with all of the Admin's Permissions.
API=localhost:8080/api/v1; H=(-H "Authorization: Bearer $PAT" -H 'Content-Type: application/json')
# put sends the whole body $2 to $API/$1 with the ETag of its read as If-Match: a body without the required fields
# of its schema is a 400 of the request validation.
put() { curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s "${H[@]}" "$API/$1" | jq -r .etag)" -d "$2" "$API/$1"; }
C=$(curl -s "${H[@]}" $API/connections | jq -r '.items[] | select(.name=="Example Mattermost") | .id')
put connections/$C "$(curl -s "${H[@]}" $API/connections/$C |
  jq -c '{type, name, server_url, limiter, proxy: {enabled: .proxy.enabled}, managed_by: "terraform"}')" |
  jq -r .managed_by                                                                                  # terraform
put organization "$(curl -s "${H[@]}" $API/organization |
  jq -c 'del(.id, .etag, .working_hours) + {outgoing_heartbeat: {proxy: {enabled: false}}, managed_by: "terraform"}')" |
  jq -r .managed_by                                                                                  # terraform
put organization/outbound-policy "$(curl -s "${H[@]}" $API/organization/outbound-policy |
  jq -c '{policy, allowed, denied, managed_by: "terraform"}')" | jq -r .managed_by                   # terraform
put oidc-settings "$(curl -s "${H[@]}" $API/oidc-settings |
  jq -c 'del(.etag, .client_secret_status, .warnings, .updated_at, .claims_supported) +
    {proxy: {enabled: .proxy.enabled}, managed_by: "Terraform"}')" | jq -c '{status, p: .errors[0].pointer}'
# {"status":422,"p":"/managed_by"}
D=$(curl -s "${H[@]}" -X POST $API/destinations -d '{"type": "webhook", "name": "tf-hook", "mode": "events",
  "events": {"url": "http://127.0.0.1:18093/hook/tf", "headers": []}, "proxy": {"enabled": false},
  "limiter": {"limit": 50, "per_seconds": 1}, "managed_by": "terraform"}' | jq -r .destination.id)
curl -s "${H[@]}" -X PUT -d '{"value":"s3cr3t"}' $API/destinations/$D/secrets/api_key -o /dev/null -w '%{http_code}\n'   # 200
curl -s "${H[@]}" $API/destinations/$D | jq -r .managed_by                                                               # terraform
```

The demo objects of `muster dev` stand in for each type; where the demo has no Destination of a type — it has none —
the test creates one. A `PUT` sends the whole input of its type, built from the read, as in S-154's Verification.

## Open questions

None.

## Notes

- Suggested commit: `feat(api): mark connections, destinations and settings managed by a tool`.
- Ships in 0.2.0 with S-179 and S-181, as the maintainer decided (C-33.FR-4).
- The Organization's settings span several pages of the web UI but are one object with one ETag, so they carry one
  mark; the Outbound address policy and the OIDC settings have ETags of their own and carry their own.
- A save that changes only the mark moves the version (a new ETag) and is recorded with `/managed_by` alone in its
  diff, and keeps every secret: a Connection's save sets or deletes no Telegram webhook, forgets no bot, info or Slack
  workspace and reads no bridge info, and its new version makes the Leader's poller or socket of the Connection pick
  it up as after any save; a Destination's save runs its Destination check as every save does and requests no
  re-render; the Organization's requests no re-render of the open Root messages; the Outbound address policy's sends
  no `NOTIFY` on `muster_outbound_policy`, runs no hook and logs no `outbound_policy_updated`, since the policy every
  replica applies is the same; the OIDC settings' keep the claims sign-ins carried.
- Beyond the kinds' own files, `files_touched` has `internal/api/middleware.go` and `internal/api/problem.go` (the
  request validation above, which reads the 2020-12 validator's messages through one helper), `api/README.md` (its
  `x-unprocessable` row says the middleware reads the variants of a `oneOf` body, which must agree), and
  `internal/managedby/queries_test.go`, which fails when a query outside the reads, creations and updates of the
  twelve kinds mentions `managed_by` in `internal/*/query.sql`: the domain tests' stores are fakes and cannot see a
  writer that nulls the column.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-11 | partial | completes the API with S-179 |
| C-33.AC-3 | partial | completes it with S-179 |
