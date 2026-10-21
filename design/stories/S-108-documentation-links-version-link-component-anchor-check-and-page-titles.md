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
  - internal/api/profile.go
  - internal/api/sessions_test.go
  - internal/internalalerts/raise.go
  - internal/internalalerts/internalalerts_test.go
  - deploy/helm/muster/templates/_helpers.tpl
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
acceptance:
  - "[C-21.FR-11, C-21.AC-7, C-19.FR-9] `getMe` returns `docs_version`, computed from the binary's version by `internalalerts.RunbookVersion`: `0.1.0` and `0.1.7` give `0.1`, `1.12.3` gives `1.12`, and `0.0.0-dev`, `0.1.1-SNAPSHOT-abc1234`, `0.2.0-rc.1`, `0.1.0+meta` and `main` give `latest` (`TestRunbookVersion`, `sessions_test.go`); the `runbook_url` of Internal alerts and the chart helper `muster.runbookVersion` follow the same rule (`chartcheck_test.go`), and `curl` of `getMe` on a binary built with `VERSION=0.1.0` shows `\"docs_version\":\"0.1\"`."
  - "[C-21.FR-11] `web/src/lib/docs.ts` builds every documentation URL from the build constant `https://muster-io.github.io/muster`, the session's `docs_version` (`latest` until `getMe` has answered) and an entry of `docs-links.json` — `concepts/alert-groups.md` becomes `…/latest/concepts/alert-groups/`, `messengers/index.md#what-each-destination-type-can-do` becomes `…/latest/messengers/#what-each-destination-type-can-do` (`docs.test.ts`); no other file of `web/src` outside the generated client and tests names the site's address."
  - "[C-21.FR-11, C-21.AC-8] `DocsLink` renders a link with an icon and the text \"Guide\", \"Learn more\" or \"How to fix\", an accessible name that adds what it is about and \"(opens in a new tab)\", `target=\"_blank\"` and `rel=\"noopener noreferrer\"`; `PageTitle` renders the page's `h1` with its Guide link beside it, wrapping under the title at 360 px; the browser loads nothing from the site until the link is followed (`docs-link.test.tsx`)."
  - "[C-21.FR-11, C-21.AC-8] The pages of the rows of reference.md's \"Next to page titles\" marked S-108 — Alert Groups, an Alert Group, Integrations and an Integration with its new, edit and Stored Snapshot pages, Routes, Connections and Destinations with their new and detail pages — show the Guide link to the row's target, a Connection or Destination page the one of its type or mode; the sign-in, password setup and TOTP pages show none; no empty state shows one; the links read «Руководство» with the language switched to Russian; and these pages keep no horizontal scrollbar at 360 px wide (`web/e2e/docs-links.spec.ts`)."
  - "[C-21.FR-12, C-21.AC-9] `make docs` runs `internal/tools/docslinkcheck` after the build: it reads `web/src/lib/docs-links.json` and fails, naming the key and the target, when the built site has no page for an entry or the page has no element with the entry's anchor as its `id`; `docslinkcheck_test.go` covers a missing page, a missing anchor and a passing list, and removing a linked heading from a page makes `make docs-check` fail naming its key."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-108. Documentation links in the web UI: the documentation version, the link component, the check of linked anchors, and links next to the titles of the Alert Group, Integration, Route, Connection and Destination pages (FE)

## Scope

**IN**

- The documentation version: `docs_version` in `getMe`, and the release rule shared with the `runbook_url` of Internal
  alerts and the chart's rules.
- The link module with the site's address as a build constant, the list of linked pages and anchors, the components
  `DocsLink` and `PageTitle`, and their texts in English and Russian.
- The check of the list against the built site in `make docs`.
- The Guide links next to the titles of the pages of Alert Groups, Integrations, Routes, Connections and Destinations.

**OUT**

- The links next to fields and problems on those pages (S-109).
- The links of the Statistics, profile, Access and Organization pages, and their fields and problems (S-110).
- The pages and sections the links open (S-106, S-107; earlier documentation stories).

## Contracts

- **API**: `Me` gains the required `docs_version` (string, pattern `^(latest|[0-9]+\.[0-9]+)$`, described as the version
  segment of the documentation site the web UI links to, C-21.FR-11); `getMe` fills it from `buildinfo.Version`. Spec
  first, then `make generate`.
- **The rule** (`internalalerts.RunbookVersion`): a version `X.Y.Z`, with an optional leading `v` and nothing after
  it, gives `X.Y`; anything else gives `latest` — the development version `0.0.0-dev`, the snapshot versions
  `X.Y.Z-SNAPSHOT-<commit>` of the `sha-…` and `edge` images, pre-releases and build metadata. Until now the function
  and the chart helper `muster.runbookVersion` (`deploy/helm/muster/templates/_helpers.tpl`) gave `X.Y` for any suffix
  but `-dev`, so a snapshot image linked its runbooks to a minor whose documentation may not exist yet; both change
  to this rule, which C-19.FR-9 already states, and their tests take the same table of versions.
- **Link module** (`web/src/lib/docs.ts`): the constant `DOCS_SITE = "https://muster-io.github.io/muster"`, the only
  place of the SPA that names it; `docsVersion()` from the cached `getMe` answer; `docsUrl(key)` for a key of
  `docs-links.json`, typed from the JSON's keys so that an unknown key does not compile. A target `<path>.md#<anchor>`
  becomes `<site>/<version>/<path>/#<anchor>`, an `index.md` its directory, the anchor kept as written.
- **The list** (`web/src/lib/docs-links.json`): one object from key to target, sorted by key, holding the keys of
  reference.md's [Documentation links](../prd/l1/reference.md#documentation-links) that this story places
  (`page.alert_groups`, `page.alert_group`, `page.integrations`, `page.stored_snapshot`, `page.routes`,
  `page.connections`, `page.connection.<type>` for `mattermost`, `telegram`, `slack` and `bridge`,
  `page.destinations`, `page.destination.<type>` for the same four and `webhook_events` and `webhook_template`);
  S-109 and S-110 add theirs.
- **Components** (`web/src/components/docs-link.tsx`): `DocsLink` (`to`, `kind` — `guide`, `learn_more` or
  `how_to_fix` — and `about`, the words its accessible name adds), and `PageTitle` (`docs` and the title), which the
  pages use in place of their `h1`; the icon is from the SPA's icon set, the link styled as the UI's other links.
- **Placement**: each page of the rows of "Next to page titles" marked S-108 replaces its `h1` with `PageTitle`; a
  Connection page and a Destination page pick the key of their type, and an outgoing webhook Destination of its mode;
  `alert-group-header.tsx` carries the title of the Alert Group page.
- **Texts** (`en.json`, `ru.json`): `docs.guide` "Guide" / «Руководство», `docs.learnMore` "Learn more" /
  «Подробнее», `docs.howToFix` "How to fix" / «Как исправить», and the accessible names `docs.guideAbout` "Guide:
  {about} (opens in a new tab)" and the like, in both languages.
- **Anchor check** (`internal/tools/docslinkcheck`, Go, no dependencies): `-links web/src/lib/docs-links.json -site
  site`; for each entry, the page `site/<path>/index.html` (or `site/<dir>/index.html` for an `index.md`) exists and, for
  an anchor, holds an element with `id="<anchor>"`; it prints the number of entries checked. `make docs` runs it after
  `zensical build`, so `make docs-check` and the docs workflow run it too.
- **End-to-end** (`web/e2e/docs-links.spec.ts`): the placement, texts and widths of the acceptance against `muster dev`.

## Steps

1. Change the rule with its table of versions in Go and in the chart helper; add `docs_version` to the spec and
   `getMe`. Check: `go test ./internal/internalalerts ./internal/tools/chartcheck ./internal/api`, `make helm-check`.
2. Write the link module, the list and the components with their texts. Check: `make lint`, `make test-web`.
3. Write `docslinkcheck` with its tests and run it from `make docs`. Check: `make docs-check`.
4. Place `PageTitle` on the pages and write the end-to-end spec. Check: `make e2e-web`, then Verification.

## Verification

Against `muster dev` (version `0.0.0-dev`), with a Personal access token `$T` of the Admin:

```sh
curl -s -H "Authorization: Bearer $T" localhost:8080/api/v1/me | jq -r .docs_version          # latest
# stop make dev, then start a binary built as a release on the same development database:
make build VERSION=0.1.0 && ./bin/muster dev &
curl -s -H "Authorization: Bearer $T" localhost:8080/api/v1/me | jq -r .docs_version          # 0.1
git grep -n 'muster-io.github.io/muster' web/src -- ':!web/src/api/gen' ':!*.test.ts' ':!*.test.tsx'   # web/src/lib/docs.ts only
make docs-check                                                                              # passes; docslinkcheck prints its count
sed -i.bak 's/^## Add a Destination$/## Destinations/' docs/messengers/telegram.md && make docs-check
                     # fails: page.destination.telegram: messengers/telegram.md#add-a-destination: no element with this id
mv docs/messengers/telegram.md.bak docs/messengers/telegram.md
```

Playwright at 1280 and 360 px, signed in as the Admin:

1. Routes: "Guide" beside the title "Routes", with the accessible name "Guide: Routes (opens in a new tab)", `href`
   `https://muster-io.github.io/muster/latest/concepts/routes-and-group-keys/`, `target="_blank"`, `rel` with
   `noopener` and `noreferrer`; following it opens a new page at that address.
2. Connections → the demo Telegram Connection: the link goes to `…/latest/messengers/telegram/`; Destinations → a
   Slack Destination: to `…/latest/messengers/slack/#invite-the-bot-and-create-destinations`; an outgoing webhook
   Destination in template mode: to `…/latest/outgoing-webhooks/templates/`.
3. The Alert Group list, an Alert Group, Integrations, an Integration, its edit page and a Stored Snapshot each show
   their link; the sign-in page, signed out, shows no link to the site.
4. Profile → Language → Russian: the link beside «Маршруты» reads «Руководство».
5. At 360 px the Routes, Connection and Destination pages and the Alert Group page have
   `document.documentElement.scrollWidth <= 360`.
6. With the release build of the commands above, the Routes page links to `…/muster/0.1/concepts/routes-and-group-keys/`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): link page titles to the documentation of the running version`.
- The version comes from the server, not from the bundle, because the SPA's files are served to anyone before sign-in
  and nothing public names the version (C-03.FR-24); the address of the site stays a build constant (D419). A
  closed network without access to the site sees links that do not open; Muster has no setting for a mirror.
- The rule changes what a snapshot image's Internal alerts link to; a release is unaffected.
- The story has a small backend part — one field and the shared rule — in the same pull request, because the field is
  useless without the links and the links are wrong without the field; S-087 set the precedent.
- With 34 files the story stays under the size signal; the links next to fields and problems are S-109 and S-110.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-11 | partial | the version, the component, the texts and the page titles of these pages; S-109 and S-110 place the rest |
| C-21.FR-12 | full | |
| C-21.AC-7 | full | |
| C-21.AC-8 | partial | these pages; S-109 and S-110 complete the table |
| C-21.AC-9 | full | |
| C-19.FR-9 | partial | `latest` for snapshot and pre-release versions, in the Internal alerts and the chart helper; S-053 and S-059 built the rest |
