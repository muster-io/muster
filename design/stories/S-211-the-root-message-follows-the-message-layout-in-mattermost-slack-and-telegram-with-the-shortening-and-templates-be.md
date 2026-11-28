---
id: S-211
title: The Root message follows the message layout in Mattermost, Slack and Telegram, with the shortening and the templates (BE)
capability: C-38
kind: be
layer: L2
depends_on: [S-210, S-209, S-125, S-168, S-113]
covers: [C-38.FR-1, C-38.FR-2, C-38.FR-3, C-38.FR-6, C-38.FR-7, C-38.FR-8, C-38.FR-14, C-38.AC-1, C-38.AC-2, C-38.AC-3, C-38.AC-5, C-38.AC-6, C-38.AC-11]
files_touched:
  - internal/messages/layoutsettings.go
  - internal/messages/layoutsettings_test.go
  - internal/messages/message.go
  - internal/messages/default.go
  - internal/messages/default_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/shorten.go
  - internal/messages/shorten_test.go
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/messages/query.sql
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/rerender_integration_test.go
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - test/e2e/route_message_layout_render_test.go
acceptance:
  - "[C-38.FR-1, C-38.AC-1] A Route without its own layout renders the Root messages of C-29.AC-1 byte for byte as before: the golden outputs of `default_test.go`, `layout_test.go` of the three adapters and `render_test.go` are unchanged, and no Root message is edited when the release is installed (`rerender_integration_test.go`)."
  - "[C-38.FR-2, C-38.FR-6, C-38.AC-2] A layout with the fields Severity, Started, \"Firing / total\" and the label `cluster`, the description hidden and the Alert list before the key labels renders for an Alert Group of three Alerts of which two fire: in Mattermost four short `fields` in that order with \"2 / 3\", no description, the Alert list above the key labels and no `cluster` among them; in Slack one `section` with those four `fields`; in Telegram four lines \"{name}: {value}\" (`default_test.go`, the three `layout_test.go`, `test/e2e/route_message_layout_render_test.go`)."
  - "[C-38.FR-2, C-38.AC-3] A label field whose label differs among the firing Alerts shows \"2 values\" and one no Alert has is left out, and a label shown as a field is left out of the key labels; a layout that hides every movable block and the other links still shows the title with the status, the notices, the people and the footer, the buttons and \"Open in Muster\"; in Mattermost the Fields block moved below the Text block still gives the attachment's `fields`, drawn as F-NNN (S-209) says (`default_test.go`, `mattermost/layout_test.go`, `test/e2e/route_message_layout_render_test.go`)."
  - "[C-38.FR-3] Shortening keeps its order whatever the layout — the description, then the Alert list, then the summary, then the key labels — never touches what FR-2 always shows or the fields, and does not count a hidden block against the limit (`shorten_test.go`)."
  - "[C-38.FR-8, C-38.AC-6] On a Route with a Root message template, the template's body takes the place of the first of the text, key labels and Alert list blocks in the layout's order, the fields and their order and the other links follow the layout, a line template still renders each listed Alert, the Fallback template keeps every label and ignores the layout, and `HasLayout` stays false for the first and the last (`render_test.go`, `layout_test.go`)."
  - "[C-38.FR-1, C-38.FR-6] A Team's Destination that a Route delivers to renders the Route's layout while keeping its own Mentions and button set; a Destination of the Organization's set keeps `organization.buttons` (`render_test.go`, `test/e2e/route_message_layout_render_test.go`)."
  - "[C-38.FR-7] Thread replies, Storm summaries and the Mentions of a Loud message are the same under any layout (`render_test.go`, `default_test.go`)."
  - "[C-38.FR-14, C-38.AC-5] Saving a layout edits the open Root messages of the Route once, Quietly and without Mentions, in the fake Mattermost and Telegram servers; a Root message the change does not alter is not edited; three saves before the re-render runs leave one pending row and one edit per message (`rerender_integration_test.go`, `test/e2e/route_message_layout_render_test.go`)."
  - "[C-38.AC-11] An Alert list of `alerts_listed` 3 lists three values and \"…and 9 more\" for twelve Alerts, in every messenger (`default_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-211. The Root message follows the message layout in Mattermost, Slack and Telegram, with the shortening and the templates (BE)

## Scope

**IN**

- The renderer taking the Route's effective message layout: the order of the four movable blocks, the fields of the
  catalogue (Severity, Route, Teams, Started, "Firing / total" and labels), the switches of the summary, description,
  key labels, Alert list and other links, and `alerts_listed` (C-38.FR-2).
- The shortening that keeps its order under any layout (C-38.FR-3), the templates' relation to the layout
  (C-38.FR-8), and the lay-out of each adapter in the layout's order where its messenger allows (C-38.FR-6).
- That a saved layout reaches the open Root messages through the re-render row that S-210 inserts, Quietly and once
  (C-38.FR-14).

**OUT**

- The storage, the API and the re-render row: S-210. The Bridge adapter's `layout` with `order`, the fake bridge and
  `muster bridge check`: S-212. The body of a bridge in the layout's order is rendered here, since it is the
  CommonMark markup of the same renderer; what S-212 adds is the `layout` envelope's `order` and hidden blocks.
- The preview and the Destination test taking an unsaved layout: S-215, S-216.

## Contracts

- **Settings object** (`internal/messages/layoutsettings.go`): `LayoutSettings`, the parsed form of
  `routes.message_layout`, with `Default(compactAlertsListed)` for a Route without its own (the layout of C-29.FR-1),
  read by the query of the Route's settings in `query.sql` (`org_id` in the query, lint 1) and carried on `Source`
  beside the Route's labels, so that the preview of S-215 and the test message of S-216 pass an unsaved one the same
  way. The package does not validate: `internal/routing` did (S-210); a stored layout that no longer parses, by an
  error of a later version, renders as Muster's default and logs the existing template-error event, never a failure of
  a delivery.
- **Renderer** (`default.go`, `message.go`, `render.go`): `compactBody` and `fields` take the settings; a field is a
  `Label` as before, one more entry in `Message.Fields`; "Firing / total" is "{firing} / {all}" of the Alert Group's
  Alerts; a label field takes the value rules of `keyLabels` (one value, "N values", left out when no Alert has it)
  and is removed from the key labels; Teams is left out when the Alert Group has none (C-38.FR-2). `Message` gains the
  order of the four blocks. A hidden block is not rendered and is not counted against a limit.
- **Shortening** (`shorten.go`): the cut order of C-29.FR-6 is unchanged — description, Alert list, summary, key
  labels — and a hidden block is skipped in it; the fields, the notices, the people, the buttons and the link to Muster
  are never cut. The result reports which blocks it cut, which S-215 returns to the editor.
- **Templates** (`render.go`, `layout.go`): a Route's `root_message` template renders the three blocks it replaces as
  one body at the place of the first of them in the layout's order; `HasLayout` is unchanged: false for a template, a
  line template and the Fallback (C-38.FR-8). `LayoutOf` gives the fields of the layout and an empty value for a hidden
  block — an empty list, an empty text, no Alert list — which S-212 puts on the wire; the order is added there.
- **Adapters**: `internal/mattermost/layout.go` — the fields as the attachment's short `fields` in their order, the
  other blocks as the attachment's text in the layout's order, the links its last line, the buttons after it; the
  fields stay under the text whatever the place of their block and after an edit, as F-148 records.
  `internal/slack/layout.go` — the fields as the `fields` of one `section` in their order, each other block in its
  order, key labels and people as `context` blocks, the buttons in an `actions` block, within the 10 `fields` that a
  `section` takes (P-94, F-149) and the limits of F-076.
  `internal/telegram/layout.go` — one HTML message, the fields as lines "{name}: {value}", the blocks in their order,
  the keyboard after it. Each keeps its own escaping of the markup (C-12.FR-7).
- **Delivery** (`internal/delivery/render.go`): the Root message is rendered with the Route's effective layout for every
  Destination the Route delivers to, a Team's Destinations included (C-26); the Destination keeps its Mentions and
  button set (C-28.FR-1, FR-8). The open-Alert-Group walk of the row `route_message_settings` reads the layout as it is
  then and edits only a Root message whose rendering changed — the existing comparison of the Desired state — Quietly,
  through each Destination's limiter, and never adds Mentions (ADR-0005).
- **Registries**: no new log event, metric or Internal alert; the existing template-error event covers a stored
  layout that does not parse.

## Steps

1. Write `layoutsettings.go`, read it with the Route's settings and carry it on `Source`. Check:
   `layoutsettings_test.go`; the golden tests of the default layout still pass byte for byte.
2. Teach `default.go` and `message.go` the fields, the order and the switches; teach `shorten.go` to skip hidden blocks
   and to report the cut ones. Check: `default_test.go`, `shorten_test.go`.
3. Add the relation to templates. Check: `render_test.go`, `layout_test.go`.
4. Lay out each adapter. Check: the three `layout_test.go`, each with the layouts of C-38.AC-2 and AC-3.
5. Wire delivery and the re-render. Check: `render_test.go`, `rerender_integration_test.go` (one edit, Quietly, none for
   an unchanged message).
6. Write `test/e2e/route_message_layout_render_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-125: A, the Integration "sev" with PUTA, NOTIFY and AG, the Team "payments", the Route "Infrastructure" R (policy P) with the
# Mattermost Destination D and the Telegram Destination TD (and the Slack Destination SD of S-097's setup), FMM, FTG and FSL.
API=localhost:8080/api/v1
# Alert Group c1 of three Alerts, two firing: KubernetesContainerOOMKilled, labels cluster=prod and cluster=staging on two of them
mm() { curl -s $FMM/posts | jq -c --arg g "$G" '[.[] | select(.root_id=="" and (.props.attachments[0].title_link | test($g)))] | last | .props.attachments[0]'; }
mm | jq -c '{f: [.fields[].title], t: (.text | split("\n") | map(select(. != "")))}'
# the default layout, as S-125 shows: {"f":["Severity","Route","Teams","Started"], ...}     (C-38.AC-1: unchanged)
# C-38.AC-2: the layout of the acceptance statement, saved with the update of S-210 ($L: order fields,text,alerts,labels; fields severity,
# started, firing_total, label cluster; show_description false), the open Root messages are edited within a few seconds
E=$(curl -si "${A[@]}" $API/routes/$R | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -s "${A[@]}" -H "If-Match: $E" -X PUT $API/routes/$R -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c --argjson l "$L" 'del(.id, .etag, .position) | .own_message_layout = true | .message_layout = $l')" > /dev/null; sleep 3
mm | jq -c '{f: [.fields[] | [.title, .value]], t: (.text | split("\n") | map(select(. != "")))}'
# {"f":[["Severity","warning"],["Started","…"],["Firing / total","2 / 3"],["cluster","2 values"]],"t":["Container killed for memory","pod: …","system=kubernetes · team=payments · namespace=checkout","[Open in Muster](…)"]}
curl -s "$FTG/messages?chat=$CH" | jq -r '[.[] | select(.text | test("KubernetesContainerOOMKilled"))] | last | .text' | head -6   # Severity / Started / Firing / total / cluster lines
curl -s 127.0.0.1:18097/_fake/requests | jq -c '[.[] | select(.path | endswith("chat.postMessage") or endswith("chat.update"))] | last
  | .body.blocks | map(select(.type=="section")) | .[0].fields | map(.text)'   # four fields in order
curl -s $FMM/requests | jq '[.[] | select(.method=="PUT" and (.path | endswith("/patch")))] | length'   # 1 edit of the Root message; its body has no Mention
# C-38.AC-3: every movable block hidden and other links off -> title, status, notices, footer, buttons, "Open in Muster" remain
# C-38.AC-6: set policy.templates.root_message on R: the template body stands where the first of text, labels, alerts stood; fields follow the layout
# C-38.AC-11: alerts_listed 3 with twelve Alerts of one Alert Group: three values and "…and 9 more"
# C-38.AC-5: three saves in a row (sleep 0 between them), then wait: psql shows one route_message_settings row, the fake one edit per message
```

`test/e2e/route_message_layout_render_test.go` repeats these through the recording test adapter of C-11 and the fake
Mattermost, Telegram and Slack servers, with the bridge's `layout` left to S-212, and adds the Team Destination case
(a Route delivering to a Team's Destination whose Mention entries and button set stay its own).

## Open questions

None; the facts of S-209 settle what the Mattermost and Slack adapters rely on.

## Notes

- Suggested commit: `feat(messages): lay out root messages by the message layout of the route in every messenger`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- **Byte for byte.** C-38.AC-1 is the reason `Default()` is built from the same constants as C-29's layout, and why the
  golden outputs are not regenerated by this story: a changed golden file is a defect to explain in the pull request.
- S-209 found that Slack takes 10 `fields` in a section, as P-94 assumed, and refuses 11 with `invalid_blocks` (F-149);
  the adapter never sends more than Slack accepts, whatever a stored layout holds.
- The order inside a Slack message follows C-38.FR-6: the fields are one `section` placed where their block stands;
  Mattermost cannot do that (the fields stay under the text, F-148), and the editor says so (S-217).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-1 | partial | every Destination of the Route renders it and the default renders as before; the setting is S-210 |
| C-38.FR-2 | partial | the rendering of the layout; the content and its checks are S-210 |
| C-38.FR-3 | full | |
| C-38.FR-6 | partial | Mattermost, Slack, Telegram and the bridge's CommonMark body; the `layout` with `order` is S-212 |
| C-38.FR-7 | partial | Thread replies, Storm summaries and Mentions unchanged; the test message is S-216 |
| C-38.FR-8 | full | the preview's own `layout` null is S-215 |
| C-38.FR-14 | partial | the edits, Quietly and once; the row is S-210 |
| C-38.AC-1 | partial | the Root message byte for byte; the read-back is S-210 |
| C-38.AC-2 | partial | Mattermost, Slack and Telegram; the fake bridge's `layout` is S-212 |
| C-38.AC-3 | full | |
| C-38.AC-5 | full | |
| C-38.AC-6 | partial | the rendering; the preview's `layout` null is S-215 |
| C-38.AC-11 | partial | the three values and "…and 9 more"; the refusals are S-210, the `order` S-212 |
