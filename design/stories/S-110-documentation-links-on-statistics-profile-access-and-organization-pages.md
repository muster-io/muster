---
id: S-110
title: "Documentation links on the Statistics, profile, Access and Organization pages: page titles, fields and problems (FE)"
capability: C-21
kind: fe
layer: L1
depends_on: [S-109]
covers: [C-21.FR-11, C-21.AC-8]
files_touched:
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/docs-links-admin.spec.ts
  - web/src/routes/statistics.tsx
  - web/src/routes/profile.tsx
  - web/src/routes/admin.users.index.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/routes/admin.service-accounts.index.tsx
  - web/src/routes/admin.service-accounts.$serviceAccountId.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/components/organization-form.tsx
  - web/src/components/organization-nav.tsx
  - web/src/routes/admin.organization.security.tsx
  - web/src/routes/admin.organization.outgoing-heartbeat.tsx
  - web/src/routes/admin.organization.lookup-tables.index.tsx
  - web/src/routes/admin.organization.lookup-tables.new.tsx
  - web/src/routes/admin.organization.lookup-tables.$lookupTableId.tsx
  - web/src/routes/admin.organization.link-rules.index.tsx
  - web/src/routes/admin.organization.link-rules.new.tsx
  - web/src/routes/admin.organization.link-rules.$linkRuleId.tsx
  - web/src/routes/admin.system-status.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/components/permission-picker.tsx
  - web/src/components/link-rule-form.tsx
  - web/src/components/lookup-table-editor.tsx
  - web/src/components/network-list-editor.tsx
  - web/src/components/notice-banners.tsx
  - web/src/components/system-status-sections.tsx
  - web/src/components/keyring-status.tsx
acceptance:
  - "[C-21.FR-11, C-21.AC-8] The pages of the rows of reference.md's \"Next to page titles\" marked S-110 — Statistics, Profile, Access → Users, a User, Service accounts, a Service account and OIDC, and every page of Organization's sub-navigation (General, Severity levels, Alert handling, Security, Outbound addresses, Retention, Keyring, Outgoing heartbeat, Lookup tables and a Lookup table, Link rules and a Link rule, System status, Audit log) — show the Guide link to the row's target; the pages that `organization-form.tsx` draws take their key from their entry of `organization-nav.tsx`."
  - "[C-21.FR-11, C-21.AC-8] \"Learn more\" shows beside OIDC's group mapping and proxy, the Security page's TOTP policy, a Service account's Permissions, a Link rule's template, a Lookup table's rows and the allowed networks of Outbound addresses; \"How to fix\" beside the banners \"No replica is leading\" and \"Muster is recovering after downtime\", the OIDC warnings that nobody can sign in and that the client secret expires, the System status's missing outgoing heartbeat and the Keyring's two notices — each to the row's target of reference.md."
  - "[C-21.AC-8] Every place of reference.md's Documentation links now shows its link: `docs-links.json` holds every key of the table and `make docs-check` passes; the links read in Russian as in English; and the Organization, Access and Statistics pages keep no horizontal scrollbar at 360 px wide (`web/e2e/docs-links-admin.spec.ts`)."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-110. Documentation links on the Statistics, profile, Access and Organization pages: page titles, fields and problems (FE)

## Scope

**IN**

- The Guide links of the Statistics, profile, Access and Organization pages, the System status and the Audit log.
- "Learn more" in their forms and "How to fix" beside their warnings and the banners every page shows Admins.

**OUT**

- The version, the components, the anchor check and the other page titles (S-108).
- The fields and problems of the Integration, Route, Connection, Destination and Alert Group pages (S-109).

## Contracts

- **The list**: `docs-links.json` gains the keys of reference.md's
  [Documentation links](../prd/l1/reference.md#documentation-links) marked S-110; afterwards it holds every key of the
  table, and the pull request lists the table's keys against the file's.
- **Placement**: the pages with their own `h1` use `PageTitle` of S-108; `organization-nav.tsx` gives each entry of the
  Organization's sub-navigation its key, which `organization-form.tsx` reads for the pages it draws, so the seven
  pages that use it change no route file; the Security page's form is its own route file. "Learn more" and "How to
  fix" as in S-109; the banners of `notice-banners.tsx` show "How to fix" only to Admins, who see those banners.
- **Texts**: those of S-108, with accessible names in both languages.
- **End-to-end** (`web/e2e/docs-links-admin.spec.ts`): the placement of the acceptance on `muster dev`. A banner or
  notice that `muster dev` cannot bring about within a spec — "No replica is leading", the recovery after downtime, the
  Keyring's notices — is shown by the component's test with its link instead, and that test file is added to
  `files_touched` in the pull request.

## Steps

1. Add the keys, and the titles of the Access, profile and Statistics pages. Check: `make lint`, `make docs-check`.
2. Give the Organization's sub-navigation its keys and place the titles of its pages. Check: `make test-web`.
3. Place "Learn more" and "How to fix" in the admin forms, the System status, the Keyring and the banners. Check:
   `make test-web`.
4. Write the end-to-end spec. Check: `make e2e-web`, then Verification.

## Verification

Playwright against `muster dev` at 1280 and 360 px, signed in as the Admin:

1. Organization → Retention: "Guide" beside the title links to
   `https://muster-io.github.io/muster/latest/configure/organization/#retention`; Organization → Keyring to
   `…/operations/key-rotation/`; Organization → Audit log to `…/configure/organization/#audit-log`; System status to
   `…/operations/monitoring/#system-status`.
2. Access → Users: to `…/sign-in/#add-a-user`; Access → OIDC: "Learn more" beside the group mapping to
   `…/sign-in/oidc-keycloak/#2-add-the-groups-claim`; with an empty mapping the warning "Nobody will be able to sign in
   through OIDC." shows "How to fix" to the same section.
3. System status without an outgoing heartbeat: "Set up an outgoing heartbeat so that a stopped Muster is noticed."
   with "How to fix" to `…/operations/outgoing-heartbeat/`.
4. Statistics and Profile show their Guide links, also for a Responder.
5. Russian: «Руководство», «Подробнее», «Как исправить» on the same places.
6. At 360 px the Organization pages, Access → Users and Statistics have `document.documentElement.scrollWidth <= 360`.

```sh
jq -r 'keys[]' web/src/lib/docs-links.json | wc -l      # the number of keys of reference.md's table
make docs-check                                          # passes
```

## Open questions

None.

## Notes

- Suggested commit: `feat(web): link the organization, access and profile pages to the documentation`.
- With 30 files the story stays under the size signal.
- It completes C-21.FR-11 and C-21.AC-8: after it every place of the table shows its link.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-11 | partial | these pages; S-108 and S-109 cover the rest, and this story completes the requirement |
| C-21.AC-8 | partial | these places; S-108 and S-109 cover the rest, and this story completes the statement |
