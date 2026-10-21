---
id: S-079
title: "\"Write a bridge\" and the Bridge protocol reference on the documentation site"
capability: C-22
kind: docs
layer: L1
depends_on: [S-086]
covers: [C-22.FR-20, C-22.FR-23, C-22.AC-24]
files_touched:
  - docs/messengers/write-a-bridge.md
  - docs/messengers/bridge.md
  - docs/index.md
  - mkdocs.yml
  - Makefile
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - .gitignore
  - .github/workflows/ci.yml
acceptance:
  - "[C-22.FR-23] Messengers → Write a bridge starts with the Go bridge library and the example bridge built on it, then covers every topic C-22.FR-23 lists for a bridge written without a library — the protocol step by step, required and optional features, the envelope and its CommonMark, Mention markers, idempotency keys, confirming presses within 3 seconds, `thread_ref`, error classes and who decides `gone`, safe truncation, both event delivery modes with their signatures, testing with `muster bridge check` and the fake bridge, the example bridge and the licences of the protocol, the libraries and the example — with the five Mermaid diagrams that Contracts names, checked against the list in Verification."
  - "[C-22.FR-20, C-22.AC-24] `make docs` renders `api/bridge.yaml` with Redocly into the site as Reference → Bridge protocol, beside the API reference, and fails when the rendered file is missing; `refgen` writes its page, which links to the rendered reference and to `api/bridge.yaml` on `master` (`refgen_test.go`); both pages and Write a bridge are in the navigation, and `make docs-check` passes with no broken link or anchor."
  - "[C-22.FR-23] The home page lists Bridges among the messengers, and Messengers → Bridge links to Write a bridge and to the protocol reference."
  - "[C-22.FR-20] The rendered reference loads Redoc from the site's vendored copy and carries the Content-Security-Policy of the API reference, so the remote-asset check of `make docs` passes; `docs/reference/bridge/spec/` is ignored by git."
  - "[C-22.FR-20] The `oasdiff` job of CI fails a pull request that breaks `api/bridge.yaml` against its base branch in either direction, with both reports in the step summary; the report on `api/openapi.yaml` stays non-blocking (a scratch branch that removes a required answer field, adds one or makes a required request field optional fails the step; one that adds an optional field or an operation passes it, and so does this branch)."
verify: "make ci docs-check"
operator_attention: false
issue: 237
---

# S-079. "Write a bridge" and the Bridge protocol reference on the documentation site

## Scope

**IN**

- The page Messengers → Write a bridge, for people who write a bridge: the Go bridge library and the example bridge
  first, then the protocol for a bridge written without a library.
- The protocol reference rendered from `api/bridge.yaml`, its page in Reference, and the navigation.
- The CI check that refuses a breaking change of `api/bridge.yaml`, from the publication of the protocol on.

**OUT**

- Messengers → Bridge, for Admins (S-074, with its edit windows from S-081), and the Bridge section of Account links
  (S-076); the remaining sections of the site (S-060).
- The Python bridge library's section of Write a bridge, which S-060 adds; the Python library itself (S-084), built
  beside this story (D369); the libraries' own READMEs (S-083, S-084, S-085).

## Contracts

- **Write a bridge** (`docs/messengers/write-a-bridge.md`), in English, with five Mermaid diagrams (C-21.FR-5, D351):
  the architecture as a flowchart of Muster, the bridge and the messenger; the publication, edit and Thread reply with
  `Idempotency-Key`, including the retry after a lost answer and the `replayed` answer; a press by polling and by push,
  confirmed at once and answered later through `answerEvent`; an Account link by token and by code; and the refused edit
  past an edit window with the republication and the pointer reply. It covers: what a bridge is and when to write one;
  **start with a library** (ADR-0017) — what the bridge libraries do and what is left to the bridge author, then the Go
  library: `go get github.com/muster-io/muster-bridge-go` without a version, since the libraries are released only with
  Muster's first release (D366), a `bridge.Messenger` for the messenger, `bridge.New` with the bot's token, the features
  and the limits, `Handler()` behind an HTTP server, the reporting calls for presses, Thread messages, deleted messages
  and direct messages, a `Store` that survives restarts, and `muster bridge check` against it, with the minimal example
  of the library's README; the example bridge on the library, its image `ghcr.io/muster-io/muster-example-bridge:edge`
  until the libraries' first release, then the tag `vX.Y.Z` (D366), and how to try it next to `muster dev`; that the
  libraries stay `0.x` until Muster 1.0, speak protocol version 1 and are released from their own repositories. Then,
  for a bridge written without a library, the protocol in the order a bridge author implements it — `getInfo`,
  `checkChannel`, `postMessage`, `editMessage`, `replyInThread`, events — with the request and answer of each, taken
  from the examples of `api/bridge.yaml` where it has them and otherwise built from its schemas and valid against them;
  required and optional features and how Muster behaves without each (no buttons: the commands line and Text commands;
  no direct messages: tokens only); rendering the envelope: the CommonMark Muster writes, Mention markers, colours and
  emoji, Quiet messages; `Idempotency-Key`, why a bridge must keep keys for 24 hours and keep them per token;
  `thread_ref`; confirming presses at once and answering later through `answerEvent`; the error classes with an example
  mapping for a fictional messenger, who decides `gone`, why a missing channel is `forbidden`, how Muster reads an
  answer without a `class` (and never as `gone`), the `edit_window_closed` reason and what Muster does past an edit
  window; safe truncation; polling with cursors and pushing with Standard Webhooks signatures, with a verification
  snippet; testing with `muster bridge check`, the fake bridge of `muster dev` and the example bridge; running a bridge
  — its token and push secret, logs without secrets, monitoring through the Destination's health; the licence — the
  protocol, the libraries and the example are MIT, a bridge may stay closed, the libraries take contributions without a
  CLA, and contributions to `api/bridge.yaml` in Muster's repository fall under the CLA.
- **Reference** (`Makefile`, `.gitignore`, `internal/tools/refgen`): `make docs` builds
  `docs/reference/bridge/spec/index.html` from `api/bridge.yaml` with the same Redocly command as the API reference and
  the same rewrite that `docs-api` applies since the documentation site stopped loading third-party scripts: Redoc from
  the vendored copy at `../../../assets/vendor/redoc/redoc.standalone.js`, which holds because the page lies as deep as
  `docs/reference/api/spec/index.html`, and the Content-Security-Policy that keeps images on the site, so the check of
  `make docs` that fails on a page loading a script or a stylesheet from another host passes;
  `/docs/reference/bridge/spec/` joins the ignored paths of `.gitignore` beside the API reference's. `make docs` fails
  when the rendered file is missing afterwards, so a skipped render cannot publish a page that links nowhere; `refgen`
  writes `docs/reference/bridge-protocol.md` — the protocol version, the link to the rendered reference and to the raw
  `api/bridge.yaml` on `master`, since no release tag exists before L1 is released — and lists it in the Reference
  index; `generate-check` covers the page.
- **Navigation** (`mkdocs.yml`): Messengers gains Bridge (from S-074) and Write a bridge; Reference gains Bridge
  protocol.
- **Protocol compatibility** (`.github/workflows/ci.yml`, ADR-0017): the `oasdiff` job gains a blocking step that
  runs `oasdiff breaking --fail-on ERR --flatten-allof` in both directions, from `origin/<base branch>:api/bridge.yaml`
  to `api/bridge.yaml` and back, each report under its own heading in the step summary. The bridge serves the
  operations Muster calls, so a change is checked as Muster the client sees it and as an existing bridge sees it; an
  operation added shows as removed in the reverse direction and passes there, since it is additive (Muster calls it
  only when the bridge's `info` declares it). `--flatten-allof` makes a new required member of an event fail, which
  oasdiff otherwise reports only as a warning. The report on `api/openapi.yaml` stays non-blocking (ADR-0008). From
  this story on, the protocol is published and frozen: a breaking change is a new major path, so the step needs no
  override. Before it, the Bridge stories could still correct the protocol they implemented for the first time.

## Steps

1. Render the reference and write its page. Check: `refgen_test.go`, `make docs`.
2. Write Write a bridge, then link it from the home page and Messengers → Bridge. Check: Verification.
3. Add the blocking `oasdiff` step. Check: a scratch branch that removes a required answer field of
   `api/bridge.yaml`, adds one or makes a required request field optional fails the step; one that adds an optional
   field or an operation passes it, and so does this branch.

## Verification

```sh
make docs-check                                                       # passes: no stale page, no broken link or anchor
ls site/reference/bridge/spec/index.html site/messengers/write-a-bridge/index.html site/reference/bridge-protocol/index.html
for t in "getInfo" "checkChannel" "Idempotency-Key" "thread_ref" "answerEvent" "3 seconds" "forbidden" "edit_window_closed" \
  "max_body_length" "webhook-signature" "muster bridge check" "example bridge" "MIT" "muster-mention:" \
  "go get github.com/muster-io/muster-bridge-go" "bridge.Messenger" "ghcr.io/muster-io/muster-example-bridge"; do
  grep -q -- "$t" docs/messengers/write-a-bridge.md || echo "missing: $t"; done                         # (nothing)
grep -c '```mermaid' docs/messengers/write-a-bridge.md                # 5
grep -c 'assets/vendor/redoc/redoc.standalone.js' site/reference/bridge/spec/index.html   # 1
git check-ignore -q docs/reference/bridge/spec/index.html && echo ignored              # ignored
```

Each of the five diagrams renders as an `svg` in a browser with the vendored Mermaid: Playwright opens
`site/messengers/write-a-bridge/index.html` served from `site/`. Material renders each diagram in a closed shadow
root, so the check forces `attachShadow` open before the page loads, then expects five `svg` elements in the diagrams,
no Mermaid error element, and no request to a host other than the site and `api.github.com` (the theme's repository
widget, which every page loads).

The blocking `oasdiff` step is checked on the pull request itself, which passes it, and by running its script,
extracted from `ci.yml`, against scratch edits of `api/bridge.yaml`, one at a time: removing a required answer field,
adding one, making a required request field optional, adding a required member to an event, adding a request enum
value or removing an operation fails it, under the heading of the direction that catches the change; adding an
optional field or an operation passes it.

## Open questions

None.

## Notes

- Suggested commit: `docs: add the write a bridge guide and the bridge protocol reference`.
- The page names no real messenger as the target of an example; its error mapping uses a fictional one, so that the
  guide does not age with any vendor's API.
- The page leads with the libraries (D343): most bridge authors should not write idempotency, the event queue and
  signed pushes again. The Go library and the example exist when this story starts (S-083, S-085); the Python library
  (S-084) depends on S-078 and S-083, not on this story, and is built beside it (D369); S-060, which follows both, adds
  its section. Neither library is released before L1 is complete (D366), hence `go get` without a version and the
  `edge` image of the example.
- The blocking compatibility check of `api/bridge.yaml` moved here from S-073 after the second plan review of the Bridge
  phase (D335): the protocol is frozen once it is published, not while the Bridge stories implement it for the first
  time, when a defect they find would otherwise need a new major path although nothing is released.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-20 | partial | the published reference and the compatibility check; the spec's generated models and copied schemas are S-073 |
| C-22.FR-23 | partial | Write a bridge with the Go library; its Python section is S-060, Messengers → Bridge S-074 and S-081, Account links S-076 |
| C-22.AC-24 | full | |
