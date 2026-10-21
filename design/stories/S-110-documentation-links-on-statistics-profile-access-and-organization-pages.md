---
id: S-110
title: "Documentation links on the Statistics, profile, Access and Organization pages: page titles, fields and problems (FE)"
capability: C-21
kind: fe
layer: L1
depends_on: [S-109, S-107]
covers: [C-21.FR-11, C-21.AC-8, C-21.FR-9]
files_touched:
  - AGENTS.md
  - CONTRIBUTING.md
  - docs/messengers/write-a-bridge.md
  - internal/accountlinks/accountlinks_test.go
  - internal/accountlinks/bridge_test.go
  - internal/accountlinks/live_test.go
  - internal/accountlinks/slack_test.go
  - internal/api/accountlinks_test.go
  - internal/api/connections_test.go
  - internal/api/oidc_test.go
  - internal/cli/dev.go
  - internal/connections/bridge_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/draft_test.go
  - internal/connections/live_test.go
  - internal/connections/slack_test.go
  - internal/connections/telegram_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/doctor/doctor_integration_test.go
  - internal/doctor/doctor_test.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/oidc/oidc_integration_test.go
  - internal/oidc/settings_test.go
  - internal/runtime/runtime_test.go
  - internal/slack/check_test.go
  - test/e2e/account_links_test.go
  - test/e2e/bridge_example_test.go
  - test/e2e/bridge_test.go
  - test/e2e/connection_drafts_test.go
  - test/e2e/request_log_test.go
  - test/e2e/several_connections_test.go
  - test/e2e/smoke_test.go
  - test/e2e/telegram_test.go
  - web/e2e/account-links.spec.ts
  - web/e2e/bridge-connection.spec.ts
  - web/e2e/bridge-destination.spec.ts
  - web/e2e/connection-checks.spec.ts
  - web/e2e/destination-preview.spec.ts
  - web/e2e/destination-test.spec.ts
  - web/e2e/docs-links-admin.spec.ts
  - web/e2e/docs-links-forms.spec.ts
  - web/e2e/docs-links.spec.ts
  - web/e2e/mattermost-connection.spec.ts
  - web/e2e/oidc-settings.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/profile.spec.ts
  - web/e2e/request-log.spec.ts
  - web/e2e/screenshots/bridge.spec.ts
  - web/e2e/screenshots/first-alert.spec.ts
  - web/e2e/screenshots/slack.spec.ts
  - web/e2e/several-connections.spec.ts
  - web/e2e/sign-in.spec.ts
  - web/e2e/slack-connection.spec.ts
  - web/e2e/support.ts
  - web/e2e/system-status.spec.ts
  - web/e2e/telegram-connection.spec.ts
  - web/e2e/telegram-destination.spec.ts
  - web/e2e/tokens.spec.ts
  - web/e2e/users.spec.ts
  - web/src/components/account-links-table.test.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/components/bridge-destination-fields.test.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/destination-label.test.ts
  - web/src/components/docs-link.tsx
  - web/src/components/keyring-status.test.tsx
  - web/src/components/keyring-status.tsx
  - web/src/components/link-bridge-dialog.test.tsx
  - web/src/components/link-rule-form.tsx
  - web/src/components/link-slack-dialog.test.tsx
  - web/src/components/link-telegram-dialog.test.tsx
  - web/src/components/lookup-table-editor.tsx
  - web/src/components/network-list-editor.tsx
  - web/src/components/notice-banners.test.tsx
  - web/src/components/notice-banners.tsx
  - web/src/components/organization-form.tsx
  - web/src/components/organization-nav.tsx
  - web/src/components/route-destinations.test.tsx
  - web/src/components/slack-connection-fields.test.tsx
  - web/src/components/slack-destination-fields.test.tsx
  - web/src/components/system-status-sections.test.tsx
  - web/src/components/system-status-sections.tsx
  - web/src/components/telegram-destination-fields.test.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/src/routes/admin.audit-log.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/routes/admin.organization.link-rules.$linkRuleId.tsx
  - web/src/routes/admin.organization.link-rules.index.tsx
  - web/src/routes/admin.organization.link-rules.new.tsx
  - web/src/routes/admin.organization.lookup-tables.$lookupTableId.tsx
  - web/src/routes/admin.organization.lookup-tables.index.tsx
  - web/src/routes/admin.organization.lookup-tables.new.tsx
  - web/src/routes/admin.organization.outbound.tsx
  - web/src/routes/admin.organization.outgoing-heartbeat.tsx
  - web/src/routes/admin.organization.security.tsx
  - web/src/routes/admin.service-accounts.$serviceAccountId.tsx
  - web/src/routes/admin.service-accounts.index.tsx
  - web/src/routes/admin.system-status.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/routes/admin.users.index.tsx
  - web/src/routes/profile.tsx
  - web/src/routes/statistics.tsx
acceptance:
  - "[C-21.FR-11, C-21.AC-8] The pages of the rows of reference.md's \"Next to page titles\" marked S-110 — Statistics, Profile, Access → Users, a User, Service accounts, a Service account and OIDC, and every page of Organization's sub-navigation (General, Severity levels, Alert handling, Security, Outbound addresses, Retention, Keyring, Outgoing heartbeat, Lookup tables and a Lookup table, Link rules and a Link rule, System status, Audit log) — show the Guide link to the row's target; the pages that `organization-form.tsx` draws take their key from their entry of `organization-nav.tsx`."
  - "[C-21.FR-11, C-21.AC-8] \"Learn more\" shows beside OIDC's group mapping, the Security page's TOTP policy, a Service account's Permissions, a Link rule's template, a Lookup table's rows and the allowed networks of Outbound addresses; \"How to fix\" beside the banners \"No replica is leading\" and \"Muster is recovering after downtime\", the OIDC warnings that nobody can sign in and that the client secret expires, the System status's missing outgoing heartbeat and the Keyring's two notices — each to the row's target of reference.md."
  - "[C-21.FR-9] With every link in place, `make docs-screenshots` takes every image of the guides again and the pull request commits them, so that the guides show the links of S-108 to S-110; `make docs-check` passes."
  - "[C-21.AC-8] Every place of reference.md's Documentation links now shows its link: `docs-links.json` holds every key of the table and `make docs-check` passes; the links read in Russian as in English; and the Organization, Access and Statistics pages keep no horizontal scrollbar at 360 px wide (`web/e2e/docs-links-admin.spec.ts`)."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 339
---

# S-110. Documentation links on the Statistics, profile, Access and Organization pages: page titles, fields and problems (FE)

## Scope

**IN**

- The Guide links of the Statistics, profile, Access and Organization pages, the System status and the Audit log.
- "Learn more" in their forms and "How to fix" beside their warnings and the banners every page shows Admins.
- Neutral display names of the fakes and of the demo data of `muster dev`, before the screenshots are taken again: the
  fake bridge's bridge "Muster fake bridge 1.0.0" and messenger "Fake chat", the fake Slack server's workspaces "Fake
  workspace" and "Fake workspace B", the display name "Dev IdP" of the demo OIDC configuration ("Sign in with Dev
  IdP") and the demo Connections "Dev Mattermost", "Dev Telegram", "Dev Bridge" and "Dev Slack" show in the guides'
  screenshots of S-106 (review of S-106, point 7). They become "Example bridge" (1.0.0) and "Example chat", "Example
  workspace" and "Example workspace B", "Example SSO", and "Example Mattermost", "Example Telegram", "Example Bridge"
  and "Example Slack", with every test, spec and page that names them (`AGENTS.md`, `CONTRIBUTING.md`,
  `docs/messengers/write-a-bridge.md`); the published development tokens, ids, ports and behaviour stay as they are.
  `muster dev` ensures its demo Connections by name, so a development database that already holds the old ones keeps
  them beside the new ones; a fresh database, as the end-to-end runs and the screenshots use, has only the new ones.

**OUT**

- The version, the components, the anchor check and the other page titles (S-108).
- The fields and problems of the Integration, Route, Connection, Destination and Alert Group pages (S-109).

## Contracts

- **The list**: `docs-links.json` gains the keys of reference.md's
  [Documentation links](../prd/l1/reference.md#documentation-links) marked S-110; afterwards it holds every key of the
  table, and the pull request lists the table's keys against the file's.
- **Screenshots**: the last step runs `make docs-screenshots` of S-106 and commits the images it changes; they are
  generated files and not listed. A spec that the new links break (a locator that now matches the link too) is fixed
  in the same pull request and added to `files_touched`.
- **Placement**: the pages with their own `h1` use `PageTitle` of S-108; `organization-nav.tsx` gives each entry of the
  Organization's sub-navigation its key, which `organization-form.tsx` reads for the pages it draws, so the seven pages
  that use it change no route file; the Security and Outbound addresses pages hold the fields that get "Learn more" in
  their own route files. A Service account has the Permissions of its Role, so its "Learn more" sits beside the Role
  on its page (`permission-picker.tsx` draws only the Permissions of a Personal access token and is not touched).
  "Learn more" and "How to fix" as in S-109; the banners of `notice-banners.tsx` show "How to fix" only to Admins, who
  see those banners. The Keyring's two notices are its sentence of a key that can be removed and its "Missing on
  {replicas}" of a key, which keeps the key from being activated (the refusal "Replicas {names} do not hold key {id}
  yet." itself is printed by the CLI).
- **Texts**: those of S-108, with accessible names in both languages.
- **End-to-end** (`web/e2e/docs-links-admin.spec.ts`): the placement of the acceptance on `muster dev`. A banner or
  notice that this spec does not bring about — "No replica is leading", the recovery after downtime, the Keyring's
  notices, which need the leader gone, a downtime, a key rotation or a replica without a key — is shown by the
  component's test with its link instead (`notice-banners.test.tsx`, new, and `keyring-status.test.tsx`).
  `organization-settings.spec.ts`, which activates a second key on `muster dev`, also checks the link beside the
  sentence of a key that can be removed once the key gets there.

## Steps

1. Add the keys, and the titles of the Access, profile and Statistics pages. Check: `make lint`, `make docs-check`.
2. Give the Organization's sub-navigation its keys and place the titles of its pages. Check: `make test-web`.
3. Place "Learn more" and "How to fix" in the admin forms, the System status, the Keyring and the banners. Check:
   `make test-web`.
4. Write the end-to-end spec. Check: `make e2e-web`, then Verification.
5. Take the screenshots again. Check: `make docs-screenshots`, then `make docs-check`.

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

On the VM muster-lab, where `make docs-screenshots` runs (S-106):

```sh
jq -r 'keys[]' web/src/lib/docs-links.json | wc -l      # the number of keys of reference.md's table
make docs-screenshots                                    # every spec passes; the images now show the links
make docs-check                                          # passes
```

## Open questions

None.

## Notes

- Suggested commit: `feat(web): link the organization, access and profile pages to the documentation`.
- The story touches 109 hand-written files: 36 for the links (components, routes, texts, the list, their tests and
  specs, and the two specs the links or the screenshots needed fixed) and 73 for the neutral names, each a mechanical
  change of a name in a test, a spec or a page. It stays one pull request because the names must change before the
  screenshots are taken again, and this story owns that re-shoot.
- It completes C-21.FR-11 and C-21.AC-8: after it every place of the table shows its link.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-11 | partial | these pages; S-108 and S-109 cover the rest, and this story completes the requirement |
| C-21.AC-8 | partial | these places; S-108 and S-109 cover the rest, and this story completes the statement |
| C-21.FR-9 | partial | the screenshots taken again with the links; S-106 and S-107 wrote the guides |
