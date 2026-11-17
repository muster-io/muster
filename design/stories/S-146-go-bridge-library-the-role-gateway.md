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
  - gateway.go
  - gateway_test.go
  - server.go
  - events.go
  - spec_test.go
  - example_test.go
  - doc.go
  - README.md
  - AGENTS.md
  - .github/workflows/ci.yml
  - examples/gateway/main.go
  - Makefile
acceptance:
  - "[C-30.FR-2] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `models.gen.go` current with `Info.Roles`, `NotificationInput`, `NotificationAction`, `NotificationSent` and `NotificationAcknowledgedEvent`."
  - "[C-30.FR-2] `bridge.Config` takes `Roles` (`messenger` by default) and a `Gateway`, which `sendNotification` calls with the bot of the token; `bridge.New` refuses `gateway` without a `Gateway`, `messenger` without a `Messenger`, and accepts a nil `Messenger` for a bridge whose only role is `gateway`, naming the reason, and also refuses a `Gateway` without the role `gateway`, a `Messenger` without the role `messenger`, and messenger-only settings (capabilities, user lookup, mentions, link template) on a bridge without the role `messenger`; `info` declares the roles (`gateway_test.go`: `TestNewRoles`, `TestNewRefusesMessengerFeaturesOfAGateway`, `TestGatewayInfoDeclaresRoles`)."
  - "[C-30.FR-2] `sendNotification` keeps the idempotency of the other calls — a repeated key answers the first `notification_id` with `replayed` true without calling the `Gateway` — refuses `action.data` over 64 bytes and a text over 1,000 code points (both by the validation of the request against the specification) and a call on a bridge without the role as `invalid` before the `Gateway` is called, and keeps each notification's `action` for the bot (`gateway_test.go`, `spec_test.go`)."
  - "[C-30.FR-2] `Bridge.Acknowledge(ctx, bot, notificationID)` queues `notification_acknowledged` with the `data` of that notification, delivered by polling and by push like the other events; an id the library did not issue, or a notification without `action`, is refused; `answerEvent` for that event reaches `Gateway.Answer` while the push mode delivers it (`gateway_test.go`: `TestAcknowledgeQueuesTheEvent`, `TestAnswerReachesTheGatewayWhilePushDelivers`; `spec_test.go`: `TestPushesMatchSpec`)."
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
  {`SendNotification(ctx, bot string, NotificationInput) (NotificationSent, error)`, `Answer(ctx, bot string, Event,
  text) (ShownAs, error)`} in `gateway.go`, which take the bot as its name, as `Messenger` does;
  `Bridge.Acknowledge(ctx, bot, notificationID)`; the server routes `sendNotification` and the answers of
  `notification_acknowledged` (`server.go`, `events.go`); the Store is not changed: it keeps a notification's action
  as an entry under a derived key that starts with a space, so that it never meets an Idempotency-Key, for 24 hours,
  as it keeps answers; a Store that fails to keep it makes that notification unacknowledgeable (logged; the call still
  succeeds). The limits of `action.data` and of the text are enforced by the validation of the request against the
  specification, before the Gateway is called.
- **Example** (`examples/gateway/main.go`): an in-memory gateway that prints each notification and offers a page to
  acknowledge it; `make conformance` gains a run of it with `--contact`.
- **Tests**: `spec_test.go` validates `sendNotification`, its problems and the event against the copy; `gateway_test.go` holds the
  tests of the role, the call, the acknowledgement and the answer; `example_test.go` holds `Example_gateway`.

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
- Changes made while the story was worked (muster-bridge-go#26), with their reasons: `Acknowledge` takes a `ctx`, as the
  other reports do, and the Gateway's methods take the bot's name; the notification's action is kept as a Store entry
  under a derived key and the Store interface is unchanged; the limits are enforced by the specification's validation;
  `New` refuses a mismatched role as the contract says, and also messenger-only settings on a bridge without the role
  `messenger`; the tests of the role are in `gateway_test.go`, `spec_test.go` and `example_test.go` and none in
  `bridge_test.go`, `server_test.go` or `events_test.go`; `AGENTS.md` and a comment of `.github/workflows/ci.yml` are
  touched, and the Makefile's `build` also builds `bin/gateway`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-2 | partial | the Go library; Muster's side is S-145 and S-148, the Python library S-147 |
