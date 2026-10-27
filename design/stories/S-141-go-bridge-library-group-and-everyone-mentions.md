---
id: S-141
title: Group mentions and mentions of everyone in the chat in the Go bridge library (BE)
capability: C-28
kind: be
layer: L2
repository: muster-io/muster-bridge-go
depends_on: [S-171, S-127]
covers: [C-28.FR-2, C-25.FR-8]
files_touched:
  - api/bridge.yaml
  - bridge.go
  - bridge_test.go
  - mention.go
  - mention_test.go
  - doc.go
  - README.md
  - spec_test.go
  - server_test.go
  - example_test.go
acceptance:
  - "[C-28.FR-2] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `models.gen.go` current with `Capabilities.GroupMentions`, `Capabilities.EveryoneMentions` and `Mention.Kind`; a `Mention` without `kind` reads as `user`."
  - "[C-28.FR-2, C-25.FR-8] `bridge.Config` takes `GroupMentions` and `EveryoneMentions`, which `info` declares; `bridge.New` refuses a name of `EveryoneMentions` outside `channel`, `here`, `all` and `everyone`, or a repeated one, naming the reason (`bridge_test.go`)."
  - "[C-28.FR-2] `ReplaceMentions` hands the render function each entry with its kind, so that a bridge renders a group as its messenger's group mention and `channel` as its mention of everyone, and a user as before; a body whose `group` or `everyone` entry arrives at a bridge that declared none is refused as `invalid` before the messenger is called (`mention_test.go`, `server_test.go`)."
  - "[C-28.FR-2] `Example_groupMentions` in `example_test.go` declares group mentions and `here`, and renders a body with a user, a group and `here`; its output is checked; README.md and `doc.go` describe the declaration and the kinds."
  - "[C-28.FR-2] `make ci conformance` passes, the conformance check from `ghcr.io/muster-io/muster:edge` included, and the release pull request that release-please opens names the change in its changelog."
verify: "make ci conformance"
operator_attention: true
issue: 430
---

# S-141. Group mentions and mentions of everyone in the chat in the Go bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with the `info` fields of S-139 and the `kind` of a Mention of S-171, the
  generated types, a way to declare them in the configuration, the kind in `ReplaceMentions`, and their documentation
  (ADR-0017: a bridge may declare group mentions and the mentions of everyone in the chat in `info`).

**OUT**

- Muster's side: S-139 (`info`) and S-171 (`Mention.kind`). The Python library: S-142. The documentation for bridge
  authors on Muster's site: S-144.
- The role `gateway`: S-146.

## Contracts

- **Repository** `muster-io/muster-bridge-go` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request says
  `Part of muster-io/muster#<issue>` and `Story: S-141`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master` after S-171, which also carries S-126's
  `layout` that S-127 took; `models.gen.go` is regenerated (never listed).
- **Package**: `Config.GroupMentions bool` and `Config.EveryoneMentions []string` go into `info.capabilities`;
  `bridge.New` validates them as the specification does; `ReplaceMentions(body, mentions, render)` passes the entry with
  its `Kind` to `render` (a `user` by default); the handler refuses a `group` or `everyone` entry that the bridge did
  not declare as `invalid`.
- **Tests**: `spec_test.go` validates a request with each kind and an `info` with both fields against the copy;
  `bridge_test.go`, `mention_test.go`, `server_test.go`; `example_test.go` is the runnable example.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Add the configuration, the kinds and the refusal, with their tests and the example; update README.md and `doc.go`.
   Check: `make ci`.
3. Run the conformance check. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes; `go test -run Example_groupMentions -v` prints a body with the user's,
the group's and `here`'s renderings. Against Muster: with `make dev` running from Muster's `master`, a bridge built on
this branch from `examples/minimal` with `GroupMentions` set is checked from the Connection page, which lists group
mentions among its features, and a Team mention can then be saved for it.

## Open questions

None.

## Notes

- Suggested commit: `feat: declare group mentions and mentions of everyone, and pass the kind of a mention`.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version; an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-139's merge until this story merges; that failure is expected,
  and this story waits for S-171 so that it takes both changes of `api/bridge.yaml` at once.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-2 | partial | the Go library; Muster's side is S-139 and S-171 |
| C-25.FR-8 | partial | a bridge built on the library can declare the group mentions that Team mentions need |
