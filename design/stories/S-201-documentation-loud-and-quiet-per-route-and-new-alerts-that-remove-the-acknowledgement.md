---
id: S-201
title: "Documentation: Loud and Quiet per Route, and new Alerts that remove the acknowledgement"
capability: C-36
kind: docs
layer: L2
depends_on: [S-198, S-199, S-200]
covers: [C-36.FR-11]
files_touched:
  - docs/concepts/loud-and-quiet.md
  - docs/concepts/routes-and-group-keys.md
  - docs/concepts/statuses-and-commands.md
  - docs/concepts/what-critical-changes.md
  - docs/outgoing-webhooks/events.md
  - docs/operations/upgrades.md
  - web/e2e/screenshots/routes-and-templates.spec.ts
acceptance:
  - "[C-36.FR-11] Concepts → Loud and Quiet describes the two settings of a Route, the table of which Loud event governs which row and the rows that stay Loud on every Route (takeovers, Reminders, auto-unacknowledge, the Owner of an ended or removed acknowledgement, Pages by Mention), the two Route profiles and what they pre-fill, and what Quiet means in each messenger — a new message without Mentions, `disable_notification` in Telegram, `notify` false in outgoing webhook events; it replaces the sentence that Muster fixes the loudness of every change and the section \"Why the rule is fixed\" with what stays fixed (an edit never notifies anybody)."
  - "[C-36.FR-11] The page has the section \"New Alerts remove the acknowledgement\": what it does, the order against the rise to Urgent (which is checked first), what it never does (a Replacement, a Continuation, a snoozed Alert Group, a Reopen, an attached Alert Group), what it starts over (Escalation chains, or the ack timeout; Reminders end; an Acknowledge afterwards is not a Takeover), and the worked scenario of a payments Route with Alice's acknowledgement."
  - "[C-36.FR-11] Concepts → Statuses and commands, Concepts → Routes and group keys, Concepts → What critical changes and the outgoing webhooks' events page point to it where they mention acknowledgements, the profiles' table or `notify`; Operations → Upgrades tells operators that every existing Route keeps all seven Loud events after the upgrade and suggests unticking events on Informational Routes."
  - "[C-36.FR-11] `make docs-check` passes: the page builds strictly, every image has alternative text, every link and anchor resolves and `docslinkcheck` finds `field.loud_quiet`; the screenshot of the section in the Route form is taken by `make docs-screenshots GUIDES=routes-and-templates`."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-201. Documentation: Loud and Quiet per Route, and new Alerts that remove the acknowledgement

## Scope

**IN**

- The page Concepts → Loud and Quiet, rewritten for the settings of a Route (C-36.FR-11), with the section on new Alerts
  that remove the acknowledgement.
- The sentences and rows on the neighbouring pages that this changes, and the note on upgrading.
- One screenshot of the new form section.

**OUT**

- The behaviour and the form: S-197 to S-200. The API reference, which `make docs` renders from the spec.
- Attach and Detach, whose effect on loudness the page names with a pointer: S-208.

## Contracts

- **Page**: `docs/concepts/loud-and-quiet.md`, in its place in `mkdocs.yml`; it keeps its present sections on what Loud
  means in each Destination, Mentions, Account links and Storm summaries, and gains "Set per Route" (the seven Loud
  events with the table of the rows each governs, the rows that stay Loud, the Route profiles' values), "What Quiet
  means" (per messenger, from `reference.md`'s Loud and Quiet), and "New Alerts remove the acknowledgement" with the
  anchor `#new-alerts-remove-the-acknowledgement`. The old "Why the rule is fixed" keeps what is still fixed: an edit
  never notifies anybody; the row's loudness is a Route's choice from 0.2.x.
- **Neighbours**: `statuses-and-commands.md` (the paragraph on a rise to Urgent that removes the acknowledgement gains
  the other way), `routes-and-group-keys.md` (the profiles' table gains the two rows), `what-critical-changes.md` (the
  order against the rise), `outgoing-webhooks/events.md` (`notify`), `operations/upgrades.md` (the upgrade note).
- **Texts**: the English UI texts of S-200, in bold as the pages name controls today; the Russian pages, if the site has
  them, follow the existing practice of the docs. Examples are generic: the Routes "certificates" and
  "payments-critical", Alice and Bob; no employer or work-specific system is named.
- **Documentation links**: `field.loud_quiet` already points at the page; the Route form's new "Learn more" (S-200) uses
  it. The new anchor is not given its own entry.
- **Screenshot** (`web/e2e/screenshots/routes-and-templates.spec.ts`): `NN-loud-and-quiet` of the section in
  `docs/assets/screenshots/concepts/routes-and-group-keys/` (or the page's own folder, as the spec's existing images
  do); generated and never listed.
- **Settings cited**: `route.loud_events`, `route.new_alerts_remove_ack`, `route.urgent_rise_removes_ack`.

## Steps

1. Write the sections of the page from the PRD and the form of S-200. Check: `make docs-serve`, read in the browser.
2. Edit the four neighbouring pages and the upgrade note. Check: `make docs-check` for links and anchors.
3. Add the screenshot to the spec and take it. Check: `make docs-screenshots GUIDES=routes-and-templates`, then
   `make docs-check`.

## Verification

1. `make docs-check` passes: the page builds strictly, every image exists and has alternative text, every link and anchor
   resolves, and `docslinkcheck` still finds `field.loud_quiet`.
2. `make docs-serve` → Concepts → Loud and Quiet reads as the acceptance says; its table agrees with
   `design/prd/l2/C-36-route-loud-and-quiet-settings.md` row by row.
3. On `muster dev`, the "Learn more" beside "Loud and Quiet" in a Route form opens the page.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe the loud and quiet settings of a route and new alerts that remove the acknowledgement`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- Placed after the form so that the screenshot shows the finished section.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-36.FR-11 | full | the form's link is S-200's |
