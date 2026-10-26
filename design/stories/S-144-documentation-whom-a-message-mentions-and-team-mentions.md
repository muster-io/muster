---
id: S-144
title: "Documentation: whom a message mentions, with the entries, what each messenger supports and Team mentions"
capability: C-28
kind: docs
layer: L2
depends_on: [S-132, S-143]
covers: [C-28.FR-11, C-25.FR-12]
files_touched:
  - docs/concepts/mentions.md
  - docs/concepts/teams.md
  - docs/concepts/loud-and-quiet.md
  - docs/configure/messages-and-templates.md
  - docs/messengers/mattermost.md
  - docs/messengers/slack.md
  - docs/messengers/telegram.md
  - docs/messengers/bridge.md
  - docs/messengers/write-a-bridge.md
  - docs/outgoing-webhooks/events.md
  - docs/outgoing-webhooks/templates.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
  - web/e2e/screenshots/mentions.spec.ts
acceptance:
  - "[C-28.FR-11] Concepts → Whom a message mentions describes the entries and their Loud events with the example of `infra-critical` mentioning \"On-call of the Team from the label\" on every Loud event and `@here` only for a new Alert Group, a rise to critical and the start of a Storm; the table of C-28.FR-2; Team mentions without a fallback to the On-call; undelivered Mentions; the Owner rules that stay; and the migration of the 0.1 settings, with screenshots of the Mentions grid and of \"How to mention the Team\"."
  - "[C-25.FR-12] Concepts → Teams has \"Team mentions\" per messenger — a Mattermost group, a Slack user group with `usergroups:read`, a bridge's group, none in Telegram — and links to the page above; the page on buttons (Statuses and commands, \"Button sets\") is linked from it."
  - "[C-28.FR-11] Loud and Quiet, Messages and templates, the pages of each messenger, Write a bridge (declaring group mentions and mentions of everyone in `info`, and the `kind` of a Mention) and the pages of outgoing webhooks (a Team as a target of `mentions`) point to the new page instead of describing the 0.1 settings; what S-132 and S-138 recorded about groups, `<!everyone>` and muted discussion groups is said where it matters, citing the facts."
  - "`make docs-screenshots GUIDES=mentions` takes the screenshots of the new spec, every image has its alternative text, `make docs-check` passes, and `llms.txt` lists the page in a scenario on mentioning the right people."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-144. Documentation: whom a message mentions, with the entries, what each messenger supports and Team mentions

## Scope

**IN**

- The completion of Concepts → Whom a message mentions (C-28.FR-11) from the sections S-140 and S-165 wrote: the
  worked example, the table of what each messenger supports, Team mentions without a fallback to the On-call, the Owner
  rules, undelivered Mentions and the migration from 0.1, with screenshots.
- The section "Team mentions" of Concepts → Teams completed (the part of C-25.FR-12 that S-124 left open), and the
  pages that described the 0.1 Mention settings pointed at the new page.
- What the spikes S-132 and S-138 recorded, where readers need it.

**OUT**

- The sections already written by the BE stories, which this story keeps and joins. Pages by Mention of Escalation
  chains and "How an alert reaches people": S-161.

## Contracts

- **Pages**: `docs/concepts/mentions.md` (started by S-140, with S-165's section), linked from Concepts → Teams, Loud
  and Quiet, Messages and templates ("Whom a message mentions" becomes a short pointer), each messenger's page, the
  Bridge pages and the outgoing webhook pages, and from the home page's list of concepts; the anchors that
  `docs-links.json` names (S-143) stay.
- **Examples**: generic and from C-28's scenarios only — Destinations `infra-critical`, `infra-notice`,
  `payments-notice`, `search-notice`; Teams `payments`, `devops`, `search`; the Mattermost group `payments_team`;
  people `ivan`, `petr`. No employer and no work-specific system is named.
- **Screenshots** (`web/e2e/screenshots/mentions.spec.ts`, Playwright on `muster dev`): the Mentions grid of a
  Mattermost Destination with a warning, the same section of a Telegram Destination with its unavailable targets, and
  "How to mention the Team"; the images go to `docs/assets/screenshots/concepts/mentions/` and are never listed.
- **llms.txt** (`docs/llms.txt.tmpl`): a scenario "Mention the right people" with Concepts → Whom a message mentions and
  Concepts → Teams.

## Steps

1. Write the page from the sections already there and point the other pages at it. Check: `make docs-serve`, read in
   the browser.
2. Write the screenshot spec and take the screenshots. Check: `make docs-screenshots GUIDES=mentions`.
3. Add the scenario and the links. Check: `make docs-check`.

## Verification

1. `make docs-check` passes: every page builds strictly, every image exists and has its alternative text, every link and
   anchor resolves, and `docslinkcheck` finds every anchor of `docs-links.json`.
2. `make docs-serve` → Concepts → Whom a message mentions reads as the acceptance says; its worked example gives the same
   Mentions as the check of C-28.AC-1 in S-140.
3. On `muster dev` (`make dev`), "Learn more" next to "Mentions" on a Destination and next to "How to mention the Team"
   on a Team opens the pages at their sections.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe whom a message mentions and team mentions`.
- C-25.FR-12 is complete with this story: S-115, S-117, S-118 and S-124 wrote the rest of the Teams page.
- Placed after S-143 so that the screenshots show the finished form.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-11 | partial | the page on whom a message mentions; the page on buttons is S-113 and S-114 |
| C-25.FR-12 | partial | Team mentions per messenger; S-115, S-117, S-118, S-124 and S-139 wrote the rest |
