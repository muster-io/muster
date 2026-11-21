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
  - internal/destinations/query.sql
  - internal/destinations/write.go
  - internal/organization/query.sql
  - internal/organization/settings.go
  - internal/organization/outbound.go
  - internal/oidc/query.sql
  - internal/oidc/settings.go
  - internal/api/connections.go
  - internal/api/destinations.go
  - internal/api/organization.go
  - internal/api/oidc.go
  - test/e2e/managed_by_test.go
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
  `SlackConnectionInput`, the input schema of each Destination type (`MattermostDestinationInput`,
  `TelegramDestinationInput`, `BridgeDestinationInput`, `SlackDestinationInput`, `WebhookDestinationInput`),
  `OrganizationInput` (through `OrganizationBase`), `OutboundPolicy` and `OidcSettingsInput` (through
  `OidcSettingsBase`), so that the reads carry it; not in the reads' `required` lists, as in S-179.
- **Tables**: `connections`, `destinations`, `organizations`, `outbound_policies` and `oidc_settings`, the columns of
  migration 0055 (S-179); no migration of its own.
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
C=$(curl -s "${H[@]}" $API/connections | jq -r '.items[] | select(.name=="Example Mattermost") | .id')
E=$(curl -s "${H[@]}" $API/connections/$C | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $E" -d '{"type":"mattermost","managed_by":"terraform"}' $API/connections/$C | jq -r .managed_by
# terraform
E=$(curl -s "${H[@]}" $API/organization | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $E" -d '{"managed_by":"terraform"}' $API/organization | jq -r .managed_by   # terraform
E=$(curl -s "${H[@]}" $API/organization/outbound-policy | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $E" -d '{"managed_by":"terraform"}' $API/organization/outbound-policy | jq -r .managed_by
# terraform
E=$(curl -s "${H[@]}" $API/oidc-settings | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $E" -d '{"managed_by":"Terraform"}' $API/oidc-settings | jq -c '{status, p: .errors[0].pointer}'
# {"status":422,"p":"/managed_by"}
D=$(curl -s "${H[@]}" $API/destinations | jq -r '.items[] | select(.type=="webhook") | .id' | head -1)
E=$(curl -s "${H[@]}" $API/destinations/$D | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $E" -d '{"type":"webhook","managed_by":"terraform"}' $API/destinations/$D > /dev/null
curl -s "${H[@]}" -X PUT -d '{"value":"s3cr3t"}' $API/destinations/$D/secrets/api_key -o /dev/null -w '%{http_code}\n'   # 200
curl -s "${H[@]}" $API/destinations/$D | jq -r .managed_by                                                               # terraform
```

The demo objects of `muster dev` stand in for each type; where the demo has no Destination of a type, the test creates
one. A body that the `PUT` of a type needs beyond `type` follows the read, as in S-154's Verification.

## Open questions

None.

## Notes

- Suggested commit: `feat(api): mark connections, destinations and settings managed by a tool`.
- Ships in 0.2.0 with S-179 and S-181, as the maintainer decided (C-33.FR-4).
- The Organization's settings span several pages of the web UI but are one object with one ETag, so they carry one
  mark; the Outbound address policy and the OIDC settings have ETags of their own and carry their own.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-11 | partial | completes the API with S-179 |
| C-33.AC-3 | partial | completes it with S-179 |
