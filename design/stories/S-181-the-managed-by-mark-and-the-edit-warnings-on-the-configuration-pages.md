---
id: S-181
title: The "Managed by" mark and the warnings before a change on every configuration page (FE)
capability: C-33
kind: fe
layer: L2
depends_on: [S-180, S-158]
covers: [C-33.FR-12, C-33.AC-4]
files_touched:
  - web/src/components/managed-by.tsx
  - web/src/components/managed-by.test.tsx
  - web/src/components/integration-delete-dialog.tsx
  - web/src/components/route-delete-dialog.tsx
  - web/src/components/destination-delete-dialog.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/routes/integrations.$integrationId.edit.tsx
  - web/src/routes/routes.$routeId.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/routes/schedules.$scheduleId.tsx
  - web/src/routes/escalation-chains.$chainId.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/routes/admin.organization.lookup-tables.$lookupTableId.tsx
  - web/src/routes/admin.organization.link-rules.$linkRuleId.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/link-rule-form.tsx
  - web/src/components/lookup-table-editor.tsx
  - web/src/components/organization-form.tsx
  - web/src/components/organization-form.test.tsx
  - web/src/routes/admin.organization.outbound.tsx
  - web/src/routes/admin.organization.outgoing-heartbeat.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/managed-by.spec.ts
acceptance:
  - "[C-33.FR-12] `ManagedByBadge` renders \"Managed by terraform\" («Управляется terraform») for `managed_by` `terraform` and nothing for null or a read without the field; `ManagedByWarning` renders the warning of C-33.FR-12 with the object's kind and the tool, and nothing without a mark; the delete line renders \"terraform creates it again on its next run.\" (`managed-by.test.tsx`)."
  - "[C-33.FR-12, C-33.AC-4] In a real browser, in English and in Russian, a Route marked `terraform` shows \"Managed by terraform\" next to its title and the warning above its form; changing its name and pressing \"Save\" saves it with no extra step and the Route still reads `managed_by` `terraform`; its delete dialog shows the line of C-33.FR-12; a Route without a mark shows neither (`managed-by.spec.ts`)."
  - "[C-33.FR-12, C-33.AC-4] With marks on an Integration, a Team, a Schedule, an Escalation chain, a Connection, a Destination, a Lookup table and a Link rule, each page shows the mark next to its title and the warning in its form; with the Organization marked, every page that edits its settings (General, Severity levels, Alert handling, Security, Retention, Outgoing heartbeat) shows the Organization's mark, and the Outbound address policy page and the OIDC settings page show their own marks only when those objects carry them (`managed-by.spec.ts`)."
  - "[C-33.FR-11, C-33.FR-12] The update that a page of the Organization sends has no `managed_by`, whether the Organization is marked or not (`organization-form.test.tsx`)."
verify: "make ci && make e2e-web"
operator_attention: false
issue: 699
---

# S-181. The "Managed by" mark and the warnings before a change on every configuration page (FE)

## Scope

**IN**

- A shared component for the mark, the warning in a form and the line of a delete dialog, with their texts in English
  and Russian (C-33.FR-12).
- The mark and the warning on the pages of Integrations, Routes (the Default route included), Teams, Schedules,
  Escalation chains, Connections, Destinations, Lookup tables and Link rules, the Organization's pages, the Outbound
  address policy page and the OIDC settings page; the delete line in their delete dialogs.

**OUT**

- The field in the API (S-179, S-180); the link from the mark to the documentation (S-189).
- A way to clear the mark from the web UI: it is cleared through the API, or by the tool that set it (C-33.FR-14).

## Contracts

- **Reads**: `managed_by` of the reads of S-179 and S-180, through the generated orval types; a read without the field,
  from an older server or a test fixture, shows no mark.
- **Component** (`managed-by.tsx`): `ManagedByBadge` (`{tool}`), shown next to the page title in the place where the
  Team page shows its slug; `ManagedByWarning` (`{object, tool}`), an informational banner — not an error, never
  disabling the form — above the form's first section; `managedByDeleteLine(tool)`, the line that the delete dialogs
  add. The object's kind in the warning is the singular of its page title in lower case, as the existing dialogs name
  it ("route", "team", "escalation chain", …). The singletons have no such title, so their words are "organization"
  («организация»), "outbound address policy" («политика исходящих адресов») and "OIDC configuration" («настройки
  OIDC»): the page titles ("General", "Retention", "Outbound addresses", "OIDC") name a part of the object or are not
  nouns that fit "This … is managed by", and one word serves every page of the Organization. The read-only page of an
  Integration shows the badge only; its warning is on the edit page, which is the form that saves.
- **Texts** (`en.json`, `ru.json`), keys under `managedBy`: "Managed by {{tool}}" / «Управляется {{tool}}»; "This
  {{object}} is managed by {{tool}}. Changes you save here are undone by the next run of {{tool}}: change its code
  instead." / «Этим объектом ({{object}}) управляет {{tool}}. Изменения, сохранённые здесь, отменит следующий запуск
  {{tool}}: меняйте его код.»; "{{tool}} creates it again on its next run." / «{{tool}} создаст его снова при следующем
  запуске.». The tool's name is shown as the API gives it, untranslated.
- **Saving**: the forms keep sending what they send today; the API keeps a left-out `managed_by` (C-33.FR-11), so a
  save from the web UI keeps the mark; the Organization's update leaves `managed_by` out of its body
  (`organizationInput` drops it), as every other form does. The pages that edit the Organization's settings (General,
  Severity levels, Alert handling, Security, Retention, Outgoing heartbeat) show the mark of `getOrganization`, which
  they read already, in their heading; Keyring, System status, Audit log and the pages of Lookup tables and Link rules
  show none of it. The Outbound address policy page reads the mark of `getOutboundPolicy`, and the OIDC settings page
  that of `getOidcSettings`; neither shows the Organization's.
- **Phone width**: the mark wraps under the title at 360 pixels without horizontal scrolling.

## Steps

1. Write the component and its texts with `managed-by.test.tsx`. Check: `make test-web`.
2. Place the mark and the warning on each page and the line in each delete dialog — the three dialog components and
   the dialogs inside the Team, Schedule, Escalation chain, Connection, Lookup table and Link rule pages. Check: the
   pages' existing component tests.
3. Write `managed-by.spec.ts`, which marks the objects through the API first. Check: `make e2e-web` with the spec,
   then `make ci`.

## Verification

Run `make dev`; with the Admin's Personal access token (S-023), mark through the API — as in S-179's and S-180's
Verification — the Route "Default route", a new Route "tf-route", the demo Integration, a Team "payments" with its
Schedule "Main", an Escalation chain "payments", the Connection "Example Mattermost", one of its Destinations, a Lookup
table, a Link rule and the Organization, leaving the Outbound address policy and the OIDC settings unmarked. Then in
Playwright at 1280 × 800, in English, and again in Russian:

1. Routes → "tf-route" → "Managed by terraform" next to the title; the banner "This route is managed by terraform.
   Changes you save here are undone by the next run of terraform: change its code instead." above the form.
2. Change the name to "tf-route-2" → "Save" → the Routes list opens, as after any Route save; the page of "tf-route-2"
   shows "Managed by terraform" still; `GET /routes/{id}` answers `managed_by` `terraform`.
3. "Delete" → the dialog contains "terraform creates it again on its next run."; "Cancel".
4. A Route created in the web UI → no mark, no banner, no delete line.
5. Each other marked object's page → its mark and banner (the read-only Integration page: its mark only; the banner is
   on its edit page); Organization → General, Severity levels, Retention → "Managed by terraform" on each; Keyring →
   no mark; Organization → Outbound, and the OIDC settings page → no mark; mark the policy and the OIDC settings
   through the API and reload → the mark and the banner on each.
6. At 360 × 740, the Route page with its mark passes `expectNoHorizontalScroll`.

`make e2e-web` runs these steps in `managed-by.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): show which tool manages a configuration object and warn before changes`.
- S-158 rebuilds the Escalation chain page; this story follows it so as not to conflict with it.
- Ships in 0.2.0 with S-179 and S-180, as the maintainer decided; the provider (S-182) starts after that tag.
- The warning never asks to confirm a save: the maintainer decided that the mark warns and never blocks (journal D757).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-12 | full | |
| C-33.AC-4 | full | |
