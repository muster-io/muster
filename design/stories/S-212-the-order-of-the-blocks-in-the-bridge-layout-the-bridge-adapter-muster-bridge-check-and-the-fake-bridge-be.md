---
id: S-212
title: The order of the blocks in the Bridge layout, the Bridge adapter, muster bridge check and the fake bridge (BE)
capability: C-38
kind: be
layer: L2
depends_on: [S-211, S-126, S-171]
covers: [C-38.FR-6, C-38.AC-2, C-38.AC-11]
files_touched:
  - api/bridge.yaml
  - api/openapi.yaml
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/bridge/conformance.go
  - internal/bridge/conformance_test.go
  - internal/bridge/testmsg.go
  - internal/bridge/testmsg_test.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - test/e2e/bridge_layout_order_test.go
acceptance:
  - "[C-38.FR-6] `api/bridge.yaml` gives `Layout` the optional `order` — the names of the four movable blocks, `fields`, `text`, `labels` and `alerts`, in the layout's order, hidden ones included — additively in version 1 of the Bridge protocol, and says that a bridge rendering natively follows it and one that ignores it keeps its own order; `make lint` passes Redocly and oasdiff reports no breaking change; `api/openapi.yaml`'s `MessageLayout` carries the same `order` (`make generate-check`, `make lint`)."
  - "[C-38.FR-6, C-38.AC-2] For the layout of C-38.AC-2 the fake bridge receives, beside the body in the layout's order, a `layout` with four `fields` in their order, an empty `description` and `order` `fields`, `text`, `alerts`, `labels`; a hidden block is empty in it — an empty list, an empty text or a null `alerts` — and a field of the catalogue is one more entry of `fields` (`envelope_test.go`, `test/e2e/bridge_layout_order_test.go`)."
  - "[C-38.AC-11] For a layout with the Alert list before the fields and the key labels hidden the fake bridge receives a `layout` whose `order` is `alerts`, `fields`, `text`, `labels` and whose `labels` is empty, and `muster bridge check` accepts it against a bridge that ignores `order` and one that renders it (`envelope_test.go`, `conformance_test.go`, `fakebridge_test.go`, `test/e2e/bridge_layout_order_test.go`)."
  - "[C-38.FR-6] When the body must be cut below `limits.max_body_length`, `layout` loses the same parts in the same order and keeps `order` and its fields, so that the two never disagree; a Root message that a template renders, and the Fallback, carry no `layout` and so no `order` (`delivery_test.go`, `envelope_test.go`)."
  - "[C-38.FR-6] The fake bridge records `order` with every message and edit in `/_fake/messages`, and a test message carries it too (`fakebridge_test.go`, `testmsg_test.go`)."
verify: "make ci e2e-go"
operator_attention: false
issue: null
---

# S-212. The order of the blocks in the Bridge layout, the Bridge adapter, muster bridge check and the fake bridge (BE)

## Scope

**IN**

- `order` in the `layout` of the Bridge protocol (and in the same `MessageLayout` of the API), additively in version 1
  (C-38.FR-6, ADR-0017).
- The Bridge adapter filling it, with hidden blocks empty and the catalogue's fields as entries of `fields`.
- `muster bridge check` accepting it, and the fake bridge recording it.

**OUT**

- The renderer, the shortening and the three other adapters: S-211, which also renders the CommonMark body in the
  layout's order.
- The types in the libraries for Go and Python: S-213, S-214. The guide for bridge authors: S-219.
- The preview's Bridge tab: S-215, S-218.

## Contracts

- **Spec** (`api/bridge.yaml`, changed first, then generated into `internal/bridge/gen/`): `Layout` gains the optional
  `order`, an array of the enum `fields`, `text`, `labels`, `alerts`, each once, all four present when it is present.
  The description says: a hidden block keeps its place in `order` and is empty in `layout`; a bridge that renders
  natively follows `order`, one that ignores it keeps its own order; the title with the status, the notices, the people,
  the buttons and the link to Muster are not in it, since they never move (C-38.FR-2). Protocol version 1 is unchanged,
  as with `layout` itself in S-126. `api/openapi.yaml`'s `MessageLayout` — the same content the preview returns — gets
  the same property (see Open questions).
- **Package** (`internal/messages/layout.go`): `MessageLayout` gains `Order []string`, filled by `LayoutOf` from the
  `Message`'s order (S-211) and left empty when `HasLayout` is false.
- **Bridge adapter** (`internal/bridge/envelope.go`, `delivery.go`, `testmsg.go`): `layoutOf` copies `Order`, puts the
  catalogue's fields into `fields` in their order, and when `Shorten` cuts a block clears the same block's part of the
  `layout`; `conformance.go` sends the new field in its message steps and fails only on a bridge that rejects an
  additive property, which version 1 forbids.
- **Fake bridge** (`internal/fakes/fakebridge/messages.go`): records `order` beside `layout` in `/_fake/messages`.
- **Libraries**: the types in `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python` are S-213 and S-214;
  their copy check against Muster's `api/bridge.yaml` fails on their `master` from this story's merge until they merge,
  which is expected (S-127 says the same of `layout`).
- **Registries**: no new log event, metric or Internal alert.

## Steps

1. Add `order` to the two specifications and regenerate. Check: `make generate-check lint` (Redocly and oasdiff: no
   breaking change).
2. Fill it in `LayoutOf` and `layoutOf`, with the hidden blocks empty, and keep it in step with the shortening. Check:
   `layout_test.go`, `envelope_test.go`, `delivery_test.go`.
3. Teach `conformance.go`, `testmsg.go` and the fake bridge. Check: `conformance_test.go`, `testmsg_test.go`,
   `fakebridge_test.go`.
4. Write `test/e2e/bridge_layout_order_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-126: the Connection "Example Bridge" (poll mode), its Destination BD on the Route R of S-211, the alert group c1 of three Alerts,
# two firing; FB=127.0.0.1:18094/_fake
# an item of /_fake/messages is {envelope, edits, replies, …}: a Root message edited after the save shows its layout in the
# last edit (the whole envelope Muster sent), an unedited one in the envelope; LY is the layout of the newest message
LY='[.[] | select((.edits[-1] // .envelope).layout != null)] | last | (.edits[-1] // .envelope).layout'
# C-38.AC-2: the layout of S-211 ($L) saved on R, then
curl -s $FB/messages | jq -c "$LY | {order: .order, f: [.fields[] | .name], d: .description}"
# {"order":["fields","text","alerts","labels"],"f":["Severity","Started","Firing / total","cluster"],"d":""}
# C-38.AC-11: the Alert list first, the key labels hidden
curl -s $FB/messages | jq -c "$LY | {order: .order, labels: .labels}"
# {"order":["alerts","fields","text","labels"],"labels":[]}
./bin/muster dev bridge check --url http://127.0.0.1:18094 --token muster-dev-bridge-token | tail -3   # every step passes, the message steps included
# a Route with a policy.templates.root_message: its Alert Group "TemplatedAlert" has no layout, so no order
curl -s $FB/messages | jq -c '[.[] | select(.envelope.title | test("TemplatedAlert"))] | last | (.edits[-1] // .envelope).layout'   # null
# a body limit of 1000 (/_fake/config) with a long description: the layout's description is cut with the body's, order unchanged
```

`test/e2e/bridge_layout_order_test.go` repeats these, adds a bridge that ignores `order` (the fake with a flag) and
`muster bridge check` against both.

## Open questions

- **`order` in the API's `MessageLayout`.** C-38 names `order` only in the `layout` of the `MessageInput` of
  `api/bridge.yaml`. The preview's Bridge tab (C-38.FR-12) shows the `layout` the bridge would receive, which is
  `MessageLayout` of `api/openapi.yaml` (S-126), so this story adds `order` there too; if the maintainer would rather
  keep the two apart, S-215 returns it beside `layout` instead.

## Notes

- Suggested commit: `feat(bridge): add the order of the message blocks to the layout of the bridge protocol`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- ADR-0017 allows additive properties in version 1, so no new version of the protocol and no new `info` capability is
  needed; a bridge that never reads `order` is unaffected, as one that never read `layout` was.
- The libraries release their own `0.x` versions with the types (S-213, S-214); the maintainer merges their release
  pull requests.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-6 | partial | the Bridge adapter and the protocol's `order`; Mattermost, Slack and Telegram are S-211, the libraries S-213, S-214, the facts S-209 |
| C-38.AC-2 | partial | the fake bridge's `layout`; Mattermost, Slack and Telegram are S-211 |
| C-38.AC-11 | partial | the `order` and `muster bridge check`; the three values and the refusals are S-211, S-210 |
