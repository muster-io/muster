---
id: S-214
title: The order of the message blocks in the Python bridge library (BE)
capability: C-38
kind: be
layer: L2
repository: muster-io/muster-bridge-python
depends_on: [S-212, S-128]
covers: [C-38.FR-6]
files_touched:
  - api/bridge.yaml
  - src/muster_bridge/__init__.py
  - README.md
  - tests/test_spec.py
  - tests/test_app.py
  - tests/test_layout.py
acceptance:
  - "[C-38.FR-6] `api/bridge.yaml` is Muster's copy from `master` byte for byte, the copy check passes, and the generated models are current with `Layout.order`, which is `None` when Muster sends none."
  - "[C-38.FR-6] `post_message` and `edit_message` of a messenger receive the `order` that Muster sent, unchanged, and a request without it reaches them with it `None`; a request whose `order` names a block other than `fields`, `text`, `labels` or `alerts` is refused as `invalid` before the messenger is called (`tests/test_app.py`, `tests/test_spec.py`)."
  - "[C-38.FR-6] `tests/test_layout.py` renders the blocks of a `layout` in the order of `order`, skipping an empty one, and falls back to its own order when `order` is `None`; its output is checked, and README.md says that a bridge that renders natively follows `order` and one that ignores it keeps its own order."
  - "[C-38.FR-6] The library's CI, the conformance check from `ghcr.io/muster-io/muster:edge` included, passes, and the release pull request that release-please opens names the property in its changelog."
verify: "make ci conformance"
operator_attention: true
issue: null
---

# S-214. The order of the message blocks in the Python bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with `order` (S-212), the regenerated models, and the documentation of how a
  bridge reads it (C-38.FR-6).

**OUT**

- Muster's side: S-212. The Go library: S-213. The guide on Muster's documentation site: S-219.

## Contracts

- **Repository** `muster-io/muster-bridge-python` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request
  says `Part of muster-io/muster#<issue>` and `Story: S-214`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master`; the generated models are regenerated
  (never listed); `src/muster_bridge/__init__.py` keeps re-exporting `Layout`, whose `order` is the new field.
- **Tests**: `test_spec.py` validates a request with `order` against the copy and refuses an unknown block;
  `test_app.py` checks that the messenger receives it; `test_layout.py` is the rendering example.

## Steps

1. Take the copy and regenerate. Check: the repository's copy check and generation check.
2. Write the tests, the example and the README text. Check: `make ci`.
3. Run the conformance check. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes; `python -c "from muster_bridge import Layout;
print(Layout.model_fields['order'])"`
(or the generated model's equivalent) shows the field. Against Muster: with `make dev` running from Muster's `master`,
a bridge built on this branch with the library's minimal example logs `order` of a Root message of a Route whose layout
was saved with the Alert list first.

## Open questions

None.

## Notes

- Suggested commit: `feat: add the order of the message blocks to the layout models`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version with the property (ADR-0017); an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-212's merge until this story merges; that failure is expected.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-6 | partial | the Python library; Muster's side is S-211, S-212, Go S-213 |
