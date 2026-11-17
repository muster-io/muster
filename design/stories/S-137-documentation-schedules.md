---
id: S-137
title: "Documentation: Schedules with Rotation layers, several Schedules, Overrides, gaps and the iCal feed"
capability: C-27
kind: docs
layer: L2
depends_on: [S-136]
covers: [C-27.FR-14]
files_touched:
  - docs/concepts/schedules.md
  - docs/concepts/teams.md
  - docs/operations/runbooks/MusterScheduleGap.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
  - web/e2e/screenshots/schedules.clock.spec.ts
acceptance:
  - "[C-27.FR-14] Concepts → Schedules has the worked example of a weekly Rotation layer around the clock under a working-hours Rotation layer with other people — the Schedule \"Main\" of \"payments\" of scenario 1 — and shows who is On-call on a Wednesday at 15:00 and at 21:00; several Schedules for a first and a second line, with the main Schedule naming the Team's On-call; Overrides, who may make them and that they send no message; gaps and the people whose Shifts are skipped; the Internal alert `MusterScheduleGap`, and that a Schedule which covers only part of the day, such as a second line in working hours, keeps it off; unreachable On-calls; and the iCal feed."
  - "[C-27.FR-14] The page has screenshots of the Schedule page with its two-week view and the Override form, and of \"On call now\" on the Home page; Concepts → Teams and the runbook of `MusterScheduleGap` link to it."
  - "`make docs-screenshots GUIDES=schedules` takes the screenshots of the new spec, every image has its alternative text, `make docs-check` passes, and `llms-full-guide.txt` lists the page in a scenario on setting up who is On-call."
verify: "make docs-check"
operator_attention: false
issue: 426
---

# S-137. Documentation: Schedules with Rotation layers, several Schedules, Overrides, gaps and the iCal feed

## Scope

**IN**

- The completion of Concepts → Schedules (C-27.FR-14) from the sections that S-131, S-133, S-164 and S-134 wrote: the
  worked example, several Schedules, Overrides, gaps and skipped people, the Internal alert and when to keep it off,
  unreachable On-calls and the iCal feed, with screenshots.
- Links from Concepts → Teams and from the runbook of `MusterScheduleGap`, and the scenario in `llms-full-guide.txt`.

**OUT**

- The sections already written by the BE stories, which this story keeps and joins. Escalation steps that Page the
  On-call of a Schedule: S-161. Whom a message mentions, with the On-call entries: S-144.

## Contracts

- **Pages**: `docs/concepts/schedules.md` (started by S-131), linked from `docs/concepts/teams.md` ("Schedules"), from
  the runbook page `docs/operations/runbooks/MusterScheduleGap.md` and from the home page's list of concepts
  (`docs/index.md`); the anchors that `docs-links.json` names (S-135, S-136) stay.
- **Examples**: generic and from C-27's scenarios only — Teams `payments`, `devops`, `search`; people `petr`, `sergey`,
  `olga`, `ivan`; Rotation layers "Around the clock" and "Working hours 11–19". No employer and no work-specific system
  is named.
- **Facts**: where the page explains unreachable On-calls, what S-132 recorded about Telegram, citing the facts: a
  `tg://user?id=` Mention in a comment rang a member of the discussion group who had muted it, on the desktop and on the
  phone, so an On-call may mute the discussion group (F-129); it did not ring the group's creator, anonymous in it, who
  had muted it, so an On-call who created the discussion group or is anonymous in it does not mute it, which the
  Destination check cannot see (F-129); and a person who leaves the discussion group may show as unreachable only some
  time later, about a minute in F-128.
- **Screenshots** (`web/e2e/screenshots/schedules.clock.spec.ts`, a clock spec: it moves the development clock to a
  fixed Tuesday 15:00, so that the weeks the images show are the same in every run; Playwright on `muster dev`): the
  Schedule page with a gap and a warning and its two-week view; the Override form in the mode "I give my Shift to…"; "On call now" on the Home
  page; the images go to `docs/assets/screenshots/concepts/schedules/` and are never listed.
- **llms-full-guide.txt** (the template `full` of `docs/llms.txt.tmpl`): a scenario "Set up who is On-call" with Concepts → Teams and Concepts →
  Schedules.

## Steps

1. Write the page from the sections already there. Check: `make docs-serve`, read in the browser.
2. Write the screenshot spec and take the screenshots. Check: `make docs-screenshots GUIDES=schedules`.
3. Add the links and the scenario. Check: `make docs-check`.

## Verification

1. `make docs-check` passes: every page builds strictly, every image exists and has its alternative text, every link and
   anchor resolves, and `docslinkcheck` finds every anchor of `docs-links.json`.
2. `make docs-serve` → Concepts → Schedules reads as the acceptance says; the worked example gives the same On-call as
   the check of C-27.AC-1 in S-131.
3. On `muster dev` (`make dev`), "Guide" next to the Schedule page's title and "Learn more" next to the gap alert and
   the iCal section open the page at its sections.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe schedules, overrides, gaps and the ical feed`.
- Placed after S-136 so that the screenshots show the finished pages; the BE stories wrote the sections the pages link
  to, so no link waits for this story.
- The spec is a clock spec (`schedules.clock.spec.ts`, which `GUIDES=schedules` also takes): the page shows weekdays and
  dates of the weeks around now, which no rewrite of `capture` fixes, so the spec moves the development clock to the
  Tuesday 15:00 Berlin of the 52-week series from 2027-01-12 that lies after the real now.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-14 | partial | the worked example, the screenshots and the joined page; S-131, S-133, S-164 and S-134 wrote its sections |
