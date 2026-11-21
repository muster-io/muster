---
id: S-215
title: The preview of an unsaved message layout on an Alert Group that arrived, with the payload of each messenger (BE)
capability: C-38
kind: be
layer: L2
depends_on: [S-211, S-212, S-129, S-113, S-036]
covers: [C-38.FR-10, C-38.FR-11, C-38.FR-12, C-38.FR-13, C-38.AC-6, C-38.AC-7, C-38.AC-8]
files_touched:
  - api/openapi.yaml
  - internal/messages/preview.go
  - internal/messages/preview_test.go
  - internal/messages/shorten.go
  - internal/messages/shorten_test.go
  - internal/mattermost/preview.go
  - internal/mattermost/preview_test.go
  - internal/slack/preview.go
  - internal/slack/preview_test.go
  - internal/telegram/preview.go
  - internal/telegram/preview_test.go
  - internal/bridge/preview.go
  - internal/bridge/preview_test.go
  - internal/api/templates.go
  - internal/api/templates_test.go
  - test/e2e/layout_preview_test.go
acceptance:
  - "[C-38.FR-10, C-38.FR-12] `previewTemplate` for `root_message` takes an unsaved `message_layout` and a `destination_id` and returns, besides `output`, `length` and `messenger_limit`, the messenger payload of the requested `format` — the Mattermost attachment, the Slack blocks, the Telegram HTML with the inline keyboard, the Bridge body with its `layout` and `buttons` — the number of Slack blocks, and the blocks that shortening cut; the buttons are those of the chosen Destination's set or, without one, of the Organization's set; nothing is sent and no Desired state or `open_group_updates` row changes (`preview_test.go`, `templates_test.go`, `test/e2e/layout_preview_test.go`)."
  - "[C-38.FR-10, C-38.AC-7] An `alert_group_id` of another Route renders in this Route's language and layout, with the Alert Group's current status, Alerts, Owner or Assignees, notices and links, and with every escape of the messenger's markup; any Alert Group of the Organization may be named (`preview_test.go`, `test/e2e/layout_preview_test.go`)."
  - "[C-38.FR-11, C-38.AC-7] On a Route with no Alert Group, and without an `alert_group_id`, the preview renders the built-in example and `sample` is `example`; with one, `sample` is `alert_group` (`preview_test.go`)."
  - "[C-38.FR-12, C-38.AC-8] The result gives the length against each messenger's limit — 16,383 characters for Mattermost, 4,096 for Telegram, 50 blocks and 3,000 characters in each `section` for Slack, and for a Bridge the `limits.max_body_length` of the chosen Bridge Destination's Connection or none; a message over the limit with the description shown reports the description cut first, and the same message with the description hidden reports the Alert list cut first; a fake bridge that declares a `max_body_length` of 1,000 gives that `messenger_limit` (`shorten_test.go`, `preview_test.go`, `test/e2e/layout_preview_test.go`)."
  - "[C-38.AC-6] For a Route with a Root message template the preview's `layout` is null, as the bridge then receives none, and the body takes the place of the first of the text, key labels and Alert list blocks (`preview_test.go`)."
  - "[C-38.FR-13, C-38.AC-7] The operation needs `templates:preview`: a Responder's token gets `403`, an Admin's `200`; the preview has no switch to show the original of a value (`templates_test.go`, `test/e2e/layout_preview_test.go`)."
verify: "make ci e2e-go"
operator_attention: false
issue: null
---

# S-215. The preview of an unsaved message layout on an Alert Group that arrived, with the payload of each messenger (BE)

## Scope

**IN**

- `previewTemplate` for `root_message` with an unsaved `message_layout`, a `destination_id` and an `alert_group_id` of
  any Alert Group, and the result's payload, block count and cut blocks (C-38.FR-10 to FR-13).
- The exported render of each adapter's payload without sending, which the preview calls.

**OUT**

- The Route's recent Alert Groups and the lookup by number: the Alert Group list answers both with the filters it has
  (`route`, `status`, `number`); no new operation. The offered order — open ones first, then resolved ones, each newest
  first, at most `message.layout_preview_groups` — is the editor's (S-218), as two ordinary list calls.
- The editor and its tabs: S-217, S-218. The test message: S-216.
- The Redaction rules: see Notes.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `TemplatePreviewRequest` gains `message_layout`
  (`RouteMessageLayout`, S-210; the unsaved layout, honoured only for `root_message`) and `destination_id`
  (`NullablePublicId`: the Destination whose button set the message carries and, for a Bridge Destination, whose
  Connection's `limits.max_body_length` is the limit). `TemplatePreviewResult` gains `payload` — the messenger
  payload of the request's `format`: `mattermost_attachment` for `markdown`, `slack_blocks` for `mrkdwn`,
  `telegram_html` with `telegram_keyboard` for `html`, and `bridge_buttons` with the `layout` already returned for
  `commonmark` — `slack_block_count`, and `cut` (the blocks shortening removed: `description`, `summary`, `labels`,
  and the number of Alerts left out of the Alert list). `alert_group_id` is described as naming any Alert Group, whose
  Route need not be the request's `route_id`. `length`, `messenger_limit`, `sample` and `layout` keep their meaning
  (S-129).
- **Package** (`internal/messages/preview.go`): `Preview` builds the `Source` from the Alert Group (as `previewGroup`
  does) or from the built-in example, applies the request's unsaved layout in place of the stored one (S-211's
  `LayoutSettings`), renders for the format, and calls the adapter's payload builder; the Route's language is the
  request's `language` or the Route's, never the Alert Group's Route's. With no layout in the request the Route's
  effective one is used, so that the existing editor of the Route's template keeps working. `shorten.go` reports what
  it cut (S-211) and the preview passes it on.
- **Adapters** (`internal/mattermost/preview.go`, `slack/preview.go`, `telegram/preview.go`, `bridge/preview.go`): each
  exports the pure function that the delivery path already runs to build the post, the blocks, the HTML with its
  keyboard or the envelope's `MessageContent`, taking a `messages.Message` and the button set, and returns it without
  touching a Connection. Lint 3 is unaffected: nothing here calls `Publish`, `Update` or `Reply`. For the Bridge the
  limit comes from the Connection's cached `info` (C-22.FR-7); a Connection with no `info` yields none.
- **API handler** (`internal/api/templates.go`): maps the new fields, requires `templates:preview` as the operation
  already does, and checks that the `destination_id` names a Destination of the Organization; a Destination that does
  not deliver for this Route is accepted — the buttons are the Destination's set, as C-38.FR-10 says "a chosen
  Destination of the Route or of the Organization's set", and the editor offers only the Route's.
- **Registries**: no new log event, metric or Internal alert.

## Steps

1. Add the spec and regenerate. Check: `make generate-check lint`.
2. Export the payload builders of the four adapters, with their tests on the golden messages of S-211. Check: the four
   `preview_test.go`.
3. Teach `Preview` the unsaved layout, the Destination's buttons and the cut report. Check: `preview_test.go`,
   `shorten_test.go`.
4. Wire the handler. Check: `templates_test.go` (the `403`, the Destination of another Organization refused with
   `422`).
5. Write `test/e2e/layout_preview_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-211: A, the Route R with D (Mattermost), TD (Telegram), SD (Slack) and BD (the Example Bridge), Alert Group c1 (G) of three Alerts.
API=localhost:8080/api/v1; LAYOUT='{"order":["fields","text","alerts","labels"],"fields":[{"field":"severity"},{"field":"firing_total"}],"show_summary":true,
  "show_description":false,"show_labels":true,"show_alerts":true,"alerts_listed":5,"show_links":true}'
pv() { curl -s "${A[@]}" $API/template-previews -d "{\"kind\":\"root_message\",\"template\":\"\",\"route_id\":\"$R\",\"alert_group_id\":\"$G\",
  \"destination_id\":\"$1\",\"format\":\"$2\",\"message_layout\":$LAYOUT}"; }
pv $D markdown | jq -c '{s: .sample, len: .length, lim: .messenger_limit, f: [.payload.mattermost_attachment.fields[].title], b: [.payload.mattermost_attachment.actions[].name]}'
# {"s":"alert_group","len":…,"lim":16383,"f":["Severity","Firing / total"],"b":["Acknowledge","Resolve",…]}
pv $SD mrkdwn | jq -c '{blocks: .slack_block_count, lim: .messenger_limit}'                       # {"blocks":…,"lim":3000}
pv $TD html | jq -c '{lim: .messenger_limit, kb: (.payload.telegram_keyboard | length)}'              # {"lim":4096,"kb":…}
pv $BD commonmark | jq -c '{lim: .messenger_limit, order: .layout.order, b: (.payload.bridge_buttons | length)}'   # limit: the fake bridge's max_body_length
# the Organization's button set without a Destination, and an Alert Group of another Route in this Route's layout and language
curl -s "${A[@]}" $API/template-previews -d "{\"kind\":\"root_message\",\"template\":\"\",\"route_id\":\"$R\",\"alert_group_id\":\"$OTHER\",\"format\":\"markdown\",\"language\":\"ru\",\"message_layout\":$LAYOUT}" | jq -c '.payload.mattermost_attachment.title'   # in Russian
# C-38.AC-8: over the limit, the description shown reports it cut first; hidden, the Alert list first
curl -s "${A[@]}" $API/template-previews -d "{…twelve Alerts, \"length_limit\":600, \"message_layout\":{…show_description true…}}" | jq -c .cut   # {"description":true,…}
# C-38.AC-7: no Alert Group at all -> the example
curl -s "${A[@]}" $API/template-previews -d "{\"kind\":\"root_message\",\"template\":\"\",\"route_id\":\"$EMPTYROUTE\",\"format\":\"markdown\",\"message_layout\":$LAYOUT}" | jq -r .sample   # example
curl -s -o /dev/null -w '%{http_code}\n' "${RESP[@]}" $API/template-previews -d '{"kind":"root_message","template":""}'   # 403 for a Responder's token
# nothing was sent or changed
curl -s $FMM/requests | jq length   # unchanged by the previews; psql: select count(*) from open_group_updates   # unchanged
```

`test/e2e/layout_preview_test.go` repeats these, adds the Route with a Root message template (`layout` null, the body in
the place of the first of the three blocks) and the Bridge Destination whose fake bridge declares a `max_body_length` of
1,000.

## Open questions

- **Redaction rules (#373).** C-38.FR-13 has the preview apply the Redaction rules as delivery does and mark each
  replaced value, "from the Redaction rules on". They are not specified yet (L2 §1.5), so this story has nothing to
  call; the story that specifies them adds the preview to its files and its acceptance (the replaced values marked in
  the `output`, no switch to show the original). Confirm that C-38.FR-13 is complete with the permission rule and that
  clause is a later story's, not an `open` row here.

## Notes

- Suggested commit: `feat(messages): preview a message layout on any alert group with the payload of each messenger`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- The payload is built by the same functions as delivery's, not a second implementation, so a preview cannot drift from
  what is sent; the lint that guards sends (lint 3) is not involved.
- The preview of an Alert Group of another Route is "as if this Route had taken it": its Alerts, status and links are
  the Alert Group's, its layout, language and button set are the request's.
- The `cut` object tells the editor "Shortened: the description and 4 Alerts are cut" (C-38 UI) without a second render.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-10 | full | |
| C-38.FR-11 | partial | the example and the any-Route sample; the offered list and "Find by number" are S-218 |
| C-38.FR-12 | partial | the payload, the length and the cut blocks; the tabs are S-218 |
| C-38.FR-13 | full | the Redaction clause waits for the capability that specifies it (Open questions) |
| C-38.AC-6 | partial | the preview's `layout` null; the rendering is S-211 |
| C-38.AC-7 | partial | the other Route's Alert Group, the example and the `403`; the offered order is S-218, the Viewer's read S-210 |
| C-38.AC-8 | full | |
