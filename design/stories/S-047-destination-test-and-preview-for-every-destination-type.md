---
id: S-047
title: Destination test and preview for every Destination type (BE)
capability: C-16
kind: be
layer: L1
depends_on: [S-042, S-045]
covers: [C-16.FR-1, C-16.FR-2, C-16.FR-3, C-16.FR-4, C-16.FR-5, C-16.FR-6, C-16.FR-7, C-16.AC-1, C-16.AC-2, C-16.AC-3, C-16.AC-4, C-16.AC-5, C-16.AC-6, C-16.AC-7, C-15.FR-2, C-15.FR-10, C-13.FR-13, C-11.FR-2, C-11.FR-9, C-01.FR-13]
files_touched:
  - internal/destinations/test.go
  - internal/destinations/preview.go
  - internal/destinations/test_test.go
  - internal/mattermost/testmsg.go
  - internal/mattermost/callback.go
  - internal/mattermost/testmsg_test.go
  - internal/telegram/testmsg.go
  - internal/telegram/presses.go
  - internal/telegram/testmsg_test.go
  - internal/webhooks/testevent.go
  - internal/webhooks/testevent_test.go
  - internal/messages/default.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/fakes/fakemattermost/presses.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/destination_tests_test.go
acceptance:
  - "[C-16.FR-1, C-16.FR-6, C-16.AC-1] \"Send test message\" to a Mattermost or Telegram Destination posts one Root message marked \"🧪 Test message\", built from a recent Alert Group of one of its Routes or from the built-in example, through the interactive path; no Alert Group's Timeline or delivery changes, and the test message is never reconciled."
  - "[C-16.FR-2, C-16.AC-2] The result shows the request with Secrets, the Signing secret and tokens masked, the response status and body (truncated, masked), the duration and the error class; a test against an unreachable endpoint returns within `delivery.interactive_budget` with the class `transient`, and with no limiter token free in time the step's class is `limited` and nothing is sent."
  - "[C-16.FR-3, C-16.AC-4] Pressing a button of a test message in the fake Mattermost or Telegram server changes nothing and is answered privately with \"This is a test message; nothing was changed\"."
  - "[C-16.FR-3, C-16.AC-7, C-13.FR-13] A test of a Mattermost Destination then presses the test message's first button through `POST /api/v4/posts/{post_id}/actions/{action_id}` as a second interactive request: when the fake server delivers the press to Muster's callback, the result says \"Button presses reach Muster.\" and no ephemeral post is sent; when it answers \"Action integration error\", the result says \"The button press did not reach Muster. Add the host of {MUSTER_INGEST_URL} to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.\""
  - "[C-16.FR-1, C-16.AC-6, C-15.FR-2] A test of an outgoing webhook in events mode delivers one `test` event with `version` 1, `test: true` and `sequence` 0, signed with the Signing secret and verified by the fake endpoint; no Alert Group's events change; in mode `both` the `event` step comes before the `create` step."
  - "[C-16.AC-3, C-15.FR-10] A test of an outgoing webhook in template mode shows the values extracted from the \"create\" response and every Secret value as `[redacted]`."
  - "[C-16.FR-7, C-16.AC-5] A successful test of a Broken Destination makes it healthy and resolves `MusterDestinationBroken`; a failed test leaves it Broken and changes nothing."
  - "[C-16.FR-4] `previewDestination` renders, without sending anything, the Root message of a Mattermost (attachment and Markdown) or Telegram (HTML) Destination, or the requests of an outgoing webhook — the event body, \"create\", \"update\", \"open thread\" and \"reply in thread\" — for a chosen Alert Group or the example, Secrets masked."
  - "[C-16.FR-5] Every test writes one Audit log entry `destination.tested` with the source and each step's error class."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 47
---

# S-047. Destination test and preview for every Destination type (BE)

## Scope

**IN**

- `testDestination` for Mattermost, Telegram and outgoing webhooks (the `test` event and the "create" request), through
  the interactive path, with masked results, the `limited` class, the Audit log entry and the end of a Broken state.
- Test message buttons: the private answer to a person, and in Mattermost the bot's own press with its result.
- `previewDestination` for every type.

**OUT**

- The panels on the Destination page (S-048); testing the full lifecycle on a test message, which is not part of L1
  (C-16.FR-6).

## Contracts

- **Operations implemented**: `testDestination` and `previewDestination` (`destinations:test`). Schemas:
  `DestinationTestRequest`, `TestSource`, `DestinationTestResult`, `TestStep` (with the `press` step),
  `RenderedRequest`, `DeliveryErrorClass` (`limited`), `DestinationPreviewRequest`,
  `DestinationPreviewItem`, `DestinationPreview`.
- **Source** (C-16.FR-1, FR-4): `{kind: "alert_group", alert_group_id}` must name an Alert Group of one of the
  Destination's Routes (`422 unknown_id` otherwise); `{kind: "example"}` uses a built-in example Alert Group with three
  Alerts. The test never reads or writes `deliveries`, `thread_replies` or `webhook_events`, and records nothing in any
  Timeline (C-16.FR-3).
- **Messenger test** (C-16.FR-1; `internal/mattermost/testmsg.go`, `internal/telegram/testmsg.go`): the Root message of
  the source rendered by S-036 with the heading prefixed "🧪 Test message", the buttons of its status carrying action ids
  of the subject kind `test` with the Destination's `public_id`, sent once with `Publish` through
  `delivery.Interactive` on the Destination's and the Connection's limiters, in the interactive class; step `message`.
- **Mattermost press check** (C-16.FR-3, C-13.FR-13; F-054, F-022): after the test post, a second interactive request
  `POST /api/v4/posts/{post_id}/actions/{action_id}` with the bot token presses its first button. The callback handler
  of S-061, on any replica, recognises a test action id pressed by the Connection's `bot_user_id`, answers `{}` without
  an ephemeral post and sends `NOTIFY test_press` with the test's nonce; the test waits for it within the remaining
  interactive budget. Step `press`: reached → no error; the server answered "Action integration error" or the
  notification did not come → `error` "The button press did not reach Muster. Add the host of {MUSTER_INGEST_URL} to
  ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server." with the class `unknown`.
- **Presses by people** (C-16.FR-3): a test action id pressed by anyone else gets "This is a test message; nothing was
  changed" — an ephemeral post in Mattermost, or the answer's `ephemeral_text` when that post is refused (S-061's
  path), the `answerCallbackQuery` text in Telegram (S-042's path) — and changes nothing.
- **Outgoing webhook test** (C-16.FR-1, C-15.FR-2; `internal/webhooks/testevent.go`): events mode — one `test` event
  (`version` 1, `event` `test`, `test: true`, `sequence` 0, `notify` false, `mentions` `[]`, the source as
  `alert_group` and `alerts`), signed like every request, with a new `webhook-id`, never queued; template mode — the
  "create" request rendered for the source, its extraction rules applied and shown as `extracted`, nothing stored in
  `response_values`; mode `both` — step `event`, then step `create`.
- **Result** (C-16.FR-2): per step `request` (method, URL, headers and body after rendering, with every Secret, the
  Signing secret and the token masked as `[redacted]`), `response_status`, `response_body` (the first 4 KB, masked,
  untrusted text), `extracted`, `duration_ms`, `error_class` (`none`, `retry_after`, `transient`, `fatal`, `unknown`,
  `template_error`, `blocked`, `limited`) and `error`; `health` after the test. When no limiter token is free within
  `delivery.interactive_budget`, the step is `limited` and nothing is sent; the operation still answers `200`.
- **Broken** (C-16.FR-7): when every step succeeds on a Broken Destination, the test calls `MarkHealthy` of S-035
  (resolving `MusterDestinationBroken` and starting recovery); a failed test changes nothing.
- **Audit log** (C-16.FR-5): `destination.tested` with the Destination, the source and each step's name and class.
- **Preview** (C-16.FR-4; `internal/destinations/preview.go`): items per type — Mattermost `message` (format
  `markdown`, the attachment as `request.body`), Telegram `message` (format `html`), outgoing webhook `event` (format
  `json`) in events mode and `create`, `update`, `open_thread`, `reply_in_thread` (the requests that exist) in template
  mode, with `.Response` filled with `example-<name>`; nothing is sent; Secrets masked.
- **Fake Mattermost** (C-01.FR-13): `POST /api/v4/posts/{post_id}/actions/{action_id}` with the bot token calls the
  button's integration URL with the bot's `user_id` and `user_name` and answers `200 {"status":"OK","trigger_id":…}`
  (F-054), or `400` "Action integration error" when the URL's host is not allowed (F-022).
- **Log events**: `destination_tested` (INFO: `destination`, `steps` with their classes).

## Steps

1. Extend the fake Mattermost server with the actions endpoint. Check: `fakemattermost_test.go` reproduces F-054 with
   the host allowed and not allowed.
2. Write the messenger tests with test buttons and the Mattermost press check. Check: `testmsg_test.go` of both
   packages.
3. Write the outgoing webhook test. Check: `testevent_test.go` covers the three modes and masking.
4. Write the result, `limited`, Broken, the Audit log entry and the preview. Check: `test_test.go`.
5. Add the secret probe and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev &
# as in S-039, S-061, S-042 and S-044: the session `H`, the Mattermost Destination "alerts" (MD), the Telegram Destination
# "alerts" of the Dev Telegram Connection (TD), the events-mode webhook "auto" (WD, its secret in SEC registered with
# the fake endpoint) and the template-mode webhook "chat" (CD) with the Secret "token" in an Authorization header
# the Route "db" with MD and the fake Alertmanager group d1 of S-061, with NOTIFY
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; FWH=127.0.0.1:18093
TEST() { curl -s "${H[@]}" $API/destinations/$1/tests -d '{"source":{"kind":"example"}}'; }

# C-16.AC-1, AC-7: a Mattermost test message, and the press reaching Muster
N0=$(curl -s -b jar "$API/alert-groups?status=open" | jq '[.items[].id] | length')
TEST $MD | jq -c '{s: [.steps[] | {name, error_class, error}], h: .health.state}'
# {"s":[{"name":"message","error_class":"none","error":null},{"name":"press","error_class":"none","error":null}],"h":"healthy"}
curl -s $FMM/posts | jq -r '.[-1].props.attachments[0].title | startswith("🧪 Test message")'   # true
curl -s $FMM/ephemeral | jq '[.[] | select(.message | test("test message"))] | length'          # 0
curl -s -b jar "$API/alert-groups?status=open" | jq '[.items[].id] | length' | xargs test $N0 -eq && echo unchanged   # unchanged
# the Mattermost server does not allow the address
curl -s -X PUT $FMM/config -d '{"allowed_untrusted_internal_connections":""}' > /dev/null
TEST $MD | jq -r '.steps[] | select(.name == "press") | .error'
# The button press did not reach Muster. Add the host of http://localhost:8081 to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.
curl -s -X PUT $FMM/config -d '{"allowed_untrusted_internal_connections":"localhost 127.0.0.1"}' > /dev/null

# C-16.AC-4: a person presses a test button
TP=$(curl -s $FMM/posts | jq -r '.[-1].id')
curl -s -X POST $FMM/press -d "{\"post_id\":\"$TP\",\"action_id\":\"ack\",\"user_id\":\"u-alice\"}" > /dev/null
curl -s $FMM/ephemeral | jq -r '.[-1].message'                                 # This is a test message; nothing was changed
TEST $TD | jq -c '[.steps[] | {name, error_class}]'                            # [{"name":"message","error_class":"none"}]
TM=$(curl -s "$FTG/messages?chat=-1001000000001" | jq '.[-1].message_id')
curl -s -X POST $FTG/press -d "{\"chat\":-1001000000001,\"message_id\":$TM,\"from\":{\"id\":5001},\"button\":\"Ack\"}" > /dev/null; sleep 1
curl -s $FTG/answers | jq -r '.[-1].text'                                      # This is a test message; nothing was changed

# C-16.AC-6: the test event, verified with the Signing secret
TEST $WD | jq -c '[.steps[] | {name, error_class, response_status}]'           # [{"name":"event","error_class":"none","response_status":200}]
curl -s $FWH/_fake/received/auto | jq -c '.[-1] | {e: .body.event, t: .body.test, v: .body.version, seq: .body.sequence, sig: .signatures_valid}'
# {"e":"test","t":true,"v":1,"seq":0,"sig":1}

# C-16.AC-3: the template-mode test shows extracted values and masks the Secret
TEST $CD | jq -c '.steps[0] | {name, extracted, auth: (.request.headers[] | select(.name == "Authorization") | .value)}'
# {"name":"create","extracted":{"id":"m…"},"auth":"Bearer [redacted]"}

# C-16.AC-2: an unreachable endpoint returns within the interactive budget
UD=$(curl -s "${H[@]}" $API/destinations -d '{"type":"webhook","name":"down","mode":"events","events":{"url":"http://10.255.255.1/hook","headers":[]},
  "proxy":{"enabled":false},"mentions":'"$(curl -s -b jar $API/destinations/$WD | jq -c .mentions)"',"limiter":{"limit":5,"per_seconds":1}}' | jq -r .destination.id)
time (TEST $UD | jq -r '.steps[0].error_class')                                 # transient   (real < 5.5 s)

# C-16.AC-5: a successful test ends the Broken state; a failed one changes nothing
curl -s -X DELETE $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
TEST $MD | jq -r '.steps[0].error_class'                                       # fatal
curl -s -b jar $API/destinations/$MD | jq -r .health.state                     # healthy   (a failed test changes nothing)
curl -s -X PUT $FAM/groups/d1/alerts/k1 -d '{"labels":{"team":"db","cluster":"k","pod":"k1"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; sleep 1                              # a new Alert Group of the Route "db"
curl -s -b jar $API/destinations/$MD | jq -r .health.state                     # broken
curl -s -X PUT $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
TEST $MD | jq -r '.health.state'                                               # healthy
curl -s -b jar "$API/integrations/$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" \
  | jq '[.items[] | select(.labels.alertname == "MusterDestinationBroken")] | length'   # 0

# C-16.FR-4: preview without sending
P0=$(curl -s $FMM/posts | jq length)
curl -s "${H[@]}" $API/destinations/$MD/previews -d '{"source":{"kind":"example"}}' | jq -c '[.items[] | {name, format}]'   # [{"name":"message","format":"markdown"}]
curl -s "${H[@]}" $API/destinations/$CD/previews -d '{"source":{"kind":"example"}}' | jq -c '[.items[].name]'
# ["create","update","open_thread","reply_in_thread"]
curl -s $FMM/posts | jq length | xargs test $P0 -eq && echo "nothing sent"       # nothing sent

# C-16.FR-5
curl -s -b jar "$API/audit-log?action=destination.tested&limit=1" | jq -r '.items[0].action'   # destination.tested
```

`test/e2e/destination_tests_test.go` repeats these steps and adds the `limited` class (the Destination's limiter set
to 1 per 60 s and spent by a delivery just before the test: the step is `limited` and the fake receives nothing), mode
`both` (`event` then `create`) and a test from a recent Alert Group of the Destination's Route.

**Optional manual check against a real server** (the operator's test Mattermost): with Muster's ingest address not
listed in `AllowedUntrustedInternalConnections`, a test of a Mattermost Destination reports that the press did not
reach Muster; after the Mattermost admin adds the host, the next test reports that presses arrive (C-16, scenario 5).

## Open questions

None.

## Notes

- Suggested commit: `feat(destinations): add destination tests and previews for every type`.
- The press check reaches Muster through the ingest listener, possibly on another replica; `NOTIFY test_press` is how
  the waiting test learns of it without a table.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-16.FR-1 | partial | the API; the panel is S-048 |
| C-16.FR-2 | partial | the API; the panel is S-048 |
| C-16.FR-3 | partial | the API; the result on the page is S-048 |
| C-16.FR-4 | partial | the API; the panel is S-048 |
| C-16.FR-5 | full | |
| C-16.FR-6 | full | nothing beyond one "create" request is tested |
| C-16.FR-7 | full | |
| C-16.AC-1 | full | |
| C-16.AC-2 | full | |
| C-16.AC-3 | partial | the API; the panel shows it in S-048 |
| C-16.AC-4 | full | |
| C-16.AC-5 | full | |
| C-16.AC-6 | full | |
| C-16.AC-7 | partial | the API; the panel shows it in S-048 |
| C-15.FR-2 | partial | the `test` event |
| C-15.FR-10 | partial | masking in test results and previews |
| C-13.FR-13 | partial | the bot's own press and its result |
| C-11.FR-2 | partial | test messages on the interactive path and the `limited` class |
| C-11.FR-9 | partial | a successful test ends the Broken state |
| C-01.FR-13 | partial | the actions endpoint of the fake Mattermost server |
