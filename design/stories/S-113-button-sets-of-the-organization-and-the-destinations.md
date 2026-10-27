---
id: S-113
title: Button sets of the Organization and the Destinations (BE)
capability: C-28
kind: be
layer: L2
depends_on: [S-111, S-166]
covers: [C-28.FR-8, C-28.FR-9, C-28.FR-10, C-28.AC-7, C-10.AC-23, C-16.FR-4, C-20.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0023_button_sets.up.sql
  - internal/db/migrations/0023_button_sets.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/buttons/set.go
  - internal/buttons/set_test.go
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/destinations/destinations.go
  - internal/destinations/write.go
  - internal/destinations/query.sql
  - internal/destinations/write_test.go
  - internal/destinations/test.go
  - internal/destinations/test_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/delivery/delivery.go
  - internal/delivery/enqueue.go
  - internal/delivery/membership.go
  - internal/delivery/rerender.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/query.sql
  - internal/messages/render.go
  - internal/messages/default.go
  - internal/messages/destinationtest_test.go
  - internal/slack/layout_test.go
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - internal/groups/query.sql
  - test/e2e/button_sets_test.go
  - web/src/test/fixtures/destinations.ts
  - docs/concepts/statuses-and-commands.md
acceptance:
  - "[C-28.FR-8, C-28.FR-10] `getOrganization` carries `buttons` `{\"all\": true}` on a new database and after the migration; `updateOrganization` takes `{\"all\": false, \"items\": [...]}` whose items are `{\"button\": \"acknowledge\"}`, `{\"button\": \"resolve\"}`, `{\"button\": \"snooze\", \"snooze_seconds\": N}` or `{\"button\": \"snooze_preset\", \"preset\": \"tomorrow\" | \"week_start\"}`, records the change in the diff of `organization.updated` and keeps the set when the field is omitted; a duplicate item, a `snooze` without `snooze_seconds` above 0 and a `snooze_preset` without `preset` are refused with 422 (`duplicate`, `required`, `out_of_range`) at the item's pointer (`organization_test.go`)."
  - "[C-28.FR-8, C-28.FR-10] Every messenger Destination — Mattermost, Telegram, Bridge and Slack — reads `buttons` `{\"source\": \"organization\"}` until an update sends `{\"source\": \"own\", \"items\": [...]}`, with the same items and refusals; the change is in the diff of `destination.updated`, and an update with a stale `If-Match` gets 412 (`write_test.go`)."
  - "[C-28.FR-8, C-28.AC-7] On a Route with the Snooze durations 1 h, 4 h and 24 h, a Mattermost Destination whose own set is Acknowledge, `snooze` 86400 and `snooze_preset` `tomorrow` shows on a firing Root message exactly Ack, \"Snooze 24 h\" and \"Until tomorrow 09:00\", in the order of reference.md, and on an acknowledged one Unack, \"Snooze 24 h\" and \"Until tomorrow 09:00\"; a snoozed one shows Unsnooze, which is not part of the set, with Ack; a second Destination of the same Route that follows the Organization shows every button (`render_test.go`, `button_sets_test.go`)."
  - "[C-28.FR-9, C-28.AC-7] Within that Destination's Thread a Reminder still carries \"Still on it\" and \"Unack\", a press on a button of the full set sent earlier still runs its Command, and `resolve` from the API still works; with a set that has no Snooze button, the Slack Root message has no menu \"Snooze…\" (`layout_test.go`), and `testDestination` of a Destination with its own set sends exactly the buttons of the set (`destinationtest_test.go`, `test_test.go`)."
  - "[C-28.FR-8, C-28.FR-9, C-28.AC-7] With the Organization's set left `all`, a Snooze duration of 2 h added to the Route is offered on the next re-render of a Destination that follows the Organization; a saved change of a Destination's set writes an `open_group_updates` row of the kind `destination_buttons`, and one of the Organization's set a row of the kind `organization_buttons`, whose run re-renders the open Root messages of the Destinations that use the set within a few seconds, Quietly, as edits; a second run of the row changes nothing (`updates_test.go`, `button_sets_test.go`)."
  - "[C-10.AC-23, C-16.FR-4, C-20.FR-1] A Root message of a Paging Route still shows \"Until tomorrow 09:00\" and \"Until Monday 09:00\" after \"Snooze 24 h\" in a Destination that follows the Organization's `all`; the test message and the preview of a Destination show the buttons of its set."
  - "The documentation page Statuses and commands has the section \"Button sets\": which buttons a Root message shows from a set, that Unsnooze always shows, and that Reminders, Text commands, the UI and the API keep every action; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 402
---

# S-113. Button sets of the Organization and the Destinations (BE)

## Scope

**IN**

- The Organization's button set (`organization.buttons`, by default all buttons) and a Destination's own set or "As in
  the Organization" (`destination.buttons`), stored, read and written through the API with optimistic locking and the
  Audit log (C-28.FR-8, FR-10).
- Root messages that show only the buttons of their Destination's set, in the order of reference.md, Unsnooze always;
  test messages and previews alike (C-28.FR-9, C-16.FR-4 revised).
- The re-render of the open Root messages of the Destinations that use a changed set, through `open_group_updates`.
- The documentation of button sets.

**OUT**

- The Buttons section of the Destination form with its preview, and the Organization's button set on Alert handling:
  S-114.
- The Mentions of a Destination: S-140 and S-143.
- The compact layout, which places the buttons after the notices: S-125.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - New schemas: `ButtonSetItem` {`button` — `acknowledge` (Acknowledge with Unacknowledge), `resolve`, `snooze`,
    `snooze_preset`; `snooze_seconds` (integer ≥ 1, required with `snooze`); `preset` (`SnoozePreset`, required with
    `snooze_preset`)}; `OrganizationButtonSet` {`all` (boolean, required), `items` (`ButtonSetItem`[], required when
    `all` is false, ignored otherwise)}; `DestinationButtonSet` {`source` — `organization` or `own` (required);
    `items` (required with `own`)}. A deliberate choice is a field of its own, as `api/README.md` asks, so neither
    "all" nor "As in the Organization" is an absent value.
  - `OrganizationBase.buttons` (`OrganizationButtonSet`, optional: omitted on an update keeps it); `Organization` lists
    it in its `required`.
  - Each `*DestinationInput` of a messenger (Mattermost, Telegram, Bridge, Slack) gains `buttons`
    (`DestinationButtonSet`, optional: omitted on create is `organization`, omitted on update keeps it), and each of
    their read schemas lists it in its `required`. An outgoing webhook has no buttons and no field (Open questions).
  - Refusals, `422` `validation-failed` at the item's pointer: `duplicate` (the same `button`, `snooze_seconds` or
    `preset` twice), `required` (`snooze_seconds` or `preset` missing, `items` missing with `own` or `all: false`),
    `out_of_range` (`snooze_seconds` below 1). An item of a duration that no Route has is accepted and simply never
    shown.
- **Schema** (migration `0023_button_sets`, expand only; `schema.md` §4.2 and §4.5): `organizations.buttons jsonb NOT
  NULL DEFAULT '{"all": true}'` with `CHECK (jsonb_typeof(buttons) = 'object')`; `destinations.buttons jsonb` (null =
  "As in the Organization") with `CHECK (buttons IS NULL OR jsonb_typeof(buttons) = 'array')`; the CHECK of
  `open_group_updates.kind` is replaced by `open_group_updates_kind_v2_check`, which lists every kind present —
  `route_minimum_severity`, `destination_buttons` and `organization_buttons` — and a CHECK that `destination_buttons`
  names a Destination (new nullable column `open_group_updates.destination_id`), all `NOT VALID` and validated in a
  second transaction, as 0018 does. The down migration drops the columns and restores the kind CHECK.
- **Buttons** (`internal/buttons/set.go`): `Set` — all, or a list of items — with `Shows(Button, durations)`:
  Acknowledge and Unacknowledge by `acknowledge`, Resolve by `resolve`, a duration button when its value is in the set,
  a preset button when its preset is; Unsnooze and the Reminder's buttons always. `ForStatus` keeps its order and the
  filter keeps it too, so the order of reference.md stays.
- **Delivery** (`internal/delivery`): `Destination` gains `Buttons`; `desiredOf` drops from the Root message of each
  Destination the buttons its effective set (its own, else the Organization's) does not show, before the Desired
  state's hash, so a set that changes nothing changes no hash; Thread replies, Storm summaries and Reminders are not
  filtered.
- **Messages** (`internal/messages`): `Source` gains the button set of a test or preview, which `TestRoot` and the
  preview apply; `destinations/test.go` fills it from the Destination under test, so a test message shows its set
  (C-16.FR-4).
- **Re-render**: a save that changes a Destination's set inserts an `open_group_updates` row `destination_buttons` with
  the Destination, and one that changes the Organization's set a row `organization_buttons`; the worker of S-111 gains
  the two kinds: for each open Alert Group with a delivery in the Destination — or in any Destination that follows the
  Organization — it runs the dispatcher's re-render step without a lifecycle event, which edits the Root messages
  whose Desired state changed, Quietly. Safe to run twice: the second run finds every hash unchanged.
- **Audit log and live updates**: `organization.updated` and `destination.updated` carry `/buttons` in their diffs; the
  `organization` and `destination` hints as for every field.
- **Web test fixtures** (`web/src/test/fixtures/destinations.ts`, S-166): the builders of Destination reads set the
  neutral value of `buttons` (`{"source": "organization"}`), so `make generate` leaves the TypeScript check of the web
  tests green.
- **Settings**: `organization.buttons`, `destination.buttons`, `route.snooze_durations`; reference.md "Buttons and
  links by status" already holds the revised rule.
- **Log events, metrics**: none new; the worker's `open_group_update_done` names the new kinds.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up on PostgreSQL 14 and 17;
   `psql "$MUSTER_DATABASE_URL" -Atc "SELECT buttons FROM organizations"` prints `{"all": true}`.
2. Write `Set` and its filter. Check: `set_test.go` covers every status, a set without durations, a Route without
   durations (no presets) and the order.
3. Add the fields to the Organization and the Destinations with their validation, diff and spec. Check:
   `make generate-check`; `organization_test.go` and `write_test.go`.
4. Filter in `desiredOf`, the test message and the preview; add the two kinds to the worker. Check: `render_test.go`,
   `destinationtest_test.go`, `test_test.go`, `layout_test.go`, `updates_test.go`.
5. Write `test/e2e/button_sets_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-082: the Admin's Personal access token A, the Integration "lab" with FIRE and ADV, the Paging policy in P, the
# Route "t" with the Mattermost Destinations "alerts" (D, channel ch-alerts) and "alerts-prod" (D2, channel ch-alerts-prod).
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake
MMROOT() { curl -s $FMM/posts | jq -c --arg g "$1" --arg c "$2" '[.[] | select(.root_id == "" and .channel_id == $c and
  (.props.attachments[0].title_link | test($g)))] | last | [.props.attachments[0].actions[].name]'; }
DPUT() { curl -s "${A[@]}" -X PUT -H "If-Match: $(curl -s "${A[@]}" $API/destinations/$1 | jq -r .etag)" $API/destinations/$1 \
  -d "$(curl -s "${A[@]}" $API/destinations/$1 | jq -c "del(.id, .etag, .health, .routes, .created_at, .team_name,
  .channel_name, .template_error) | .buttons = $2")"; }

curl -s "${A[@]}" $API/organization | jq -c .buttons                                                       # {"all":true}
curl -s "${A[@]}" $API/destinations/$D | jq -c .buttons                                                   # {"source":"organization"}

# C-28.FR-8, C-28.AC-7: the own set of "alerts"
DPUT $D '{"source":"own","items":[{"button":"acknowledge"},{"button":"snooze","snooze_seconds":86400},
  {"button":"snooze_preset","preset":"tomorrow"}]}' | jq -c .buttons.source                                  # "own"
G=$(FIRE b1 DiskFull t); sleep 1
MMROOT $G ch-alerts      # ["Ack","Snooze 24 h","Until tomorrow 09:00"]
MMROOT $G ch-alerts-prod # ["Ack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h","Until tomorrow 09:00","Until Monday 09:00"]
curl -s "${A[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
MMROOT $G ch-alerts      # ["Unack","Snooze 24 h","Until tomorrow 09:00"]

# C-28.FR-9: the Reminder keeps its buttons; the API keeps Resolve
ADV 14400; sleep 1
curl -s $FMM/posts | jq -c '[.[] | select(.root_id != "" and .channel_id == "ch-alerts" and ((.props.attachments[0].actions // []) | length) == 2)] | last | [.props.attachments[0].actions[].name]'
# ["Still on it","Unack"]

# the re-render after a change of the set: Resolve appears in the open Root message, as an edit
DPUT $D '{"source":"own","items":[{"button":"acknowledge"},{"button":"resolve"}]}' > /dev/null; sleep 2
MMROOT $G ch-alerts      # ["Unack","Resolve"]
curl -s $FMM/requests | jq -r '[.[] | select(.method == "PUT")] | length > 0'                              # true
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM open_group_updates"                                 # 0

# the Organization's set
O=$(curl -s "${A[@]}" $API/organization)
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c 'del(.id, .etag) |
  .buttons = {"all":false,"items":[{"button":"acknowledge"},{"button":"snooze","snooze_seconds":86400}]}' <<<"$O")" > /dev/null; sleep 2
MMROOT $G ch-alerts-prod # ["Unack","Snooze 24 h"]
curl -s "${A[@]}" -X PUT -H "If-Match: $(curl -s "${A[@]}" $API/organization | jq -r .etag)" $API/organization \
  -d "$(curl -s "${A[@]}" $API/organization | jq -c 'del(.id, .etag) | .buttons = {"all":false,"items":[{"button":"resolve"},{"button":"resolve"}]}')" \
  | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'           # {"status":422,"p":"/buttons/items/1","c":"duplicate"}

# C-16.FR-4: the test message shows the set
curl -s "${A[@]}" -X POST $API/destinations/$D/tests -d '{}' > /dev/null; sleep 1
curl -s $FMM/posts | jq -c '[.[] | select(.channel_id == "ch-alerts" and (.props.attachments[0].text // "" | test("Test message")))] | last | [.props.attachments[0].actions[].name]'
# ["Ack","Resolve"]
```

`test/e2e/button_sets_test.go` repeats these checks, adds a Snooze duration of 2 h to the Route with the Organization's
set `all` and finds "Snooze 2 h" after the next re-render, and checks a Telegram Destination's keyboard the same way.

## Open questions

1. **A button set on outgoing webhooks?** Settled by the maintainer (review of PR #399): no. An outgoing webhook sends
   no buttons in either mode, so it has no `buttons` field and the form of S-114 shows no Buttons section for it;
   C-28.FR-10 now gives the button set to the form of every messenger type. The contract above follows it.

## Notes

- Suggested commit: `feat(destinations): choose the buttons that root messages show`.
- C-28.FR-8 names the set's entries by their meaning; the API names them by `button`, with the value of a duration in
  seconds as `route.snooze_durations` does, so the form can offer the distinct durations of the Organization's Routes
  and a set can name a duration that no Route has yet.
- A press on a button that a set no longer shows still runs its Command: the action id stays valid, and the Command is
  checked as any other (C-28.FR-9).
- Depends on S-111 for `open_group_updates`; it needs neither Teams nor Schedules.
- Migration `0023`: the story map gave it `0022`, which S-115 (`0022_teams`) took by merging first, so it takes the
  next free number as §4 of the stories README says; a story merged out of that order renumbers its migration to the
  next free number in its own pull request and re-versions the CHECKs it replaces, listing every value present
  when it merges (S-131's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-8 | partial | the sets in the API and the filter; the forms are S-114 |
| C-28.FR-9 | full | |
| C-28.FR-10 | partial | the buttons in the API, with the Audit log and locking; the form is S-114, the Mentions S-140 and S-143 |
| C-28.AC-7 | full | |
| C-10.AC-23 | partial | revised for L2: the buttons of the Destination's set; the Paging durations are S-162 |
| C-16.FR-4 | partial | revised for L2: the Destination's buttons in tests and previews; the compact layout is S-125 |
| C-20.FR-1 | partial | revised for L2: the Organization's button set in the API; the page is S-114 |
