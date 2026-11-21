---
id: S-233
title: The steps of a test run for Bridge and outgoing webhook Destinations (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-231, S-074, S-081, S-126, S-044, S-045, S-177, S-212]
covers: [C-42.FR-2, C-42.FR-3, C-42.FR-4, C-42.FR-6, C-42.AC-2, C-42.AC-4]
files_touched:
  - internal/bridge/teststeps.go
  - internal/bridge/teststeps_test.go
  - internal/bridge/testmsg.go
  - internal/webhooks/teststeps.go
  - internal/webhooks/teststeps_test.go
  - internal/webhooks/testevent.go
  - internal/webhooks/body.go
  - internal/webhooks/template.go
  - internal/destinations/testruns.go
  - internal/destinations/testruns_test.go
  - internal/destinations/testmentions.go
  - internal/destinations/testmentions_test.go
  - internal/delivery/teststeps.go
  - internal/delivery/teststeps_test.go
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/fakes/fakewebhook/fakewebhook.go
  - internal/fakes/fakewebhook/chat.go
  - internal/fakes/fakewebhook/fakewebhook_test.go
  - test/e2e/destination_test_runs_bridge_test.go
  - test/e2e/destination_test_runs_webhook_test.go
acceptance:
  - "[C-42.FR-2, C-42.FR-3] A step-by-step test of a Bridge Destination makes the eight steps of S-231 in order as the Bridge protocol's requests that delivery sends for each lifecycle event — the Publication of the Root message with the `layout` and its `order`, the edits within the bridge's edit window or the pointer reply and final edit where `info` says it cannot edit, and the Thread replies — each through the interactive path with the Connection's and the Destination's limiters, the token masked, the answer truncated and masked, and the error class of the Bridge's classification; the Root message is marked \"🧪 Test message\" in the layout's summary, and a bridge that answers `rate_limited` ends the step `limited` (`teststeps_test.go` of `bridge`, `test/e2e/destination_test_runs_bridge_test.go`)."
  - "[C-42.FR-2, C-42.AC-2] A step-by-step test of an outgoing webhook Destination in events mode delivers one event for each step, marked `test: true` like the `test` event of C-16.FR-1 — the events `alert_group` for Created, `alerts_added` for New Alert, then the events of the lifecycle step in its place — each with `version` 1 and the signature of the Signing secret, which the fake endpoint verifies; its answers are shown masked, truncated and as untrusted text; in mode `both` the `event` request comes before the template request of each step (`teststeps_test.go` of `webhooks`, `test/e2e/destination_test_runs_webhook_test.go`)."
  - "[C-42.FR-2, C-42.AC-2] In template mode the fake endpoint receives the \"create\", \"update\" and \"reply in thread\" requests that the Destination's templates build for each step, with the extraction rules applied to the \"create\" answer and the extracted id used by the next steps' \"update\" and \"reply in thread\" as delivery uses it, Secrets masked in the shown requests and the values extracted shown; a step whose template fails to render ends with the template error as the step's error and stops the run (`teststeps_test.go`, `test/e2e/destination_test_runs_webhook_test.go`)."
  - "[C-42.FR-4, C-42.AC-4] Each Loud step of a Bridge or webhook run lists whom the Destination's Mention entries would mention with the reason of each that would mention nobody — a person with no Account link in the Identity space of the bridge's Connection, a group or everyone that the bridge's `info` does not declare — and the names are plain text after \"Would mention:\": the `mentions` of a Bridge request and the Mentions as data of a webhook request are empty, no \"Mention not delivered\" is recorded and no warning of the Destination changes (`testmentions_test.go`, `teststeps_test.go`, the e2e tests)."
  - "[C-42.FR-3, C-42.FR-6] A Bridge or webhook run adds no row of `deliveries`, `thread_replies` or `webhook_events`, writes no Alert Group, Alert, Timeline, Note or Assignee, never reconciles its messages and counts only in `muster_client_requests_total` with the client `interactive`; the first step of a successful run ends the Broken state of a Broken Destination as C-16.FR-7 says; a webhook run's outgoing requests pass the outbound address policy and are masked in the Request log as the kind `test` under one check run (`teststeps_test.go`, the e2e tests, `make lint-arch`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-233. The steps of a test run for Bridge and outgoing webhook Destinations (BE)

## Scope

**IN**

- The stepper of S-231 for a Bridge and for an outgoing webhook in events mode, template mode and `both`: the eight
  steps as protocol requests and as events or templated requests, with masking, classes, "Would mention:" and no Mention
  data (C-42.FR-2, FR-3, FR-4, FR-6).

**OUT**

- The run, the order, the expiry and the Audit log: S-231. Mattermost: S-231; Telegram and Slack: S-232.
- The presses on a Bridge's Test message and the text commands in its Thread: S-234.
- The `order` of the blocks in a bridge's layout: S-212, which this story reads.

## Contracts

- **Stepper**: `delivery.Stepper` of S-231 implemented by `*bridge.Tester` in `internal/bridge/teststeps.go` beside
  `testmsg.go`, and by `*webhooks.Tester` in `internal/webhooks/teststeps.go` beside `testevent.go`. The Bridge stepper
  builds the envelope of each step from the `messages.Source` of S-231 with the bridge adapter's own layout, edit-window
  rules and idempotency keys (S-074, S-081, S-126), sending through `internal/bridge`'s client — the protocol client
  stays the only caller; the webhook stepper renders the event of each step with `internal/webhooks`'s body builder, or
  the template requests with its template engine and the Destination's Secrets, and extracts values from the "create"
  answer.
- **Registry and wiring**: `internal/destinations/testruns.go` gains the two types; `internal/runtime/runtime.go` wires
  them; with this story merged no Destination type answers `501` for a run.
- **Mentions**: `testmentions.go` gains the reasons of these types, and the webhook's "Mentions as data" and the
  bridge's `mentions` are left empty in the requests (the names are text, as in S-231).
- **Events** (events mode): the event for each step is the event name delivery uses for that lifecycle event
  (`webhook_events` names, C-15.FR-2), with `test: true`, the `sequence` of the run, and `version` 1; the signing is the
  one of S-044.
- **Fake servers**: `internal/fakes/fakebridge/messages.go` and `internal/fakes/fakewebhook/{fakewebhook,chat}.go` keep the
  requests in order and verify signatures, as they do; extended only where they lack the edit or the reply, so the e2e
  tests read the eight steps.
- **Log events, metrics, settings**: those of S-231; none are added.

## Steps

1. Write the Bridge stepper. Check: `teststeps_test.go` of `bridge` (the envelopes, the edit window and the pointer
   reply, masking, `rate_limited`, `layout.order`).
2. Write the webhook stepper for events and template modes. Check: `teststeps_test.go` of `webhooks` (the events with
   `test: true`, the signature, the three templated requests, the extracted id, `both`, a template error).
3. Register the two types and add the Mention reasons. Check: `testruns_test.go`, `testmentions_test.go`.
4. Extend the two fakes and write the e2e tests. Check: the Verification below; `make lint-arch`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FBR=127.0.0.1:18094/_fake; FWH=127.0.0.1:18093
# A: the Admin's header array, as in S-231; the Destinations "bridge-alerts", "auto" (events mode) and "chat" (template mode) of S-048's setup
step_all() { D=$(curl -s "${A[@]}" $API/destinations | jq -r --arg n "$1" '.items[] | select(.name==$n) | .id')
  RID=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"critical"}' | jq -r .id)
  for i in 2 3 4 5 6 7 8; do curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps | jq -r '.name + " " + (.requests[0].status|tostring)'; done; }
step_all bridge-alerts
curl -s $FBR/messages | jq -c '[.[] | {k: .kind, t: (.layout.title // "" | test("Test message"))}]'    # the Publication, then edits and replies in order
# C-42.AC-2: events mode: one event per step, marked test: true, verified with the Signing secret
step_all auto
curl -s $FWH/_fake/requests | jq -c '[.[] | {e: .body.event, t: .body.test, ok: .signature_ok}]'
# [{"e":"alert_group","t":true,"ok":true}, ... eight in order, all signed correctly]
# template mode: "create", "update" and "reply in thread"
step_all chat
curl -s $FWH/_fake/requests | jq -r '[.[] | .path] | join(" ")'     # the create path, then the update and the reply paths of the templates, in order
# the extracted id of the "create" answer is used by the later requests; Secrets are [redacted] in the result of a step
R=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"}}')
jq -c '.steps[0].requests[0].headers' <<<"$R"   # the Secret's header reads [redacted]
# C-42.AC-4 and no Mention data
curl -s $FBR/messages | jq -c '[.[] | .mentions // []] | flatten | length'      # 0
psql "$MUSTER_DB" -c "select (select count(*) from webhook_events), (select count(*) from deliveries)"   # as before
```

`test/e2e/destination_test_runs_bridge_test.go` and `…_webhook_test.go` repeat these through the generated client, for
mode `both` too (the `event` request before the template request) and with a template that fails to render, where the
run stops with the template error.

## Open questions

None.

## Notes

- Suggested commit: `feat(webhooks): add step-by-step test runs for bridge and outgoing webhook destinations`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- A webhook that is an outgoing endpoint of a third party receives these requests for real, marked `test: true` in
  events mode; in template mode the templates build the requests, so what marks a test there is the content of the
  example source ("Test message" in the summary). The documentation page of S-239 says so.
- The Bridge protocol's client stays the one place that talks to a bridge (lint 4 and ADR-0017); the stepper calls it
  and the `muster bridge check` command is unchanged.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-2 | partial | the steps for a Bridge and a webhook; Mattermost is S-231, Telegram and Slack S-232 |
| C-42.FR-3 | partial | the interactive path, masking and classes for the two; the others are S-231, S-232 |
| C-42.FR-4 | partial | "Would mention" and no Mention data for the two; the others are S-231, S-232 |
| C-42.FR-6 | partial | no writes and no counts for the two; the others are S-231, S-232 |
| C-42.AC-2 | full | |
| C-42.AC-4 | partial | the entry that mentions nobody in a Bridge and a webhook; Mattermost is S-231, Telegram and Slack S-232 |
