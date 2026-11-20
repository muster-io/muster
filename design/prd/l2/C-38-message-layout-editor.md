# C-38. Message layout editor

[L2 index](../L2.md) · Release: 0.2.x · UI: yes · Depends on: C-12, C-13, C-14, C-16, C-22, C-24, C-28, C-29

**Goal.** An Admin tunes the Root message of a Route on the Alert Groups that actually arrived (issue #375, journal
D543): which fields its grid shows and in which order, whether the summary, the description, the key labels, the
Alert list and the other links show, and the order of the blocks between the title and the notices — and sees, before
saving, each messenger's message for a real Alert Group of the Route. The editor shapes the compact layout of C-29; it
never removes what makes a Root message actionable — the title with the status, the notices, the people, the buttons
and the link to Muster.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the settings this file introduces
get their rows with the story that implements them.

## Scenarios

1. The Admin opens "Message layout" on the Route "Infrastructure". The preview shows #1287
   `KubernetesContainerOOMKilled`, one of the Route's recent Alert Groups, in the Mattermost tab, laid out as C-29
   scenario 1 describes.
2. The Admin adds the field "Firing / total" ("2 / 3") after Started and the label `cluster` as a field, then switches
   the preview to #1301 `NodeDiskPressure`, acknowledged, with twelve Alerts: the grid shows both new fields, the Alert
   list ends with "…and 7 more", and the footer reads "Acknowledged by @ivan". The Slack tab shows 9 of 50 blocks, the
   Telegram tab 812 of 4,096 characters.
3. The Admin turns the description off and moves the Alert list above the key labels; "Apply to the Route" saves the
   layout, and the open Root messages of the Route are edited Quietly to the new layout.
4. A second Admin had saved the Route in the meantime; the save is refused, and the editor offers to reload the Route.
5. The Route has its own Root message template. The editor shows the summary, description, key labels and Alert list
   as "Rendered by the Route's template", with a link to the template editor; the fields and their order still apply.
6. "Reset to default" returns the Route to Muster's default layout, which later releases may improve; the Audit log
   records the change with the layout before it.

## Functional requirements

### The layout of a Route

- **C-38.FR-1** A Route has a message layout (`route.message_layout`): Muster's default — the layout of C-29.FR-1,
  which follows later releases and the Organization's settings as they are — until an Admin saves one of the Route's
  own. A Route without its own layout renders exactly as C-29 says, so the release that brings the editor edits no Root
  message. The layout applies to the Root messages of the Route in every Destination of the Route, a Team's
  Destinations that the Route delivers to included (C-26); a Destination keeps its own Mentions and button set
  (C-28.FR-1, C-28.FR-8).
- **C-38.FR-2** A layout is the order of four movable blocks and the content of each, and whether the other links
  show:
  1. **Fields** — an ordered list taken from the catalogue: Severity, Route, Teams, Started (C-29.FR-1 item 2), "Firing
     / total" (the firing Alerts and all Alerts of the Alert Group) and a label by its name, shown as its value, as "N
     values" when the firing Alerts differ, and left out when no Alert has it, like a key label (C-29.FR-2). Each field
     appears at most once; Teams is left out when the Alert Group has none, as before. A label shown as a field is left
     out of the key labels, as C-29.FR-2 leaves out the severity label. At most `route.message_layout_fields` fields
     (Open question 6).
  2. **Text** — the summary and the description, each shown or not, both shown by default (C-29.FR-1 item 3), in that
     order.
  3. **Key labels** — shown or not; the labels it lists stay the Route's "Labels in the message"
     (`route.message_labels`, C-29.FR-2), which the editor edits in place rather than duplicating.
  4. **Alert list** — shown or not, by the distinguishing label of C-29.FR-3, with up to `message.compact_alerts_listed`
     values.

  **Other links** — the links of C-12.FR-9 after "Open in Muster": Runbook, Dashboard, Source and the Link rules — are
  shown or not, and stay in the line of links. The title with the status (C-29.FR-1 item 1) stays first; the notices,
  the people and the footer (items 6 and 7), the buttons (item 8) and "Open in Muster" always show, in the places
  C-29.FR-4 gives them in each messenger. The four movable blocks take any order between the title and the notices; a
  hidden block keeps its place in the order, so that showing it again puts it back.
- **C-38.FR-3** Shortening (C-29.FR-6) keeps its order whatever the layout: the description first, then the Alert list,
  then the summary, then the key labels, and never what FR-2 always shows or the fields. A hidden block is not rendered
  and is not counted against the limit.
- **C-38.FR-4** Saving checks the layout. An unknown block or field is refused by request validation with `400`
  `invalid_format`; a field given twice with `422` `duplicate`, a label field with an empty or invalid label name with
  `422` `invalid_format`, and more fields than the limit with `422` `too_long`, each with its field pointer. The save
  uses optimistic locking with the Route's ETag (C-08.FR-10) and records the Audit log with a before/after diff of the
  layout.
- **C-38.FR-5** "Reset to default" makes the Route follow Muster's default again (FR-1); "Labels in the message" keep
  their own setting and their own reset (C-29.FR-2).

### Each messenger

- **C-38.FR-6** Each adapter lays the layout out as C-29.FR-4 says, with the blocks in the layout's order where its
  messenger allows: in Mattermost, the fields as the attachment's short `fields` in their order, which the attachment
  draws under its text whatever the place of the Fields block — the editor says so on that block — and the other blocks
  as the attachment's text in their order, the links its last line and the buttons after it; in Slack, the fields as
  the `fields` of one `section` in their order, each other block in its order, the key labels and the people as
  `context` blocks and the buttons in an `actions` block; in Telegram, one HTML message with the fields as lines
  "{name}: {value}" and the blocks in their order; through a Bridge, the body in CommonMark in the layout's order and
  the `layout` of C-29.FR-8, in which a hidden block is empty — an empty list, an empty text or a null `alerts` — and a
  field of the catalogue is one more entry of `fields`. A bridge that renders from `layout` places its parts in its own
  order (Open question 4).
- **C-38.FR-7** A layout never changes Thread replies, Storm summaries or what a Loud message mentions. A test message
  (C-16.FR-1) built from an Alert Group of the Route shows the Route's layout; one built from the example shows Muster's
  default.

### Templates

- **C-38.FR-8** A Route's Root message template, if set, renders the summary, the description, the key labels and the
  Alert list as one body, as C-29.FR-5 says: the layout's settings of those blocks do not apply while it is set, and
  the body takes the place of the first of the text, key labels and Alert list blocks in the layout's order. The
  fields, their order and the other links follow the layout. A line template renders each listed Alert as before, and
  the Fallback template (C-12.FR-6) keeps every label and ignores the layout. A Root message whose content a Root
  message or line template, or the Fallback template, renders carries no bridge `layout`, as the `layout` of
  `template-previews` already says.
- **C-38.FR-9** The editor shows the blocks that a template renders as "Rendered by the Route's template", with a link
  to that template's editor (C-12 UI).

### Preview on Alert Groups that arrived

- **C-38.FR-10** The preview renders the unsaved layout against a real Alert Group through the same path as the
  messages Muster sends (C-12.FR-5): its current status, Alerts, Owner or Assignees, notices and links, in the Route's
  language or another chosen one, with the buttons of the set of a chosen Destination of the Route or of the
  Organization's set (C-28.FR-8), and with every escape of the messenger's markup (C-12.FR-7). Nothing is sent and no
  Desired state changes.
- **C-38.FR-11** The preview offers the Route's most recent Alert Groups — open ones first, then resolved ones kept
  within `retention.alert_details`, each newest first, at most `route.message_layout_preview_groups` — each with its
  number, title, status and number of Alerts, and an Alert Group of any Route found by its number, which then renders
  as if this Route had taken it. When the Route has no such Alert Group it renders the built-in example, marked
  "Example", until one is found by number. It shows the first of the offered ones by default.
- **C-38.FR-12** The preview has one tab per messenger — Mattermost, Slack, Telegram and Bridge — each showing the
  message as that messenger draws it, closely enough to judge the layout, and the exact payload it would receive: the
  Mattermost attachment, the Slack blocks, the Telegram HTML with the inline keyboard, and the Bridge body with its
  `layout` and `buttons`. Each tab shows the length against its messenger's limit — 16,383 characters for Mattermost
  (F-057), 4,096 for Telegram (C-14.FR-13), 50 blocks and 3,000 characters in each `section` for Slack (F-076), and for
  a Bridge the `limits.max_body_length` of the chosen Bridge Destination's Connection (C-22.FR-7), or none — and, when
  the message is over it, which blocks shortening cuts (FR-3).
- **C-38.FR-13** The preview needs `templates:preview`. From the Redaction rules on (L2 §1.5, issue #373), the preview
  applies them as delivery does and marks each replaced value, so that it never shows a message the messenger would not
  receive.

### Applying a change

- **C-38.FR-14** Saving a layout, or resetting it, re-renders the open Root messages of the Route Quietly and as edits,
  like a change of "Labels in the message" (C-29.FR-2): through the durable work rows of the re-render of a Route
  (L2 §6, C-08.FR-8), one pending row per Route however many saves come before it runs (issue #542), and a Root message
  the change does not alter is not edited. Edits go through each Destination's limiter (C-11) and never add Mentions
  (ADR-0005). A Root message that Muster can no longer edit is handled as any other edit is (C-11).

### Reading and documentation

- **C-38.FR-15** Every User with `routes:read` sees the Route's layout, read-only; editing it needs `routes:write`, and
  the preview `templates:preview` (FR-13). The API gives the layout of a Route, its own or Muster's default with which
  of the two it is, so that a client such as the Terraform provider reads and writes it like any other Route setting,
  and a client that sends back what it read never turns Muster's default into a layout of the Route's own.
- **C-38.FR-16** The documentation's page on messages describes the editor, the blocks that always show, the relation
  to templates and the preview on real Alert Groups, with a screenshot from `make docs-screenshots`.

## UI

Route page, section "Message": "Message layout" opens the editor, a page of its own under the Route with the header
"Message layout · Route {name}", the hint "Applies to every Destination of the Route", and the actions "Reset to
default" and "Apply to the Route".

- Left, "Blocks from top to bottom": the title "#N title and the status as a word", fixed; the four movable blocks of
  FR-2, each with a switch and a handle to drag it, and "Move up" and "Move down" for the keyboard and touch; under
  Fields, the chosen fields as removable chips in their order with "+ field" offering the catalogue and the labels of
  recent Alert Groups of the Route with an example value, and the hint "In Mattermost the fields always show under the
  text"; under Text, the switches "Summary" and "Description"; under Key labels, "Labels in the message" as C-29 shows
  them; then the fixed blocks "Notices", "Assignees / Acknowledged by", "Buttons — the set of each Destination" and
  "Links", with "Open in Muster" always on and the switch "Other links". A Route with a Root message template shows the
  hint of FR-9 on the blocks it renders.
- Right, "Preview on real Alert Groups": the Alert Group picker of FR-11 with "Find by number", the language, "Buttons
  as in" with the Route's Destinations and "The Organization's set", the four messenger tabs of FR-12 with "Show the
  payload" and the length line ("Length: 812 / 4,096 (Telegram)", "Blocks: 9 / 50 (Slack)"), and, when the message is
  too long, "Shortened: the description and 4 Alerts are cut".
- Leaving with unsaved changes asks for confirmation; a refused save (`412`) offers "Reload the Route", which drops the
  unsaved layout. At 360 px the preview follows the blocks and nothing scrolls sideways; the payload scrolls inside its
  own box.
- Every text in English and Russian; the field names in messages are built-in texts in the Route's language (C-12.FR-3).

## API surface

On `routes`, `own_message_layout` and `message_layout`, as `own_message_labels` and `message_labels` are (C-29.FR-2):
`own_message_layout` is writable — false, the default of a create, follows Muster's default and ignores
`message_layout`, and an update that omits it keeps it — and `message_layout` is read only when it is true; a read
gives the effective layout, and Muster's default does not change the Route's ETag. "Reset to default" sets
`own_message_layout` false. `template-previews` takes an unsaved `message_layout` for `root_message` and a
`destination_id` — its button set and, for a Bridge Destination, its Connection's `limits.max_body_length` — and
returns, besides `output`, `length` and `messenger_limit`, the messenger payload (the Mattermost attachment, the Slack
blocks, the Telegram inline keyboard, the Bridge `buttons`), the number of Slack blocks and the blocks shortening cut;
`alert_group_id` names any Alert Group. The Route's recent Alert Groups come from the Alert Group list filtered by
Route. No new Permission, metric, log event or Internal alert.

## Acceptance

Checked through `template-previews`, the recording test adapter of C-11, the fake messenger servers and the fake
bridge.

- **C-38.AC-1** A Route without its own layout renders the Root messages of C-29.AC-1 byte for byte as before the
  release, and reads back its layout resolved, with `own_message_layout` false; a read sent back unchanged keeps
  `own_message_layout` false and the ETag.
- **C-38.AC-2** A layout with the fields Severity, Started, "Firing / total" and the label `cluster`, the description
  hidden and the Alert list before the key labels renders, for an Alert Group of three Alerts of which two fire: in
  Mattermost four short `fields` in that order, "2 / 3", no description and the Alert list above the key labels, with
  no `cluster` among them; in Slack one `section` with those four `fields`; in Telegram four lines "{name}: {value}";
  through the fake bridge a `layout` with four `fields` and an empty `description`.
- **C-38.AC-3** A label field whose label differs among the firing Alerts shows "2 values", and one no Alert has is
  left out; a layout that hides every movable block and the other links still shows the title, the status, the
  notices, the footer, the buttons and "Open in Muster"; in Mattermost, the Fields block moved below the Text block
  still gives the attachment's `fields`.
- **C-38.AC-4** Saving a layout with an unknown block gets `400` `invalid_format`, with a field twice `422`
  `duplicate`, with more fields than the limit `422` `too_long`, and with a stale `If-Match` `412`; a valid save writes
  one Audit log row with the layout before and after.
- **C-38.AC-5** Saving a layout edits the open Root messages of the Route once, Quietly and without Mentions, in the
  fake Mattermost and Telegram servers; a Root message the change does not alter is not edited; three saves before the
  re-render runs leave one pending work row for the Route.
- **C-38.AC-6** On a Route with a Root message template, the template's body takes the place of the first of the
  text, key labels and Alert list blocks in the layout's order, and the fields follow the layout; the preview's
  `layout` is null, as the bridge then receives none.
- **C-38.AC-7** The preview of an Alert Group of another Route renders in this Route's language and layout; the
  offered Alert Groups list the open ones first, then the resolved ones, each newest first; on a Route with no Alert
  Group the preview renders the example, marked "Example"; a Responder's token gets `403` from `template-previews`, and
  a Viewer reads the Route's layout.
- **C-38.AC-8** A message over the limit with the description shown reports the description cut first, and the same
  message with the description hidden reports the Alert list cut first; the Bridge tab of a Bridge Destination whose
  fake bridge declares a `max_body_length` of 1,000 reports that limit.
- **C-38.AC-9** A test message of a Destination of the Route, built from an Alert Group of the Route, shows the
  Route's layout; one built from the example shows the default.
- **C-38.AC-10** In a real browser, in English and in Russian, the Admin adds a field, moves the Alert list up by
  keyboard, hides the description, switches the preview between two Alert Groups and the four tabs, and applies the
  layout; the page has no horizontal scrolling at 360 px; "Reset to default" restores the layout of C-29.

## Open questions

Settled by the maintainer before the stories that depend on them. Each has a recommended answer.

1. **Where a layout lives.** Per Route only (as the approved mockup and "Labels in the message"), or also an
   Organization default, or per Destination? — _Recommended:_ per Route only. Muster's default plays the Organization
   default; the button set already covers what differs per Destination. A per-Destination layout multiplies the
   preview and the re-render and is not asked for.
2. **People and notices.** The mockup has a switch on "Assignees / Acknowledged by". — _Recommended:_ keep them always
   shown, as C-29.FR-6 never cuts them and Pages rely on the Assignees being visible (C-32.FR-12); the editor shows the
   block fixed.
3. **The template in the editor.** The mockup's "Custom template…" says a template replaces the whole layout. —
   _Recommended:_ keep C-29.FR-5, as FR-8 does: a template renders the text blocks only, so the title, fields, people,
   buttons and links stay Muster's. Whether the editor also offers "Remove the template" in the same save as the layout
   — _recommended:_ yes, as a convenience, since the template editor already offers the reset.
4. **Order for bridges.** The `layout` of C-29.FR-8 has fixed keys and no order. — _Recommended:_ add an optional
   `order` list of block names to `layout`, additively in Bridge protocol version 1, so that a bridge rendering
   natively can follow the Admin's order; a bridge that ignores it keeps its own order.
5. **The number of values in the Alert list.** Per Route in the editor, or the built-in
   `message.compact_alerts_listed`? — _Recommended:_ per Route, 1 to 20, default 5, so that busy Routes show fewer and
   node-level Routes more; provisional with a `P-NN` until the test environment confirms the bounds.
6. **The limit of fields.** — _Recommended:_ at most 10 (`route.message_layout_fields`), the count of `fields` Slack
   takes in one `section`, verified as a fact before the story, since F-076 does not list it, together with the
   Mattermost attachment drawing its `fields` under its text (FR-6); provisional with a `P-NN` until then. Twenty
   Alert Groups in the preview's list (`route.message_layout_preview_groups`) likewise.
7. **Choosing the distinguishing label.** — _Recommended:_ keep it automatic (C-29.FR-3, P-67) in this capability; a
   choice per Route waits for P-67 to be confirmed or refuted.
8. **Sending a real test from the editor.** — _Recommended:_ add "Send a test message" to a chosen Destination of the
   Route, through the test of C-16 with the unsaved layout, since the preview approximates how each messenger draws; it
   needs `destinations:test` and sends nothing Loud.
9. **Undo and earlier versions.** — _Recommended:_ no list of earlier versions beyond the unsaved draft, "Reset to
   default" and the Audit log's before/after diff, from which an Admin copies an earlier layout back; a list of
   versions waits for demand.
10. **Redaction in the preview.** — _Recommended:_ as FR-13 says, the preview shows only the redacted message, marked,
    and never a "show original" switch; whoever needs the original reads the Alert Group page.
11. **The re-render note of L2 §6.** — _Recommended:_ when this capability is promoted from draft, add "the message
    layout of a Route" to the changes whose re-render runs through durable work rows in the note under the table of
    L2 §6.

## Related ADRs

ADR-0005, ADR-0012, ADR-0016, ADR-0017.

## Depends on

C-12 — templates and previews; C-13, C-14, C-22, C-24 — the adapters; C-16 — test messages; C-28 — the button sets;
C-29 — the compact layout, "Labels in the message" and the bridge `layout`.

## Suggested story split

- **BE** — `message_layout` on Routes with its checks, the renderer and the adapters following it, the re-render, and
  the preview taking an unsaved layout and a Destination and returning the payload; the fake bridge records the
  `layout`.
- **FE** — the editor page with the blocks, the fields catalogue, the preview on real Alert Groups for each messenger,
  and the documentation with its screenshot.
