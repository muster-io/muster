---
id: S-216
title: The test message with the Route's layout or an unsaved one, built from any Alert Group the preview shows (BE)
capability: C-38
kind: be
layer: L2
depends_on: [S-211, S-215, S-047, S-113]
covers: [C-38.FR-7, C-38.FR-17, C-38.AC-9, C-38.AC-12]
files_touched:
  - api/openapi.yaml
  - internal/destinations/test.go
  - internal/destinations/test_test.go
  - internal/delivery/destinationtest.go
  - internal/delivery/destinationtest_test.go
  - internal/messages/default.go
  - internal/messages/destinationtest_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - test/e2e/layout_test_message_test.go
acceptance:
  - "[C-38.FR-7, C-38.AC-9] A test message (C-16.FR-1) of a Destination of the Route, built from an Alert Group of the Route, shows the Route's own layout; one built from the built-in example shows Muster's default, whatever layout the Default route saved (`destinationtest_test.go`, `test_test.go`, `test/e2e/layout_test_message_test.go`)."
  - "[C-38.FR-17, C-38.AC-12] `testDestination` takes an optional unsaved `message_layout` with the `route_id` of the Route it belongs to and delivers to the fake Mattermost server one \"🧪 Test message\" with that layout, built from the `alert_group_id` of the source — of any Route, not only of the Destination's — with no Mentions, no change of any Desired state and no edit of the Route's open Root messages (`destinations_test.go`, `test/e2e/layout_test_message_test.go`)."
  - "[C-38.FR-17] The same request to a Destination of each messenger — Mattermost, Slack, Telegram, a Bridge — sends one test message in that messenger with the layout, and for an outgoing webhook the layout is ignored and the existing steps run; a request with `message_layout` and no `route_id` gets `422` `required`, and one with an invalid layout the `422` codes of S-210 at `/message_layout/...` (`destinations_test.go`, `test/e2e/layout_test_message_test.go`)."
  - "[C-38.FR-17, C-38.AC-12] The operation needs `destinations:test` as it does: a Responder's token gets `403` and sends nothing; the Audit log row `destination.tested` names the source and, when an unsaved layout was used, `message_layout` as given without its content being stored anywhere else (`destinations_test.go`)."
verify: "make ci e2e-go"
operator_attention: false
issue: null
---

# S-216. The test message with the Route's layout or an unsaved one, built from any Alert Group the preview shows (BE)

## Scope

**IN**

- That a Destination test built from an Alert Group of the Route shows the Route's layout, and one from the example
  Muster's default (C-38.FR-7).
- `testDestination` with an unsaved layout and an Alert Group of any Route, for the editor's "Send test message"
  (C-38.FR-17), the revision of C-16.FR-1 that L2 §6 lists.

**OUT**

- The preview: S-215. The button in the editor: S-218.
- The test event and the "create" request of an outgoing webhook, which a layout does not touch.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `DestinationTestRequest` gains `message_layout`
  (`RouteMessageLayout`, S-210; null or absent for the saved layout) and `route_id` (`NullablePublicId`; required
  with `message_layout`; the Route whose language and Mention-free test it takes). `TestSource.alert_group_id`'s
  description drops "of one of the Destination's Routes" for a request with `message_layout`, which accepts any
  Alert Group of the Organization, as the preview does (S-215): C-16.FR-1 as revised in L2 §6 says "built from the
  Alert Group its preview shows, of whichever Route".
- **Package** (`internal/destinations/test.go`, `internal/delivery/destinationtest.go`): `Source` and `TestInput`
  carry the unsaved layout and the Route; the check of the Alert Group's Route is kept for a request without
  `message_layout` and lifted for one with it. The unsaved layout is validated by `internal/routing`'s `Validate`
  (S-210), not a second copy, and its pointers are prefixed `/message_layout`.
- **Rendering** (`internal/messages/default.go`): `TestRoot` takes the layout from `Source` — the Route's effective
  one for an Alert Group, the built-in default for the example, the request's for an unsaved one — so a test message
  and the preview of S-215 are the same render; its buttons still name the Destination and change nothing when pressed
  (C-16.FR-4, C-28.FR-9), and it mentions nobody (C-16.FR-1, ADR-0005).
- **Audit log**: `destination.tested` records the source and the layout's presence; the layout's content is the
  request's and is not stored.
- **Permissions**: `destinations:test`, unchanged. The preview needs `templates:preview` and the test
  `destinations:test`: an Admin has both; the editor offers the test only to who has it (S-218).

## Steps

1. Add the spec and regenerate. Check: `make generate-check lint`.
2. Carry the layout and the Route through `Source` and `TestInput`, and render with them. Check: `test_test.go`,
   `destinationtest_test.go`.
3. Wire the handler with the validation. Check: `destinations_test.go` (each messenger, the webhook, `422`, `403`).
4. Write `test/e2e/layout_test_message_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-215: A, R with the layout $L of S-211 saved, D (Mattermost) on R, Alert Group G of R, OTHER of another Route, FMM
API=localhost:8080/api/v1
t() { curl -s "${A[@]}" $API/destinations/$D/tests -d "$1" | jq -c '[.steps[] | {name, ok}]'; }
# C-38.AC-9: from an Alert Group of the Route, the Route's layout; from the example, the default
t "{\"source\":{\"kind\":\"alert_group\",\"alert_group_id\":\"$G\"}}"
curl -s $FMM/posts | jq -c '[.[] | select(.props.attachments[0].title | contains("Test message"))] | last | .props.attachments[0].fields | map(.title)'
# ["Severity","Started","Firing / total","cluster"]
t '{"source":{"kind":"example"}}'
curl -s $FMM/posts | jq -c '[.[] | select(.props.attachments[0].title | contains("Test message"))] | last | .props.attachments[0].fields | map(.title)'
# ["Severity","Route","Teams","Started"]
# C-38.AC-12: an unsaved layout and an Alert Group of another Route; the saved layout is untouched, the open Root messages are not edited
t "{\"source\":{\"kind\":\"alert_group\",\"alert_group_id\":\"$OTHER\"},\"route_id\":\"$R\",\"message_layout\":$UNSAVED}"
curl -s $FMM/posts | jq -c '[.[] | select(.props.attachments[0].title | contains("Test message"))] | length'     # one more than before; no @-mention in .message
curl -s "${A[@]}" $API/routes/$R | jq -c .message_layout.fields | head -c 120                                       # still the saved layout
curl -s $FMM/requests | jq '[.[] | select(.method=="PUT")] | length'                                                # no edit of a Root message
# refusals
t "{\"source\":{\"kind\":\"example\"},\"message_layout\":$UNSAVED}"   # 422 required at /route_id
curl -s -o /dev/null -w '%{http_code}\n' "${RESP[@]}" $API/destinations/$D/tests -d '{"source":{"kind":"example"}}'   # 403
```

`test/e2e/layout_test_message_test.go` repeats these for Mattermost, Telegram, Slack and the fake bridge, and checks that
an outgoing webhook Destination runs its steps as before with the layout in the request.

## Open questions

None.

## Notes

- Suggested commit: `feat(destinations): send a test message with the route's layout or an unsaved one from any alert group`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- "Mentions nobody" is already true of every test message; the story's tests assert it for the new path because the
  unsaved layout reaches the renderer by a new route.
- An Alert Group of another Route is rendered "as if this Route had taken it" exactly as in the preview, so the
  message the Admin sees in the messenger and the one in the preview agree.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-7 | partial | the test message's layout; Thread replies, Storm summaries and Mentions unchanged are S-211 |
| C-38.FR-17 | partial | the operation; the editor's button is S-218 |
| C-38.AC-9 | full | |
| C-38.AC-12 | partial | the test message and the `403`; the one save of the layout and the template's removal is S-210 |
