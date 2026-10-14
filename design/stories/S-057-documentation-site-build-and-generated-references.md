---
id: S-057
title: Documentation site, its build and the generated references
capability: C-21
kind: docs
layer: L1
depends_on: [S-053, S-051]
covers: [C-21.FR-1, C-21.FR-2, C-21.FR-4, C-21.FR-5, C-21.AC-1]
files_touched:
  - mkdocs.yml
  - docs/requirements.in
  - docs/requirements.txt
  - docs/index.md
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/config/config.go
  - internal/config/config_test.go
  - deploy/helm/muster/values.schema.json
  - internal/tools/docspublish/main.go
  - internal/tools/docspublish/docspublish_test.go
  - Makefile
  - .gitignore
  - .github/workflows/ci.yml
  - .github/workflows/docs.yml
  - AGENTS.md
acceptance:
  - "[C-21.FR-1] `make docs` builds the site from `docs/` with Zensical — or with Material for MkDocs and the same `mkdocs.yml` if Zensical cannot build it — in strict mode, with the toolchain pinned by hash in `docs/requirements.txt`."
  - "[C-21.FR-1, C-21.AC-1] The publishing tool writes a build into the `gh-pages` branch under `<major.minor>/` for a release and `latest/` for `master`, keeps `versions.json` and the root redirect to the newest release — to `latest/` while no release is published — and refuses to change a minor older than the newest one."
  - "[C-21.FR-2] The Reference section has the generated pages for metrics, log events, Internal alerts, environment variables and chart values, and the API reference rendered from `api/openapi.yaml`, the document the binary serves at `/api/v1/openapi.yaml`."
  - "[C-21.FR-4] CI runs `make docs-check` on every pull request and fails when a generated reference page is not current and when the site has a broken internal link or anchor."
  - "[C-21.FR-5] The site is in English and renders Mermaid diagrams."
verify: "make ci docs-check"
operator_attention: true
issue: 57
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
  installed by `make docs` into a local virtual environment, `bin/docs-venv/`. `make licenses` reports the licences of
  the packages in that environment next to the other tooling licences, without failing on them.
- **Configuration** (`mkdocs.yml`): `site_url` `https://muster-io.github.io/muster/`, the repository link, `docs_dir`
  `docs`, `strict: true` with link and anchor validation turned into errors, the `pymdownx.superfences` fence for
  `mermaid`, admonitions, tables, the search, the version selector reading `versions.json`, and the navigation: Home;
  Alertmanager (`integrations/alertmanager.md`, `integrations/heartbeat.md`); Messengers (`messengers/mattermost.md`,
  `messengers/telegram.md`, `messengers/account-links.md` of S-051); Outgoing webhooks (`outgoing-webhooks/events.md`,
  `outgoing-webhooks/templates.md`); Sign-in (`sign-in/oidc-keycloak.md`); Reference. Later stories add their pages to
  it. `exclude_docs` leaves out `brand/README.md`, the notes on the Muster mark, which is not a page of the site, and
  `requirements.*`, the pins of the toolchain; the SVG files of `docs/brand/` stay available to the theme as logo and
  favicon.
- **Home** (`docs/index.md`): what Muster is, a Mermaid diagram of the path Alertmanager → Muster → Destinations, and
  links to the sections.
- **Generated references** (C-21.FR-2, FR-4; `internal/tools/refgen`, `make generate`): besides `metrics.md`,
  `log-events.md` and `internal-alerts.md`, it writes `docs/reference/environment.md` from the bootstrap settings
  registry of `internal/config` (name, default, the `_FILE` form, whether it is a secret, description) and
  `docs/reference/chart-values.md` from `deploy/helm/muster/values.schema.json` (path, type, description), which gains a
  description for every value, and the defaults of `deploy/helm/muster/values.yaml`. `make generate-check` fails when
  any of them is not current. Everything in `docs/reference/` is generated: `make generate` removes
  `docs/reference/*.md` before refgen runs (`Makefile`), so a hand-written page there would be deleted. refgen therefore
  also writes `docs/reference/index.md`, the list of the reference pages, and `docs/reference/api.md`, the API page
  below, from text kept in refgen.
- **API reference** (`docs/reference/api.md`, written by refgen): a page that explains the conventions of
  `api/README.md` in short and links to `api/spec/index.html`. Before the site is built, `make docs` renders
  `api/openapi.yaml` with Redocly CLI — the `@redocly/cli` pinned in `web/package.json` that `make lint` already runs,
  through pnpm — into `docs/reference/api/spec/index.html`, inside `docs_dir`, so the strict build checks the link and
  copies the page into the site; each version of the site has the reference of that version. The rendered file is
  ignored by git (`.gitignore` gains `/docs/reference/api/spec/`, `/site/`; the virtual environment lives under the
  ignored `/bin/`) and survives `make generate`, which removes only `docs/reference/*.md`. The page builds to
  `reference/api/` and the rendered specification to `reference/api/spec/`, so the two never collide.
- **Publishing** (C-21.FR-1; `internal/tools/docspublish`, `.github/workflows/docs.yml`): `docspublish --site site
  --target <gh-pages checkout> --version <major.minor|latest>` replaces that directory, updates `versions.json` (version,
  title, aliases) and the root `index.html` redirect to the newest release — to `latest/` while no release is published,
  as before the first release — and refuses a version older than the newest published minor. A minor that is not
  published yet may always be published first, so the release tag of the first release, or a manual run after a
  failed tag run, adds its minor. `docs.yml` only publishes: on every push to `master` — `latest`; on a release tag
  `vX.Y.Z` — `X.Y`; on manual dispatch from `master` with a version — a documentation-only correction, allowed only for
  the newest released minor. GitHub Pages serves the `gh-pages` branch.
- **Check on every pull request** (C-21.FR-4; `.github/workflows/ci.yml`): `make docs-check` runs as a job of the
  pull-request workflow, with no path filter, because a pull request that adds an Internal alert, a chart rule, a
  metric or a setting changes generated pages and the runbook check of S-058 and S-059 without touching `docs/`. The
  tools it calls (helm in S-059) come from the Makefile's pinned tool targets, never from the runner.
- **Make targets**: `docs` (generate, render the API reference, build strictly into `site/`), `docs-serve`,
  `docs-check` (`generate-check` and `docs`); `make ci` stays unchanged and `docs-check` runs in its own job of
  `ci.yml`.
- **Language and diagrams** (C-21.FR-5): pages are in English; diagrams are Mermaid fences, never images of diagrams.

## Steps

1. Set up the generator and write `mkdocs.yml`, `docs/requirements.txt` and the home page. Check: `make docs` builds; a
   broken link fails it.
2. Add the two new generated pages, the generated index and API pages, and the rendered API reference. Check:
   `make generate-check` fails on an edited page and keeps `index.md` and `api.md`; the built site has every Reference
   page.
3. Write `docspublish`, the publishing workflow and the `docs-check` job of `ci.yml`. Check: `docspublish_test.go`
   covers a new minor, the first minor, `latest`, the redirect to `latest/` before a release and to the newest release
   after it, and the refusal of an older minor; `make licenses` lists the documentation toolchain; Verification below.

## Verification

```sh
make docs; echo "exit=$?"                                   # exit=0
for p in metrics log-events internal-alerts environment chart-values api; do test -f site/reference/$p/index.html && echo $p; done | wc -l   # 6
# built pages may be minified onto few lines, so the checks test for a match, not a count of lines
grep -q 'muster_build_info' site/reference/metrics/index.html && echo ok        # ok
grep -q 'MUSTER_SECRET_KEYS' site/reference/environment/index.html && echo ok   # ok
grep -q 'existingSecret' site/reference/chart-values/index.html && echo ok      # ok
grep -q 'getSystemStatus' site/reference/api/spec/index.html && echo ok         # ok
make generate && git status --porcelain docs/reference                 # (no output: index.md, api.md and the ignored spec survive)
test -f docs/reference/api/spec/index.html && echo ok                  # ok
grep -q 'class="mermaid"' site/index.html && echo ok                   # ok

# C-21.FR-4: a broken link and a stale generated page fail
echo '[broken](no-such-page.md)' >> docs/index.md; make docs > /dev/null 2>&1; echo "exit=$?"   # exit=2 (or 1)
git checkout docs/index.md
echo '<!-- edited -->' >> docs/reference/environment.md; make generate-check; echo "exit=$?"
# stale generated files: docs/reference/environment.md
# exit=2
git checkout docs/reference/environment.md

# C-21.FR-1, C-21.AC-1: the versioned layout
rm -rf /tmp/gh && mkdir /tmp/gh
go run ./internal/tools/docspublish --site site --target /tmp/gh --version latest
grep -o 'url=[^"]*' /tmp/gh/index.html                                 # url=latest/   (no release published yet)
go run ./internal/tools/docspublish --site site --target /tmp/gh --version 0.4   # the first minor may always be published
jq -c '[.[].version]' /tmp/gh/versions.json                            # ["latest","0.4"]
grep -o 'url=[^"]*' /tmp/gh/index.html                                 # url=0.4/
go run ./internal/tools/docspublish --site site --target /tmp/gh --version 0.3; echo "exit=$?"
# version 0.3 is older than the newest published minor 0.4; older minors are not changed
# exit=1
(cd /tmp/gh && python3 -m http.server 8000 > /dev/null 2>&1 &) ; sleep 1
curl -s localhost:8000/0.4/reference/metrics/ | grep -q 'muster_leader' && echo ok   # ok
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
- Corrections made in the pull request: `docs/requirements.in` holds the single pin from which
  `docs/requirements.txt` is compiled with hashes, and its comment and the header of `requirements.txt` record the
  command; `internal/config/config_test.go` tests `config.Variables()`, the registry the environment page is generated
  from; `AGENTS.md` lists `make docs`, `make docs-serve` and `make docs-check` among the Make targets. The chart values
  take their defaults from `values.yaml`, not from the schema, which has no defaults, so that they are kept in one
  place. `exclude_docs` also leaves out `docs/requirements.*`, which the site would otherwise publish.
- Zensical checks links to pages and their anchors in strict mode, but not links to files that are not pages, such as
  the rendered API reference, and it ignores `validation.nav`: a nav entry to a missing page and a page left out of the
  nav both build. `make docs` therefore checks after the build that `site/reference/api/spec/index.html` exists, that
  every page the nav of `mkdocs.yml` names exists under `docs/`, and that every page under `docs/`, except those
  `exclude_docs` names, is in the nav.
- No page of the site requests fonts from Google (NFR-7): `theme.font` is `false`, and the API reference is rendered
  with `--disableGoogleFont`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-1 | full | |
| C-21.FR-2 | partial | the Reference section and the navigation of existing pages; the runbooks and "Telegram in restricted networks" are S-058, the remaining sections S-060 |
| C-21.FR-4 | full | |
| C-21.FR-5 | partial | the configuration and the home page; the pages of S-058 and S-060 follow it |
| C-21.AC-1 | partial | the versioned layout the `runbook_url`s point into; the pages are S-058, the chart's URLs S-059 |
