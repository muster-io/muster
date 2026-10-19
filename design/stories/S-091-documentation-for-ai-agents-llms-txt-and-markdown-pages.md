---
id: S-091
title: "Documentation for AI agents: llms.txt, the full text and a Markdown version of every page"
capability: C-21
kind: docs
layer: L1
depends_on: [S-060]
covers: [C-21.FR-8, C-21.AC-4]
files_touched:
  - docs/llms.txt.tmpl
  - internal/tools/llmsgen/main.go
  - internal/tools/llmsgen/llmsgen_test.go
  - internal/tools/llmsgen/testdata/**
  - internal/tools/docspublish/main.go
  - internal/tools/docspublish/docspublish_test.go
  - Makefile
  - mkdocs.yml
  - .github/workflows/docs.yml
  - docs/index.md
  - AGENTS.md
acceptance:
  - "[C-21.FR-8] `make docs` writes `site/llms.txt` from `docs/llms.txt.tmpl` and the navigation of `mkdocs.yml`, in English: the title, the statement that it is the canonical reference of Muster for AI agents and that an agent must not rely on its training data about Muster, the rules, the four scenarios — a bridge in Go, a bridge in Python, connecting Alertmanager, automating through the API — each naming the pages to read, and the document map, with the size of `llms-full.txt` (`llmsgen_test.go`, Verification)."
  - "[C-21.FR-8] Each rule takes its value from its source when the site is built: the error classes from the `ErrorClass` enum of `api/bridge.yaml`; confirming a press at most 3 seconds after it and keeping each `Idempotency-Key` per token for at least 24 hours from the description of `api/bridge.yaml`; the edit-window margin and the longest wait of `rate_limited` from `bridge.edit_window_margin` and `bridge.retry_after_max` in defaults.md; and the sources of truth are `api/openapi.yaml` and `api/bridge.yaml`, which the build publishes under `api/` beside the pages of the same version."
  - "[C-21.FR-8] Every page of the navigation has a Markdown version at its source path under `docs/` — `messengers/bridge.md` for `messengers/bridge/`, `reference/index.md` for the section page `reference/` — byte for byte its source, and one entry in the document map of `llms.txt`, in the order of the navigation, with its title and the first sentence of its first paragraph, linked to the Markdown version."
  - "[C-21.FR-8] `site/llms-full.txt` holds every page of the navigation once, in the order of the navigation, each after a line with its absolute `.md` URL, with relative links made absolute; the rendered API and Bridge protocol references are no pages and have no Markdown version, and `llms.txt` names their specifications instead."
  - "[C-21.FR-8] Every link of `llms.txt` points into the same version of the site, `<site_url><version>/`, with the version from `DOCS_VERSION` — `latest` by default, the published minor in the publishing workflow — or to a repository under `https://github.com/muster-io/`; `docspublish` copies the `llms.txt` of the version that the root redirects to into the root of the published site, so that `https://muster-io.github.io/muster/llms.txt` answers (`docspublish_test.go`)."
  - "[C-21.FR-8, C-21.AC-4] `make docs`, and so `make docs-check`, fails naming the file when a phrase, a setting, an enum or a page that `docs/llms.txt.tmpl` names is no longer in its source, when an anchor it links is no `id` of the built page, when a page of the navigation has no title, when a link of `llms.txt` or `llms-full.txt` into the site does not resolve under `site/`, and when `llms.txt` is larger than 32 KiB (`llmsgen_test.go`, Verification)."
  - "[C-21.AC-4] On the built site, `llms.txt` links to the Markdown version of every page of the navigation and every one of its links resolves under `site/`; `llms-full.txt` holds every page and is smaller than 1 MiB, under about 262,000 tokens at 4 bytes a token."
  - "[C-21.FR-8] The home page points AI agents to `llms.txt`."
verify: "make ci docs-check"
operator_attention: false
issue: null
---

# S-091. Documentation for AI agents: llms.txt, the full text and a Markdown version of every page

## Scope

**IN**

- `llms.txt` at the root of every version of the site and of the published site: the entry point for AI agents and
  coding assistants, with the rules that agents get wrong most, the scenarios and the map of the documentation.
- `llms-full.txt`, the text of the whole site in one file, and a Markdown version of every page.
- The generator `llmsgen`, run by `make docs` after the site is built, with the checks that make `make docs-check`
  fail when the hand-written part of `llms.txt` no longer matches its sources.
- The root copy of `llms.txt` in `docspublish`, and the version of the links in the publishing workflow.

**OUT**

- The pages themselves: the site and its references (S-057), the runbooks (S-058), Bridge and Write a bridge with the
  protocol reference (S-074, S-081, S-076, S-079) and the remaining sections (S-060), which this story only maps.
- The guides for agents inside the libraries' repositories: their `AGENTS.md` exist already (S-083, S-084); this story
  links to the repositories.
- A Russian `llms.txt`: the site is in English (C-21.FR-5).

## Contracts

- **Mechanism** (C-21.FR-8): a Go tool of this repository, `internal/tools/llmsgen`, run by `make docs` after the strict
  build of Zensical and the checks that follow it, writes everything into `site/` from the sources of the site — the
  `nav` and `site_url` of `mkdocs.yml`, the pages under `docs/` and the hand-written template `docs/llms.txt.tmpl` — and
  checks it. It is built with the standard library and `go.yaml.in/yaml/v3`, which the tools of the repository already
  use, so no dependency is added (ADR-0001). The outputs live only in the built site, which git ignores, and are made
  again by every `make docs`, so they can never lag the pages; what can go stale is the hand-written template, and the
  checks below find it. Zensical's own `llmstxt` support is not used (Notes).
- **Version and links**: the links of `llms.txt` and `llms-full.txt` are absolute, under `<site_url><version>/`, where
  `site_url` comes from `mkdocs.yml` (`https://muster-io.github.io/muster/`) and the version from `DOCS_VERSION`, a
  variable of the Makefile that is `latest` by default; `.github/workflows/docs.yml` runs `make docs` with
  `DOCS_VERSION` set to the version it publishes, so a release's `llms.txt` points into that release.
- **Markdown versions**: for every page of the navigation, `site/<source path>` is a byte-for-byte copy of
  `docs/<source path>`. With directory URLs this is the page's URL with `.md` added — `messengers/write-a-bridge/` has
  `messengers/write-a-bridge.md` — and a section page or the home page keeps its file name: `reference/` has
  `reference/index.md`, the home page `index.md`. Relative links between pages stay valid as written, because the
  Markdown versions lie where their sources do. Files under `docs/` that are no pages (`exclude_docs`, assets) get none.
- **Specifications**: `llmsgen` copies `api/openapi.yaml` and `api/bridge.yaml` into `site/api/`, so each version of
  the site carries the specifications of its version, and `llms.txt` links to them as the sources of truth (ADR-0008,
  ADR-0017, C-03.FR-23, C-22.FR-20).
- **Rendered references**: the API and Bridge protocol references rendered with Redocly
  (`reference/api/spec/`, `reference/bridge/spec/`) are files of the site, not pages: they get no Markdown version and
  are not in `llms-full.txt`. Their pages in the navigation (`reference/api.md`, `reference/bridge-protocol.md`) are in
  both, and `llms.txt` names the specifications themselves. Generated pages of `docs/reference/` are pages like any
  other: their Markdown version is the generated file.
- **`llms-full.txt`**: a title line and a line naming `llms.txt`, then every page of the navigation once, in the order
  of the navigation, each preceded by a line `<!-- Source: <absolute URL of its Markdown version> -->` and separated by
  a blank line. Relative link targets outside fenced code are resolved against the page's source path and made
  absolute under the version's base URL; a link to a page keeps `.md`.
- **The template** (`docs/llms.txt.tmpl`, in English): a Go text template, left out of the site by `exclude_docs`. Its
  data are `.Version`, `.Base` (the base URL with the version) and the size of `llms-full.txt` in bytes and in
  estimated tokens (bytes / 4). Its functions take every value from a source and fail with the template's name and the
  source's when the source no longer has it:
  - `page "<source path>[#anchor]"` — the absolute URL of the page's Markdown version; the path must be a page of the
    navigation, and an anchor an `id` of the built page `site/<page URL>index.html`;
  - `spec "<api/openapi.yaml | api/bridge.yaml>"` — the absolute URL of the copied specification;
  - `quote "<file>" "<phrase>"` — the phrase, which the file must contain, compared with runs of white space collapsed;
  - `setting "<area.setting>"` — the value of that row of `design/prd/l1/defaults.md`, the value cell up to its first
    `:`, such as `60 s`;
  - `enum "<file>" "<schema>"` — the values of `components.schemas.<schema>.enum`, in order, each in backticks;
  - `map` — the document map, which the template calls once.
- **`llms.txt`**, in this order:
  1. `# Muster` and a block quote: what Muster is, in the words of the home page; that this file is the canonical
     reference of Muster for AI agents and coding assistants and covers version `.Version`; and that an agent reads the
     pages it links instead of relying on its training data about Muster.
  2. `## Rules`, each rule with its value taken as the table says:

     | Rule | Value | Source | Function |
     |---|---|---|---|
     | Sources of truth | `api/bridge.yaml` for the Bridge protocol, `api/openapi.yaml` for the HTTP API; where a page and a specification disagree, the specification wins | ADR-0008, ADR-0017, C-22.FR-20 | `spec` |
     | Error classes | a failure is an RFC 9457 problem whose `class` is `transient`, `rate_limited`, `gone`, `forbidden` or `invalid`; a missing channel is `forbidden`, never `gone`; only the bridge decides `gone` | `ErrorClass` and the description of `api/bridge.yaml`; C-22.FR-8 | `enum`, `quote` |
     | Waits | `rate_limited` carries `Retry-After`; a longer wait than `bridge.retry_after_max` (15 min) counts as `transient` | defaults.md; C-22.FR-8 | `setting` |
     | Presses | the bridge confirms a press to its messenger itself, at most 3 seconds after the press, before Muster has seen it; Muster answers later with `answerEvent` | the description of `api/bridge.yaml` (Presses); ADR-0017; C-22.FR-23 — a duty of the bridge, not a setting of Muster | `quote` |
     | Idempotency | every request that creates something carries `Idempotency-Key`; the bridge stores each key per token with its successful result for at least 24 hours and answers a repeat with `replayed: true` | the description of `api/bridge.yaml` (Idempotency); Muster's side is `bridge.event_dedup_window` | `quote` |
     | Edit windows | a messenger that limits edits is declared with `limits.edit_max_age_seconds`; Muster stops editing `bridge.edit_window_margin` (60 s) before it and publishes the Alert Group again on its next change of status; an edit the messenger refuses for its age is `gone` with the `reason` `edit_window_closed` | `Limits` and `Problem` of `api/bridge.yaml`; defaults.md; C-22.FR-9 | `quote`, `setting` |
     | Finishing a bridge | a bridge is finished only when `muster bridge check` passes against it; use a bridge library instead of writing the protocol | C-22.FR-21, C-22.FR-25 | `page` |

  3. `## Scenarios`, one `###` heading each, with the pages to read in order and the commands that finish it:
     - **Write a bridge in Go**: Write a bridge (its Go library and example bridge), Messengers → Bridge, Reference →
       Bridge protocol and `api/bridge.yaml`; `go get github.com/muster-io/muster-bridge-go` and the library's
       repository, whose README and `AGENTS.md` guide the work; finished when `muster bridge check` passes.
     - **Write a bridge in Python**: Write a bridge (its Python section, S-060), the same protocol pages;
       `pip install muster-bridge` and the repository `muster-io/muster-bridge-python`, with its `AGENTS.md`; finished
       when `muster bridge check` passes.
     - **Connect Alertmanager**: Quick start, Alertmanager → Connect Alertmanager and Heartbeat, Concepts → Snapshot
       semantics.
     - **Automate through the API**: Reference → API with its conventions and tokens, `api/openapi.yaml` (also served by
       every Muster at `/api/v1/openapi.yaml`), Concepts → statuses and commands, Operations → CLI.
  4. `## Document map`, written by `map`: one `###` heading per top-level section of the navigation, a nested section a
     level lower, and per page `- [<title>](<Markdown URL>): <description>` in the order of the navigation. The title is
     the page's title in the navigation when it has one, otherwise its first `#` heading; the description is the first
     sentence of the first paragraph after it, skipping headings, comments, admonitions and fenced blocks, and is left
     out when the page has no such paragraph.
  5. `## Optional`: `llms-full.txt` with its size.
- **Checks** (`llmsgen`, C-21.AC-4): it exits non-zero, naming the file and what is wrong, when a template function
  fails as above, when the template does not call `map` exactly once, when a page of the navigation does not exist or
  has no title, when a link of `llms.txt` or `llms-full.txt` under the version's base URL does not resolve to a file
  under `site/`, when a link of `llms.txt` leaves the site other than to a repository under
  `https://github.com/muster-io/`, and when `llms.txt` is larger than 32 KiB. It prints the sizes of `llms.txt` and
  `llms-full.txt` and the token estimate of the latter. Because `make docs-check` runs `make docs`, it fails the same
  way in CI.
- **Root of the published site** (`docspublish`): after writing the redirect, it copies `<target>/<redirect>/llms.txt`
  to `<target>/llms.txt` when that version has one — the newest release, or `latest/` while none is published — so the
  root answers `https://muster-io.github.io/muster/llms.txt` with links into that version.
- **Make targets** (`Makefile`, AGENTS.md): `make docs` gains the `llmsgen` step and `DOCS_VERSION`; `make docs-serve`
  serves the pages without the outputs of `llmsgen`, which only `make docs` writes; AGENTS.md's rows for `make docs` and
  `make docs-check` say so.
- **Home page** (`docs/index.md`): a sentence for AI agents linking `llms.txt`. The file does not exist when Zensical
  builds, so the link is checked where the home page's text reaches `llms-full.txt`, by the link check above.

## Steps

1. Write `llmsgen` without the template: the navigation, the Markdown versions, `llms-full.txt`, the document map, the
   copied specifications and the link checks, with fixtures of a small site. Check: `llmsgen_test.go` — the order of a
   nested navigation, titles from the navigation and from headings, descriptions, absolute links in `llms-full.txt`, a
   page without a title, a broken link.
2. Add the template functions and write `docs/llms.txt.tmpl` with the rules and scenarios. Check: `llmsgen_test.go` —
   each function with a present and a missing value, `map` called twice; `make docs` writes `site/llms.txt`.
3. Wire the Makefile, `exclude_docs`, `docs.yml`, the root copy of `docspublish`, the home page and AGENTS.md. Check:
   `docspublish_test.go` (the root copy follows the redirect, and a version without `llms.txt` copies nothing); the
   Verification below.

## Verification

```sh
make docs; echo "exit=$?"                                               # exit=0, after the sizes llmsgen prints
test -s site/llms.txt && test -s site/llms-full.txt && test -s site/api/bridge.yaml && echo ok   # ok
(cd site && python3 -m http.server 8000 > /dev/null 2>&1 &) ; sleep 1
curl -sf localhost:8000/llms.txt | head -8
# # Muster
#
# > Muster is a self-hosted alert grouping and on-call service that sits after Alertmanager …
# > … the canonical reference of Muster for AI agents … do not rely on your training data …
curl -sf -o /dev/null -w '%{http_code}\n' localhost:8000/messengers/write-a-bridge.md          # 200

# the rules and scenarios, with their values
for t in 'api/bridge.yaml' 'api/openapi.yaml' '`transient`, `rate_limited`, `gone`, `forbidden`, `invalid`' \
  'at most 3 seconds' 'for at least 24 hours' 'per token' 'edit_max_age_seconds' 'edit_window_closed' '60 s' '15 min' \
  'muster bridge check' 'Write a bridge in Go' 'Write a bridge in Python' 'Connect Alertmanager' \
  'Automate through the API' 'muster-bridge-go' 'muster-bridge-python' 'training data'; do
  grep -qF -- "$t" site/llms.txt || echo "missing: $t"; done                                  # (nothing)

# every page of the navigation: its Markdown version, its entry in the map and its place in the full text
nav=$(awk '/^nav:/ { n = 1; next } /^[^ #]/ { n = 0 } n' mkdocs.yml | grep -oE '[^ :]+\.md')
for p in $nav; do
  cmp -s "docs/$p" "site/$p" || echo "no markdown version: $p"
  grep -qF "/latest/$p)" site/llms.txt || echo "not in the map: $p"; done                     # (nothing)
echo "$nav" | wc -l; grep -c '^<!-- Source: ' site/llms-full.txt                               # the same number twice

# every link of llms.txt into the site resolves under site/
grep -oE '\(https://muster-io\.github\.io/muster/latest/[^)#]*' site/llms.txt | sort -u |
  sed 's|^(https://muster-io.github.io/muster/latest/||' |
  while read -r f; do test -f "site/$f" || echo "broken: $f"; done      # (nothing)
wc -c < site/llms.txt                                                   # under 32768
wc -c < site/llms-full.txt                                              # under 1048576
# about bytes / 4 tokens: under 262,000

# the version of the links
make docs DOCS_VERSION=0.4 > /dev/null
grep -c 'muster/0.4/' site/llms.txt                                     # a number above 0
grep -c 'muster/latest/' site/llms.txt                                  # 0

# stale: the template names a value its source no longer has
sed -i.bak 's/at most 3 seconds/at most 2 seconds/' docs/llms.txt.tmpl
make docs-check > /tmp/s091.log 2>&1; echo "exit=$?"; grep 'llms.txt.tmpl' /tmp/s091.log
# exit=2
# docs/llms.txt.tmpl: api/bridge.yaml does not say "at most 2 seconds"
mv docs/llms.txt.tmpl.bak docs/llms.txt.tmpl
sed -i.bak 's|messengers/write-a-bridge.md|messengers/write-a-bot.md|' docs/llms.txt.tmpl
make docs > /dev/null 2>&1; echo "exit=$?"                              # exit=2
# docs/llms.txt.tmpl: messengers/write-a-bot.md is not a page of the navigation
mv docs/llms.txt.tmpl.bak docs/llms.txt.tmpl

# a value follows its source without an edit of the template
sed -i.bak 's/^| `bridge.edit_window_margin` | 60 s/| `bridge.edit_window_margin` | 90 s/' design/prd/l1/defaults.md
make docs > /dev/null && grep -c '90 s' site/llms.txt                   # 1 or more
mv design/prd/l1/defaults.md.bak design/prd/l1/defaults.md

# the root of the published site
rm -rf /tmp/gh && mkdir /tmp/gh && make docs > /dev/null
go run ./internal/tools/docspublish --site site --target /tmp/gh --version latest
cmp /tmp/gh/llms.txt /tmp/gh/latest/llms.txt && echo same               # same
```

After merging, the first publication of `latest` answers
`curl -sf https://muster-io.github.io/muster/llms.txt | head -1` with `# Muster`, and
`https://muster-io.github.io/muster/latest/messengers/write-a-bridge.md` with the page's Markdown.

## Open questions

None.

## Notes

- Suggested commit: `docs: publish llms.txt, the full text and markdown versions of the pages for ai agents`.
- Origin: the maintainer added documentation for AI agents to L1 as its own story right after S-060 (D371).
- Zensical's built-in `llmstxt` support, part of the pinned Zensical (MIT, no new dependency), was tried on this site
  with Zensical 0.0.70 and not chosen: it converts the rendered HTML back to Markdown, which drops the language of
  every code fence — the five Mermaid diagrams of Write a bridge become plain code — and escapes text (`public\_id`);
  it writes `<page>/index.md` with links under `site_url`, which lacks the version path the site is published under;
  it orders pages by its own lists, not the navigation; and it cannot check a value against defaults.md or the
  specification. The page sources are already the clean Markdown an agent needs.
- The libraries' repositories have their own `AGENTS.md`, for agents that change the libraries (S-083, S-084); this
  story's `llms.txt` is for agents that use Muster or write a bridge, and points to those repositories.
- `llms.txt` stays an index: the page text belongs in the pages, and a value belongs in its source; the template only
  names them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-8 | full | |
| C-21.AC-4 | full | |
