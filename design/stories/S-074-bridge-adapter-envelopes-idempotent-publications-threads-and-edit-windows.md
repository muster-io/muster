---
id: S-074
title: "Bridge adapter: envelopes in CommonMark, idempotent Publications, Thread replies, error classes and edit windows (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-073]
covers: [C-22.FR-3, C-22.FR-4, C-22.FR-5, C-22.FR-6, C-22.FR-7, C-22.FR-8, C-22.FR-9, C-22.FR-17, C-22.FR-19, C-22.FR-23, C-22.FR-24, C-22.AC-4, C-22.AC-5, C-22.AC-6, C-22.AC-7, C-22.AC-8, C-22.AC-9, C-22.AC-10, C-22.AC-11, C-22.AC-12]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0009_bridge_deliveries.up.sql
  - internal/db/migrations/0009_bridge_deliveries.down.sql
  - design/db/schema.md
  - internal/bridge/adapter.go
  - internal/bridge/adapter_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/client.go
  - internal/bridge/testmsg.go
  - internal/bridge/testmsg_test.go
  - internal/messages/message.go
  - internal/messages/safe.go
  - internal/messages/safe_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/delivery/delivery.go
  - internal/delivery/render.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/publication.go
  - internal/delivery/publication_test.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/events.go
  - internal/delivery/query.sql
  - internal/delivery/live_test.go
  - internal/mentions/mentions.go
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/logging/events.go
  - docs/messengers/bridge.md
  - mkdocs.yml
  - test/e2e/bridge_test.go
acceptance:
  - "[C-22.FR-4, C-22.FR-5, C-22.AC-4] A new Alert Group posts one `postMessage` of the kind `root` with the status `firing`, `#d32f2f`, 🔴, the title `#N <title>`, the link \"Open in Muster\" to the Alert Group page, a CommonMark body with items 3 to 11 of C-12.FR-1, `notify` true, and the buttons Ack (`primary`), Resolve (`success`) and the Route's Snooze durations (`neutral`) with signed data of at most 64 bytes; Acknowledge through the API makes one `editMessage` with the acknowledged buttons and no Mentions."
  - "[C-22.FR-6, C-22.AC-4] A new Alert creates a `replyInThread` under the Root message with the `thread_ref` its `postMessage` returned, or null without one; the `thread_ref` each reply returns is stored and sent with the next reply; each reply carries its own `Idempotency-Key`."
  - "[C-22.FR-4, C-22.AC-5] A label value `@all *x* <b>y</b> [z](http://evil)` reaches the fake bridge as literal text: escaped for CommonMark, `@` neutralized, no link and no HTML (`safe_test.go`, Verification)."
  - "[C-22.FR-4] A Route template preview with the markup `commonmark` renders a Root message template and a line template in CommonMark with alert data escaped (`createTemplatePreview`)."
  - "[C-22.FR-6, C-22.AC-6] When the fake bridge posts a Publication and drops the connection before answering, the retry carries the same `Idempotency-Key`, the fake holds one message, the answer is `replayed`, no possible duplicate is recorded, and an `editMessage` follows with the current state — acknowledged when Acknowledge came before the retry; with replays turned off in the fake, the retry posts a second message and a possible duplicate is recorded as a delivery event."
  - "[C-22.FR-8, C-22.AC-7] `rate_limited` with `Retry-After: 7` delays the next call to that Destination by 7 to 8 seconds without counting an attempt, and with `scope` `bot` every Destination of the Connection; a `Retry-After` longer than `bridge.retry_after_max`, and a `429` without one, count as Transient; `transient` follows the Transient budget; `forbidden` makes the Destination Broken at once with the bridge's `detail` as the reason; `invalid` ends the delivery as Not delivered and leaves the Destination healthy; `gone` on an edit and on a Thread reply each start the deleted Root message flow once."
  - "[C-22.FR-9, C-22.AC-8] With `edit_max_age_seconds` 600, once the development clock is 11 minutes past the Publication, Acknowledge posts a new Root message Quietly, replies \"This message is no longer updated. The current one: {url}\" in the old Thread, and sends the next Thread reply under the new Root message; a new Alert past the window adds a Thread reply and makes no edit and no Publication; the delivery state is `past_edit_window` meanwhile."
  - "[C-22.FR-9, C-22.AC-9] An edit refused as `gone` with the `reason` `edit_window_closed` before the declared window marks the Root message as past its window, is not the deleted Root message flow, and the next change of status republishes as above; the final edit of a Root message past its window is a Thread reply with the same text; a republished delivery keeps the ids of its older Root messages for presses and Text commands (S-075)."
  - "[C-22.FR-7, C-22.AC-10] With `max_body_length` 1,000, an Alert Group with 40 Alerts posts a body of at most 1,000 code points that keeps its link line, notices and footer; `truncated` from the bridge is accepted as delivered."
  - "[C-22.FR-5, C-22.AC-11] Without `buttons` and `thread_messages` in `info`, a Root message has no buttons and no commands line; with `thread_messages` `all` and no `buttons`, a firing Root message ends with \"Reply in the Thread: ack · resolve · snooze 1h\", an acknowledged one with \"unack · resolve · snooze 1h\", a resolved one with none; with `mentions_only` the line starts \"Reply in the Thread, mentioning the bot:\"."
  - "[C-22.FR-17, C-22.AC-12] `createDestinationTest` posts one `postMessage` of the kind `test` with \"🧪 Test message\" through the interactive path and changes no Timeline; `createDestinationPreview` returns the envelope without posting."
  - "[C-22.FR-3, C-22.FR-19] With a Bridge Destination Broken and nothing waiting, the probe after `delivery.broken_probe_interval` runs `checkChannel` in the delivery client class and ends the Broken state once the fake answers `200`."
  - "[C-22.FR-24] Each republication past an edit window logs `bridge_republished_past_edit_window` with the Destination, the Alert Group and the old and new message ids, registered with its reference page."
  - "[C-22.FR-23] The page Messengers → Bridge is in the site's navigation and covers the topics of C-22.FR-23 that this story implements: the Connection, event delivery modes, Destinations by channel id, the Connection check, edit windows and Broken Destinations."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-074. Bridge adapter: envelopes in CommonMark, idempotent Publications, Thread replies, error classes and edit windows (BE)

## Scope

**IN**

- The Bridge adapter of `delivery.Adapter` and `delivery.Checker`: Publications, edits and Thread replies as envelopes,
  with `Idempotency-Key`s and `thread_ref`; the length limit from `info`; the answer classification of S-073 mapped to
  delivery's outcomes; the Broken probe through `checkChannel`.
- The `commonmark` markup: escaping, the default message, built-in texts, Route templates and their previews.
- Buttons with style hints when the bridge has them; the commands line when it has only Thread messages.
- Edit windows: no edits past `edit_max_age_seconds`, republication on a change of status with the pointer reply, the
  `edit_window_closed` reason, the final edit as a Thread reply.
- Test messages and previews of Bridge Destinations; the page Messengers → Bridge.

**OUT**

- Presses, answers, Text commands, deleted-message events and the Transport `bridge` (S-075).
- Mentions of linked people and their names in footers (S-076): until then a Mention of a User is their display name as
  text, which mentions nobody, as for a User without a link.
- The pages (S-077); the example bridge and `muster bridge check` (S-078); "Write a bridge" (S-079).

## Contracts

- **Adapter** (`internal/bridge/adapter.go`, `envelope.go`): `Publish` → `postMessage` (`kind` `root` or
  `storm_summary`); `Update` → `editMessage` with the whole envelope, no Mentions, `notify` absent; `Reply` →
  `replyInThread` with `Root.ThreadAnchorID` — the last `thread_ref` received from the post or a reply, empty for
  `null` — as `thread_ref`. The envelope takes the status, colour and emoji of item 1 of C-12.FR-1, the heading as
  `title` and `link`, the buttons and the CommonMark body of items 3 to 11. `Outcome` gains `ThreadRef` (from
  `postMessage` or `replyInThread`, stored in `deliveries.thread_anchor_id` when present), `Replayed`, and a kind
  `edit_window_closed`; `MessageURL` is the answer's `url`. The client of S-073 gains `postMessage`, `editMessage` and `replyInThread`.
- **Idempotency keys**: a Publication's key is `pub-<delivery public id>-<publications + 1>`, the same for every retry of
  that Publication; a Thread reply's `reply-<thread reply id>`; the pointer reply's `pointer-<delivery public
  id>-<publications>`; a final edit sent as a Thread reply `final-<delivery public id>`; a republication past the edit
  window counts as a new Publication. `publication.go` records a possible duplicate after a started Publication only
  when the outcome is not `Replayed` (C-11.FR-12 for Bridges); a `Replayed` Publication leaves the delivery pending with
  its actual state unknown, so the worker's next call is an edit to the latest Desired state.
- **Length**: `delivery.Adapter` gains the per-Destination limit `LengthLimitOf(ctx, Destination) int`, which Mattermost
  and Telegram answer with their constants and the Bridge adapter from the Connection's `info`
  (`limits.max_body_length`, read again past `bridge.info_max_age`); rendering shortens by it (C-12.FR-11).
- **Markup** (`internal/messages`): `MarkupCommonMark`; `EscapeCommonMark` escapes every CommonMark punctuation that
  can start markup and line-leading block markers, after `Neutralize`; the default layout writes items 3 to 11 with
  block quotes for the label sections, a bullet list for the Alerts and strikethrough for resolved ones; built-in texts
  in English and Russian for the commands line, the pointer reply and "🧪 Test message"; `createTemplatePreview` accepts
  `markup` `commonmark` (spec: `TemplateMarkup` gains it).
- **Buttons and commands line** (C-22.FR-5): delivery renders the buttons of the status as for every messenger; the
  adapter keeps them only when `info` has `buttons`, drops Snooze durations from the longest beyond
  `buttons.max_per_message`, and sets the style hints. Without `buttons` and with `thread_messages`, it appends the
  commands line of the status; resolved messages and Storm summaries get none.
- **Error mapping** (C-22.FR-8): `transient` → `OutcomeTransient`, `rate_limited` → `OutcomeRetryAfter` with
  `ScopeConnection` for `scope` `bot` — `OutcomeTransient` when `Retry-After` is missing or above
  `bridge.retry_after_max` — `gone` → `OutcomeGone` (on a reply too: the Root message or its Thread is gone, the deleted
  Root message flow), `gone` with `edit_window_closed` → `OutcomeEditWindowClosed`, `forbidden` → `OutcomeFatal` with
  `detail`, `invalid` → `OutcomeUnknown`.
- **Edit windows** (C-22.FR-9; migration `0009_bridge_deliveries`): `deliveries` gains `actual_status`,
  `past_edit_window` (boolean) and `superseded_message_ids` (the older Root messages of a republished delivery, which
  S-075 binds presses and Text commands to); `delivery_events.kind` gains `republished_past_edit_window` and the delivery state
  enum of the API `past_edit_window`. Before an edit of a Bridge Root message, the worker compares its `published_at`
  with `edit_max_age_seconds` less `bridge.edit_window_margin` on the business clock; past it, a Desired state whose
  status equals `actual_status` makes no call and leaves the row delivered with `past_edit_window`; a new status
  publishes a new Root message Quietly with a new key, then replies under the old one with the pointer text and the new
  `url` (or the Alert Group page), stores the new message id and its `thread_ref`, and records the delivery event. The
  pointer reply is not retried: a failure is recorded with the event. `OutcomeEditWindowClosed` sets
  `past_edit_window` and applies the same rule to the Desired state it was carrying. A final edit (C-11.FR-14) of a row
  past its window is sent as a Thread reply.
- **Probe and check**: the adapter's `Check` is S-073's `checkChannel` check, run by the Broken probe in the delivery
  class (C-11.FR-9).
- **Test and preview** (`testmsg.go`): `bridge.Tester` makes one `postMessage` of the kind `test` through the
  interactive path; the preview returns the envelope as JSON.
- **Mentions** (`internal/mentions`): the Bridge type resolves a Mention of a User to the display name as text until
  S-076 adds Account links.
- **Fake bridge** (`messages.go`): `postMessage` (no `thread_ref`), `editMessage`, `replyInThread` (a `thread_ref`
  `t-<root id>`), the idempotency store with `replayed`, `/_fake/messages` (each with its envelope, edits and replies,
  and `url` `http://127.0.0.1:18094/m/<id>`), `DELETE /_fake/messages/{id}` (a person deletes), `/_fake/config`
  `replays` (default on), `edit_max_age_seconds` enforced with `gone` + `edit_window_closed`, and faults that answer a
  problem with a class, or post and then close the connection (`after_create`).
- **Log events**: `bridge_republished_past_edit_window` (info: Destination, Alert Group public id, old and new message
  ids).
- **Defaults**: `bridge.edit_window_margin`, `bridge.retry_after_max`, `bridge.info_max_age`,
  `delivery.transient_backoff`, `delivery.transient_budget`, `delivery.broken_probe_interval`.

## Steps

1. Add `commonmark`: escaping, the default layout, texts and previews. Check: `safe_test.go`, `render_test.go`.
2. Write the envelope and the adapter with the fake's messages. Check: `envelope_test.go`, `adapter_test.go`.
3. Wire idempotency keys, `thread_ref` and the per-Destination length. Check: `publication_test.go`,
   `threads_test.go`.
4. Add the migration and the edit window rules. Check: `worker_test.go` with a manual clock.
5. Add the test message, the probe, the page and the end-to-end test. Check: Verification.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011; an Integration "lab" with its fake receiver, NOTIFY, ADV and AG as in
# S-020 and S-028; the On-call policy in P as in S-025; the Connection "br" (C) and the Destination "alerts" on
# ch-alerts (D) as in S-073
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FB=127.0.0.1:18094/_fake
MSG() { curl -s $FB/messages | jq -c "$1"; }
R=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"br\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"br\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" | jq -r .id)

# C-22.AC-4, AC-5: a new Alert Group with a hostile label value
curl -s -X PUT $FAM/groups/b1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"DiskFull"}}' > /dev/null
curl -s -X PUT $FAM/groups/b1/alerts/i1 -d '{"labels":{"team":"br","pod":"i1","note":"@all *x* <b>y</b> [z](http://evil)"}}' > /dev/null
NOTIFY b1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'pod%3D%22i1%22')
MSG '.[0].envelope | {kind, status, color, emoji, title, l: .link.label, notify, b: [.buttons[] | "\(.label):\(.style)"], d: ([.buttons[].data | length] | max <= 64)}'
# {"kind":"root","status":"firing","color":"#d32f2f","emoji":"🔴","title":"#1 DiskFull","l":"Open in Muster","notify":true,
#  "b":["Ack:primary","Resolve:success","Snooze 1h:neutral","Snooze 4h:neutral","Snooze 24h:neutral"],"d":true}
MSG '.[0].envelope.body | contains("@​all \\*x\\* \\<b\\>y\\</b\\> \\[z\\](http://evil)")'   # true

# C-22.AC-4: Acknowledge edits; new Alerts go to the Thread with thread_ref
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
MSG '.[0].edits[-1] | {status, b: [.buttons[].label], m: has("mentions")}'   # {"status":"acknowledged","b":["Unack","Resolve",...],"m":false}
for i in i2 i3; do curl -s -X PUT $FAM/groups/b1/alerts/$i -d "{\"labels\":{\"team\":\"br\",\"pod\":\"$i\"}}" > /dev/null
  NOTIFY b1 '{"reason":"new alerts added"}'; ADV 61; sleep 1; done
curl -s $FB/requests | jq -c '[.[] | select(.path | endswith("/thread-replies")) | .body | fromjson | .thread_ref]'
# [null,"t-<root id>"]

# C-22.AC-6: a Publication whose answer is lost, with and without replays
curl -s -X POST $FB/faults -d '{"path":"/v1/messages","after_create":true,"close":true,"times":1}' > /dev/null
curl -s -X PUT $FAM/groups/b2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CpuHigh"}}' > /dev/null
curl -s -X PUT $FAM/groups/b2/alerts/j1 -d '{"labels":{"team":"br","pod":"j1"}}' > /dev/null
NOTIFY b2 '{"reason":"first notification"}'; ADV 3; sleep 2; G2=$(AG 'pod%3D%22j1%22')
MSG '[.[] | select(.envelope.title == "#2 CpuHigh")] | length'                   # 1
curl -s $FB/requests | jq -c '[.[] | select(.path == "/v1/messages") | .response.replayed] | .[-2:]'   # [null,true]
curl -s -b jar "$API/alert-groups/$G2/timeline" | jq '[.items[] | select(.event == "possible_duplicate")] | length'   # 0
MSG '[.[] | select(.envelope.title == "#2 CpuHigh")][0].edits | length'     # 1: the edit that follows a replay
# (the same with `/_fake/config {"replays": false}` gives 2 messages and one possible_duplicate: test/e2e/bridge_test.go)

# C-22.AC-7: rate_limited for the whole bot, then forbidden
curl -s -X POST $FB/faults -d '{"path":"/v1/messages/*","status":429,"problem":{"class":"rate_limited","scope":"bot"},
  "retry_after_seconds":7,"times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G/resolve > /dev/null; curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
sleep 9; curl -s $FB/requests | jq -c '[.[] | select(.method == "PATCH")] | .[-2:] | (.[1].at_ms - .[0].at_ms) / 1000 | . >= 7 and . < 8'   # true
curl -s -X POST $FB/faults -d '{"path":"/v1/messages/*","status":403,"problem":{"class":"forbidden","detail":"The bot was removed from this channel."},"times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
curl -s -b jar $API/destinations/$D | jq -c '.health | {state, reason}'   # {"state":"broken","reason":"The bot was removed from this channel."}
ADV 300; sleep 2; curl -s -b jar $API/destinations/$D | jq -r .health.state      # healthy (the probe's checkChannel)

# C-22.AC-8: the edit window
curl -s -X PUT $FB/config -d '{"limits":{"max_body_length":4000,"edit_max_age_seconds":600}}' > /dev/null
curl -s -X PUT $FAM/groups/b3 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Latency"}}' > /dev/null
curl -s -X PUT $FAM/groups/b3/alerts/k1 -d '{"labels":{"team":"br","pod":"k1"}}' > /dev/null
NOTIFY b3 '{"reason":"first notification"}'; sleep 1; G3=$(AG 'pod%3D%22k1%22'); ADV 660
curl -s "${H[@]}" -X POST $API/alert-groups/$G3/acknowledge > /dev/null; sleep 1
MSG '[.[] | select(.envelope.title == "#3 Latency")] | {n: length, s: [.[].envelope.status], notify: .[1].envelope.notify,
  p: (.[0].replies[-1].body | test("no longer updated. The current one: http://127.0.0.1:18094/m/"))}'
# {"n":2,"s":["firing","acknowledged"],"notify":false,"p":true}
curl -s -b jar "$API/alert-groups/$G3" | jq -r '.deliveries[0].state'          # delivered
curl -s -X PUT $FAM/groups/b3/alerts/k2 -d '{"labels":{"team":"br","pod":"k2"}}' > /dev/null
NOTIFY b3 '{"reason":"new alerts added"}'; ADV 61; sleep 1
MSG '[.[] | select(.envelope.title == "#3 Latency")] | {n: length, e: (.[1].edits | length), r: (.[1].replies | length)}'   # {"n":2,"e":0,"r":1}

# C-22.AC-11: no buttons, Thread messages only
curl -s -X PUT $FB/config -d '{"capabilities":{"thread_messages":"all"}}' > /dev/null
curl -s -X PUT $FAM/groups/b4 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Queue"}}' > /dev/null
curl -s -X PUT $FAM/groups/b4/alerts/q1 -d '{"labels":{"team":"br","pod":"q1"}}' > /dev/null
NOTIFY b4 '{"reason":"first notification"}'; sleep 1
MSG '[.[] | select(.envelope.title == "#4 Queue")][0].envelope | {b: (.buttons | length), c: (.body | endswith("Reply in the Thread: ack · resolve · snooze 1h"))}'
# {"b":0,"c":true}

# C-22.AC-12: a test message
curl -s "${H[@]}" $API/destinations/$D/tests -d '{"source":"example"}' | jq -c '{ok, class: .error_class}'   # {"ok":true,"class":null}
MSG '.[-1].envelope | {kind, t: (.body | test("🧪 Test message"))}'          # {"kind":"test","t":true}
```

`test/e2e/bridge_test.go` covers the rest of C-22.AC-6, AC-7 (`transient`, `invalid`, `gone` on an edit and on a
reply), AC-9 and AC-10 against the fake bridge, with the development clock.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): deliver alert groups through bridges with idempotent posts and edit windows`.
- The pointer reply is part of the republication, not a lifecycle event: it has no `thread_replies` row and is not
  retried, so a bridge that fails it leaves only the new Root message, which the Alert Group page links to.
- The commands line is laid out by the adapter, not the renderer, because it depends on the Connection's `info`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-3 | partial | the Broken probe through `checkChannel` |
| C-22.FR-4 | full | |
| C-22.FR-5 | full | presses on the buttons are S-075 |
| C-22.FR-6 | full | Mentions of linked people are S-076 |
| C-22.FR-7 | full | |
| C-22.FR-8 | partial | delivery's use of the classification of S-073 |
| C-22.FR-9 | partial | the behaviour; the delivery state on the page is S-077 |
| C-22.FR-17 | partial | test messages and previews; the press on a test message is S-075 |
| C-22.FR-19 | full | |
| C-22.FR-23 | partial | Messengers → Bridge; Account links are S-076, "Write a bridge" S-079 |
| C-22.FR-24 | partial | `bridge_republished_past_edit_window` |
| C-22.AC-4 | full | |
| C-22.AC-5 | full | |
| C-22.AC-6 | full | |
| C-22.AC-7 | full | |
| C-22.AC-8 | full | |
| C-22.AC-9 | full | |
| C-22.AC-10 | full | |
| C-22.AC-11 | full | |
| C-22.AC-12 | partial | the press answer is S-075 |
