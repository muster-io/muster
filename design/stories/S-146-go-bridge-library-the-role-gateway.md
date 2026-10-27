---
id: S-146
title: The role gateway in the Go bridge library (BE)
capability: C-30
kind: be
layer: L2
repository: muster-io/muster-bridge-go
depends_on: [S-145, S-141]
covers: [C-30.FR-2]
files_touched:
  - api/bridge.yaml
  - bridge.go
  - bridge_test.go
  - gateway.go
  - gateway_test.go
  - server.go
  - server_test.go
  - events.go
  - events_test.go
  - spec_test.go
  - example_test.go
  - doc.go
  - README.md
  - examples/gateway/main.go
  - Makefile
acceptance:
  - "[C-30.FR-2] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `models.gen.go` current with `Info.Roles`, `NotificationInput`, `NotificationAction`, `NotificationSent` and `NotificationAcknowledgedEvent`."
  - "[C-30.FR-2] `bridge.Config` takes `Roles` (`messenger` by default) and a `Gateway`, which `sendNotification` calls with the bot of the token; `bridge.New` refuses `gateway` without a `Gateway`, `messenger` without a `Messenger`, and accepts a nil `Messenger` for a bridge whose only role is `gateway`, naming the reason; `info` declares the roles (`bridge_test.go`)."
  - "[C-30.FR-2] `sendNotification` keeps the idempotency of the other calls — a repeated key answers the first `notification_id` with `replayed` true without calling the `Gateway` — refuses `action.data` over 64 bytes, a text over 1,000 code points and a call on a bridge without the role as `invalid` before the `Gateway` is called, and keeps each notification's `action` for the bot (`gateway_test.go`, `server_test.go`)."
  - "[C-30.FR-2] `Bridge.Acknowledge(bot, notificationID)` queues `notification_acknowledged` with the `data` of that notification, delivered by polling and by push like the other events; an id the library did not issue, or a notification without `action`, is refused; `answerEvent` for that event reaches `Gateway.Answer` while the push mode delivers it (`events_test.go`)."
  - "[C-30.FR-2] `examples/gateway` is a gateway bridge of one file that prints each notification and acknowledges it from a `/demo/acknowledge` page; `Example_gateway` is checked; README.md and `doc.go` describe the role, the answer during a call in the push mode and the example; `make conformance` runs `muster bridge check` from `ghcr.io/muster-io/muster:edge` against it with `--contact` and passes interactively."
verify: "make ci conformance"
operator_attention: true
issue: 435
---

# S-146. The role gateway in the Go bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with the role, `sendNotification` and `notification_acknowledged` of S-145,
  the generated types, the configuration of the role, the call, the event and the answer, a minimal gateway example and
  their documentation (C-30.FR-2: "the bridge libraries for Go and Python support the role in releases of their own").

**OUT**

- Muster's side: S-145. The Python library: S-147. The page on writing a gateway bridge on Muster's site: S-151.

## Contracts

- **Repository** `muster-io/muster-bridge-go` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request says
  `Part of muster-io/muster#<issue>` and `Story: S-146`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master` after S-145, which also carries the
  changes S-141 took; `models.gen.go` is regenerated (never listed).
- **Package**: `Config.Roles []Role` (`RoleMessenger`, `RoleGateway`) and `Config.Gateway`; the interface `Gateway`
  {`SendNotification(ctx, Bot, NotificationInput) (NotificationSent, error)`, `Answer(ctx, Bot, Event, text) (ShownAs,
  error)`} in `gateway.go`; `Bridge.Acknowledge(bot, notificationID)`; the server routes `sendNotification` and the
  answers of `notification_acknowledged` (`server.go`, `events.go`); the Store keeps a notification's action with its
  key for 24 hours, as it keeps answers.
- **Example** (`examples/gateway/main.go`): an in-memory gateway that prints each notification and offers a page to
  acknowledge it; `make conformance` gains a run of it with `--contact`.
- **Tests**: `spec_test.go` validates `sendNotification`, its problems and the event against the copy; `bridge_test.go`,
  `gateway_test.go`, `server_test.go`, `events_test.go`; `example_test.go` holds `Example_gateway`.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Add the role, the interface, the call, the event and the answer, with their tests. Check: `make ci`.
3. Write the example and its conformance run; update README.md and `doc.go`. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes, the gateway example included. Against Muster, with what S-145
provides: `muster bridge check` from Muster's `master` against `examples/gateway` with `--contact +10000000001` passes
every gateway step interactively, the acknowledgement made from the example's page; with `make dev` running from
`master`, the example added as a Bridge Connection in poll mode reads `roles` `["gateway"]` through `getConnection`, and
a Destination on it is refused with `gateway_only`.

## Open questions

None.

## Notes

- Suggested commit: `feat: support the gateway role with sendNotification and notification_acknowledged`.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version; an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-145's merge until this story merges; that failure is expected.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-2 | partial | the Go library; Muster's side is S-145 and S-148, the Python library S-147 |
