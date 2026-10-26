---
id: S-142
title: Group mentions and mentions of everyone in the chat in the Python bridge library (BE)
capability: C-28
kind: be
layer: L2
repository: muster-io/muster-bridge-python
depends_on: [S-140, S-128]
covers: [C-28.FR-2, C-25.FR-8]
files_touched:
  - api/bridge.yaml
  - src/muster_bridge/__init__.py
  - src/muster_bridge/config.py
  - src/muster_bridge/mentions.py
  - src/muster_bridge/app.py
  - README.md
  - tests/test_config.py
  - tests/test_mentions.py
  - tests/test_app.py
  - tests/test_spec.py
acceptance:
  - "[C-28.FR-2] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `src/muster_bridge/models.py` current with `Capabilities.group_mentions`, `Capabilities.everyone_mentions` and `Mention.kind`; a `Mention` without `kind` reads as `user`."
  - "[C-28.FR-2, C-25.FR-8] The configuration takes `group_mentions` and `everyone_mentions`, which `info` declares; a name outside `channel`, `here`, `all` and `everyone`, or a repeated one, raises `ConfigError` naming the reason (`test_config.py`)."
  - "[C-28.FR-2] `replace_mentions` hands the render function each entry with its kind, and a body whose `group` or `everyone` entry arrives at a bridge that declared none is refused as `invalid` before the messenger is called (`test_mentions.py`, `test_app.py`)."
  - "[C-28.FR-2] README.md shows a bridge that declares group mentions and `here` and renders a user, a group and `here`, and `tests/test_mentions.py` runs that example."
  - "[C-28.FR-2] `make ci conformance` passes, the conformance check from `ghcr.io/muster-io/muster:edge` included."
verify: "make ci conformance"
operator_attention: true
issue: null
---

# S-142. Group mentions and mentions of everyone in the chat in the Python bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with the `info` fields of S-139 and the `kind` of a Mention of S-140, the
  generated models, their configuration, the kind in `replace_mentions`, and their documentation (ADR-0017).

**OUT**

- Muster's side: S-139 and S-140. The Go library: S-141. The documentation on Muster's site: S-144. The role `gateway`:
  S-147.

## Contracts

- **Repository** `muster-io/muster-bridge-python` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request
  says `Part of muster-io/muster#<issue>` and `Story: S-142`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master` after S-140, which also carries the
  `layout` that S-128 took; `src/muster_bridge/models.py` is regenerated (never listed).
- **Package**: `Config.group_mentions` and `Config.everyone_mentions` go into `info.capabilities` and are validated as
  the specification does (`config.py`); `replace_mentions(body, mentions, render)` passes the entry with its `kind`
  (`mentions.py`); the application refuses an undeclared `group` or `everyone` entry as `invalid` (`app.py`);
  `muster_bridge` re-exports the new names (`__init__.py`).
- **Tests**: `test_spec.py` validates a request with each kind and an `info` with both fields against the copy with
  openapi-core; `test_config.py`, `test_mentions.py`, `test_app.py`.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Add the configuration, the kinds and the refusal, with their tests; update README.md. Check: `make ci`.
3. Run the conformance check. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes; `pytest tests/test_mentions.py -v` runs the README's example. Against
Muster: with `make dev` running from Muster's `master`, `examples/memory_bridge.py` started with group mentions
declared is checked from the Connection page, which lists group mentions among its features.

## Open questions

None.

## Notes

- Suggested commit: `feat: declare group mentions and mentions of everyone, and pass the kind of a mention`.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version to PyPI; an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-139's merge until this story merges; that failure is expected.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-2 | partial | the Python library; Muster's side is S-139 and S-140 |
| C-25.FR-8 | partial | a bridge built on the library can declare the group mentions that Team mentions need |
