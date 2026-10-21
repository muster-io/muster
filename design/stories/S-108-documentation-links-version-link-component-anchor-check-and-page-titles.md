---
id: S-108
title: "Documentation links in the web UI: the documentation version, the link component, the check of linked anchors, and links next to the titles of the Alert Group, Integration, Route, Connection and Destination pages (FE)"
capability: C-21
kind: fe
layer: L1
depends_on: [S-106]
covers: [C-21.FR-11, C-21.FR-12, C-21.AC-7, C-21.AC-8, C-21.AC-9, C-19.FR-9]
files_touched:
  - api/openapi.yaml
  - design/prd/l1/reference.md
  - .github/workflows/docs.yml
  - web/index.html
  - internal/internalalerts/testdata/runbook_versions.json
  - internal/api/sessions.go
  - internal/api/sessions_test.go
  - docs/operations/runbooks/index.md
  - AGENTS.md
  - internal/internalalerts/raise.go
  - internal/internalalerts/internalalerts_test.go
  - deploy/helm/muster/templates/_helpers.tpl
  - deploy/helm/muster/values.schema.json
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/tools/chartcheck/chartcheck_test.go
  - internal/tools/docslinkcheck/main.go
  - internal/tools/docslinkcheck/docslinkcheck_test.go
  - Makefile
  - web/src/lib/docs.ts
  - web/src/lib/docs.test.ts
  - web/src/lib/docs-links.json
  - web/src/components/docs-link.tsx
  - web/src/components/docs-link.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/docs-links.spec.ts
  - web/src/routes/alert-groups.index.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-header.test.tsx
  - web/src/routes/integrations.index.tsx
  - web/src/routes/integrations.new.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/routes/integrations.$integrationId.edit.tsx
  - web/src/routes/integrations.$integrationId.snapshots.$storedSnapshotId.tsx
  - web/src/routes/routes.index.tsx
  - web/src/routes/routes.new.tsx
  - web/src/routes/routes.$routeId.tsx
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/destinations.index.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/access-nav.test.tsx
  - web/src/components/account-links-table.test.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/components/bridge-destination-fields.test.tsx
  - web/src/components/bulk-actions-bar.test.tsx
  - web/src/components/command-buttons.test.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/destination-preview-panel.test.tsx
  - web/src/components/destination-secrets.test.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/link-bridge-dialog.test.tsx
  - web/src/components/link-mattermost-dialog.test.tsx
  - web/src/components/link-slack-dialog.test.tsx
  - web/src/components/link-telegram-dialog.test.tsx
  - web/src/components/live-indicator.test.tsx
  - web/src/components/lookup-table-editor.test.tsx
  - web/src/components/request-log.test.tsx
  - web/src/components/request-record.test.tsx
  - web/src/components/route-destinations.test.tsx
  - web/src/components/slack-connection-fields.test.tsx
  - web/src/components/slack-destination-fields.test.tsx
  - web/src/components/snooze-dialog.test.tsx
  - web/src/components/system-status-sections.test.tsx
  - web/src/components/telegram-connection-fields.test.tsx
  - web/src/components/telegram-destination-fields.test.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/webhook-destination-fields.test.tsx
acceptance:
  - "[C-21.FR-11, C-21.AC-7, C-19.FR-9] `Session` — the answer of `getCurrentSession`, `createSession` and `submitSessionTotp` — carries `docs_version`, computed from the binary's version by `internalalerts.RunbookVersion`: `0.1.0` and `0.1.7` give `0.1`, `1.12.3` gives `1.12`, and `0.0.0-dev`, `0.1.1-SNAPSHOT-abc1234`, `0.2.0-rc.1`, `0.1.0+meta` and `main` give `latest` (`TestRunbookVersion`, `sessions_test.go`); the `runbook_url` of Internal alerts and the chart helper `muster.runbookVersion` follow the same rule (`chartcheck_test.go`), and a sign-in with `curl` on a binary built with `VERSION=0.1.0` shows `\"docs_version\":\"0.1\"`."
  - "[C-21.FR-11] `web/src/lib/docs.ts` builds every documentation URL from the build constant `https://muster-io.github.io/muster`, the session's `docs_version` (`latest` until the session has been read) and an entry of `docs-links.json` — `concepts/alert-groups.md` becomes `…/latest/concepts/alert-groups/`, `messengers/index.md#what-each-destination-type-can-do` becomes `…/latest/messengers/#what-each-destination-type-can-do` (`docs.test.ts`); no other file of `web/src` outside the generated client and tests names the site's address."
  - "[C-21.FR-11, C-21.AC-8] `DocsLink` renders a link with an icon and the text \"Guide\", \"Learn more\" or \"How to fix\", an accessible name that adds what it is about and \"(opens in a new tab)\", `target=\"_blank\"` and `rel=\"noopener noreferrer\"`; `PageTitle` renders the page's `h1` with its Guide link beside it, wrapping under the title at 360 px; the browser loads nothing from the site until the link is followed (`docs-link.test.tsx`)."
  - "[C-21.FR-11, C-21.AC-8] The pages of the rows of reference.md's \"Next to page titles\" marked S-108 — Alert Groups, an Alert Group, Integrations and an Integration with its new, edit and Stored Snapshot pages, Routes, Connections and Destinations with their new and detail pages — show the Guide link to the row's target, a Connection or Destination page the one of its type or mode; the pages outside an active, full session show none — sign-in, password setup, the TOTP prompt and TOTP enrolment of a limited session, and the page that says the person lacks the Permission; no empty state shows one; the links read «Руководство» with the language switched to Russian; and these pages keep no horizontal scrollbar at 360 px wide (`web/e2e/docs-links.spec.ts`)."
  - "[C-21.FR-12, C-21.AC-9] `make docs` runs `internal/tools/docslinkcheck` after the build: it reads `web/src/lib/docs-links.json` and fails, naming the key and the target, when the built site has no page for an entry or the page has no element with the entry's anchor as its `id`; `docslinkcheck_test.go` covers a missing page, a missing anchor and a passing list, and an entry of `docs-links.json` changed to an anchor the page lacks makes `make docs-check` fail naming its key; a removed heading that the UI links to fails the build, naming the key or, when a page of the site links to the heading too, that page (the strict build checks it first)."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 337
---

# S-108. Documentation links in the web UI: the documentation version, the link component, the check of linked anchors, and links next to the titles of the Alert Group, Integration, Route, Connection and Destination pages (FE)

## Scope

**IN**

- The documentation version: `docs_version` in the session, and the release rule shared with the `runbook_url` of
  Internal alerts and the chart's rules.
- The link module with the site's address as a build constant, the list of linked pages and anchors, the components
  `DocsLink` and `PageTitle`, and their texts in English and Russian.
- The check of the list against the built site in `make docs`.
- The Guide links next to the titles of the pages of Alert Groups, Integrations, Routes, Connections and Destinations.

**OUT**

- The links next to fields and problems on those pages (S-109).
- The links of the Statistics, profile, Access and Organization pages, and their fields and problems (S-110).
- The pages and sections the links open (S-106, S-107; earlier documentation stories).

## Contracts

- **API**: `Session` gains the required `docs_version` (string, pattern `^(latest|[0-9]+\.[0-9]+)$`, described as the
  version segment of the documentation site the web UI links to, C-21.FR-11); `sessionOf` in `internal/api/sessions.go`
  fills it from `buildinfo.Version` for every operation that answers a `Session`. The SPA already reads the session on
  every navigation (`useSession`, `SESSION_QUERY_KEY` in `web/src/lib/api.ts`), so the version needs no request of its
  own. Spec first, then `make generate`.
- **The rule** (`internalalerts.RunbookVersion`): a version `X.Y.Z`, with an optional leading `v` and nothing after
  it, gives `X.Y`; anything else gives `latest` — the development version `0.0.0-dev`, the snapshot versions
  `X.Y.Z-SNAPSHOT-<commit>` of the `sha-…` and `edge` images, pre-releases and build metadata. Until now the function
  and the chart helper `muster.runbookVersion` (`deploy/helm/muster/templates/_helpers.tpl`) gave `X.Y` for any suffix
  but `-dev`, so a snapshot image linked its runbooks to a minor whose documentation may not exist yet; both change
  to this rule, which C-19.FR-9 now states, and their tests take the same table of versions.
- **Link module** (`web/src/lib/docs.ts`): the constant `DOCS_SITE = "https://muster-io.github.io/muster"`, the only
  place of the SPA that names it; `docsVersion(session)`, the session's `docs_version` or `latest` before the session
  has been read (or for a value that is no version segment), and `useDocsVersion()`, the same for the session the root
  route last read; `docsUrl(key, version?)` for a key of `docs-links.json`, typed from the JSON's keys so that an
  unknown key does not compile, in `latest` when no version is given; `connectionDocs(type)` and
  `destinationDocs(destination)`, the key of a Connection's or a Destination's page. A target `<path>.md#<anchor>`
  becomes `<site>/<version>/<path>/#<anchor>`, an `index.md` its directory, the anchor kept as written.
- **The list** (`web/src/lib/docs-links.json`): one object from key to target, sorted by key, holding the keys of
  reference.md's [Documentation links](../prd/l1/reference.md#documentation-links) that this story places
  (`page.alert_groups`, `page.alert_group`, `page.integrations`, `page.stored_snapshot`, `page.routes`,
  `page.connections`, `page.connection.<type>` for `mattermost`, `telegram`, `slack` and `bridge`,
  `page.destinations`, `page.destination.<type>` for the same four and `webhook_events` and `webhook_template`);
  S-109 and S-110 add theirs.
- **Components** (`web/src/components/docs-link.tsx`): `DocsLink` (`to`, `kind` — `guide`, `learn_more` or
  `how_to_fix` — and `about`, the words its accessible name adds), and `PageTitle` (`docs` and the title), which the
  pages use in place of their `h1`; its accessible name says what the guide of its key is about ("Guide: Routes",
  "Guide: Telegram Connection", "Guide: Outgoing webhook templates"), not the page's title; the icon is from the
  SPA's icon set, the link styled as the UI's other links.
- **Placement**: each page of the rows of "Next to page titles" marked S-108 replaces its `h1` with `PageTitle`; a
  Connection page and a Destination page pick the key of their type, and an outgoing webhook Destination of its mode;
  `alert-group-header.tsx` carries the title of the Alert Group page.
- **Texts** (`en.json`, `ru.json`): `docs.guide` "Guide" / «Руководство», `docs.learnMore` "Learn more" /
  «Подробнее», `docs.howToFix` "How to fix" / «Как исправить», and the accessible names `docs.guideAbout` "Guide:
  {about} (opens in a new tab)" and the like, and what each page's guide is about (`docs.guides.*`, «Руководство:
  Подключение Telegram (откроется в новой вкладке)»), in both languages. `web/index.html` turns DNS prefetching off,
  so that the browser resolves nothing of the site before a link is followed.
- **Anchor check** (`internal/tools/docslinkcheck`, Go, no dependencies): `-links web/src/lib/docs-links.json -site
  site`; for each entry, the page `site/<path>/index.html` (or `site/<dir>/index.html` for an `index.md`) exists and,
  for an anchor, holds an element with `id="<anchor>"`; it prints the number of entries checked, or one line per
  problem on stderr, or the single line "site not built: <dir>" when there is no built site. `make docs` runs it
  after `zensical build`, so `make docs-check` and the docs workflow run it too. A manual run of the docs workflow,
  which publishes master's site as the newest released minor, also checks the `docs-links.json` of that release's
  tag, whose binaries link to its entries; a tag older than the list is skipped with a message.
- **End-to-end** (`web/e2e/docs-links.spec.ts`): the placement, texts and widths of the acceptance against `muster dev`;
  the site's address is answered by `context.route`, so the spec needs no network and checks that no `Referer` arrives.
- **AGENTS.md**: the row of `make docs-check` names the check of linked anchors.
- **Other statements of the rule**: the description of `alerting.runbookBaseURL` in
  `deploy/helm/muster/values.schema.json` (and the generated `docs/reference/chart-values.md`) and the introduction of
  the Internal alerts reference in `internal/tools/refgen/main.go` (and the generated
  `docs/reference/internal-alerts.md`) say "`latest` for any version that is not a release `X.Y.Z`" instead of "for a
  development build"; `make generate` rewrites the generated pages.

## Steps

1. Change the rule with its table of versions in Go and in the chart helper; add `docs_version` to the spec and
   the session; `docs/operations/runbooks/index.md` says that `<version>` is `latest` for any version that is not a
   release. Check: `go test ./internal/internalalerts ./internal/tools/chartcheck ./internal/api`, `make helm-check`.
2. Write the link module, the list and the components with their texts. Check: `make lint`, `make test-web`.
3. Write `docslinkcheck` with its tests and run it from `make docs`. Check: `make docs-check`.
4. Place `PageTitle` on the pages and write the end-to-end spec. Check: `make e2e-web`, then Verification.

## Verification

Against `muster dev` (version `0.0.0-dev`):

```sh
signin='{"login":"admin@example.org","password":"muster-dev-password"}'
curl -s -H 'Content-Type: application/json' -d "$signin" localhost:8080/api/v1/sessions | jq -r .docs_version   # latest
# stop make dev, then start a binary built as a release on the same development database:
make build VERSION=0.1.0 && ./bin/muster dev &
curl -s -H 'Content-Type: application/json' -d "$signin" localhost:8080/api/v1/sessions | jq -r .docs_version   # 0.1
git grep -n 'muster-io.github.io/muster' -- web/src ':!web/src/api/gen' ':!*.test.ts' ':!*.test.tsx'   # web/src/lib/docs.ts only
make docs-check                                                                              # passes; docslinkcheck prints its count
cp web/src/lib/docs-links.json /tmp/docs-links.json
jq '."page.routes" = "concepts/routes-and-group-keys.md#no-such-section"' /tmp/docs-links.json >web/src/lib/docs-links.json
make docs-check      # fails: page.routes: concepts/routes-and-group-keys.md#no-such-section: no element with this id
cp /tmp/docs-links.json web/src/lib/docs-links.json
```

Playwright at 1280 and 360 px, signed in as the Admin:

1. Routes: "Guide" beside the title "Routes", with the accessible name "Guide: Routes (opens in a new tab)", `href`
   `https://muster-io.github.io/muster/latest/concepts/routes-and-group-keys/`, `target="_blank"`, `rel` with
   `noopener` and `noreferrer`; following it opens a new page at that address, which the spec answers itself
   (`context.route` on the site's address, so the run needs no network) and which receives no `Referer` header.
2. Connections → the demo Telegram Connection: the link goes to `…/latest/messengers/telegram/`; Destinations → a
   Slack Destination: to `…/latest/messengers/slack/#invite-the-bot-and-create-destinations`; an outgoing webhook
   Destination in template mode: to `…/latest/outgoing-webhooks/templates/`.
3. The Alert Group list, an Alert Group, Integrations, an Integration, its edit page and a Stored Snapshot each show
   their link; the sign-in page, signed out, shows no link to the site.
4. Profile → Language → Russian: the link beside «Маршруты» reads «Руководство».
5. At 360 px the Routes, Connection and Destination pages and the Alert Group page have
   `document.documentElement.scrollWidth <= 360`.
6. With the release build of the commands above, the Routes page links to
   `…/muster/0.1/concepts/routes-and-group-keys/`.
7. A user under a TOTP policy who has not enrolled signs in: the enrolment page shows no link to the site; a Viewer
   opening an Admin's page sees the page about the missing Permission without one.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): link page titles to the documentation of the running version`.
- The documentation version is carried by the session (D426): the SPA already reads the session on every page; the
  served API specification, about 15,000 lines, is no source for it, since its `info.version` is the API document's
  version, not the build's, and cannot tell a snapshot from a release; and the version segment (`X.Y` or `latest`) is
  derived on the server, where the build is known, by the same function as the `runbook_url`. The address of the site
  stays a build constant (D419). A closed network without access to the site sees links that do not open; Muster has no
  setting for a mirror.
- The rule changes what a snapshot image's Internal alerts link to; a release is unaffected.
- The story has a small backend part — one field and the shared rule — in the same pull request, because the field is
  useless without the links and the links are wrong without the field; S-087 set the precedent.
- With 40 files the story is at the edge of the size signal; the links next to fields and problems are S-109 and S-110.
  The required `docs_version` also makes the TypeScript type check fail on every component test that builds a
  `Session` for the query cache, so those 30 tests gain the line `docs_version: "latest"` and are listed in
  `files_touched` too; they change nothing else, so the story is not split for them.
- An outgoing webhook Destination in the mode `both` links to `page.destination.webhook_template`, as the row of
  reference.md now says (D431): the templates page describes that mode.
- `.github/workflows/docs.yml` is touched because a documentation correction republishes a released minor from
  master, and master's list may have renamed an anchor that the release's binaries still link to.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-11 | partial | the version, the component, the texts and the page titles of these pages; S-109 and S-110 place the rest |
| C-21.FR-12 | full | |
| C-21.AC-7 | full | |
| C-21.AC-8 | partial | these pages; S-109 and S-110 complete the table |
| C-21.AC-9 | full | |
| C-19.FR-9 | partial | `latest` for snapshot and pre-release versions, in the Internal alerts and the chart helper; S-053 and S-059 built the rest |
