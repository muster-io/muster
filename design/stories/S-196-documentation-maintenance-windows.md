---
id: S-196
title: "Documentation: Maintenance windows"
capability: C-34
kind: docs
layer: L2
depends_on: [S-193, S-194, S-195]
covers: [C-34.FR-23]
files_touched:
  - docs/concepts/maintenance-windows.md
  - mkdocs.yml
  - docs/concepts/alert-groups.md
  - docs/concepts/escalation-chains.md
  - docs/outgoing-webhooks/events.md
  - web/src/lib/docs-links.json
  - web/src/components/docs-link.tsx
  - web/src/routes/maintenance-windows.index.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/screenshots/maintenance-windows.clock.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-34.FR-23] Concepts → Maintenance windows, in the navigation, describes the three objects, the rule of every Team and of every Integration with the worked example of scenario 3 (`KubePodCrashLooping` with Alerts of payments and analytics is not kept back by the window of payments), Urgent Alert Groups and `maintenance.urgent_passes`, what happens at the end (published with the mark, the Thread reply, chains starting over, resolved ones never posted), the Internal alerts of an Integration, and that time to acknowledge includes the window."
  - "[C-34.FR-23] The page has a CI example that opens a window with a Service account token of the Role Responder before a deploy and closes it after, with `curl` against `createMaintenanceWindow` and `endMaintenanceWindow`, and says that windows do not repeat and that Terraform manages them with a resource the provider's own capability adds later."
  - "[C-34.FR-23] \"Guide\" beside the title of the page \"Maintenance windows\" and the link of \"Let Urgent Alert Groups through\" open the page and its section `#urgent-alert-groups`; `make docs-check` passes, including `docslinkcheck`, every image has its alternative text and every link and anchor resolves."
  - "[C-34.FR-23] The page has screenshots of the Team's section with the banner and of the Alert Group page with its banner, taken by `make docs-screenshots GUIDES=maintenance-windows` with the development clock."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-196. Documentation: Maintenance windows

## Scope

**IN**

- The page Concepts → Maintenance windows (C-34.FR-23) with its CI example, the rows added to Concepts → Alert Groups and
  Concepts → Escalation chains that point to it, and the two events in the outgoing webhooks' page if S-192 left a
  gap.
- The Guide link of the page "Maintenance windows" and the field link of "Let Urgent Alert Groups through" in the web
  UI, with their entries in `docs-links.json` and `GUIDE_ABOUT`.
- The screenshots, generated.

**OUT**

- The behaviour and the pages: S-190 to S-195. The API reference, which `make docs` renders from the spec. The Terraform
  resource and its guide: specified with the provider (L2 §1.5).

## Contracts

- **Page**: `docs/concepts/maintenance-windows.md`, in `mkdocs.yml` after Escalation chains; the anchor
  `#urgent-alert-groups`; the sections "What a window is", "Who can open one", "Which Alert Groups it keeps back" with the
  Team and Integration rules and the worked example, "Urgent Alert Groups", "What happens at the end", "Statistics", "From
  CI" and "Scheduled windows".
- **Examples**: generic — the Teams payments and analytics, the Integration `am-prod`, the Service account `ci`; no
  employer or work-specific system is named. The CI example is a shell snippet with `curl`, the Service account token in
  a variable and `jq`.
- **Texts**: the English UI texts of S-194 and S-195, in bold as the pages name controls today; the Russian pages, if the
  site has them, follow the existing practice of the docs.
- **Documentation links** (`web/src/lib/docs-links.json`): `page.maintenance_windows` → `concepts/maintenance-windows.md`
  with its `GUIDE_ABOUT` entry in `docs-link.tsx` ("Maintenance windows" / «Окна обслуживания»), and
  `field.maintenance_urgent` → `concepts/maintenance-windows.md#urgent-alert-groups`, used by the page of S-194's list and
  the Team page's form (`maintenance-windows.index.tsx`, `teams.$teamId.tsx`) and the new text in `en.json` / `ru.json`.
- **Screenshots** (`web/e2e/screenshots/maintenance-windows.clock.spec.ts`): `01-team-section`, `02-alert-group-banner`
  in `docs/assets/screenshots/concepts/maintenance-windows/`; generated and never listed.
- **Settings cited**: `maintenance.limits`, `maintenance.presets`, `maintenance.urgent_passes`, `retention.audit_log`.

## Steps

1. Write the page from the PRD and the pages of S-194 and S-195. Check: `make docs-serve`, read in the browser.
2. Add the rows to the two concept pages and the links in the web UI. Check: `docslinkcheck` inside `make docs-check`; the
   TypeScript check.
3. Write the screenshot spec and take the screenshots. Check: `make docs-screenshots GUIDES=maintenance-windows`, then
   `make docs-check`.

## Verification

1. `make docs-check` passes: the page builds strictly, every image exists and has alternative text, every link and anchor
   resolves, and `docslinkcheck` finds the two new entries.
2. `make docs-serve` → Concepts → Maintenance windows reads as the acceptance says; the CI example runs against
   `make dev` with a Service account token (Responder): the first `curl` answers `201` with a window `state` of `active`,
   the second closes it and answers `ended`.
3. On `muster dev`, "Guide" next to the title of the page "Maintenance windows" opens the page; the link beside "Let
   Urgent Alert Groups through" opens `#urgent-alert-groups`.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe maintenance windows and how to open one from a pipeline`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- Placed last so that the screenshots show the finished pages and the docs-link check finds the page it names.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-23 | full | the statistics half of the requirement is a property of the existing statistics, which counts kept-back Alert Groups like any other (S-191 to S-193 change nothing there) |
