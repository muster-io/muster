---
id: S-124
title: "Documentation: Teams with a worked example, Routing to Teams, and \"What critical changes\""
capability: C-26
kind: docs
layer: L2
depends_on: [S-123]
covers: [C-26.FR-13, C-25.FR-12]
files_touched:
  - docs/concepts/teams.md
  - docs/concepts/routing-to-teams.md
  - docs/concepts/what-critical-changes.md
  - docs/concepts/routes-and-group-keys.md
  - docs/concepts/alert-groups.md
  - docs/configure/organization.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
  - web/e2e/screenshots/teams.spec.ts
acceptance:
  - "[C-26.FR-13] Concepts → Routing to Teams has the worked example of operations and team channels by Severity — the Route \"Infrastructure\" with `infra-critical` and `infra-notice`, \"Also to the Teams from the label\", and Teams with a critical and a notice channel each — says that the feature is optional and that many installations need only two operations channels, explains with an example that a shared Alert Group has one status and one list of Alerts in every channel, that teams wanting their own add the Team label to the Group key, and shows a Route for test clusters that delivers to the Teams without their Escalation chains; every Route setting of S-123 links to it."
  - "[C-26.FR-13] The new page Concepts → What critical changes lists the effects of an Urgent Alert Group — the order of the delivery queue, individual publication during a Storm, the end of a Snooze or an acknowledgement on a rise, the filter and column of the list, `urgent` in outgoing webhook events, the count in Storm summaries — and the Minimum severity of a Route, and Routes and Group keys links to it."
  - "[C-25.FR-12] Concepts → Teams has what a Team owns and what it does not grant, the Team label and Team matchers with a worked example of labels such as `category`, `team` and `system` set by a rule generator, Teams without people that only name Alerts, test clusters where every team shares one namespace (match `pod`, an app label, or put the team label on the workloads), and membership from the identity provider with the claim of each entry and subtrees, with screenshots of the Teams list, a Team page and the Route form."
  - "`make docs-screenshots GUIDES=teams` takes the screenshots of the new spec, every image has its alternative text, `make docs-check` passes, and `llms.txt` lists the new pages in a scenario on setting up Teams and routing."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-124. Documentation: Teams with a worked example, Routing to Teams, and "What critical changes"

## Scope

**IN**

- The completion of Concepts → Teams (C-25.FR-12) with the parts of C-25 that exist after S-123: the worked example,
  Teams without people, test clusters, membership from the identity provider, and screenshots.
- The completion of Concepts → Routing to Teams (C-26.FR-13): the worked example, optionality, one status in every
  channel, the Team label in the Group key, the Route for test clusters.
- The new page Concepts → What critical changes (C-26.FR-13).
- A screenshot spec for these pages and the scenario in `llms.txt`.

**OUT**

- Team mentions per messenger, and whom a message mentions: S-144. Schedules: S-137. Escalation chains, Notification
  rules and "How an alert reaches people": S-161.
- The sections that the BE stories already wrote (S-111, S-115, S-117, S-118, S-121, S-122), which this story keeps and
  joins into the pages.

## Contracts

- **Pages**: `docs/concepts/teams.md`, `docs/concepts/routing-to-teams.md` (both started by the BE stories) and the new
  `docs/concepts/what-critical-changes.md`, under Concepts in `mkdocs.yml`; links from Routes and Group keys (the
  Minimum severity and Urgent sections), Alert Groups (the Teams of an Alert Group), Organization settings (the Team
  label and Working hours on General) and the home page's list of concepts. The anchors that `docs-links.json` names
  (S-112, S-119, S-120, S-123) stay.
- **Examples**: generic and from the PRD's scenarios only — Teams `payments`, `checkout`, `search`, `devops`, people
  `alice`, `bob`, `olga`, `petr`, `example.org`; Alerts such as `KubernetesContainerOOMKilled` with `category`, `team`
  and `system` labels from a rule generator. No employer and no work-specific system is named.
- **Screenshots** (`web/e2e/screenshots/teams.spec.ts`, Playwright on `muster dev`): the Teams list with unknown values,
  the Team page with its people, identity provider groups and Destinations, the Route form with the option and the
  preview, and the Alert Group page with its Teams; the images go to `docs/assets/screenshots/concepts/teams/` and are
  never listed.
- **llms.txt** (`docs/llms.txt.tmpl`): a scenario "Set up Teams and route to them" with the three pages.

## Steps

1. Write the pages from the sections already there. Check: `make docs-serve`, read in the browser.
2. Write the screenshot spec and take the screenshots. Check: `make docs-screenshots GUIDES=teams`.
3. Add the scenario to `llms.txt` and the links. Check: `make docs-check`.

## Verification

1. `make docs-check` passes: every page builds strictly, every image exists and has its alternative text, every link and
   anchor resolves, and `docslinkcheck` finds every anchor of `docs-links.json`.
2. `make docs-serve` → Concepts → Teams, Routing to Teams and What critical changes read as the acceptance says; the
   worked example of Routing to Teams gives the same channels as the end-to-end test of S-121.
3. On `muster dev` (`make dev`), "Learn more" next to "Also to the Teams from the label" and next to "Minimum severity"
   on the Route form opens these pages at their sections.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe teams, routing to teams and what critical changes`.
- Placed after S-123 so that its screenshots show the finished pages; the BE stories wrote the sections that the forms
  link to, so no link waits for this story.
- Team mentions are left to S-144, which documents them with the Mention entries; C-25.FR-12 is complete with it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-13 | partial | the routing page, "What critical changes"; S-111, S-121 and S-122 wrote their sections |
| C-25.FR-12 | partial | the Teams page without Team mentions, which are S-144's |
