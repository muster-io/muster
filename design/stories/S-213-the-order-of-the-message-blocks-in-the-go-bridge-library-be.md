---
id: S-213
title: The order of the message blocks in the Go bridge library (BE)
capability: C-38
kind: be
layer: L2
repository: muster-io/muster-bridge-go
depends_on: [S-212, S-127]
covers: [C-38.FR-6]
files_touched:
  - api/bridge.yaml
  - bridge.go
  - doc.go
  - README.md
  - spec_test.go
  - server_test.go
  - example_test.go
acceptance:
  - "[C-38.FR-6] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `models.gen.go` current with `Layout.Order` and its element type; `Layout.Order` is nil when Muster sends none."
  - "[C-38.FR-6] `PostMessage` and `EditMessage` of a `Messenger` receive the `order` that Muster sent, unchanged, and a request without it reaches them with it nil; a request whose `order` names a block other than `fields`, `text`, `labels` or `alerts` is refused as `invalid` before the messenger is called (`server_test.go`, `spec_test.go`)."
  - "[C-38.FR-6] `Example_layout` in `example_test.go` renders the blocks of a `layout` in the order of `order`, skipping an empty one, and falls back to its own order when `order` is nil; its output is checked, and README.md and `doc.go` say that a bridge that renders natively follows `order` and one that ignores it keeps its own order."
  - "[C-38.FR-6] `make ci conformance` passes, the conformance check from `ghcr.io/muster-io/muster:edge` included, and the release pull request that release-please opens names the property in its changelog."
verify: "make ci conformance"
operator_attention: true
issue: null
---

# S-213. The order of the message blocks in the Go bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with `order` (S-212), the regenerated types, and the documentation of how a
  bridge reads it (C-38.FR-6: "the bridge libraries for Go and Python get it in releases of their own").

**OUT**

- Muster's side: S-212. The Python library: S-214. The guide on Muster's documentation site: S-219.

## Contracts

- **Repository** `muster-io/muster-bridge-go` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request says
  `Part of muster-io/muster#<issue>` and `Story: S-213`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master`; `models.gen.go` is regenerated (never
  listed).
- **Package** (`bridge.go`, `doc.go`): no new call; `Layout` carries `Order` from the generated models, and the package
  documentation names it, with the rule that a hidden block is empty in `layout` and keeps its place in `order`.
- **Tests**: `spec_test.go` validates a request with `order` against the copy and refuses an unknown block;
  `server_test.go` checks that the messenger receives it; `example_test.go` is the runnable example.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Write the tests and the example, README.md and `doc.go`. Check: `make ci`.
3. Run the conformance check. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes; `go doc github.com/muster-io/muster-bridge-go Layout` shows `Order`;
`go test -run Example_layout -v` prints the example's output. Against Muster: with `make dev` running from Muster's
`master`, a bridge built on this branch with `examples/minimal` logs `order` of a Root message of a Route whose layout
was saved with the Alert list first (`alerts fields text labels`).

## Open questions

None.

## Notes

- Suggested commit: `feat: add the order of the message blocks to the layout types`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version with the property (ADR-0017); an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-212's merge until this story merges; that failure is expected.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-6 | partial | the Go library; Muster's side is S-211, S-212, Python S-214 |
