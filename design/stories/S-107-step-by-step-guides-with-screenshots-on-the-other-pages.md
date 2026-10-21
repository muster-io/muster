---
id: S-107
title: "Step-by-step sections with screenshots on the pages of Integrations, Routes, templates, Account links, access, Outgoing webhooks, the Organization and Alert Groups"
capability: C-21
kind: docs
layer: L1
depends_on: [S-106]
covers: [C-21.FR-9, C-21.AC-6]
files_touched:
  - docs/integrations/alertmanager.md
  - docs/integrations/grafana.md
  - docs/integrations/heartbeat.md
  - docs/concepts/routes-and-group-keys.md
  - docs/configure/messages-and-templates.md
  - docs/messengers/account-links.md
  - docs/sign-in/index.md
  - docs/sign-in/oidc-keycloak.md
  - docs/sign-in/totp.md
  - docs/configure/api-tokens.md
  - docs/outgoing-webhooks/events.md
  - docs/outgoing-webhooks/templates.md
  - docs/configure/organization.md
  - docs/operations/monitoring.md
  - docs/operations/request-log.md
  - docs/concepts/alert-groups.md
  - docs/concepts/statuses-and-commands.md
  - web/e2e/screenshots/capture.ts
  - web/e2e/support.ts
  - web/e2e/screenshots/integrations.spec.ts
  - web/e2e/screenshots/routes-and-templates.spec.ts
  - web/e2e/screenshots/account-links.spec.ts
  - web/e2e/screenshots/access.spec.ts
  - web/e2e/screenshots/outgoing-webhooks.spec.ts
  - web/e2e/screenshots/organization.spec.ts
  - web/e2e/screenshots/alert-groups.clock.spec.ts
  - docs/llms.txt.tmpl
acceptance:
  - "[C-21.FR-9] Integrations: Connect Alertmanager shows step by step, with screenshots, Integrations → New, the token dialog with the snippet and the Integration page after the first Snapshot (section \"Create a token and paste the snippet\") and the Static labels field (\"Static labels\"); Connect Grafana shows the same in \"Create the Integration and a token\"; the Heartbeat page shows turning the Heartbeat on and the badge in its states (\"Turn it on\")."
  - "[C-21.FR-9] Routes and messages: Routes and Group keys gains the section \"Create a Route\" — Routes → New, the Matcher builder, the Group key with its preview, the Destinations, the policy and the saved Route — and Messages and templates the sections \"Edit a Route's message\" (the template editor with its preview), \"Add a Lookup table\" and \"Add a Link rule\" under Links."
  - "[C-21.FR-9] Access: Sign-in gains \"Add a User\" (Access → Users → New, the Role, the setup link) and \"Your profile\" (name, time zone, language, password, sessions); the Keycloak walkthrough shows Muster's OIDC page in \"4. Configure Muster\"; TOTP shows enrolment and the policy; API tokens shows \"Create a Personal access token\" and \"Create a Service account\"; Link your messenger accounts shows each of its four ways to link and unlinking."
  - "[C-21.FR-9] Outgoing webhooks: the events page shows \"Create the Destination\", its Secrets and the Signing secret's regeneration; the templates page shows \"Create the Destination\" in template mode with its request builder."
  - "[C-21.FR-9] Organization and operations: Organization settings shows each of its pages in its section and gains \"Audit log\" (the list, a filter and an entry's diff); Monitoring gains \"System status\"; the Request log page shows \"Where to find it\"; Alert Groups gains \"In the web UI\" (the list with its filters, an Alert Group page with its Timeline) and \"Statistics\"; Statuses and commands gains \"Commands in the web UI\" (the command buttons, the Note box and the Snooze dialog) and screenshots of the Snooze presets in their \"In the web UI\"."
  - "[C-21.FR-9, C-21.AC-6] Every image of these pages is written by `make docs-screenshots` from the seven new specs, which pass on a fresh `muster dev` and each leave the Organization's settings, the Admin's profile and the OIDC settings as they found them; every screenshot is of the English UI with alternative text; the headings these sections give are the anchors of reference.md's Documentation links; and `make docs-check` passes."
verify: "make docs-check"
operator_attention: false
issue: 336
---

# S-107. Step-by-step sections with screenshots on the pages of Integrations, Routes, templates, Account links, access, Outgoing webhooks, the Organization and Alert Groups

## Scope

**IN**

- Step-by-step sections with screenshots inside the existing pages of every object the web UI configures, other than
  the messengers: Integrations, Routes, templates with Lookup tables and Link rules, Account links, Users and the
  profile, OIDC, TOTP, API tokens and Service accounts, Outgoing webhooks, the Organization's pages with the Audit log,
  the System status, the Request log, and the Alert Group list, page and Statistics.
- The screenshot specs of those pages.

**OUT**

- The screenshot suite, the image check, the tutorial and the messenger pages (S-106).
- The links from the web UI (S-108, S-109, S-110).

## Contracts

- **Specs** under `web/e2e/screenshots/`, run by `make docs-screenshots` of S-106 in its pinned environment and never
  by `make e2e`, each writing the images of its pages through `capture.ts`:
  - `integrations.spec.ts`: `integrations/alertmanager`, `integrations/grafana`, `integrations/heartbeat`; the first
    Snapshot sent through the fake Alertmanager (`notify` of `support.ts`), the Grafana Snapshot through `curl` to the
    ingest endpoint as the Grafana page's example, the Heartbeat signal of its own Integration through the fake
    Alertmanager's Heartbeat sender.
  - `routes-and-templates.spec.ts`: `concepts/routes-and-group-keys`, `configure/messages-and-templates`.
  - `account-links.spec.ts`: `messengers/account-links`, with the fake servers' second bots on the routed addresses of
    S-106, their DMs and updates.
  - `access.spec.ts`: `sign-in/index`, `sign-in/oidc-keycloak`, `sign-in/totp` (the code from `totpCode` of
    `support.ts`), `configure/api-tokens`. The OIDC page is shown as its form filled with example values —
    `https://sso.example.org/realms/muster`, the client `muster` — and taken before saving, since the issuer is not
    routed (S-106); the demo OIDC configuration stays as it was.
  - `outgoing-webhooks.spec.ts`: `outgoing-webhooks/events`, `outgoing-webhooks/templates`, against the fake webhook
    receiver.
  - `organization.spec.ts`: `configure/organization`, `operations/monitoring`, `operations/request-log`.
  - `alert-groups.clock.spec.ts`: `concepts/alert-groups`, `concepts/statuses-and-commands`, with a few Alert Groups in
    each status made through the fake Alertmanager and Commands, and the development clock advanced for the
    Statistics; as a `*.clock.spec.ts` it runs last, on a `muster dev` of its own, after turning the demo Heartbeat off
    (S-106). It creates a Personal access token for its API calls before the first advance and signs in again after
    each advance, since an advance past `auth.session_idle_timeout` ends the session.
  - `capture.ts` and `support.ts` gain only what these specs share.
- **Independence**: each spec follows S-106's rules, so `GUIDES=<spec>` takes the same images as a full run. Numbers
  and ids are read from what the spec created. What each spec saves beyond the objects it creates and removes, and
  reverts in `afterAll` through the API, with the `ETag` it read first:

  | Spec | Saves | Reverts |
  |---|---|---|
  | `integrations.spec.ts` | the Heartbeat of its own Integration | nothing: it deletes the Integration |
  | `routes-and-templates.spec.ts` | Routes, Lookup tables, Link rules | nothing: it deletes them; the Default route is not edited |
  | `account-links.spec.ts` | Connections and the Admin's Account links | nothing: it unlinks and deletes them |
  | `access.spec.ts` | a User, who signs in and enrols in TOTP (the Admin never does); a Service account and tokens; the TOTP policy (`organization.totp_required`) | deletes the User, with its TOTP, and the other objects; restores `organization.totp_required` |
  | `outgoing-webhooks.spec.ts` | Destinations and their Secrets | nothing: it deletes them |
  | `organization.spec.ts` | General (`organization.time_zone`, `organization.workday_start`, `organization.week_start`), Severity levels, Alert handling, Security, Outbound addresses, Retention, Outgoing heartbeat | every value to what it read first; Outbound addresses keep `127.0.0.0/8` at all times, so the fakes stay reachable |
  | `alert-groups.clock.spec.ts` | Alert Groups, Notes, the demo Heartbeat off | nothing: its `muster dev` ends with it |

  A spec that shows the page of an Organization setting without the guide saving it fills the form and does not save.
- **New sections**, whose headings are anchors of [Documentation links](../prd/l1/reference.md#documentation-links):
  "Create a Route" (`concepts/routes-and-group-keys.md`), "Edit a Route's message", "Add a Lookup table" and "Add a
  Link rule" (`configure/messages-and-templates.md`, the last two under Links), "Add a User" and "Your profile"
  (`sign-in/index.md`), "Audit log" (`configure/organization.md`), "System status" (`operations/monitoring.md`), "In
  the web UI" and "Statistics" (`concepts/alert-groups.md`), "Commands in the web UI"
  (`concepts/statuses-and-commands.md`, after "Commands" and its subsections, since "In the web UI" there is a
  subsection of "Snooze presets").
- **Existing sections that gain steps and screenshots**, headings unchanged: "Create a token and paste the snippet"
  and "Static labels" (Alertmanager), "Create the Integration and a token" (Grafana), "Turn it on" and "When the
  Heartbeat is lost" (Heartbeat), "Matchers", "Group keys" and "Preview before you save" (Routes), "Link Telegram",
  "Link Mattermost", "Link Slack", "Link through a Bridge" and "Unlink" (Account links), "4. Configure Muster"
  (Keycloak), "Enrol" and "The policy" (TOTP), "Create a Personal access token" and "Create a Service account" (API
  tokens), "Create the Destination", "Secrets" and "Regenerate and retire the Signing secret" (events), "Create the
  Destination" (templates), "General", "Severity levels", "Alert handling", "Security", "Outbound addresses" and
  "Retention" (Organization), "Where to find it" (Request log), "In the web UI" under "Snooze presets" (Statuses and
  commands).
- **Form**: numbered steps; each step names the screen and the UI's texts as the UI writes them, then shows the
  screenshot with the class `screenshot` and alternative text; the concepts already explained on the page stay where
  they are, and the steps link to them instead of repeating them.
- **Data**: the conventions of S-106 — demo data, the fake servers on their routed addresses, neutral names, no real
  accounts, tokens or people, no fake server's address on a screen.
- **`llms.txt.tmpl`** quotes `integrations/alertmanager.md`, `integrations/heartbeat.md` and `integrations/grafana.md`;
  a quote that the new steps change is updated in the template in the same pull request, so that `make docs-check`
  passes.

## Steps

1. Integrations and Routes: write the specs and the sections. Check: `make docs-screenshots GUIDES=integrations` and
   `GUIDES=routes-and-templates`, then `make docs`.
2. Account links and access. Check: their specs, then `make docs`.
3. Outgoing webhooks, the Organization, the System status and the Request log. Check: their specs, then `make docs`.
4. Alert Groups, Statistics and the commands. Check: `make docs-screenshots`, then `make docs-check`.

## Verification

On the VM muster-lab, as for S-106:

```sh
make docs-screenshots                                       # the twelve specs of S-106 and S-107 pass
make docs-check                                             # passes: every referenced image exists and every image is referenced
for p in integrations/alertmanager integrations/grafana integrations/heartbeat concepts/routes-and-group-keys \
  configure/messages-and-templates messengers/account-links sign-in/index sign-in/oidc-keycloak sign-in/totp \
  configure/api-tokens outgoing-webhooks/events outgoing-webhooks/templates configure/organization \
  operations/monitoring operations/request-log concepts/alert-groups concepts/statuses-and-commands; do
  n=$(grep -c '{ .screenshot }' "docs/$p.md"); echo "$p $n"; test "$n" -ge 1 || exit 1
done
# each new anchor on its own page; the loop stops at the first that is missing:
for pa in concepts/routes-and-group-keys:create-a-route configure/messages-and-templates:edit-a-routes-message \
  configure/messages-and-templates:add-a-lookup-table configure/messages-and-templates:add-a-link-rule \
  sign-in:add-a-user sign-in:your-profile configure/organization:audit-log operations/monitoring:system-status \
  concepts/alert-groups:in-the-web-ui concepts/alert-groups:statistics \
  concepts/statuses-and-commands:commands-in-the-web-ui; do
  grep -q "id=\"${pa#*:}\"" "site/${pa%%:*}/index.html" || { echo "missing: $pa"; exit 1; }
done
# one spec alone writes the same file names as in the full run:
make docs-screenshots GUIDES=organization && git status --short docs/assets/screenshots/configure \
  docs/assets/screenshots/operations   # modified only, no added or deleted file
make docs-serve &   # open each page above at http://localhost:8000/ and check that its images load and match its steps
```

After the run, the Organization settings, the Admin's profile and the OIDC settings of `muster dev` are as they were
before it (`GET /api/v1/organization` `GET /api/v1/me` and the OIDC settings compared with their values before the run).

## Open questions

None.

## Notes

- Suggested commit: `docs: add step-by-step guides with screenshots to the other pages`.
- With 27 files the story stays under the size signal; the pages share the specs' data, so splitting them would set
  up the same Alert Groups and Connections twice.
- Statistics needs Alert Groups spread over time: the spec advances the development clock between them instead of
  waiting, and the screenshot shows whatever times that gives.
- The pages before sign-in are not shown in the guides beyond the sign-in form and the TOTP prompt, which
  `access.spec.ts` takes before signing in.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-9 | partial | the pages other than the tutorial and the messengers; S-106 covers the rest |
| C-21.AC-6 | partial | the images of these pages; S-106 covers the suite and the check |
