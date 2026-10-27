---
id: S-125
title: The compact message layout in Mattermost, Slack and Telegram, with "Labels in the message" (BE)
capability: C-29
kind: be
layer: L2
depends_on: [S-113, S-118]
covers: [C-29.FR-1, C-29.FR-2, C-29.FR-3, C-29.FR-4, C-29.FR-5, C-29.FR-6, C-29.FR-7, C-29.FR-9, C-29.AC-1, C-29.AC-2, C-29.AC-3, C-29.AC-4, C-29.AC-5, C-25.FR-6, C-12.FR-1, C-12.FR-2, C-12.FR-3, C-12.FR-11, C-13.FR-3, C-14.FR-16, C-24.FR-4, C-16.FR-4, C-08.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0028_route_message_labels.up.sql
  - internal/db/migrations/0028_route_message_labels.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/routing/routes.go
  - internal/routing/labelsuggestions.go
  - internal/routing/labelsuggestions_test.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/messages/message.go
  - internal/messages/default.go
  - internal/messages/default_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/shorten.go
  - internal/messages/shorten_test.go
  - internal/messages/preview.go
  - internal/messages/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/api/templates.go
  - internal/api/templates_test.go
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - test/e2e/compact_layout_test.go
  - test/e2e/mattermost_test.go
  - test/e2e/telegram_test.go
  - test/e2e/slack_test.go
  - test/e2e/bridge_test.go
  - docs/configure/messages-and-templates.md
  - docs/install/versions.md
acceptance:
  - "[C-29.FR-1, C-29.AC-1, C-25.FR-6, C-12.FR-1, C-29.FR-4, C-13.FR-3, C-14.FR-16, C-24.FR-4] The Root message of a firing Alert Group of three `KubernetesContainerOOMKilled` Alerts with three `pod` values, on a Route with the Group key `alertname`, `system`, `team`, shows in order: the title `#N` with the Alert Group's title linked to its page and the status as a word, \"Firing\", with the status colour; the fields Severity, Route, Teams and Started (absolute, in the Organization's time zone); the summary and the description as plain text; `system=kubernetes · team=payments · namespace=checkout`; the line \"pod:\" with the three values; the notices; the footer; the buttons of the Destination's set; and the links, \"Open in Muster\" first — in Mattermost as the attachment's short `fields`, in Slack as a `section` with `fields`, in Telegram as lines \"{name}: {value}\" (`default_test.go`, `layout_test.go` of each adapter, `compact_layout_test.go`)."
  - "[C-29.FR-2, C-29.AC-2, C-08.FR-1] `route.message_labels` defaults to the labels of the Group key without `alertname` and the severity label, plus the Team label and `namespace`, computed from the current settings until the Route has its own list (`own_message_labels`); a label is shown as `name=value` when every firing Alert has that value, as `name: 2 values` when they differ, and not at all when no Alert has it; on a Route whose Group key holds `severity` the line has no `severity`; one Alert shows no Alerts line (`default_test.go`)."
  - "[C-29.FR-3, C-29.AC-3] The Alerts are named by their distinguishing label — the first of `organization.instance_labels` whose values differ, else the first differing label by name — with up to `message.compact_alerts_listed` values, firing ones first and resolved ones struck through, then \"…and K more\": twelve Alerts list five values and \"…and 7 more\" (`default_test.go`)."
  - "[C-29.FR-6, C-29.AC-3, C-12.FR-11] Rendered with a limit of 4,096 characters and a long description and summary, the message is cut in the order description, Alerts, summary, key labels, and always keeps the title, the status, the fields, the people, the buttons and the link to Muster; an Alert Group whose Alerts have only a `description` shows it, and one with neither annotation shows no text (`shorten_test.go`)."
  - "[C-29.FR-5, C-29.AC-5, C-12.FR-2] A Route with its own Root message template keeps its body in place of items 3 to 5 under Muster's title, fields, notices, people, buttons and links; a line template renders each listed Alert on a line of its own; the Fallback template keeps every label (`render_test.go`)."
  - "[C-29.FR-2, C-29.AC-4] Adding `cluster` to a Route's `message_labels` writes an `open_group_updates` row of the kind `route_message_labels`, whose run edits the Route's open Root messages Quietly to show it; `listRouteLabelSuggestions` (`routes:read`) offers the labels of the Route's recent Alert Groups with an example value each (`updates_test.go`, `labelsuggestions_test.go`)."
  - "[C-29.FR-7, C-16.FR-4, C-12.FR-3] The default layout replaces that of L1 for every Route from 0.2.0 on, in Root messages, test messages and `previewTemplate`, whose result for `root_message` also carries `layout`, the structured content of the message, and `length` against `messenger_limit`; Thread replies and Storm summaries keep their forms; the built-in texts of the layout exist in English and Russian; the upgrade notes of 0.2.0 say that the layout changes (`templates_test.go`)."
  - "[C-29.FR-9] The documentation page Messages and templates shows the new default message and has the section \"Labels in the message\"; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 414
---

# S-125. The compact message layout in Mattermost, Slack and Telegram, with "Labels in the message" (BE)

## Scope

**IN**

- The default Root message of C-29.FR-1 in the renderer — title with the status as a word, the fields, `summary` and
  `description`, the key labels, the Alerts by their distinguishing label, the notices, the footer, the buttons and the
  links — and its layout in Mattermost, Slack and Telegram (C-29.FR-4).
- The Route setting "Labels in the message" (`route.message_labels`) with its computed default, the suggestions from
  recent Alert Groups, and the re-render of the open Root messages on a change (C-29.FR-2).
- Route templates within the layout, the shortening order, the new layout in test messages and previews, and the
  upgrade note (C-29.FR-5 to FR-7).
- The section of the messages page that describes the default message and "Labels in the message".

**OUT**

- `layout` in the Bridge protocol, the Bridge adapter and the fake bridge: S-126; the bridge libraries: S-127, S-128.
- "Labels in the message" on the Route form and the previews for every markup on the page: S-129.
- The Assignees and the Owner's Team in item 7, with "· Escalation chains stopped": S-168. Until then item 7 is the
  footer of L1.
- The messages page with the layout per messenger: S-130. The visual editor of the layout: later in L2.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `RouteInput` gains `own_message_labels` (boolean, optional, default false) and `message_labels` (string[],
    required when `own_message_labels`); `Route` requires both, `message_labels` being the effective list (computed
    while not own).
  - `listRouteLabelSuggestions` (`GET /routes/{route_id}/label-suggestions`, tag Routes, `routes:read`) →
    `RouteLabelSuggestionList` {`items` — {`label`, `example_value`, `alert_group_count`}}, from the common labels of
    the Route's Alert Groups of `routing.group_key_preview_period`, most frequent first, at most 50.
  - New schema `MessageLayout` — `number`, `title`, `status` {`key`, `word`}, `fields` ({`name`, `value`}[], `summary`,
    `description`, `labels` ({`key`, `value`}[]), `alerts` {`label`, `values` ({`value`, `resolved`}[]), `total`},
    `notices` (string[]), `assignees` and `owner` (people {`name`, `ref`, and the optional `team` {`name`}}), `links`
    ({`label`, `url`}[]) — the same content S-126 puts into `api/bridge.yaml`; `TemplatePreviewResult` gains `layout`
    (for `root_message`), `length` and `messenger_limit`.
- **Schema** (migration `0028_route_message_labels`, expand only; `schema.md` §4.8): `routes.message_labels text[]`
  (null = computed); `open_group_updates_kind_v5_check` lists every kind of `_v4` and `route_message_labels` (`NOT
  VALID`, validated, the old one dropped).
- **Messages** (`internal/messages`): `Message` gains `StatusWord`, `Fields`, `Description`, `KeyLabels`, `AlertsLine`
  (the distinguishing label, its values with their state, the total) and keeps `Footer`, `Notices`, `Buttons`,
  `Links`; the default body no longer fills `Environment`, `GroupLabels`, `CommonLabels` and `CommonAnnotations`, which
  the Fallback template keeps; `Source` gains the Teams (S-118), the route name, the description, the key labels and the
  effective `message_labels`; `shorten.go` cuts in the order of C-29.FR-6; the preview renders the layout per markup
  with its length and the adapter's limit. The distinguishing label follows C-29.FR-3 (provisional, P-67) and the count
  `message.compact_alerts_listed` (P-83). Built-in texts: the status words, the field names "Severity", "Route",
  "Teams", "Started", "{label}: {n} values", "…and {k} more", in English and Russian.
- **End-to-end tests of L1** that assert the L1 layout assert the compact one instead: `mattermost_test.go` (the
  shortened Root message of 600 Alerts, "+590 more", "+27 more" and "Full list in Muster"), `telegram_test.go` (the
  labels in `<blockquote expandable>`), `slack_test.go` (the blocks `section`, `section`, `context`, `actions`) and
  `bridge_test.go` (the Alert lines and "+30 more" of `TestBridgeBodyLimit`'s body).
- **Adapters**: Mattermost — the one attachment of C-13.FR-3 with the title, the four fields as short `fields`, items 3
  to 7 as its text, the links as its last line, the buttons after it; Slack — the blocks of C-24.FR-4, the fields as one
  `section`'s `fields`, the key labels and the people as `context` blocks, the buttons as `actions`; Telegram — one HTML
  message with the fields as lines "{name}: {value}" and the keyboard of C-14.FR-3. Each keeps the status colour of
  C-12.FR-1 (or its emoji where it has no colour) and escapes values for its markup.
- **Re-render**: a change of `message_labels` (or of the Group key while the list is computed) inserts an
  `open_group_updates` row `route_message_labels`, whose handler re-renders the Route's open Alert Groups without a
  lifecycle event, as S-113's kinds do.
- **Settings**: `route.message_labels`, `message.compact_alerts_listed` (P-83), `organization.instance_labels`,
  `organization.severity_label`, `organization.team_label`, `organization.time_zone`, `message.value_cap`; the limits
  of the adapters (C-13, C-14, C-24).
- **Log events, metrics**: none new.
- **Documentation**: Messages and templates — the default message as it is now, and "Labels in the message";
  `docs/install/versions.md` — the new layout in the upgrade notes of 0.2.0.

## Steps

1. Write the migration, `schema.md`, the Route fields and the suggestions. Check: `make test-integration`;
   `routes_test.go`, `labelsuggestions_test.go`; `make generate-check`.
2. Render the layout. Check: `default_test.go` covers every item, the default labels, `N values`, the distinguishing
   label with and without Instance labels, twelve Alerts and a Route template; `shorten_test.go` covers the cut order.
3. Lay it out in Mattermost, Slack and Telegram. Check: each `layout_test.go` against the fixtures of C-29.AC-1.
4. Add the preview's layout and length, and the re-render kind. Check: `templates_test.go`, `updates_test.go`.
5. Write `test/e2e/compact_layout_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-118: A, the Integration "sev" with NOTIFY, PUTA and AG, and the Team "payments"; as in S-082: the Paging
# policy P, the Mattermost Destination D (channel ch-alerts of the fake Mattermost server) and the Telegram
# Destination TD (channel CH of the fake Telegram server), with FMM and FTG.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
R=$(curl -s "${A[@]}" $API/routes -d "{\"name\":\"Infrastructure\",\"matchers\":[{\"label\":\"category\",\"op\":\"=\",\"value\":\"infra\"}],
  \"group_key\":[\"alertname\",\"system\",\"team\"],\"destination_ids\":[\"$D\",\"$TD\"],\"policy\":$P}" | jq -r .id)
curl -s -X PUT $FAM/groups/c1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"KubernetesContainerOOMKilled"}}' > /dev/null
for p in checkout-api-7d9f4 checkout-api-5b2c1 checkout-worker-8e6a0; do PUTA c1 $p "{\"labels\":{\"category\":\"infra\",
  \"system\":\"kubernetes\",\"team\":\"payments\",\"namespace\":\"checkout\",\"cluster\":\"prod\",\"pod\":\"$p\",\"severity\":\"warning\"},
  \"annotations\":{\"summary\":\"Container killed for memory\",\"description\":\"The container exceeded its limit.\"}}"; done
NOTIFY c1 '{"reason":"first notification"}'; G=$(AG 'namespace%3D%22checkout%22'); sleep 1
curl -s $FMM/posts | jq -c --arg g "$G" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g)))] | last
  | .props.attachments[0] | {title, f: [.fields[] | .title], t: (.text | split("\n") | map(select(. != "")))}'
# {"title":"#… KubernetesContainerOOMKilled — Firing","f":["Severity","Route","Teams","Started"],
#  "t":["Container killed for memory","The container exceeded its limit.","system=kubernetes · team=payments · namespace=checkout",
#       "pod: checkout-api-5b2c1, checkout-api-7d9f4, checkout-worker-8e6a0","[Open in Muster](…)"]}
curl -s "$FTG/messages?chat=$CH" | jq -r '[.[] | select(.text | test("KubernetesContainerOOMKilled"))] | last | .text' | head -6
# #… KubernetesContainerOOMKilled — Firing / Severity: warning / Route: Infrastructure / Teams: payments / Started: …

# C-29.AC-4: cluster joins the labels; the open Root message is edited
E=$(curl -s "${A[@]}" $API/routes/$R | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/routes/$R -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c 'del(.id, .etag, .position,
  .is_default, .destinations, .storm_active, .open_alert_group_count, .created_at, .urgent, .owning_team)
  | .own_message_labels = true | .message_labels = ["system","team","namespace","cluster"]')" > /dev/null; sleep 2
curl -s $FMM/posts | jq -r --arg g "$G" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g)))] | last
  | .props.attachments[0].text | test("cluster=prod")'                                                     # true
curl -s "${A[@]}" $API/routes/$R/label-suggestions | jq -c '[.items[] | .label] | index("pod") != null'      # true
curl -s "${A[@]}" $API/template-previews -d "{\"kind\":\"root_message\",\"template\":\"\",\"format\":\"html\",\"route_id\":\"$R\",
  \"alert_group_id\":\"$G\",\"length_limit\":4096}" | jq -c '{n: .layout.number, t: .layout.title, f: [.layout.fields[].name], length: (.length > 0), messenger_limit}'
# {"n":…,"t":"KubernetesContainerOOMKilled","f":["Severity","Route","Teams","Started"],"length":true,"messenger_limit":4096}
```

`test/e2e/compact_layout_test.go` repeats these checks in the three messengers, adds twelve Alerts with "…and 7 more",
two `namespace` values, a Route template, and the long description cut first.

## Open questions

None.

## Notes

- Suggested commit: `feat(messages): lay out root messages compactly with labels chosen per route`.
- The approved mockup has the field "Alerts firing/total" and `category` among the default labels; C-29 keeps the four
  fields and the default labels of journal D543 (L2.md §5.2), and so does this contract.
- The status emoji of L1 stays beside the word in Telegram (L2.md §5.2).
- The distinguishing label (P-67) and the count of values (P-83) stay provisional; the test environment confirms them,
  not this story.
- With 41 files the story is at the edge of the size signal: four of them are end-to-end tests of L1 that asserted
  the old layout, which the review of the contracts added.
- `MessageLayout` in `openapi.yaml` is the content S-126 sends to bridges, so the preview shows a bridge author the same
  structure.
- Migration `0028` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request and re-versions the CHECKs it replaces, listing every value present
  when it merges (S-131's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-29.FR-1 | partial | Mattermost, Slack and Telegram; Bridges S-126, the Assignees and the Owner's Team S-168 |
| C-29.FR-2 | partial | the setting, suggestions and re-render; the form is S-129 |
| C-29.FR-3 | full | |
| C-29.FR-4 | partial | Mattermost, Slack and Telegram; the Bridge envelope is S-126 |
| C-29.FR-5 | full | |
| C-29.FR-6 | partial | the renderer and three adapters; the Bridge layout's cut is S-126 |
| C-29.FR-7 | full | |
| C-29.FR-9 | partial | the section on the default message and the labels; the layout per messenger and the bridge guide are S-130 |
| C-29.AC-1 | full | |
| C-29.AC-2 | full | |
| C-29.AC-3 | full | |
| C-29.AC-4 | full | |
| C-29.AC-5 | full | |
| C-25.FR-6 | partial | the field "Teams" of the Root message |
| C-12.FR-1 | partial | revised for L2: the compact layout; Bridges S-126 |
| C-12.FR-2 | full | revised for L2 |
| C-12.FR-3 | partial | revised for L2: the built-in texts of the layout; those of C-28, C-30 and C-32 come with them |
| C-12.FR-11 | full | revised for L2 |
| C-13.FR-3 | full | revised for L2 |
| C-14.FR-16 | full | revised for L2 |
| C-24.FR-4 | full | revised for L2 |
| C-16.FR-4 | partial | revised for L2: the layout in tests and previews; the buttons are S-113 |
| C-08.FR-1 | partial | revised for L2: "Labels in the message" |
