---
id: S-127
title: Types for the message layout in the Go bridge library (BE)
capability: C-29
kind: be
layer: L2
repository: muster-io/muster-bridge-go
depends_on: [S-126]
covers: [C-29.FR-8]
files_touched:
  - api/bridge.yaml
  - bridge.go
  - doc.go
  - README.md
  - spec_test.go
  - server_test.go
  - example_test.go
acceptance:
  - "[C-29.FR-8] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `models.gen.go` current with the types `Layout`, `LayoutStatus`, `LayoutField`, `LayoutLabel`, `LayoutAlerts`, `LayoutAlertValue`, `LayoutPerson` and `LayoutLink`; `MessageContent.Layout` is a pointer, nil when Muster sends none."
  - "[C-29.FR-8] `PostMessage` and `EditMessage` of a `Messenger` receive the `layout` that Muster sent, unchanged, and a request without `layout` reaches them with it nil; a request whose `layout` does not match the specification is refused as `invalid` before the messenger is called (`server_test.go`, `spec_test.go`)."
  - "[C-29.FR-8] `Example_layout` in `example_test.go` renders a message from `layout` — the number as a field of its own, the labels as key and value, the Alerts with \"and K more\" counted from `total`, the Owner by the `ref` of a Mention — and its output is checked; README.md and `doc.go` say that a bridge renders either `body` or `layout`."
  - "[C-29.FR-8] `make ci conformance` passes, the conformance check from `ghcr.io/muster-io/muster:edge` included, and the release pull request that release-please opens names the types in its changelog."
verify: "make ci conformance"
operator_attention: true
issue: 416
---

# S-127. Types for the message layout in the Go bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with `layout` (S-126), the generated types, and the documentation of how a
  bridge reads `layout` (C-29.FR-8: "the bridge libraries for Go and Python get types for `layout` in releases of their
  own").

**OUT**

- Muster's side of `layout`: S-126. The Python library: S-128. The guide on Muster's documentation site: S-130.
- Rendering `layout` in the example bridge (`examples/chat`): not required; a later change may show it.

## Contracts

- **Repository** `muster-io/muster-bridge-go` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request says
  `Part of muster-io/muster#<issue>` and `Story: S-127`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master`; `models.gen.go` is regenerated (never
  listed).
- **Package** (`bridge.go`, `doc.go`): no new call; `MessageInput` and `MessageContent` carry `Layout` from the
  generated models, and the package documentation names it with the rule that its values are plain text that the
  bridge escapes for its messenger, and that `assignees` and `owner` name people by the `ref` of the envelope's
  `mentions`, never by a marker in the text.
- **Tests**: `spec_test.go` validates a request and an answer with `layout` against the copy; `server_test.go` checks
  that the messenger receives it; `example_test.go` is the runnable example.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Write the tests and the example, README.md and `doc.go`. Check: `make ci`.
3. Run the conformance check. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes; `go doc github.com/muster-io/muster-bridge-go Layout` shows the type;
`go test -run Example_layout -v` prints the example's output. Against Muster: with `make dev` running from Muster's
`master`, a bridge built on this branch with `examples/minimal` receives `layout` in the envelope of a Root message —
the example's log shows the number and the title apart.

## Open questions

None.

## Notes

- Suggested commit: `feat: add types for the structured message layout`.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version with the types (ADR-0017: the libraries stay `0.x` and speak protocol version 1); an agent never
  merges a release pull request.
- The copy check fails on the library's `master` from S-126's merge until this story merges; that failure is expected
  and is what ADR-0017's copy check is for.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-29.FR-8 | partial | the Go library; Muster's side is S-126, Python S-128 |
