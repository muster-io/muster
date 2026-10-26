---
id: S-147
title: The role gateway in the Python bridge library (BE)
capability: C-30
kind: be
layer: L2
repository: muster-io/muster-bridge-python
depends_on: [S-145, S-142]
covers: [C-30.FR-2]
files_touched:
  - api/bridge.yaml
  - src/muster_bridge/__init__.py
  - src/muster_bridge/config.py
  - src/muster_bridge/gateway.py
  - src/muster_bridge/app.py
  - src/muster_bridge/events.py
  - src/muster_bridge/store.py
  - examples/gateway_bridge.py
  - README.md
  - Makefile
  - tests/test_config.py
  - tests/test_gateway.py
  - tests/test_app.py
  - tests/test_events.py
  - tests/test_spec.py
acceptance:
  - "[C-30.FR-2] `api/bridge.yaml` is Muster's copy from `master` byte for byte, `make spec-check` passes, and `make generate-check` finds `src/muster_bridge/models.py` current with `Info.roles`, `NotificationInput`, `NotificationAction`, `NotificationSent` and `NotificationAcknowledgedEvent`."
  - "[C-30.FR-2] The configuration takes `roles` (`messenger` by default) and a `Gateway` protocol with `async` `send_notification` and `answer`; `BridgeApp` refuses `gateway` without a `Gateway` and `messenger` without a `Messenger` with `ConfigError` naming the reason, and runs a bridge whose only role is `gateway` without a `Messenger` (`test_config.py`)."
  - "[C-30.FR-2] `sendNotification` keeps the idempotency of the other calls, refuses `action.data` over 64 bytes, a text over 1,000 code points and a call on a bridge without the role as `invalid` before the `Gateway` is called, and keeps each notification's `action` for the bot (`test_gateway.py`, `test_app.py`)."
  - "[C-30.FR-2] `BridgeApp.acknowledge(bot, notification_id)` queues `notification_acknowledged` with the `data` of that notification, delivered by polling and by push; an id the library did not issue, or a notification without `action`, raises `ValueError`; `answerEvent` for it reaches `Gateway.answer` (`test_events.py`)."
  - "[C-30.FR-2] `examples/gateway_bridge.py` prints each notification and acknowledges it from `/demo/acknowledge`; README.md shows it; `make conformance` runs `muster bridge check` from `ghcr.io/muster-io/muster:edge` against it with `--contact` and passes interactively."
verify: "make ci conformance"
operator_attention: true
issue: null
---

# S-147. The role gateway in the Python bridge library (BE)

## Scope

**IN**

- The library's copy of `api/bridge.yaml` with the role, `sendNotification` and `notification_acknowledged` of S-145,
  the generated models, the configuration of the role, the call, the event and the answer, a minimal gateway example and
  their documentation (C-30.FR-2).

**OUT**

- Muster's side: S-145. The Go library: S-146. The page on writing a gateway bridge on Muster's site: S-151.

## Contracts

- **Repository** `muster-io/muster-bridge-python` (MIT, no CLA, ADR-0017), its AGENTS.md and Makefile; the pull request
  says `Part of muster-io/muster#<issue>` and `Story: S-147`, and the issue here is closed by hand once it merges.
- **Specification copy** (`api/bridge.yaml`): replaced by Muster's from `master` after S-145, which also carries the
  changes S-142 took; `src/muster_bridge/models.py` is regenerated (never listed).
- **Package**: `Config.roles` and `Config.gateway` (`config.py`); the `Gateway` protocol with `send_notification(bot,
  notification)` and `answer(bot, event, text)` (`gateway.py`); `BridgeApp.acknowledge(bot, notification_id)` and the
  routes of `sendNotification` and of the answers (`app.py`, `events.py`); the store keeps a notification's action with
  its key for 24 hours (`store.py`); `muster_bridge` re-exports the new names (`__init__.py`).
- **Example** (`examples/gateway_bridge.py`): an in-memory gateway; `make conformance` gains a run of it with
  `--contact`.
- **Tests**: `test_spec.py` validates the call, its problems and the event against the copy with openapi-core;
  `test_config.py`, `test_gateway.py`, `test_app.py`, `test_events.py`.

## Steps

1. Take the copy and regenerate. Check: `make spec-check generate-check`.
2. Add the role, the protocol, the call, the event and the answer, with their tests. Check: `make ci`.
3. Write the example and its conformance run; update README.md. Check: `make conformance`.

## Verification

In the repository: `make ci conformance` passes, the gateway example included. Against Muster, with what S-145
provides: `muster bridge check` from Muster's `master` against `examples/gateway_bridge.py` with `--contact
+10000000001` passes every gateway step interactively; with `make dev` running from `master`, the example added as a
Bridge Connection reads `roles` `["gateway"]` through `getConnection`.

## Open questions

None.

## Notes

- Suggested commit: `feat: support the gateway role with send_notification and notification_acknowledged`.
- **Maintainer** (operator attention): merges the release pull request that release-please opens, which releases a new
  `0.x` minor version to PyPI; an agent never merges a release pull request.
- The copy check fails on the library's `master` from S-145's merge until this story merges; that failure is expected.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-2 | partial | the Python library; Muster's side is S-145 and S-148, the Go library S-146 |
