---
id: S-112
title: Minimum severity on the Route form, and the urgent mark and "critical is Urgent" taken off the pages (FE)
capability: C-26
kind: fe
layer: L2
depends_on: [S-111, S-162]
covers: [C-26.FR-8, C-26.FR-9, C-26.FR-10, C-20.FR-1]
files_touched:
  - web/src/components/route-form.tsx
  - web/src/components/route-severity-and-owner.tsx
  - web/src/components/route-severity-and-owner.test.tsx
  - web/src/components/route-list.tsx
  - web/src/routes/routes.new.tsx
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/routes.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/docs-links-forms.spec.ts
  - web/e2e/screenshots/routes-and-templates.spec.ts
  - web/e2e/screenshots/organization.spec.ts
  - docs/concepts/routes-and-group-keys.md
  - docs/configure/organization.md
acceptance:
  - "[C-26.FR-8, C-26.FR-9] The Route form has the section \"Severity, Escalation chains and owner\" with \"Minimum severity\" — None, warning, critical — and the hint \"Only raises the Severity level of the Route's Alert Groups. Replaces the former 'Urgent': an Urgent Alert Group is a critical one.\" with \"Learn more\" to the section Minimum severity of Routes and Group keys; saving critical sends `minimum_severity` `critical` and no `urgent`, and the form no longer offers the urgent switch (`route-severity-and-owner.test.tsx`, `routes.spec.ts`)."
  - "[C-26.FR-8] The Routes list marks a Route whose Minimum severity is warning or critical with \"Minimum severity: critical\" (or warning) where it showed \"Urgent\" (`routes.spec.ts`)."
  - "[C-26.FR-9] On the Alert Group page, a `severity_raised` or `urgency_raised` whose `reason_code` is `minimum_severity` reads \"Minimum severity of the Route\" in the Timeline, and the Audit log names the field `/minimum_severity` \"Minimum severity\" (`timeline.test.tsx`, `audit-diff.test.tsx`, `alert-group-page.spec.ts`)."
  - "[C-26.FR-10, C-20.FR-1] Organization → Alert handling no longer shows \"Critical is Urgent\" and saves without the field; the Instance labels stay (`organization-settings.spec.ts`)."
  - "Every new text exists in English and Russian; the documentation of the Route form and of Alert handling describes the pages as they are, its screenshots are taken again, and `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 401
---

# S-112. Minimum severity on the Route form, and the urgent mark and "critical is Urgent" taken off the pages (FE)

## Scope

**IN**

- "Minimum severity" on the Route form, in the new section "Severity, Escalation chains and owner", which S-119 extends
  with the owning Team and S-158 with the Escalation chains; the urgent switch and its read-only field leave the form.
- The Minimum severity in the Routes list instead of the urgent mark.
- The Timeline text of the reason `minimum_severity` and the Audit log name of the new field.
- "Critical is Urgent" leaves Organization → Alert handling (C-20.FR-1, revised).
- The documentation of these pages and their screenshots.

**OUT**

- The API and the rule: S-111; the profile Paging: S-162.
- The Destinations with Severity conditions, "Also to the Teams from the label" and the preview "Where alerts go":
  S-123. The owning Team: S-119. The Escalation chains of the Route: S-158.
- The page "What critical changes": S-124.

## Contracts

- **API used**: `getRoute`, `createRoute`, `updateRoute` (`minimum_severity`; `urgent` no longer sent), `listRoutes`,
  `listRouteProfiles` (`minimum_severity` pre-fills the form), `getOrganization` and `updateOrganization` (without
  `critical_is_urgent`), `getAlertGroupTimeline` (`reason_code` `minimum_severity` on `TimelineStatusEntry` and
  `TimelineAlertsEntry`), `listAuditLog`.
- **Route form** (`route-form.tsx`, new `route-severity-and-owner.tsx`): the section "Severity, Escalation chains and
  owner" after the Destinations; "Minimum severity" is a select of the values of `MinimumSeverity` labelled "None",
  "warning" and "critical" (Severity levels keep their lowercase names, as on the Severity levels page), with the hint
  above and `DocsLink` `field.minimum_severity`; the zod schema drops `urgent`; a new Route takes the profile's
  `minimum_severity`. Test ids `route-minimum-severity` and `route-severity-section`.
- **Routes list** (`route-list.tsx`): the mark `route-minimum-severity` with "Minimum severity: {level}" replaces
  `route-urgent`.
- **Timeline** (`timeline.tsx`): `changeReason` translates `minimum_severity` as "Minimum severity of the Route".
- **Audit log** (`audit-diff.tsx`): `/minimum_severity` → "Minimum severity"; `/urgent` keeps its label for older
  entries.
- **Alert handling** (`admin.organization.alert-handling.tsx`): the switch and its texts leave the page and the form
  schema; the request leaves the field out.
- **Documentation links** (`docs-links.json`): `field.minimum_severity` →
  `concepts/routes-and-group-keys.md#minimum-severity`; `field.urgent` is removed with the switch, and
  `docs-links-forms.spec.ts` checks the new link instead of "Urgent".
- **Texts** (EN / RU): "Severity, Escalation chains and owner" / «Важность, эскалация и владелец», "Minimum severity"
  / «Минимальная важность маршрута», "None" / «Нет», the hint / «Только поднимает важность групп маршрута. Вместо
  прежнего «Срочный»: срочная группа — это critical.», "Minimum severity: {{level}}" / «Минимальная важность:
  {{level}}», "Minimum severity of the Route" / «Минимальная важность маршрута» (the Timeline reason). The Russian
  texts follow the approved mockups.
- **Documentation**: Routes and Group keys — the step "Create a Route" chooses the Minimum severity instead of turning
  on Urgent, and its example uses it; Organization settings — Alert handling without "Critical is Urgent". The
  screenshot specs of both guides follow the pages, and their images are taken again.

## Steps

1. Write the section and the list mark, and drop the switch. Check: `route-severity-and-owner.test.tsx` covers the
   three values, the hint and the request body; `make lint`.
2. Add the Timeline reason, the Audit log label and the Alert handling page. Check: `timeline.test.tsx`,
   `audit-diff.test.tsx`.
3. Update the documentation links, the end-to-end specs, the documentation and the screenshot specs. Check: the
   Verification below; `make e2e`; `make docs-screenshots GUIDES="routes-and-templates organization"`;
   `make docs-check`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, and in Playwright at 1280 × 800, in English and
in Russian:

1. Routes → "Create route" → "Paging" → the section "Severity, Escalation chains and owner" shows "Minimum severity"
   with "None" selected and the hint; there is no "Urgent" switch. Choose "critical", name the Route "hb" with the
   Matcher `svc="hb"`, "Save" → the Routes list shows "hb" with "Minimum severity: critical".
2. From a terminal, as in S-111: an acknowledged warning Alert Group on a Route "db"; on its Route page set "Minimum
   severity" to "critical" → "Save" → the Alert Group page shows the Severity level critical, the status Acknowledged,
   and the Timeline entry "Became Urgent" with "Minimum severity of the Route".
3. Organization → Audit log → the entry `route.updated` of "db" shows "Minimum severity: none → critical".
4. Organization → Alert handling: no "Critical is Urgent"; change the Instance labels → "Save" → saved.
5. "Learn more" next to "Minimum severity" opens the documentation at "Minimum severity".

`make e2e` runs these steps in `routes.spec.ts`, `alert-group-page.spec.ts` and `organization-settings.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): set the minimum severity on the route form`.
- The section's name already names the Escalation chains and the owner, which S-158 and S-119 add, so that the form
  does not change its structure twice; until then it holds only "Minimum severity".

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-8 | partial | the Route form and list; the rule is S-111 |
| C-26.FR-9 | partial | the Timeline reason on the page; the rule is S-111, the Destinations S-121 |
| C-26.FR-10 | partial | the pages without the urgent mark and "critical is Urgent"; the API is S-111 |
| C-20.FR-1 | partial | revised for L2: "critical is Urgent" leaves Alert handling; the Team label and Working hours on General are S-120 and S-119, the button set S-114 |
