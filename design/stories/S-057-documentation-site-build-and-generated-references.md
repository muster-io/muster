---
id: S-057
title: Documentation site, its build and the generated references
capability: C-21
kind: docs
layer: L1
depends_on: [S-053]
covers: [C-21.FR-1, C-21.FR-2, C-21.FR-4, C-21.FR-5, C-21.AC-1]
files_touched:
  - mkdocs.yml
  - docs/requirements.txt
  - docs/index.md
  - docs/reference/index.md
  - docs/reference/api.md
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/config/config.go
  - deploy/helm/muster/values.schema.json
  - internal/tools/docspublish/main.go
  - internal/tools/docspublish/docspublish_test.go
  - Makefile
  - .github/workflows/ci.yml
  - .github/workflows/docs.yml
acceptance:
  - "[C-21.FR-1] `make docs` builds the site from `docs/` with Zensical — or with Material for MkDocs and the same `mkdocs.yml` if Zensical cannot build it — in strict mode, with the toolchain pinned by hash in `docs/requirements.txt`."
  - "[C-21.FR-1, C-21.AC-1] The publishing tool writes a build into the `gh-pages` branch under `<major.minor>/` for a release and `latest/` for `master`, keeps `versions.json` and the root redirect to the newest release, and refuses to change a minor older than the newest one."
  - "[C-21.FR-2] The Reference section has the generated pages for metrics, log events, Internal alerts, environment variables and chart values, and the API reference rendered from `api/openapi.yaml`, the document the binary serves at `/api/v1/openapi.yaml`."
  - "[C-21.FR-4] CI fails when a generated reference page is not current and when the site has a broken internal link or anchor."
  - "[C-21.FR-5] The site is in English and renders Mermaid diagrams."
verify: "make ci docs-check"
operator_attention: true
issue: null
---

# S-057. Documentation site, its build and the generated references

## Scope

**IN**

- The site configuration, its pinned toolchain and its build in strict mode.
- The versioned publishing to GitHub Pages: a frozen build per minor, documentation-only corrections of the current
  minor, `latest` from `master`.
- The Reference section: the generated pages of S-005 and S-021, two new generated pages (environment variables and
  chart values) and the rendered API reference.
- The navigation for the pages that earlier stories wrote.

**OUT**

- The runbook pages and "Telegram in restricted networks" (S-058); the remaining sections — Quick start, Install,
  Concepts, Operations and the rest of Alertmanager and Sign-in (S-060).
- The check that every chart rule and Internal alert has a runbook page (S-058, S-059).

## Contracts

- **Generator** (C-21.FR-1): Zensical. Material for MkDocs, which reads the same `mkdocs.yml`, is the fallback only if
  Zensical cannot build this site with its search, navigation and Mermaid at the time of implementation; the pull
  request then says why. The toolchain is tooling that is not
  distributed with Muster, so the licence rule of ADR-0001 for build tooling and the documentation site applies (any
  OSI-approved licence, reported by CI, not blocking); versions are pinned with hashes in `docs/requirements.txt` and
  installed by `make docs` into a local virtual environment.
- **Configuration** (`mkdocs.yml`): `site_url` `https://muster-io.github.io/muster/`, the repository link, `docs_dir`
  `docs`, `strict: true` with link and anchor validation turned into errors, the `pymdownx.superfences` fence for
  `mermaid`, admonitions, tables, the search, the version selector reading `versions.json`, and the navigation:
  Home; Alertmanager (`integrations/alertmanager.md`, `integrations/heartbeat.md`); Messengers
  (`messengers/mattermost.md`, `messengers/telegram.md`, `messengers/account-links.md`); Outgoing webhooks
  (`outgoing-webhooks/events.md`, `outgoing-webhooks/templates.md`); Sign-in (`sign-in/oidc-keycloak.md`); Reference.
  Later stories add their pages to it.
- **Home** (`docs/index.md`): what Muster is, a Mermaid diagram of the path Alertmanager → Muster → Destinations, and
  links to the sections.
- **Generated references** (C-21.FR-2, FR-4; `internal/tools/refgen`, `make generate`): besides `metrics.md`,
  `log-events.md` and `internal-alerts.md`, it writes `docs/reference/environment.md` from the bootstrap settings
  registry of `internal/config` (name, default, description, the `_FILE` form) and `docs/reference/chart-values.md`
  from `deploy/helm/muster/values.schema.json` (path, type, default, description), which gains a description for every
  value. `make generate-check` fails when any of them is not current.
- **API reference** (`docs/reference/api.md`): a page that explains the conventions of `api/README.md` in short and
  links to `reference/api/`, which `make docs` renders from `api/openapi.yaml` with Redocly CLI into the built site
  (never checked in), so each version of the site has the reference of that version.
- **Publishing** (C-21.FR-1; `internal/tools/docspublish`, `.github/workflows/docs.yml`): `docspublish --site site
  --target <gh-pages checkout> --version <major.minor|latest>` replaces that directory, updates `versions.json` (version,
  title, aliases) and the root `index.html` redirect to the newest release, and refuses a version older than the newest
  published minor. The workflow: on pull requests touching `docs/`, `mkdocs.yml`, the generators or the API
  specification — `make docs-check`; on every push to `master` — publish `latest`; on a release tag `vX.Y.Z` — publish
  `X.Y`; on manual dispatch from `master` with a version — a documentation-only correction, allowed only for the newest
  released minor. GitHub Pages serves the `gh-pages` branch.
- **Make targets**: `docs` (generate, render the API reference, build strictly into `site/`), `docs-serve`,
  `docs-check` (`generate-check` and `docs`); `make ci` stays unchanged and `docs-check` runs in its own CI job.
- **Language and diagrams** (C-21.FR-5): pages are in English; diagrams are Mermaid fences, never images of diagrams.

## Steps

1. Set up the generator and write `mkdocs.yml`, `docs/requirements.txt` and the home page. Check: `make docs` builds; a
   broken link fails it.
2. Add the two new generated pages and the API reference. Check: `make generate-check` fails on an edited page; the
   built site has every Reference page.
3. Write `docspublish` and the workflow. Check: `docspublish_test.go` covers a new minor, `latest`, the redirect and the
   refusal of an older minor; Verification below.

## Verification

```sh
make docs; echo "exit=$?"                                   # exit=0
for p in metrics log-events internal-alerts environment chart-values api; do test -f site/reference/$p/index.html && echo $p; done | wc -l   # 6
grep -c 'muster_build_info' site/reference/metrics/index.html          # 1
grep -c 'MUSTER_SECRET_KEYS' site/reference/environment/index.html     # 1
grep -c 'existingSecret' site/reference/chart-values/index.html        # 1
grep -c 'getSystemStatus' site/reference/api/index.html                # 1
grep -c 'class="mermaid"' site/index.html                              # 1

# C-21.FR-4: a broken link and a stale generated page fail
echo '[broken](no-such-page.md)' >> docs/index.md; make docs > /dev/null 2>&1; echo "exit=$?"   # exit=2 (or 1)
git checkout docs/index.md
echo '<!-- edited -->' >> docs/reference/environment.md; make generate-check; echo "exit=$?"
# stale generated files: docs/reference/environment.md
# exit=2
git checkout docs/reference/environment.md

# C-21.FR-1, C-21.AC-1: the versioned layout
rm -rf /tmp/gh && mkdir /tmp/gh
go run ./internal/tools/docspublish --site site --target /tmp/gh --version 0.4
go run ./internal/tools/docspublish --site site --target /tmp/gh --version latest
jq -c '[.[].version]' /tmp/gh/versions.json                            # ["latest","0.4"]
grep -o 'url=[^"]*' /tmp/gh/index.html                                 # url=0.4/
go run ./internal/tools/docspublish --site site --target /tmp/gh --version 0.3; echo "exit=$?"
# version 0.3 is older than the newest published minor 0.4; older minors are not changed
# exit=1
(cd /tmp/gh && python3 -m http.server 8000 > /dev/null 2>&1 &) ; sleep 1
curl -s localhost:8000/0.4/reference/metrics/ | grep -c 'muster_leader'   # 1
```

After merging, the first push to `master` publishes `latest`, and `https://muster-io.github.io/muster/latest/` shows the
home page with the version selector; the pull request records that the operator enabled GitHub Pages from `gh-pages`.

## Open questions

None.

## Notes

- Suggested commit: `docs: add the documentation site, its versioned publishing and generated references`.
- Pages written by earlier stories move into the navigation unchanged; a page that breaks the strict build is fixed in
  this pull request.
- `operator_attention`: enabling GitHub Pages in the repository settings.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-1 | full | |
| C-21.FR-2 | partial | the Reference section and the navigation of existing pages; the runbooks and "Telegram in restricted networks" are S-058, the remaining sections S-060 |
| C-21.FR-4 | full | |
| C-21.FR-5 | partial | the configuration and the home page; the pages of S-058 and S-060 follow it |
| C-21.AC-1 | partial | the versioned layout the `runbook_url`s point into; the pages are S-058, the chart's URLs S-059 |
