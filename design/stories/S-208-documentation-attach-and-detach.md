---
id: S-208
title: "Documentation: Attach and Detach"
capability: C-37
kind: docs
layer: L2
depends_on: [S-204, S-205, S-206, S-207, S-201]
covers: [C-37.FR-17]
files_touched:
  - docs/concepts/attach-and-detach.md
  - mkdocs.yml
  - docs/concepts/alert-groups.md
  - docs/concepts/statuses-and-commands.md
  - docs/concepts/loud-and-quiet.md
  - docs/concepts/escalation-chains.md
  - docs/configure/api-tokens.md
  - docs/outgoing-webhooks/events.md
  - web/src/lib/docs-links.json
  - web/src/components/docs-link.tsx
  - web/src/components/attach-dialog.tsx
  - web/src/components/alert-group-notices.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/screenshots/alert-groups.clock.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-37.FR-17] Concepts → Attach and Detach, in the navigation, says when to attach — several Alert Groups stand for one problem, as the worked scenario of the database primary and the payments errors shows — and when Incidents of L3 will fit better (several teams working on one problem together); what an attached Alert Group does and does not do (follows the status of its main Alert Group, its chains and timers stop, its Root messages are one line without buttons, every event on it is Quiet, it takes its own Alerts and the main Alert Group does not count them, Commands on it are refused with `alert_group_attached` but Add Note and Detach work); that attachment is one level deep and has a limit `alert_group.attach_max`; what Detach returns to (the status, Owner and Snooze end it followed last, chains or the ack timeout starting over, Reminders from the Detach); what happens when either Alert Group resolves; and how statistics count."
  - "[C-37.FR-17] The page names the Permission `alert-groups:attach` and who has it, says that Attach and Detach come from the web UI and the API and never from messengers, and has an API example: attaching from the API with `curl` against `attachAlertGroup` and listing the attached with `attached_to`; `docs/concepts/statuses-and-commands.md`, `alert-groups.md`, `loud-and-quiet.md` and `api-tokens.md` point to it where they list Commands, statistics, Quiet events and Permissions, and `docs/outgoing-webhooks/events.md` lists the five events and `attached_to`."
  - "[C-37.FR-17] \"Learn more\" on the dialog \"Attach to #…\" and in the notice of an attached Alert Group opens the page; the entry `page.attach_detach` is in `web/src/lib/docs-links.json` with its `GUIDE_ABOUT` text; `make docs-check` passes, including `docslinkcheck`, every image has its alternative text and every link and anchor resolves."
  - "[C-37.FR-17] The page has screenshots of the dialog and of the page of a main Alert Group with its section, taken by `make docs-screenshots GUIDES=alert-groups` with the development clock."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-208. Documentation: Attach and Detach

## Scope

**IN**

- The page Concepts → Attach and Detach (C-37.FR-17) and the rows on the neighbouring pages that point to it: Commands,
  statistics, Quiet events, Permissions, outgoing webhook events.
- The "Learn more" links of the dialog and the notice in the web UI, with their entry in `docs-links.json`.
- Two screenshots, generated.

**OUT**

- The behaviour and the pages: S-202 to S-207. The API reference, which `make docs` renders from the spec.
- Incidents (L3) and several teams on one problem: the page only says where they belong.

## Contracts

- **Page**: `docs/concepts/attach-and-detach.md`, in `mkdocs.yml` after Statuses and commands; sections "When to attach",
  "What an attached Alert Group does", "Attach", "Detach", "When an Alert Group resolves", "Statistics", "Who may" and
  "From the API". Examples are generic — the Routes "databases" and "payments", Ivan and Petr; no employer or
  work-specific system is named.
- **Neighbours**: `statuses-and-commands.md` (the Commands table gains Attach and Detach, with "Who may"),
  `alert-groups.md` (the statistics sentence that an Alert Group resolved with its main one is left out),
  `loud-and-quiet.md` (every event on an attached Alert Group is Quiet; S-201's page), `escalation-chains.md` (the row
  S-204 added links to the page), `configure/api-tokens.md` (the Permission `alert-groups:attach` in the table of
  Permissions), `outgoing-webhooks/events.md` (the five events and `attached_to`).
- **Texts**: the English UI texts of S-207, in bold as the pages name controls today; the Russian pages, if the site has
  them, follow the existing practice of the docs.
- **Documentation links** (`web/src/lib/docs-links.json`): `page.attach_detach` → `concepts/attach-and-detach.md` with
  its `GUIDE_ABOUT` entry in `docs-link.tsx` ("Attach and Detach" / «Присоединение групп алертов»), used by the dialog
  (`attach-dialog.tsx`) and the notice (`alert-group-notices.tsx`) as "Learn more", with the new text in `en.json` /
  `ru.json`.
- **Screenshots** (`web/e2e/screenshots/alert-groups.clock.spec.ts`): `NN-attach-dialog` and `NN-main-alert-group` in
  `docs/assets/screenshots/concepts/alert-groups/` or the page's own folder, as the spec's images are placed; generated
  and never listed.
- **Settings cited**: `alert_group.attach_max`, `alert_group.bulk_max`, `route.ack_timeout`, `route.reminders`.

## Steps

1. Write the page from the PRD and the pages of S-207. Check: `make docs-serve`, read in the browser.
2. Add the rows to the five neighbouring pages and the links in the web UI. Check: `docslinkcheck` inside
   `make docs-check`; the TypeScript check.
3. Add the screenshots to the spec and take them. Check: `make docs-screenshots GUIDES=alert-groups`, then
   `make docs-check`.

## Verification

1. `make docs-check` passes: the page builds strictly, every image exists and has alternative text, every link and anchor
   resolves, and `docslinkcheck` finds the new entry.
2. `make docs-serve` → Concepts → Attach and Detach reads as the acceptance says; the `curl` example runs against
   `make dev` with a Responder's token: `attachAlertGroup` answers `200` with `attached_to` set, and
   `listAlertGroups?attached_to=…` lists it.
3. On `muster dev`, "Learn more" in the dialog "Attach to #…" and in the notice of an attached Alert Group opens the page.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe attaching alert groups to a main alert group and detaching them`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- Placed last so that the screenshots show the finished pages and the docs-link check finds the page it names.
- The page tells readers that Attach and Detach are not in the messengers and not yet in the MCP server, which is not built.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-17 | full | |
