---
id: S-179
title: The API specification under MIT, and the managed_by mark on Integrations, Routes, Teams, Schedules, Escalation chains, Lookup tables and Link rules (BE)
capability: C-33
kind: be
layer: L2
depends_on: []
covers: [C-33.FR-2, C-33.FR-11, C-33.AC-3]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - NOTICE
  - internal/db/migrations/0055_managed_by.up.sql
  - internal/db/migrations/0055_managed_by.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - docs/install/versions.md
  - internal/managedby/managedby.go
  - internal/managedby/managedby_test.go
  - internal/integrations/query.sql
  - internal/integrations/integrations.go
  - internal/routing/query.sql
  - internal/routing/routes.go
  - internal/teams/query.sql
  - internal/teams/teams.go
  - internal/schedules/query.sql
  - internal/schedules/schedules.go
  - internal/escalation/query.sql
  - internal/escalation/chains.go
  - internal/links/query.sql
  - internal/links/lookup.go
  - internal/links/rules.go
  - internal/api/integrations.go
  - internal/api/routes.go
  - internal/api/teams.go
  - internal/api/schedules.go
  - internal/api/escalationchains.go
  - internal/api/links.go
  - test/e2e/managed_by_test.go
acceptance:
  - "[C-33.FR-2] `api/openapi.yaml` has `info.license` `MIT` with the identifier `MIT`; `NOTICE` lists it beside `api/bridge.yaml` as an MIT part of the repository; `api/README.md` says that the specification is MIT so that clients such as the Terraform provider may carry any licence; `make lint` and `make licenses` pass."
  - "[C-33.FR-11] Migration 0055 adds a nullable `managed_by text` to `integrations`, `routes`, `teams`, `schedules`, `escalation_chains`, `lookup_tables`, `link_rules`, `connections`, `destinations`, `organizations`, `outbound_policies` and `oidc_settings`, with no default and no CHECK; its down step drops them; `migrate_test.go` runs it up and down over a database at 0054."
  - "[C-33.FR-11] `managedby.Validate` accepts `terraform`, `opentofu`, `pulumi.v2` and a value of 32 characters, and refuses the empty string, `Terraform`, `-tf`, `a b` and a value of 33 characters (`managedby_test.go`)."
  - "[C-33.FR-11, C-33.AC-3] For an Integration, a Route, the Default route, a Team, a Schedule, an Escalation chain, a Lookup table and a Link rule: a create with `managed_by` `terraform` reads it back; a create without it reads null; an update without the field keeps `terraform`; an update with null clears it; an update with `Terraform` or with a value of 33 characters is refused with `422` and the error `invalid_format` at `/managed_by` and changes nothing; the Audit log entry of each change shows `managed_by` in its diff (`TestManagedBy`, `managed_by_test.go`)."
  - "[C-33.FR-11] The mark changes nothing else: a Route marked `terraform` routes, delivers and is reordered as before, and an update through the web session with its CSRF token succeeds on a marked Team (`TestManagedBy`)."
verify: "make ci && make e2e-go"
operator_attention: false
issue: 697
---

# S-179. The API specification under MIT, and the managed_by mark on Integrations, Routes, Teams, Schedules, Escalation chains, Lookup tables and Link rules (BE)

## Scope

**IN**

- The licence of `api/openapi.yaml` becomes MIT, with `NOTICE` and `api/README.md` (ADR-0001, C-33.FR-2).
- One migration that adds `managed_by` to the twelve tables of C-33.FR-11, and the shared validation of its value.
- The field `managed_by` in the spec, the queries, the domain packages and the handlers of Integrations, Routes (the
  Default route included), Teams, Schedules, Escalation chains, Lookup tables and Link rules.

**OUT**

- `managed_by` on Connections, Destinations, the Organization's settings, the Outbound address policy and the OIDC
  settings (S-180); the mark and the warnings on the pages (S-181).
- The provider itself (S-182 onward) and the documentation page (S-189).

## Contracts

- **Licence** (ADR-0001, amended for L2): `info.license` of `api/openapi.yaml` becomes `name: MIT`, `identifier: MIT`;
  `NOTICE`, under "MIT-licensed parts", lists `api/openapi.yaml  the API specification; its info.license says MIT`
  beside `api/bridge.yaml`; `api/README.md` gains a paragraph: the specification is MIT, as the Bridge protocol is, so
  that clients generated from it — the Terraform provider in `muster-io/terraform-provider-muster` among them — may
  carry any licence, while the server that implements it stays AGPL-3.0-only. Nothing else in the repository changes
  licence; the licence-header check already skips YAML specifications.
- **Migration** `0055_managed_by` (expand only): `ALTER TABLE … ADD COLUMN managed_by text` on the twelve tables of
  C-33.FR-11, nullable, without default, CHECK or index — the format is checked by the domain packages, so that a
  later change of the format needs no migration. `schema.md` describes the column once, in a short section that names
  the tables; `docs/install/versions.md` notes migration 0055 under the release that carries it, and that going back
  below it loses only the marks.
- **Value** (`internal/managedby`): `Validate(s string) error` accepts 1 to 32 characters of `[a-z0-9._-]` starting
  with `[a-z0-9]`; the domain packages call it and turn its error into a `422` with the error `invalid_format` at
  `/managed_by`, the answer that `x-unprocessable` gives for the same value from the request validation (api/README.md).
- **API** (`api/openapi.yaml`): the property `managed_by`, `type: [string, "null"]`, `maxLength: 32`, with the
  pattern `^[a-z0-9][a-z0-9._-]{0,31}$`, `x-unprocessable: true` as a Team's `slug` has, and the description of
  C-33.FR-11, on `IntegrationInput`, `RouteInput`,
  `TeamInput`, `ScheduleInput`, `EscalationChainInput`, `LookupTableBase` and `LinkRuleBase`, so that the reads that
  extend them carry it too. In the reads it is always sent, null when no tool manages the object, but it is not added
  to their `required` lists: clients and web tests that build reads by hand keep compiling, as with any additive field
  of a `0.2.x` release. The schemas of S-180 are left to it.
- **Writes**: create stores the value or null; update keeps the stored value when the field is left out and clears it
  on null — the API's rule for omitted and null fields; the Default route takes it like any Route. The before/after diff
  of the Audit log includes the field (C-03.FR-12). No other code reads the column: routing, delivery, ordering and the
  evaluation of Teams ignore it.
- **Log events, metrics, settings**: none.

## Steps

1. Change the licence in the spec, `NOTICE` and `api/README.md`. Check: `make lint`, `make licenses`.
2. Write the migration with its test and `schema.md`. Check: `go test -tags integration -run TestMigrat ./internal/db/`.
3. Add `internal/managedby` with its test. Check: `go test ./internal/managedby/`.
4. Add the field to the spec and run `make generate`; carry it through the queries, the domain packages and the
   handlers of the seven kinds. Check: `make generate-check`, the packages' tests.
5. Write `TestManagedBy`. Check: `make e2e-go` with `-run TestManagedBy`, then `make ci`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-023: the Admin's session headers and a Personal access token PAT with all of the Admin's Permissions.
H=(-H "Authorization: Bearer $PAT" -H 'Content-Type: application/json')
curl -s localhost:8080/api/v1/openapi.yaml | grep -A2 '^  license:'
#   license:
#     name: MIT
#     identifier: MIT
R=$(curl -s "${H[@]}" localhost:8080/api/v1/route-profiles | jq -c '.items[] | select(.id=="paging") | .policy')
ID=$(curl -s "${H[@]}" -d "{\"name\":\"tf-route\",\"matchers\":[],\"group_key\":[\"alertname\"],\"policy\":$R,\"managed_by\":\"terraform\"}" \
  localhost:8080/api/v1/routes | jq -r .id)
curl -s "${H[@]}" localhost:8080/api/v1/routes/$ID | jq -c '{managed_by, etag}'
# {"managed_by":"terraform","etag":"…"}
E=$(curl -s "${H[@]}" localhost:8080/api/v1/routes/$ID | jq -r .etag)
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -H "If-Match: $E" -X PUT -d '{"name":"tf-route-2"}' localhost:8080/api/v1/routes/$ID
# 200
curl -s "${H[@]}" localhost:8080/api/v1/routes/$ID | jq -r .managed_by          # terraform (kept)
E=$(curl -s "${H[@]}" localhost:8080/api/v1/routes/$ID | jq -r .etag)
curl -s "${H[@]}" -H "If-Match: $E" -X PUT -d '{"managed_by":"Terraform"}' localhost:8080/api/v1/routes/$ID |
  jq -c '{status, e: .errors[0] | {pointer, code}}'
# {"status":422,"e":{"pointer":"/managed_by","code":"invalid_format"}}
curl -s "${H[@]}" -H "If-Match: $E" -X PUT -d '{"managed_by":null}' localhost:8080/api/v1/routes/$ID | jq -r .managed_by
# null
curl -s "${H[@]}" "localhost:8080/api/v1/audit-log?limit=3" | jq -c '[.items[].diff[]? | select(.pointer=="/managed_by")]'
# the entry of the last change, {"pointer":"/managed_by","before":"terraform","after":null} among them
psql "$MUSTER_DATABASE_URL" -Atc \
  "select count(*) from information_schema.columns where column_name='managed_by'"   # 12
```

The same create, keep, clear and refusal run for an Integration, a Team, a Schedule, an Escalation chain, a Lookup
table, a Link rule and the Default route in `TestManagedBy`, whose output the pull request carries.

## Open questions

None.

## Notes

- Suggested commit: `feat(api): publish the specification under MIT and mark configuration managed by a tool`.
- The migration is 0055, as the orchestrator assigned it; `migrate_test.go`, `schema.md` and `versions.md` use it.
- This story, S-180 and S-181 ship in 0.2.0, as the maintainer decided, so 0.2.0 is the provider's minimum version
  (C-33.FR-4); `versions.md` notes 0055 under 0.2.0.
- The open stories of the stage "L2 Paging" change some of the same files (`internal/escalation`, the chain handlers);
  the implementer rebases on them rather than waiting for them, since nothing here depends on their behaviour.
- The licence change is the one ADR-0001 already decided for "the L2 change of the specification"; this is that
  change, and the ADR needs no edit.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-2 | partial | Muster's side, the licence; the provider's copy, its check and the generated client are S-182 |
| C-33.FR-11 | partial | the migration, the value and seven kinds; Connections, Destinations and the settings objects are S-180 |
| C-33.AC-3 | partial | seven kinds; the other five are S-180 |
