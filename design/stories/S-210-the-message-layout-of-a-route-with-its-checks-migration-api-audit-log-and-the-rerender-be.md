---
id: S-210
title: The message layout of a Route with its checks, migration, API, Audit log and the re-render row (BE)
capability: C-38
kind: be
layer: L2
depends_on: [S-209, S-125, S-162]
covers: [C-38.FR-1, C-38.FR-2, C-38.FR-4, C-38.FR-5, C-38.FR-9, C-38.FR-14, C-38.FR-15, C-38.AC-1, C-38.AC-4, C-38.AC-7, C-38.AC-11, C-38.AC-12]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/00NN_route_message_layout.up.sql
  - internal/db/migrations/00NN_route_message_layout.down.sql
  - design/db/schema.md
  - design/prd/L2.md
  - internal/db/migrate_test.go
  - internal/routing/messagelayout.go
  - internal/routing/messagelayout_test.go
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/query.sql
  - internal/routing/routing_integration_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/api/server_test.go
  - test/e2e/route_message_layout_test.go
acceptance:
  - "[C-38.FR-1, C-38.FR-15, C-38.AC-1] `getRoute`, `listRoutes`, `createRoute` and `updateRoute` carry `own_message_layout` and `message_layout`: a Route that never saved a layout reads `own_message_layout` false and `message_layout` as Muster's default; a read sent back unchanged with `If-Match` keeps `own_message_layout` false, stores no layout and leaves the ETag as it was; an update that omits `own_message_layout` keeps it; a create takes false; a change of Muster's default changes no Route's ETag (`messagelayout_test.go`, `routes_test.go`, `test/e2e/route_message_layout_test.go`)."
  - "[C-38.FR-2, C-38.FR-4, C-38.AC-4, C-38.AC-11] Saving a layout with an unknown block or field name gets `400` `invalid_format` from request validation; a field given twice `422` `duplicate` at `/message_layout/fields/{i}`; a label field with an empty or invalid label name `422` `invalid_format` at `/message_layout/fields/{i}/label`; more than `message.layout_fields` fields `422` `too_long`; an `alerts_listed` outside 1 to 20 (`route.message_layout_alerts_listed`) `422` `out_of_range` at `/message_layout/alerts_listed`, for 0 and for 21; a stale `If-Match` `412` and a missing one `428` (`messagelayout_test.go`, `routes_test.go`, `test/e2e/route_message_layout_test.go`)."
  - "[C-38.FR-2, C-38.FR-4, C-38.AC-4] A valid save writes one Audit log row `route.updated` whose diff holds `/message_layout` before and after, the layout before being Muster's default as it was then; the spec has no `pattern`, `maxItems`, `minimum` or `maximum` on the label name, the fields or `alerts_listed` (`routes_test.go`, `routing_integration_test.go`, `make lint`)."
  - "[C-38.FR-5] `own_message_layout` false stores no layout and the Route follows Muster's default again, with `message_labels` and its own setting untouched; no earlier version of a layout is kept, the Audit log's diff is the only record (`routes_test.go`, `routing_integration_test.go`)."
  - "[C-38.FR-9, C-38.AC-12] An `updateRoute` that sets `own_message_layout` true with a layout and `policy.templates.root_message` null saves both in one transaction with one Audit log row naming `/message_layout` and `/policy/templates/root_message` (`routes_test.go`, `test/e2e/route_message_layout_test.go`)."
  - "[C-38.FR-14] A save, or a reset, that changes the stored layout of a Route with open Alert Groups inserts one row `route_message_settings` of `open_group_updates` in the same transaction, one pending row per Route however many saves come before it runs; a save that changes nothing inserts none (`routing_integration_test.go`)."
  - "[C-38.FR-15, C-38.AC-7] A Viewer reads the layout with `routes:read` and gets `403` on a write (`routes_test.go`, `test/e2e/route_message_layout_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-210. The message layout of a Route with its checks, migration, API, Audit log and the re-render row (BE)

## Scope

**IN**

- The setting `route.message_layout`: the column, the Route API (`own_message_layout`, `message_layout`) with Muster's
  default read back resolved, the checks of a saved layout, `If-Match`, the Audit log diff, and the reset (C-38.FR-1,
  FR-2, FR-4, FR-5, FR-15).
- The save of the layout together with the removal of the Route's Root message template in one request
  (C-38.FR-9), which is an `updateRoute` of fields that already exist.
- The durable re-render row that a change of the layout inserts, and the sentence in L2 §6 that lists it (C-38.FR-14).

**OUT**

- What a Root message looks like under a layout: S-211 (Mattermost, Slack, Telegram, the body for a bridge) and S-212
  (the bridge's `layout` and `order`). Until S-211 merges the layout is stored, read and re-rendered with no visible
  change.
- The preview and the Destination test that take an unsaved layout: S-215, S-216. The editor: S-217, S-218.
- A Terraform attribute `message_layout` of `muster_route`: see Open questions.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `Route` and `RouteInput` gain `own_message_layout`
  (boolean; writable; an update that omits it keeps it, a create defaults to false) and `message_layout`
  (`RouteMessageLayout`, read as the effective layout, read only when `own_message_layout` is true), as
  `own_message_labels` and `message_labels` are (C-29.FR-2). `RouteMessageLayout` is the layout of C-38.FR-2 and names
  it as the PRD does: `order` (the four blocks `fields`, `text`, `labels`, `alerts` in the layout's order, hidden ones
  included; an enum), `fields` (an ordered list of `{field, label}`, where `field` is an enum of the catalogue —
  `severity`, `route`, `teams`, `started`, `firing_total`, `label` — and `label` is the label name for `label` only),
  `show_summary`, `show_description`, `show_labels`, `show_alerts`, `alerts_listed` and `show_links` (the other links
  of C-12.FR-9). Only the names of blocks and catalogue fields are enums; the label name, the number of fields and the
  bounds of `alerts_listed` are checked by the domain, since `pattern`, `maxItems`, `minimum` and `maximum` would be
  answered with `400` by request validation (C-38.FR-4); the schema's descriptions say so. "Labels in the message"
  stay `own_message_labels` and `message_labels`: the layout holds no copy of them.
  `api/README.md` says that a client that sends back what it read never turns Muster's default into a layout of the
  Route's own, because `own_message_layout` false ignores `message_layout`.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.8):
  `routes.message_layout jsonb`, null while the Route follows Muster's default, as `routes.message_labels` is null
  while its list is computed. No CHECK replaces another and nothing is backfilled; a replica of the earlier version
  neither reads nor writes the column, so that a Route it saves keeps the layout it already had, and renders the
  layout of its own version (an expand-only change, as 0029 is). The down migration drops the column. The stored JSON
  holds the order, the fields and the settings, never Muster's default, so that the default follows later releases
  (C-38.FR-1).
- **Package** (`internal/routing`, ADR-0016: flat CRUD, `org_id` in every query): `messagelayout.go` holds
  `MessageLayout`, its `Default()` — the layout of C-29.FR-1 with `alerts_listed` taken from
  `message.compact_alerts_listed` — and `Validate`, which returns the domain's errors with their pointers
  (`duplicate`, `invalid_format`, `too_long`, `out_of_range`) in the order the API lists them, reading the limits from
  `message.layout_fields` and `route.message_layout_alerts_listed` as built-in values, not settings of the
  Organization; `routes.go` reads and writes the column with the Route, resolves the effective layout on every read,
  adds `/message_layout` to the Audit log diff and the Route's version check, and, when the stored value changes,
  inserts the row of the kind `route_message_settings` of `open_group_updates` through the same path that a change of
  the Route's `language` or templates uses (migration 0039), inside the save's transaction and only for a Route with
  open Alert Groups. The kind and its CHECK are unchanged: a pending row of either kind of the same Route already
  absorbs a new one (`schema.md` §5), which is the "one pending row per Route however many saves" of C-38.FR-14. The
  save of a layout and of `policy.templates.root_message: null` (`RouteTemplates`, which null resets to the built-in
  one) in one request is the existing update; the Audit log row names both pointers, `/message_layout` and
  `/policy/templates/root_message`.
- **API handlers** (`internal/api/routes.go`): map `own_message_layout` and `message_layout` in both directions, add the
  pointers of the problems, and answer a write without `routes:write` with `403` as the other Route fields are.
- **Settings**: `route.message_layout`, `route.message_layout_alerts_listed`, `message.layout_fields` and
  `message.layout_preview_groups` are rows of `defaults.md` (decided and P-93 to P-95); the first two are used here, the
  last by S-215 and S-218.
- **Documents**: `schema.md` §4.8 (the column and the re-render), `design/prd/L2.md` §6 — the note under the table that
  lists the changes the re-render of the open Root messages must reach at once (C-08.FR-8) gains "the message layout of
  a Route (C-38.FR-14)", which C-38.FR-14 asks this story to add.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `migrate_test.go` finds
   existing Routes with a null `message_layout`.
2. Write `messagelayout.go` and its tests with the defaults, the limits and every refusal. Check:
   `messagelayout_test.go`.
3. Wire the column into `routes.go`, the Audit log diff and the re-render row. Check: `routes_test.go`,
   `routing_integration_test.go` (the three saves before the run, one pending row).
4. Add the spec and the handlers; update `api/README.md` and the note in L2 §6. Check: `make generate-check`, `make
   lint` (Redocly), `routes_test.go`.
5. Write `test/e2e/route_message_layout_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: the Admin's header array, as in S-125; V: a Viewer's
R=$(curl -s "${A[@]}" $API/routes | jq -r '.items[] | select(.name=="Infrastructure") | .id')   # the Route of S-125's setup
# C-38.AC-1: a Route that never saved a layout reads the default, and a read sent back keeps it and the ETag
curl -s "${A[@]}" $API/routes/$R | jq -c '{own: .own_message_layout, order: .message_layout.order, listed: .message_layout.alerts_listed}'
# {"own":false,"order":["fields","text","labels","alerts"],"listed":5}
E=$(curl -si "${A[@]}" $API/routes/$R | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -si "${A[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c 'del(.id, .etag, .position)')" | awk -F': ' 'tolower($1)=="etag"{print $2}'   # the same ETag
psql "$MUSTER_DB" -c "select message_layout is null from routes where public_id='$R'"   # t
# a saved layout: Severity, Started, Firing / total and the label cluster, no description, the Alert list before the key labels
L='{"order":["fields","text","alerts","labels"],"fields":[{"field":"severity"},{"field":"started"},{"field":"firing_total"},{"field":"label","label":"cluster"}],
  "show_summary":true,"show_description":false,"show_labels":true,"show_alerts":true,"alerts_listed":5,"show_links":true}'
body() { curl -s "${A[@]}" $API/routes/$R | jq -c --argjson l "$1" 'del(.id, .etag, .position) | .own_message_layout = true | .message_layout = $l'; }
curl -s "${A[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(body "$L")" | jq -c '{own: .own_message_layout, f: [.message_layout.fields[].field]}'
# {"own":true,"f":["severity","started","firing_total","label"]}
# C-38.AC-4: the refusals
E=$(curl -si "${A[@]}" $API/routes/$R | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(body '{"order":["fields","text","alerts","labels"],"fields":[{"field":"colour"}],"show_summary":true,"show_description":true,"show_labels":true,"show_alerts":true,"alerts_listed":5,"show_links":true}')"   # 400 (invalid_format)
# a field twice -> 422 duplicate at /message_layout/fields/1; a label named "bad name!" -> 422 invalid_format at /message_layout/fields/0/label;
# (each is `curl ... -d "$(body "<layout>")" | jq -c '.errors[0] | [.pointer, .code]'`)
# eleven fields -> 422 too_long; alerts_listed 0 and 21 -> 422 out_of_range at /message_layout/alerts_listed; a stale If-Match -> 412; none -> 428
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" -H "If-Match: stale" -X PUT $API/routes/$R -d "$(body "$L")"   # 412
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(body "$L")"   # 403
curl -s "${V[@]}" $API/routes/$R | jq -c '.own_message_layout'   # true: a Viewer reads
# the Audit log has one row with the diff before and after
curl -s "${A[@]}" "$API/audit-log?limit=1" | jq -c '.items[0] | {action, keys: (.diff | map(.pointer))}'   # route.updated with "/message_layout"
# C-38.FR-14: three saves before the run claims the row leave one pending row (`routing_integration_test.go` runs without the
# worker; live, read the row within the worker's wake-up, or save three times in one shell line)
psql "$MUSTER_DB" -c "select kind, count(*) from open_group_updates where route_id=(select id from routes where public_id='$R') and leased_by is null group by 1"   # route_message_settings | 1
# reset: own false stores nothing, "Labels in the message" are untouched
curl -s "${A[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c 'del(.id, .etag, .position) | .own_message_layout = false')" | jq -c '{own: .own_message_layout, labels: .own_message_labels}'
psql "$MUSTER_DB" -c "select message_layout is null from routes where public_id='$R'"   # t
```

`test/e2e/route_message_layout_test.go` repeats these, adds "Remove the template with a layout in one save" (the Route
with a `policy.templates.root_message` and one `updateRoute` that sets the layout and nulls the template: one Audit log
row, both pointers in its diff), the Responder's `403` for the write and the Viewer's read, and the migration case in
the
migration test (a Route that exists before the migration reads the default).

## Open questions

- **The Terraform provider.** C-38.FR-15 says a client such as the Terraform provider reads and writes the layout like
  any other Route setting, but no story of C-33 (S-187) names `message_layout` among the attributes of `muster_route`,
  and the provider is released on its own. Does the maintainer want a story in `muster-io/terraform-provider-muster`
  for `own_message_layout` and `message_layout`, after this story merges? Until then a configuration cannot own a
  layout, and a Route the provider manages keeps whatever an Admin saved in the UI, which the provider's plan shows
  as a drift only if its schema lists the attribute.
- **The shape of `RouteMessageLayout`.** The PRD fixes the content of the layout and the names `own_message_layout` and
  `message_layout` but not the property names inside it; this story names them as above. Say if the review wants
  others before the generated clients carry them.

## Notes

- Suggested commit: `feat(routing): add the message layout of a route with its checks and the re-render of open
  messages`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number is
  assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- The kind `route_message_settings` is reused because a change of the layout, like a change of the Route's language or
  templates, changes what its Root messages show and nothing else, and because a walk of that kind already renders
  "with every setting as it is then" (`schema.md` §5); a new kind would need a CHECK re-versioned in the order the
  stories of 0.2.x merge, for no gain.
- The default is never stored, so a later release that improves it needs no migration and edits the Root messages of
  every Route without its own layout the way any change of the compact layout does; the release notes of that release
  say so, which is C-38.FR-1.
- After this story merges and before S-211, an Admin who saves a layout on the `edge` image sees no effect, which is
  true to the capability's order of delivery.
- The pointers of the refusals follow the existing Route problems: JSON pointers into the request body.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-1 | partial | the setting, its default and the read; that every Destination of the Route renders it is S-211 |
| C-38.FR-2 | partial | what a layout may hold and its checks; how it renders is S-211, S-212 |
| C-38.FR-4 | full | |
| C-38.FR-5 | partial | the reset and the Audit log; the editor's draft is S-217 |
| C-38.FR-9 | partial | the save with the template removed; the editor's hint and action are S-217 |
| C-38.FR-14 | partial | the re-render row; that the Root messages are edited once, Quietly, is S-211 |
| C-38.FR-15 | partial | the API and the Permissions; the read-only editor is S-217 |
| C-38.AC-1 | partial | the read-back; the byte-for-byte Root message is S-211 |
| C-38.AC-4 | full | |
| C-38.AC-7 | partial | the Viewer's read; the offered Alert Groups and the preview are S-215, S-218 |
| C-38.AC-11 | partial | the refusals of 0 and 21; the three values and "…and 9 more" are S-211, the `order` S-212 |
| C-38.AC-12 | partial | the one save of the layout and the template's removal; the test message is S-216 |
