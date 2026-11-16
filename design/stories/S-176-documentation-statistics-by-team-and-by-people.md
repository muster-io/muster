---
id: S-176
title: "Documentation: statistics By Team and By people, the first acknowledgement and the filters"
capability: C-25
kind: docs
layer: L2
depends_on: [S-174, S-175]
covers: [C-09.FR-31]
files_touched:
  - docs/concepts/alert-groups.md
  - web/e2e/screenshots/alert-groups.clock.spec.ts
acceptance:
  - "[C-09.FR-31] Concepts → Alert Groups → Statistics keeps its heading and anchor `#statistics` and describes the four views; the list's filters Route, Integration, Severity level, Urgent, Team, Reopened and labels, with \"Acknowledged first by\", that apply to every view, and that the period counts the Alert Groups that started in it; By Team, where an Alert Group of several Teams counts for each and the name of a Team opens By people for it; and By people, with \"Acknowledged first\", \"Resolved\" and time to acknowledge."
  - "[C-09.FR-31] The section says that one Alert Group gives its acknowledgement to whoever acknowledged it first, that a Takeover, an Unacknowledge or a Reopen does not move it, which is why a person who takes over late gets no credit, and that it counts even when it came after the end of the period; that the row \"Unknown\" holds the Alert Groups acknowledged before 0.2.0 whose Timeline was already removed, wholly or in part (`retention.alert_details`), or first acknowledged during the rolling update, and why its numbers keep the totals equal to those of By route; and that Service accounts appear with what they resolved, since they cannot acknowledge."
  - "[C-09.FR-31] The section has screenshots of By team and of By people with the row of a Service account; `make docs-screenshots GUIDES=alert-groups` takes them, every image has its alternative text, and `make docs-check` passes."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-176. Documentation: statistics By Team and By people, the first acknowledgement and the filters

## Scope

**IN**

- The section "Statistics" of Concepts → Alert Groups (C-09.FR-31): the views, the filters, the first acknowledgement
  and its credit, the row "Unknown", Service accounts, and the period by start, with screenshots of By team and By
  people.

**OUT**

- The API and the page: S-174, S-175. The upgrade note of the migration in `docs/install/versions.md`: S-174.
- The API reference, which `make docs` renders from the spec.

## Contracts

- **Page**: `docs/concepts/alert-groups.md`, the section `## Statistics` with its anchor `#statistics`, which
  `page.statistics` names (reference.md, Documentation links) and which stays; subsections for the filters, By Team and
  By people under it.
- **Examples**: generic, from C-09's acceptance — the people alice, bob and carol, the Service account terraform, the
  Teams payments and search. No employer and no work-specific system is named.
- **Screenshots** (`web/e2e/screenshots/alert-groups.clock.spec.ts`, which already takes `06-statistics` and
  `07-statistics-days`): `08-statistics-by-team` and `09-statistics-by-people`, with a Service account's row, in
  `docs/assets/screenshots/concepts/alert-groups/`; the images are generated and never listed.
- **Texts**: the English UI texts of S-175, in bold as the page names controls today.

## Steps

1. Write the section from the PRD and the pages of S-175. Check: `make docs-serve`, read in the browser.
2. Extend the screenshot spec and take the screenshots. Check: `make docs-screenshots GUIDES=alert-groups`, then
   `make docs-check`.

## Verification

1. `make docs-check` passes: every page builds strictly, every image exists and has its alternative text, every link
   and anchor resolves, and `docslinkcheck` still finds `concepts/alert-groups.md#statistics` of `page.statistics`.
2. `make docs-serve` → Concepts → Alert Groups → Statistics reads as the acceptance says; its numbers for alice and bob
   are those of C-09.AC-28.
3. On `muster dev` (`make dev`), "Guide" next to the title of the statistics page opens the section.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe statistics by team and by people and the first acknowledgement`.
- Placed after S-175 so that the screenshots show the finished page.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-31 | full | |
