# C-29. Compact message layout

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-12, C-13, C-14, C-22, C-24, C-25, C-28

**Goal.** A Root message shows on a phone what a person needs to act (issue #375): the title with the status as a word,
a grid of a few fields, the summary, one line of key labels chosen per Route, the Alerts named by the label that tells
them apart, who is Paged or who has it, the Destination's buttons and the links — in every messenger, in its own syntax.
A bridge receives the same layout as structured data next to the body, so that it can render the message natively
without parsing markup (issue #390). The visual editor of the layout, previewed on real Alert Groups, comes later in L2.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md).

## Scenarios

1. A firing `KubernetesContainerOOMKilled` Alert Group of three Alerts shows, in `infra-notice` on a phone: "#1287
   KubernetesContainerOOMKilled — Firing"; Severity warning, Route Infrastructure, Teams payments, Started 03:12; its
   summary; `system=kubernetes · team=payments · namespace=checkout`; "pod: checkout-api-7d9f4, checkout-api-5b2c1,
   checkout-worker-8e6a0"; "Assignees: @ivan (devops), @petr (payments)"; the buttons of the Destination; "Open in
   Muster · Runbook · Source". Every other label is one click away in Muster.
2. After Acknowledge, the same message reads "Acknowledged" and "Acknowledged by @olga (payments) · Escalation chains
   stopped".
3. `NodeDiskPressure` has twelve Alerts; the message lists five nodes and "…and 7 more".
4. The Admin adds `cluster` to the Route's "Labels in the message"; the open Root messages of the Route are edited to
   show it.
5. A bridge for a messenger whose markup has no backslash escapes renders the Root message from `layout`: the number as
   a field of its own, the labels as key and value, the Assignees by its own user ids taken from the Mention entries.

## Functional requirements

- **C-29.FR-1** The default Root message contains, in order:
  1. the title — `#N` and the Alert Group's title (C-09.FR-23), linked to the Alert Group page — and the status as a
     word: Firing, Acknowledged, Snoozed or Resolved; the status colour of C-12.FR-1 stays, as the message colour or an
     emoji;
  2. fields: Severity (the Alert Group's Severity level, C-26.FR-8), Route, Teams (C-25.FR-6; left out when it has none)
     and Started (absolute, in the Organization's time zone);
  3. the Alert Group's `summary` (C-09.FR-23) as plain text;
  4. one line of key labels: the labels of the Route's "Labels in the message" (FR-2);
  5. when the Alert Group has more than one Alert, its Alerts (FR-3);
  6. the notices of [reference.md](../l1/reference.md#banners-warnings-and-notices) that apply;
  7. before an acknowledgement, the Assignees with their Teams (C-32.FR-12); otherwise the footer of C-12.FR-1 —
     "Acknowledged by {user}", with the Owner's Team (C-32.FR-12) and "· Escalation chains stopped" when the
     acknowledgement stopped chains, "Snoozed by {user} until {time}", "Resolved by {user}" or "Resolved automatically:
     {reason}";
  8. the buttons of the Destination's set (C-28.FR-8);
  9. the links of C-12.FR-9, starting with "Open in Muster".

  Names in items 7 are Mentions where the person has an Account link in the Destination's Identity space, as C-12.FR-12
  says for the footer. A Loud message carries its Mentions first, as before (C-12.FR-1). The Alert Group page shows all
  labels and annotations, and the message leaves them out.
- **C-29.FR-2** "Labels in the message" is a Route setting (`route.message_labels`): a list of label names, by default
  the labels of the Group key except `alertname`, plus the Team label and `namespace`. A label is shown as `name=value`
  when every firing Alert of the Alert Group has the same value, as `name: N values` when they differ, and not at all
  when no Alert has it. The Route form suggests labels from the Route's recent Alert Groups with an example value. A
  change re-renders the open Root messages of the Route.
- **C-29.FR-3** The Alerts of item 5 are named by their distinguishing label: the first of the Organization's Instance
  labels (`organization.instance_labels`) whose values differ among the Alerts, else the first label, by name, whose
  values differ (_provisional_, P-67). The line lists up to `message.compact_alerts_listed` values, firing Alerts first
  and resolved ones struck through, then "…and K more" (FR-7).
- **C-29.FR-4** Each adapter lays the layout out in its messenger's own form: in Mattermost, the one attachment of
  C-13.FR-3 with the title of item 1 as its title, the fields of item 2 as its short `fields`, items 3 to 7 as its text
  and the links as its last line, the buttons after it; in Slack, the blocks of C-24.FR-4 with the fields of item 2 as
  the `fields` of one `section`, the key labels and the people of item 7 as `context` blocks, and the buttons in an
  `actions` block; in Telegram, one HTML message with the fields as lines "{name}: {value}" and the buttons as the
  inline keyboard of C-14.FR-3; through a Bridge, the envelope of C-22.FR-4 with the body in CommonMark and the `layout`
  of FR-8. Thread replies, Storm summaries and test messages keep their own forms. Each adapter places the items in the
  order its messenger allows: in Mattermost the links stay the last line of the attachment's text, and the buttons come
  after it.
- **C-29.FR-5** A Route's Root message template (C-12.FR-2), if set, renders items 3 to 5 in place of the default; the
  title, fields, notices, people, buttons and links always come from Muster. A line template, if set, renders each
  listed Alert on a line of its own instead of the value of the distinguishing label. The Fallback template (C-12.FR-6)
  keeps every label.
- **C-29.FR-6** A rendered message longer than the limit its adapter declares is shortened by cutting the Alerts of item
  5 first, then the summary of item 3, then the key labels of item 4, always keeping the title, the status, the fields,
  the people of item 7, the buttons and the link to Muster (C-12.FR-11).
- **C-29.FR-7** The default layout replaces the layout of L1 for every Route from 0.2.0 on; a Route with its own Root
  message template keeps its body. The upgrade notes say so.

### Bridge layout

- **C-29.FR-8** The calls of the Bridge protocol that publish and edit a Root message, and those of a test message,
  carry next to `body` an optional `layout` with the same content, additively in protocol version 1 (ADR-0017). Its
  values are plain text, never CommonMark — the bridge escapes them for its messenger — and it has:
  - `number`, the Alert Group's number as a field of its own, and `title`, the title without the number, because `#N` in
    text becomes a hashtag in some messengers;
  - `status`, its key (`firing`, `acknowledged`, `snoozed`, `resolved`) and its word in the Route's language;
  - `fields`, a list of name and value (item 2);
  - `text`, the summary (item 3);
  - `labels`, a list of key and value (item 4), so that the bridge decides how to show them;
  - `alerts`, the distinguishing label, the values listed, each with whether its Alert is resolved, and the total number
    of Alerts — not a ready "and K more" string, because the bridge may have to cut more than Muster did and counts the
    rest itself;
  - `notices`, the notices of item 6 and the footer of item 7 other than its people, as lines;
  - `assignees` and `owner`, each person with their name and, where the message carries their Mention, the `ref` of its
    entry in the envelope's `mentions` (C-22.FR-16) — a reference, never a marker in the text — so that a messenger that
    renders a Mention as a placeholder and a list only matches the `ref` to its own user id;
  - `links`, a list of label and URL.

  The buttons stay in the envelope's `buttons`. A bridge renders either `body`, as before, or `layout`; a bridge that
  ignores `layout` is unaffected. `muster bridge check` (C-22.FR-21) accepts both, and the bridge libraries for Go and
  Python get types for `layout` in releases of their own.
- **C-29.FR-9** The documentation's page on messages shows the layout in each messenger and "Labels in the message"; the
  guide for bridge authors describes `layout` with an example of rendering it natively.

## UI

Route form, section "Message": "Labels in the message" — the chosen labels as removable chips, "+ label" with
suggestions "From recent Alert Groups of the Route" with an example value each, and the hint "By default: the Group key,
the Team label and `namespace`. Labels without a value in the Alert Group are not shown." — next to the existing
template editors and preview of C-12, whose preview shows the new layout in Mattermost, Slack, Telegram and Bridge
markup, the last with the `layout` it carries, and the length against each messenger's limit.

## API surface

`message_labels` on `routes`; the new layout in `template-previews` (C-12) for every markup, with `layout` for the
Bridge markup; `layout` in the `MessageInput` of `api/bridge.yaml`.

## Acceptance

Checked through `template-previews`, the recording test adapter of C-11 and the fake messenger servers.

- **C-29.AC-1** The Root message of a firing Alert Group of three `KubernetesContainerOOMKilled` Alerts with three `pod`
  values, on a Route with the Group key `alertname`, `system`, `team`, shows in order the title with "Firing", the
  fields Severity, Route, Teams and Started, the summary, `system=kubernetes · team=payments · namespace=checkout`, the
  line "pod:" with the three values and the links — in Mattermost as attachment `fields`, in Slack as a `section` with
  `fields`, in Telegram as lines.
- **C-29.AC-2** An Alert Group whose Alerts carry two `namespace` values shows `namespace: 2 values`; one whose Alerts
  have no `namespace` shows no `namespace`; one Alert shows no Alerts line.
- **C-29.AC-3** Twelve Alerts list `message.compact_alerts_listed` values and "…and 7 more"; rendered with a length
  limit of 4,096 characters and a long summary, the message cuts the Alerts, then the summary, and keeps the title,
  fields, people, buttons and link.
- **C-29.AC-4** Adding `cluster` to "Labels in the message" edits the open Root messages of the Route to show it.
- **C-29.AC-5** A Route with its own Root message template keeps its body under Muster's title, fields, people, buttons
  and links.
- **C-29.AC-6** The fake bridge receives, for a firing Root message, `body` and a `layout` whose `number` is the number
  alone, whose `title` has no `#`, whose values carry no CommonMark escapes and whose `alerts` gives the total; after an
  Acknowledge by a linked User, a new Root message to a bridge with `native_mentions` names the Owner with the `ref` of
  their Mention entry, and an edit, which carries no Mentions, names them with the `ref` `null`.
- **C-29.AC-7** In a real browser, the Route form's "Labels in the message" suggests labels of recent Alert Groups and
  the preview shows the layout for each messenger, in English and in Russian.

## Related ADRs

ADR-0005, ADR-0012, ADR-0017.

## Depends on

C-12 — messages and templates; C-13, C-14, C-22, C-24 — the adapters; C-25 — Teams; C-28 — the button sets. The
Assignees of item 7 appear from C-32 on.

## Suggested story split

- **BE** — the layout in the renderer and the adapters, "Labels in the message", `layout` in the Bridge protocol with
  the fake bridge.
- **FE** — "Labels in the message" and the preview for every markup.
- In `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python` — types for `layout`.
