---
id: S-079
title: "\"Write a bridge\" and the Bridge protocol reference on the documentation site"
capability: C-22
kind: docs
layer: L1
depends_on: [S-078]
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
acceptance:
  - "[C-22.FR-23] Messengers → Write a bridge covers every topic C-22.FR-23 lists for it — the protocol step by step, required and optional features, the envelope and its CommonMark, Mention markers, idempotency keys, confirming presses within 3 seconds, `thread_ref`, error classes and who decides `gone`, safe truncation, both event delivery modes with their signatures, testing with `muster bridge check` and the fake bridge, the example bridge and the MIT licence — with a Mermaid diagram of a press from the messenger to the answer, checked against the list in Verification."
  - "[C-22.FR-20, C-22.AC-24] `make docs` renders `api/bridge.yaml` with Redocly into the site as Reference → Bridge protocol, beside the API reference, and fails when the rendered file is missing; `refgen` writes its page, which links to the rendered reference and to `api/bridge.yaml` on `master` (`refgen_test.go`); both pages and Write a bridge are in the navigation, and `make docs-check` passes with no broken link or anchor."
  - "[C-22.FR-23] The home page lists Bridges among the messengers, and Messengers → Bridge links to Write a bridge and to the protocol reference."
verify: "make ci docs-check"
operator_attention: false
issue: null
---

# S-079. "Write a bridge" and the Bridge protocol reference on the documentation site

## Scope

**IN**

- The page Messengers → Write a bridge, for people who write a bridge.
- The protocol reference rendered from `api/bridge.yaml`, its page in Reference, and the navigation.

**OUT**

- Messengers → Bridge, for Admins (S-074, with its edit windows from S-081), and the Bridge section of Account links
  (S-076); the remaining sections of the site (S-060).

## Contracts

- **Write a bridge** (`docs/messengers/write-a-bridge.md`), in English with Mermaid diagrams (C-21.FR-5): what a
  bridge is and when to write one; the protocol in the order a bridge author implements it — `getInfo`, `checkChannel`,
  `postMessage`, `editMessage`, `replyInThread`, events — with the request and answer of each, taken from the
  examples of `api/bridge.yaml`; required and optional features and how Muster behaves without each (no buttons: the
  commands line and Text commands; no direct messages: tokens only); rendering the envelope: the CommonMark Muster
  writes, Mention markers, colours and emoji, Quiet messages; `Idempotency-Key`, why a bridge must keep keys for 24
  hours and keep them per token; `thread_ref`; confirming presses at once and answering later through `answerEvent`;
  the error classes with an example mapping for a fictional messenger, who decides `gone`, why a missing channel is
  `forbidden`, how Muster reads an answer without a `class` (and never as `gone`), the `edit_window_closed` reason and
  what Muster does past an edit window; safe truncation; polling with cursors and
  pushing with Standard Webhooks signatures, with a verification snippet; testing with `muster bridge check`, the fake
  bridge of `muster dev` and the example bridge; running a bridge — its token and push secret, logs without secrets,
  monitoring through the Destination's health; the licence — the protocol and the example are MIT, a bridge may stay
  closed, and contributions to them fall under the CLA.
- **Reference** (`Makefile`, `internal/tools/refgen`): `make docs` builds `docs/reference/bridge/spec/index.html` from
  `api/bridge.yaml` with the same Redocly command as the API reference (ignored by git like it), and fails when that
  file is missing afterwards, so a skipped render cannot publish a page that links nowhere; `refgen` writes
  `docs/reference/bridge-protocol.md` — the protocol version, the link to the rendered reference and to the raw
  `api/bridge.yaml` on `master`, since no release tag exists before L1 is released — and lists it in the Reference
  index; `generate-check` covers the page.
- **Navigation** (`mkdocs.yml`): Messengers gains Bridge (from S-074) and Write a bridge; Reference gains Bridge
  protocol.

## Steps

1. Render the reference and write its page. Check: `refgen_test.go`, `make docs`.
2. Write Write a bridge, then link it from the home page and Messengers → Bridge. Check: Verification.

## Verification

```sh
make docs-check                                                       # passes: no stale page, no broken link or anchor
ls site/reference/bridge/spec/index.html site/messengers/write-a-bridge/index.html site/reference/bridge-protocol/index.html
for t in "getInfo" "checkChannel" "Idempotency-Key" "thread_ref" "answerEvent" "3 seconds" "forbidden" "edit_window_closed" \
  "max_body_length" "webhook-signature" "muster bridge check" "example bridge" "MIT" "muster-mention:"; do
  grep -q -- "$t" docs/messengers/write-a-bridge.md || echo "missing: $t"; done                         # (nothing)
grep -c '```mermaid' docs/messengers/write-a-bridge.md                # 1 or more
```

## Open questions

None.

## Notes

- Suggested commit: `docs: add the write a bridge guide and the bridge protocol reference`.
- The page names no real messenger as the target of an example; its error mapping uses a fictional one, so that the
  guide does not age with any vendor's API.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-20 | partial | the published reference; the spec and its generated models are S-073 |
| C-22.FR-23 | partial | Write a bridge; Messengers → Bridge is S-074 and S-081, Account links S-076 |
| C-22.AC-24 | full | |
