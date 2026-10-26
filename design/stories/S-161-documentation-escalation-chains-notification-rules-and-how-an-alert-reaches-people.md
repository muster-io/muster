---
id: S-161
title: "Documentation: Escalation chains, Notification rules and How an alert reaches people"
capability: C-32
kind: docs
layer: L2
depends_on: [S-124, S-137, S-151, S-153, S-157, S-159, S-160]
covers: [C-32.FR-11, C-32.FR-20, C-31.FR-6]
files_touched:
  - docs/concepts/escalation-chains.md
  - docs/concepts/notification-rules.md
  - docs/concepts/how-an-alert-reaches-people.md
  - docs/concepts/teams.md
  - docs/concepts/schedules.md
  - docs/concepts/routing-to-teams.md
  - docs/configure/messages-and-templates.md
  - docs/messengers/notification-gateways.md
  - docs/messengers/write-a-bridge.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
  - web/e2e/screenshots/escalation-chains.spec.ts
acceptance:
  - "[C-32.FR-20] Concepts → Escalation chains joins the sections S-154, S-169, S-155, S-168 and S-157 wrote into one page: the steps with the payments chain of scenario 1, the table of C-32.FR-5 of when chains start, start over and stop, the ack timeout and Unclaimed, where chains Page, the table of C-32.FR-11, undelivered Pages, Assignees and the Owner's Team; it says, per messenger, what the facts of S-160 found, and, where they do not confirm a push, that at night only a Notification gateway is reliable."
  - "[C-31.FR-6] Concepts → Notification rules describes the two rules and their defaults, the switch and custom rules, how a rule combines with the steps of a chain — a step Pages each person by their normal or important rule, the rule stops with its chain — that calls combine per person and gateway, and that a Mention reaches a person only through an Account link in the Identity space of the Destination's Connection."
  - "[C-32.FR-20] The new page Concepts → How an alert reaches people walks one Alert of `team=\"payments\"` from its Route and Alert Group to its Destinations, its Teams, its Escalation chains and their Pages, by Mention and by call, with two diagrams — the path of the Alert, and the timeline of the chains with their steps — that render in both themes; Teams, Schedules and Routing to Teams link to it, and Messages and templates and Write a bridge describe the Assignees of `layout` and item 7 of the Root message."
  - "[C-32.FR-20] The pages have screenshots of the chain page and of the Alert Group page with its running chains and Assignees, taken by `web/e2e/screenshots/escalation-chains.spec.ts`; `make docs-screenshots GUIDES=escalation-chains` takes them, every image has its alternative text, `make docs-check` passes, and `llms.txt` lists the new page in a scenario on Paging people."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-161. Documentation: Escalation chains, Notification rules and How an alert reaches people

## Scope

**IN**

- The completion of Concepts → Escalation chains (C-32.FR-20) from the sections the backend stories wrote, with the
  statement about the night that the facts of S-160 call for.
- The completion of Concepts → Notification rules (C-31.FR-6).
- The new page "How an alert reaches people", with diagrams (C-32.FR-20).
- The Assignees in the layout for bridge authors, which S-130 left to this story, and the links from the pages of Teams,
  Schedules, Routing to Teams and Notification gateways.

**OUT**

- The sections already written by the backend stories, which this story keeps and joins.

## Contracts

- **Pages**: `docs/concepts/escalation-chains.md` (completed); `docs/concepts/notification-rules.md` (completed);
  `docs/concepts/how-an-alert-reaches-people.md` (new, in the navigation after Routing to Teams), with its two diagrams
  in Mermaid, as the site's other pages draw theirs, each followed by its text in words;
  `docs/configure/messages-and-templates.md` — item 7 with the Assignees and the Owner's Team;
  `docs/messengers/write-a-bridge.md` — the `assignees` of `layout` with their `ref`; links from
  `docs/concepts/teams.md`, `schedules.md`, `routing-to-teams.md` and `docs/messengers/notification-gateways.md`;
  `docs/index.md` — the page in the overview.
- **The night**: for each of Mattermost, Slack and Telegram, the page says what S-160's entry found; until S-160 merges,
  and wherever its facts do not confirm a push, "at night only a Notification gateway is reliable" stays, here and in
  Messengers → Notification gateways.
- **Screenshots**: `web/e2e/screenshots/escalation-chains.spec.ts` on `muster dev` — the chain page with the payments
  chain and "How it went", and the Alert Group page with its running chains and Assignees; the images are generated and
  not listed.
- **llms.txt** (`docs/llms.txt.tmpl`): a scenario "Page the On-call" that names the three pages.

## Steps

1. Join and complete the two pages, citing S-160's entries. Check: `make docs-check`.
2. Write "How an alert reaches people" with its diagrams, and the links. Check: `make docs-serve` renders the diagrams
   in the light and the dark theme.
3. Write the screenshot spec and take the images; add the scenario to `llms.txt`. Check: `make docs-screenshots
   GUIDES=escalation-chains`; `make docs-check`.

## Verification

`make docs-serve`, then open Concepts → How an alert reaches people: the walk of the Alert of `team="payments"` from
its Route to the Pages, both diagrams legible in the light and the dark theme, links to Teams, Routing to Teams,
Schedules, Escalation chains and Notification rules; Concepts → Escalation chains shows the table of when chains run,
the table of how a Page reaches a person with the facts of S-160 cited, and the screenshots; `make docs-check` passes;
`site/llms.txt` lists the pages under "Page the On-call".

## Open questions

None.

## Notes

- Suggested commit: `docs: explain escalation chains, notification rules and how an alert reaches people`.
- The examples are scenario 1 and 2 of C-32 with the Teams `devops` and `payments` and the people `ivan` and `petr`; no
  employer or real system is named.
- If S-160's facts confirm a push in every messenger, the statement about the night changes to say which settings a
  person needs, and the pages keep recommending a Notification gateway for people who must be reached whatever their
  messenger does.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-11 | partial | the table in the documentation with the facts; the behaviour is S-155 and S-157 |
| C-32.FR-20 | full | |
| C-31.FR-6 | full | |
