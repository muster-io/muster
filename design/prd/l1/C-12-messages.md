# C-12. Messages

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11

**Goal.** Every Alert Group reads clearly and is actionable in any messenger by default, can be customized safely, and
never lets alert data mention people or break the message.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. Without any configuration, a firing Alert Group shows its number, name, environment, start time, labels, summary,
   Alerts, links and buttons.
2. The Admin edits the Route's line template; on save Muster renders it against recent Stored Snapshots and shows the
   preview; a template with a syntax error is not saved.
3. The Admin fills the Lookup table "grafana" (`environment → address, data source UID`) and creates the Link rule "Dashboard"; messages show
   the link.
4. An alert label contains `@channel`; nobody is notified.
5. A template fails at runtime on unexpected data; the message falls back to the Fallback template, the Route shows a
   template error and `MusterTemplateError` is raised.
6. A Russian-speaking team sets their Route's language to Russian.
7. A Destination mentions `@channel` only for new Alert Groups.

## Functional requirements

- **C-12.FR-1** The default Root message contains, in order:
  1. the status colour (🔴 firing, 🟠 acknowledged, 🟢 resolved, ⚪ snoozed — as an emoji, or as the message colour where
     the messenger has one);
  2. the heading: `#N` and the Alert Group's title (C-09.FR-23), linked to the Alert Group page at
     `MUSTER_PUBLIC_URL`;
  3. the environment (`cluster` and `environment` labels when present) and the start time — absolute, in the
     Organization's time zone;
  4. group labels — the Group key labels except `alertname`;
  5. common labels — labels shared by all Alerts, except `severity`, `environment`, `prometheus`, `alertname` and the
     group labels;
  6. common annotations, except `summary`, `description` and runbook links;
  7. the Alert Group's `summary` (C-09.FR-23), in italics;
  8. the Alerts, as `message.alerts_listed` describes: up to the line limit, one line per Alert with the labels that
     differ between Alerts (minus the labels excluded in item 5), firing and newest first, resolved ones struck through;
     above it, the distinct values of each differing label up to the per-label limit, then "+K more", and "Full list in
     Muster";
  9. links (FR-9);
  10. the notices of [reference.md](reference.md#banners-warnings-and-notices) that apply (still firing after a manual
      resolve, Unclaimed, Reopen count, delivered late, previous message deleted, no longer updated here);
  11. the footer: "Acknowledged by {user}", "Resolved by {user}", "Resolved automatically: {reason}", or "Snoozed by
      {user} until {time}", where {user} is who set the current Snooze, kept on the Alert Group until it ends;
  12. the buttons of [reference.md](reference.md#buttons-and-links-by-status).

  Each adapter lays these sections out for its messenger: in Telegram, items 4 to 6 sit in an expandable blockquote and
  the Alerts stay a list, never a table, because tables break words on phones (C-14.FR-16). In Mattermost, the post's
  own text is a short summary line — the status emoji of item 1, `#N` and the title — followed by the Mentions of a Loud
  post, because a Mention inside an attachment notifies with an empty notification
  ([F-056](../../facts.md#edits-and-notifications)); the details are one attachment in the colour of item 1: its title
  is item 2, linked to the Alert Group page; the links of item 9 form one line under the Alerts that starts with "Open in
  Muster"; item 11 is the last line of its text; the attachment's own footer shows "Muster v<version>" with the Muster
  logo; and the buttons follow (C-13.FR-3).
- **C-12.FR-2** A Route's Root message template, if set, renders only the body — items 3 to 8; the status colour,
  heading, links, notices, footer and buttons always come from Muster, so no template can lose the buttons or the link
  to Muster. A Route's line template, if set, replaces the default line of each Alert in item 8.
- **C-12.FR-3** Built-in texts exist in English and Russian for: the default Root message, new-Alert Thread replies,
  Replacement, Reopen, Takeover, Snooze ended, rise to Urgent, resolution by the system, ack timeout notices, Unclaimed,
  Reminders, auto-unacknowledge, the Storm summary, delivered late, deleted and republished, and "no longer updated
  here". The language is a Route policy field (`route.language`); times in messages use the Organization's time zone,
  absolute, never relative.
- **C-12.FR-4** Templates — the Root message, the line template, the ack timeout notice, Link rule URLs and outgoing
  webhook requests — are Go templates rendered in the sandbox of ADR-0012: explicitly registered sprout functions,
  Alertmanager's template functions under their own names, `now` from Muster's clock, capped loops and output capped at
  `template.output_cap`.
- **C-12.FR-5** Saving a template parses it and dry-runs it against the most recent Stored Snapshots that the Route took
  (`template.dry_run_sample`; a built-in example when there are none); a template that fails is not saved, and the
  editor shows the rendered preview.
- **C-12.FR-6** If a message template fails at runtime, the Fallback template — every label, under Muster's heading,
  footer and buttons — is used, `muster_template_errors_total{route,destination,template}` grows, the Route shows a
  template error, the Timeline records it and the Internal alert `MusterTemplateError` is raised; it resolves when the
  template renders again. A failing Link rule is left out of the message and counted.
- **C-12.FR-7** Safe output: alert data is escaped for the Destination's markup (HTML in Telegram, Markdown in
  Mattermost — implemented by the adapters, C-13.FR-8 and C-14.FR-13); `@` in alert data is neutralized with a
  zero-width space; links are `http(s)` only; label and annotation values are truncated to `message.value_cap`.
- **C-12.FR-8** A Destination sets whom to mention for each kind of Loud event — new Alert Group, new Alerts, Reopen,
  ack timeout notice, Snooze ended and rise to Urgent, named `new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`,
  `snooze_ended` and `rise_to_urgent` in the lifecycle event tables (C-09.FR-22): nobody (`destination.mentions`);
  everyone in the chat (`@channel`, `@all` or `@here`, as the messenger supports); chosen Muster users, rendered
  through their Account links; or messenger groups. What applies per Destination type: Mattermost offers all of them,
  groups being Mattermost group names; Telegram has neither everyone in the chat nor groups, so a Telegram Destination
  takes only nobody and chosen users and refuses the rest; an outgoing webhook takes every choice and receives it as
  data (C-15.FR-11). Reminders and auto-unacknowledge always mention the Owner, Takeovers the previous Owner, a Reopen
  into acknowledged only the Owner. In templates, `{{ mention "all" }}` and similar produce trusted tokens rendered for
  the platform; a literal `@all` in a template's text is escaped. The settings form is part of the Destination form
  (C-13.FR-9).
- **C-12.FR-9** Links: the Organization keeps Lookup tables (a key and one value per named column, edited in the UI). A
  Link rule has a name, a Matcher condition checked against the Alert Group's common labels (a label that only some of
  its Alerts carry counts as missing), a scope — the Alert Group, or each distinct value of a label among its Alerts —
  and a URL template that may call `lookup`, which reads one cell: `lookup "grafana" .Labels.cluster "datasource_uid"`.
  A built-in "Explore" rule (which cannot be deleted) turns `generatorURL` into a Grafana Explore link through the
  Lookup table "environment or cluster → Grafana address and data source UID", whose columns are `address` and
  `datasource_uid`. The annotations `runbook_url` and `dashboard_url`, and `generatorURL` as "Source", become links as
  well. Links appear as one line in the message and as a block on the Alert Group page; message templates do not build
  them.
- **C-12.FR-10** The Storm summary reads "⛈ Storm on {Route}: K Alert Groups, C urgent — open in Muster" and, in its
  final state, "Storm over: M Alert Groups still open".
- **C-12.FR-11** A rendered message that exceeds the length limit the adapter declares is shortened by trimming the
  Alert list first, then the label sections, always keeping the title, status, footer, buttons and the link to Muster.
- **C-12.FR-12** The footer and Mentions show a user by their messenger username when they have an Account link in that
  messenger, otherwise by their Muster display name (checked in C-18.AC-7).
- **C-12.FR-13** Rendering exports the metrics listed for C-12 in the [metrics
  catalogue](reference.md#metrics-catalogue): template errors and the rendering time of each kind of template.

## UI

Route → Message: editors for the Root message template, the line template and the ack timeout notice, each starting
from the built-in template's source and with a preview against a chosen recent Stored Snapshot, in Mattermost or
Telegram markup, and the language; Organization → Lookup tables and Link rules with a
preview; the links block on the Alert Group page.

## API surface

Template and language fields on `routes`; `template-previews` (create: template, kind, Stored Snapshot or example,
optional length limit, markup → rendered output or errors, and the template's source — the built-in one for an empty
template); `lookup-tables` (list, create, read, update, delete; a table has named
columns and rows of a key and one value per column); `link-rules`
(list, create, read, update, delete); Mention settings on `destinations`.

## Acceptance

Checked through `template-previews` and the recording test adapter of C-11; messenger markup is checked in C-13 and
C-14.

- **C-12.AC-1** A label value `@channel` is rendered with a zero-width space after `@`, and a `javascript:` link from an
  annotation is left out.
- **C-12.AC-2** Saving a template that calls an unregistered function (for example `env`) is refused with the error
  position.
- **C-12.AC-3** A template that fails on the next Snapshot produces a message from the Fallback template, raises
  `MusterTemplateError` and increases `muster_template_errors_total` for that Route.
- **C-12.AC-4** An Alert Group with 25 Alerts shows the distinct values of the differing labels and "Full list in
  Muster"; rendered with a length limit of 4,096 characters, it keeps the title, status, footer, buttons and the link.
- **C-12.AC-5** With the Route language set to Russian, the default message and Thread replies are in Russian.
- **C-12.AC-6** A Link rule using a Lookup table renders its link in the preview; a Link rule that fails is left out and
  counted.

## Related ADRs

ADR-0005, ADR-0012.

## Depends on

C-11 — Desired state and Thread replies to render.

## Suggested story split

- **BE** — default message, built-in texts, template sandbox, previews, Fallback template, Link rules and Lookup tables,
  Mention settings.
- **FE** — Route → Message editors with preview, Lookup tables and Link rules pages, links block on the Alert Group
  page.
