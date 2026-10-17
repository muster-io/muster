---
id: S-074
title: "Bridge delivery: envelopes in CommonMark, idempotent Publications, Thread replies, error classes, test messages and the Broken probe (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-080, S-082]
covers: [C-22.FR-2, C-22.FR-3, C-22.FR-4, C-22.FR-5, C-22.FR-6, C-22.FR-7, C-22.FR-8, C-22.FR-17, C-22.FR-19, C-22.FR-23, C-22.AC-3, C-22.AC-4, C-22.AC-5, C-22.AC-6, C-22.AC-7, C-22.AC-10, C-22.AC-11, C-22.AC-12]
files_touched:
  - api/openapi.yaml
  - internal/bridge/client.go
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/testmsg.go
  - internal/bridge/testmsg_test.go
  - internal/connections/query.sql
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/messages/message.go
  - internal/messages/safe.go
  - internal/messages/safe_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/mattermost/layout.go
  - internal/delivery/delivery.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/publication.go
  - internal/delivery/publication_test.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/query.sql
  - internal/delivery/lockorder_integration_test.go
  - design/db/schema.md
  - design/prd/l1/C-22-bridge.md
  - design/stories/coverage.md
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - docs/messengers/bridge.md
  - mkdocs.yml
  - test/e2e/bridge_test.go
acceptance:
  - "[C-22.FR-4, C-22.FR-5, C-22.AC-4] A new Alert Group posts one `postMessage` of the kind `root` with the status `firing`, `#d32f2f`, 🔴, the title `#N <title>`, the link \"Open in Muster\" to the Alert Group page, a CommonMark body with items 3 to 11 of C-12.FR-1, `notify` true, and the buttons Ack (`primary`), Resolve (`success`) and the Route's Snooze durations (`neutral`) with signed data of at most 64 bytes; Acknowledge through the API makes one `editMessage` with the acknowledged buttons and no Mentions."
  - "[C-22.FR-6, C-22.AC-4] A new Alert creates a `replyInThread` under the Root message with the `thread_ref` its `postMessage` returned, or null without one; the `thread_ref` each reply returns is stored and sent with the next reply; each reply carries its own `Idempotency-Key`, `reply-<Destination public_id>-<thread reply id>`."
  - "[C-22.FR-4, C-22.AC-5] A label value `@all *x* <b>y</b> [z](http://evil)` reaches the fake bridge as literal text: escaped for CommonMark, `@` neutralized, no link and no HTML (`safe_test.go`, Verification)."
  - "[C-22.FR-4] `previewTemplate` with the `format` `commonmark` renders a Root message template and a line template in CommonMark with alert data escaped, and answers the `format` `commonmark`; delivery renders the Root messages and Thread replies of Bridge Destinations in CommonMark (`render_test.go`)."
  - "[C-22.FR-6, C-22.AC-6] A Publication's key is `pub-<Destination public_id>-<delivery id>-<publications + 1>`, the same for every retry of that Publication. When the fake bridge posts a Publication and drops the connection before answering, the retry carries the same key, the fake holds one message, the answer is `replayed`, no possible duplicate is recorded, `published_at` and the Alert Group's first Publication are the time of the first attempt, and an `editMessage` follows with the current state — acknowledged when Acknowledge came before the retry; with replays turned off in the fake, the retry posts a second message and a possible duplicate is recorded as a delivery event."
  - "[C-22.FR-6] For Bridge Destinations the possible duplicate is decided by the outcome, not before the call: a retry after a started Publication records none when its answer is `replayed`, and records one when it is not; Mattermost and Telegram keep recording it before the retry (`publication_test.go`)."
  - "[C-22.FR-8, C-22.AC-7] `rate_limited` with `Retry-After: 7` delays the next call to that Destination by 7 to 8 seconds without counting an attempt, and with `scope` `bot` every Destination of the Connection; a `Retry-After` longer than `bridge.retry_after_max`, and a `429` without one, count as Transient; `transient` follows the Transient budget; `forbidden` makes the Destination Broken at once with the bridge's `detail` as the reason; `invalid` ends the delivery as Not delivered and leaves the Destination healthy; `gone` on an edit and on a Thread reply each start the deleted Root message flow once."
  - "[C-22.FR-2, C-22.FR-3, C-22.FR-8] A `407` of the Connection's proxy is `proxy_refused` in the Connection check and the Destination check, and Transient in delivery (`delivery_test.go`)."
  - "[C-22.FR-7, C-22.AC-10] With `max_body_length` 1,000, an Alert Group with 40 Alerts posts a body of at most 1,000 code points that keeps its link line, notices and footer; `truncated` from the bridge is accepted as delivered; the limit is read from the Destination's Connection, so two Connections with different limits shorten differently (`delivery_test.go`)."
  - "[C-22.FR-5, C-22.AC-11] Without `buttons` and `thread_messages` in `info`, a Root message has no buttons and no commands line; with `thread_messages` `all` and no `buttons`, a firing Root message ends with \"Reply in the Thread: ack · resolve · snooze 1h\", an acknowledged one with \"unack · resolve · snooze 1h\", a resolved one with none; with `mentions_only` the line starts \"Reply in the Thread, mentioning the bot:\"."
  - "[C-22.FR-17, C-22.AC-12] `testDestination` posts one `postMessage` of the kind `test` with \"🧪 Test message\" through the interactive path and changes no Timeline; `previewDestination` returns the envelope as the item `message` with the format `json`, without posting."
  - "[C-22.FR-3, C-22.FR-19] With a Bridge Destination Broken and nothing waiting, the probe after `delivery.broken_probe_interval` runs `checkChannel` in the delivery client class and ends the Broken state once the fake answers `200`."
  - "[C-22.AC-3] A failed `postMessage`, `editMessage`, `replyInThread` and test message leave the token in no log line and no error returned by the API (`secretleak.go` probe `bridge_connections`, the delivery paths added)."
  - "[C-22.FR-23] The page Messengers → Bridge is in the site's navigation and covers the topics of C-22.FR-23 that this story implements: the Connection, event delivery modes, Destinations by channel id, the Connection check and Broken Destinations; `make docs-check` passes."
verify: "make ci test-integration e2e docs-check"
operator_attention: false
issue: 232
---

# S-074. Bridge delivery: envelopes in CommonMark, idempotent Publications, Thread replies, error classes, test messages and the Broken probe (BE)

## Scope

**IN**

- Bridge delivery, the implementation of `delivery.Adapter` and `delivery.Checker` for Bridges: Publications, edits
  and Thread replies as envelopes, with `Idempotency-Key`s and `thread_ref`; the body fitted to the Connection's
  `max_body_length`; the answer classification of S-073 mapped to delivery's outcomes; the Broken probe through
  `checkChannel`.
- Possible duplicates decided by the outcome for Bridges, and the edit that follows a replayed Publication.
- The `commonmark` markup: escaping, the default message, built-in texts, Route templates and their previews.
- Buttons with style hints when the bridge has them; the commands line when it has only Thread messages.
- Test messages and previews of Bridge Destinations; the page Messengers → Bridge.

**OUT**

- Edit windows — republication past `edit_max_age_seconds`, the pointer reply, the `edit_window_closed` reason, the
  final edit as a Thread reply — and their section of the page (S-081).
- Presses, answers, Text commands, deleted-message events and the Transport `bridge` (S-075).
- Mentions of linked people and their names in footers (S-076).
- The pages (S-077); `muster bridge check` (S-078); the example bridge (S-085); "Write a bridge" (S-079).
- Interim states until later stories: Muster edits a Root message at any age, and a `gone` with the reason
  `edit_window_closed` starts the deleted Root message flow, until S-081 (the fake bridge declares
  `edit_max_age_seconds` null by default); a Mention of a User is their display name as text until S-076.

## Contracts

- **Bridge delivery** (`internal/bridge/delivery.go`, `envelope.go`): `Publish` → `postMessage` (`kind` `root` or
  `storm_summary`); `Update` → `editMessage` with the whole envelope, no Mentions, `notify` absent; `Reply` →
  `replyInThread` with `Root.ThreadAnchorID` — the last `thread_ref` received from the post or a reply, empty for
  `null` — as `thread_ref`. The envelope takes the status, colour and emoji of item 1 of C-12.FR-1, the heading as
  `title` and `link`, the buttons and the CommonMark body of items 3 to 11. `Outcome` gains `ThreadRef` (from
  `postMessage` or `replyInThread`, stored in `deliveries.thread_anchor_id` when present) and `Replayed`; `MessageURL`
  is the answer's `url` when `messages.SafeURL` accepts it, and empty otherwise; `schema.md` describes
  `thread_anchor_id` as Telegram's automatic copy or a Bridge's last `thread_ref`. The client of S-073 gains
  `postMessage`, `editMessage` and `replyInThread`. As for Mattermost and Telegram, lint 3 checks the calls of these
  adapter methods, not of the protocol client beneath them (ADR-0016).
- **Targets** (`internal/connections`): the Bridge delivery reads its target through `bridge.Targets`, which
  `connections.Service` implements as it does Mattermost's and Telegram's: `BridgeTarget` (query
  `GetBridgeDestinationTarget`) is the Destination's channel id and its Connection with the protocol client — of a
  deleted Destination too, for its final edit; `bridge.ErrNoTarget` when the Connection is deleted or not a Bridge — and
  `BridgeInfo` reads the Connection's `info` through the `InfoCache` of S-073. Delivery's protocol clients are cached
  per Connection version, rebuilt after the Connection changes; the checks and `muster doctor` keep building one per
  call.
- **Length** (C-22.FR-7): `delivery.Adapter` keeps its signature. The Bridge delivery fits each body itself, before the
  call, to `limits.max_body_length` of the Destination's Connection — read through the `InfoCache` of S-073, again past
  `bridge.info_max_age` — with the shortening of C-12.FR-11; its `LengthLimit()`, which delivery never calls, answers
  the protocol's floor, 500.
- **Idempotency keys** (C-22.FR-6): a Publication's key is `pub-<Destination public_id>-<delivery id>-<publications +
  1>`, from `destinations.public_id` and `deliveries.id`, the same for every retry of that Publication; a Thread reply's
  `reply-<Destination public_id>-<thread reply id>`, from `thread_replies.id`; a test message's
  `test-<Destination public_id>-<random 16 bytes in base64url>`, new for each test. The bridge scopes keys per token.
- **Possible duplicates and replays** (`publication.go`, `worker.go`, `outcomes.go`): `delivery` declares the optional
  interface `Idempotent` — `PublicationKey(d Destination, deliveryID, publication int64) string` and
  `ReplyKey(d Destination, replyID int64) string`, the keys above — which the Bridge delivery implements; the worker
  passes the key of a Publication or a Thread reply to an `Idempotent` adapter as `Call.Key`, empty for every other
  adapter. For an `Idempotent` adapter, the worker's prepare step records no possible duplicate before a retry of a
  started Publication; the outcome records it (C-11.FR-12) when the retry's answer is not `Replayed`. `Outcome` also
  gains `Unanswered`, which only the Bridge delivery sets: the request may have reached the messenger without its answer
  — a timeout, or a connection lost after the request was sent. `StartPublication` keeps the first attempt's
  `publication_started_at` across the retries of one Publication for an `Idempotent` adapter, and `RecordDeliveryRetry`
  keeps it only when that Publication had started before the attempt or its outcome is `Unanswered`: a failure the
  bridge answered stored nothing and a refused connection sent nothing, so neither leads to a possible duplicate; Not
  delivered clears it. A `Replayed` Publication records the message id, `thread_ref` and `url` and sets `published_at`
  from that first `publication_started_at`, so the edit window of S-081 counts from when the message was really
  posted, and passes the same time to `markPublished`, so the Alert Group's first Publication (C-17.FR-1), from which
  its ack timeout counts, is the real post too; it leaves `actual_hash` unset, so the delivery stays pending and the
  worker's next call is an edit to the latest Desired state. A Publication to an `Idempotent` adapter whose start is
  kept when its Destination leaves the Route is not withheld, since its Root message may exist: it is posted again
  with the same key, Quiet, carrying the final edit (C-11.FR-14). A `Replayed` answer is then edited to the final
  text; any other answer is itself the final message, and records a possible duplicate. The Mattermost and Telegram
  paths do not change.
- **Lock order** (D307; `lockorder_integration_test.go`): a `Replayed` outcome and the edit that follows it lock the
  delivery row only, as every outcome does, never its Alert Group before it; the test gains a replayed Publication
  against an acknowledgement that holds the Alert Group and then waits for the delivery in `Enqueue`.
- **Markup** (`internal/messages`, `internal/delivery/render.go`): `MarkupCommonMark`; `EscapeCommonMark` escapes every
  CommonMark punctuation that can start markup and line-leading block markers, after `Neutralize`; the default layout
  writes items 3 to 11 with block quotes for the label sections, a bullet list for the Alerts and strikethrough for
  resolved ones; built-in texts in English and Russian for the commands line and "🧪 Test message"; delivery's
  `markupOf` maps the type `bridge` to `MarkupCommonMark`, which otherwise falls to plain text, so the Root messages and
  Thread replies of Bridges are rendered in CommonMark; `previewTemplate` accepts the `format` `commonmark` and answers
  it (spec: the `format` enums of `TemplatePreviewRequest` and `TemplatePreviewResult` gain it). The colours of the
  statuses move from `internal/mattermost/layout.go` to `internal/messages` (`messages.StatusColour`, beside
  `statusEmoji`), which Mattermost and the Bridge envelope read.
- **Buttons and commands line** (C-22.FR-5): delivery renders the buttons of the status as for every messenger; the
  Bridge delivery keeps them only when `info` has `buttons`, drops Snooze durations from the longest beyond
  `buttons.max_per_message`, and sets the style hints. Without `buttons` and with `thread_messages`, it appends the
  commands line of the status, `messages.CommandsLine`; resolved messages and Storm summaries get none. Both need the
  Route's Snooze durations, which a Snooze button names only by index: `messages.Message` gains `SnoozeSeconds`
  (`snooze_seconds`, `route.snooze_durations`), set only when the message is rendered in `MarkupCommonMark`, so the
  Desired states of the other markups, and their hashes, do not change.
- **Error mapping** (C-22.FR-8): `transient` → `OutcomeTransient`, `rate_limited` → `OutcomeRetryAfter` with
  `ScopeConnection` for `scope` `bot` — `OutcomeTransient` when `Retry-After` is missing or above
  `bridge.retry_after_max` — `gone` → `OutcomeGone` (on a reply too: the Root message or its Thread is gone, the deleted
  Root message flow), `forbidden` → `OutcomeFatal` with `detail`, `invalid` → `OutcomeUnknown`. S-081 adds the outcome
  of `gone` with `edit_window_closed`. A `407` of the Connection's proxy is no answer of the bridge (D338), as for
  Telegram: the client reports it as a proxy refusal without a status, `MessageProxyRefused`, which is
  `OutcomeTransient` in delivery and `proxy_refused` in the Connection check and the Destination check; the protocol's
  mapping of the bridge's own answers does not change.
- **Probe and check**: the Bridge delivery's `Check` is S-080's `checkChannel` check, run by the Broken probe in the
  delivery class (C-11.FR-9).
- **Test and preview** (`testmsg.go`): `bridge.Tester` makes one `postMessage` of the kind `test` through the
  interactive path for `testDestination`; `previewDestination` returns the envelope as JSON in the item `message` with
  the format `json`.
- **Fake bridge** (`messages.go`): `postMessage` (no `thread_ref`), `editMessage`, `replyInThread` (a `thread_ref`
  `t-<root id>`), the idempotency store per token with `replayed`, `/_fake/messages` (each with its envelope, edits and
  replies, and `url` `http://127.0.0.1:18094/m/<id>`), `DELETE /_fake/messages/{id}` (a person deletes),
  `/_fake/config` `replays` (default on) and `limits`. The faults of the fake-server harness script the rest: a
  problem with a class is a fault's `body` with the `content_type` `application/problem+json`, and a Publication whose
  answer is lost is a fault with `delay_ms` longer than the client waits, which posts and then holds the answer.
- **Page** (`docs/messengers/bridge.md`, `mkdocs.yml`): Messengers → Bridge for Admins — the Connection with its token
  and event delivery mode, Destinations by channel id, what the Connection check shows, test messages and Broken
  Destinations, with a link to Messengers → Account links, whose Bridge section S-076 adds; S-081 adds edit windows,
  S-079 links Write a bridge.
- **Secrets** (lint 5): probe `bridge_connections` also pushes the token through a failed `postMessage`,
  `editMessage`, `replyInThread` and test message, with the fake closing the connection and answering a problem.
- **Defaults**: `bridge.retry_after_max`, `bridge.info_max_age`, `delivery.transient_backoff`,
  `delivery.transient_budget`, `delivery.broken_probe_interval`, `delivery.interactive_budget`.

## Steps

1. Add `commonmark`: escaping, the default layout, texts, the moved colours, previews and delivery's markup of the
   type. Check: `safe_test.go`, both `render_test.go`.
2. Write the envelope and the Bridge delivery with the fake's messages. Check: `envelope_test.go`,
   `delivery_test.go`.
3. Wire idempotency keys, replays, possible duplicates and `thread_ref`. Check: `publication_test.go`,
   `outcomes_test.go`, `threads_test.go`, `go test -tags integration -run LockOrder ./internal/delivery/`.
4. Add the test message, the probe, the secret probe's delivery paths, the page and the end-to-end test. Check:
   `make lint-arch`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; an Integration "lab" with its fake receiver, NOTIFY, ADV and AG as in
# S-020 and S-028; the On-call policy in P as in S-025, with its ack timeout and Reminders off so that no other Thread
# reply comes; the Connection "br" (C) as in S-073 and the Destination "alerts" on ch-alerts (D) as in S-080. Messages
# are picked by the end of their title, so the database need not be fresh.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FB=127.0.0.1:18094/_fake
MSG() { curl -s $FB/messages | jq -c "$1"; }
# a problem answered by the fake: path, status, the problem's members, and Retry-After seconds
FAULT() { jq -cn --arg p "$1" --argjson s "$2" --argjson b "$3" --argjson ra "${4:-0}" '{path: $p, status: $s,
  retry_after_seconds: $ra, content_type: "application/problem+json", body: ($b | tojson), times: 1}' |
  curl -s -X POST $FB/faults -d @- > /dev/null; }
R=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"br\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"br\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" | jq -r .id)

# C-22.AC-4, AC-5: a new Alert Group with a hostile label value
curl -s -X PUT $FAM/groups/b1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"DiskFull"}}' > /dev/null
curl -s -X PUT $FAM/groups/b1/alerts/i1 -d '{"labels":{"team":"br","pod":"i1","note":"@all *x* <b>y</b> [z](http://evil)"}}' > /dev/null
NOTIFY b1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'pod%3D%22i1%22')
MSG '[.[] | select(.envelope.title | endswith(" DiskFull"))][0].envelope | {kind, status, color, emoji, title, l: .link.label, notify, b: [.buttons[] | "\(.label):\(.style)"], d: ([.buttons[].data | length] | max <= 64)}'
# {"kind":"root","status":"firing","color":"#d32f2f","emoji":"🔴","title":"#N DiskFull","l":"Open in Muster","notify":true,
#  "b":["Ack:primary","Resolve:success","Snooze 1 h:neutral","Snooze 4 h:neutral","Snooze 24 h:neutral"],"d":true}
MSG '[.[] | select(.envelope.title | endswith(" DiskFull"))][0].envelope.body | contains("@​all \\*x\\* \\<b\\>y\\</b\\> \\[z\\](http://evil)")'   # true
curl -s $FB/requests | jq -r '[.[] | select(.path == "/v1/messages")][0].headers["Idempotency-Key"][0]' | grep -cE '^pub-DS[0-9A-Z]{12}-[0-9]+-1$'   # 1

# C-22.AC-4: Acknowledge edits; new Alerts go to the Thread with thread_ref
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
MSG '[.[] | select(.envelope.title | endswith(" DiskFull"))][0].edits[-1] | {status, b: [.buttons[].label], m: has("mentions")}'
# {"status":"acknowledged","b":["Unack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"],"m":false}
for i in i2 i3; do curl -s -X PUT $FAM/groups/b1/alerts/$i -d "{\"labels\":{\"team\":\"br\",\"pod\":\"$i\"}}" > /dev/null
  NOTIFY b1 '{"reason":"new alerts added"}'; ADV 61; sleep 1; done
curl -s $FB/requests | jq -c '[.[] | select(.path | endswith("/thread-replies")) | .body | fromjson | .thread_ref]'
# [null,"t-<root id>"]
curl -s $FB/requests | jq -r '[.[] | select(.path | endswith("/thread-replies")) | .headers["Idempotency-Key"][0]] | .[]' | grep -cE '^reply-DS[0-9A-Z]{12}-[0-9]+$'   # 2

# C-22.AC-6: a Publication whose answer is lost, with and without replays
curl -s -X POST $FB/faults -d '{"path":"/v1/messages","delay_ms":12000,"times":1}' > /dev/null   # posts, answers too late
curl -s -X PUT $FAM/groups/b2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CpuHigh"}}' > /dev/null
curl -s -X PUT $FAM/groups/b2/alerts/j1 -d '{"labels":{"team":"br","pod":"j1"}}' > /dev/null
NOTIFY b2 '{"reason":"first notification"}'; sleep 11; ADV 3; sleep 2; G2=$(AG 'pod%3D%22j1%22')
MSG '[.[] | select(.envelope.title | endswith(" CpuHigh"))] | length'            # 1
curl -s $FB/requests | jq -c '[.[] | select(.path == "/v1/messages")] | .[-2:] | map(.headers["Idempotency-Key"][0]) | .[0] == .[1]'   # true
curl -s -b jar "$API/alert-groups/$G2/timeline" | jq '[.items[] | select(.kind == "delivery" and .delivery_event == "possible_duplicate")] | length'   # 0
MSG '[.[] | select(.envelope.title | endswith(" CpuHigh"))][0].edits | length'     # 1: the edit that follows a replay
# (the same with `/_fake/config {"replays": false}` gives 2 messages and one possible_duplicate: test/e2e/bridge_test.go)

# C-22.AC-7: rate_limited for the whole bot, then forbidden
FAULT '/v1/messages/*' 429 '{"class":"rate_limited","scope":"bot"}' 7
curl -s "${H[@]}" -X POST $API/alert-groups/$G/resolve > /dev/null; curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
# both edits wait for the bot's hold and go out together, each 7 to 8 seconds after the 429
sleep 9; curl -s $FB/requests | jq -c '[.[] | select(.method == "PATCH")] | (map(.status) | rindex(429)) as $i | .[$i].at_ms as $t |
  [.[$i + 1:][] | (.at_ms - $t) / 1000] | length > 0 and all(. >= 7 and . < 8)'   # true
FAULT '/v1/messages/*' 403 '{"class":"forbidden","detail":"The bot was removed from this channel."}'
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
curl -s -b jar $API/destinations/$D | jq -c '.health | {state, reason}'   # {"state":"broken","reason":"The bot was removed from this channel."}
ADV 300; sleep 2; curl -s -b jar $API/destinations/$D | jq -r .health.state      # healthy (the waiting edit was the probe)

# C-22.AC-10: a long Alert Group against a short limit
curl -s -X PUT $FB/config -d '{"limits":{"max_body_length":1000,"edit_max_age_seconds":null}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null                   # reads info again
curl -s -X PUT $FAM/groups/b5 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Many"}}' > /dev/null
# past 10 Alerts the list is one line of distinct values per differing label, so four long ones make about 1,450
# code points, which the limit shortens
for i in $(seq 1 40); do curl -s -X PUT $FAM/groups/b5/alerts/m$i -d "{\"labels\":{\"team\":\"br\",\"pod\":\"m$i\",
  \"instance\":\"node-$i.region-a.example.org:9100\",\"host\":\"host-$i.rack-b.datacenter-c.example.org\",
  \"disk\":\"/dev/disk/by-id/nvme-sample-serial-0000$i\"}}" > /dev/null; done
NOTIFY b5 '{"reason":"first notification"}'; sleep 1
MSG '[.[] | select(.envelope.title | endswith(" Many"))][0].envelope | {n: (.body | length <= 1000), s: (.body | contains("+30 more") | not), o: (.body | contains("Open in Muster")), l: .link.label, b: (.buttons | length > 0)}'
# {"n":true,"s":true,"o":true,"l":"Open in Muster","b":true}
curl -s -X PUT $FB/config -d '{"limits":{"max_body_length":4000,"edit_max_age_seconds":null}}' > /dev/null

# C-22.AC-11: no buttons, Thread messages only
curl -s -X PUT $FB/config -d '{"capabilities":{"thread_messages":"all"}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null
curl -s -X PUT $FAM/groups/b4 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Queue"}}' > /dev/null
curl -s -X PUT $FAM/groups/b4/alerts/q1 -d '{"labels":{"team":"br","pod":"q1"}}' > /dev/null
NOTIFY b4 '{"reason":"first notification"}'; sleep 1; G4=$(AG 'pod%3D%22q1%22')
MSG '[.[] | select(.envelope.title | endswith(" Queue"))][0].envelope | {b: (.buttons | length), c: (.body | endswith("Reply in the Thread: ack · resolve · snooze 1h"))}'
# {"b":0,"c":true}

# C-22.AC-12: a test message and a preview
BEFORE=$(curl -s -b jar "$API/alert-groups/$G4/timeline" | jq '.items | length')
curl -s "${H[@]}" $API/destinations/$D/tests -d '{"source":{"kind":"example"}}' | jq -c '[.steps[] | {name, error_class}]'   # [{"name":"message","error_class":"none"}]
MSG '.[-1].envelope | {kind, t: (.body | test("🧪 Test message")), n: .notify}'   # {"kind":"test","t":true,"n":false}
curl -s "${H[@]}" $API/destinations/$D/previews -d '{"source":{"kind":"example"}}' | jq -c '.items[0] | {name, format, k: (.text | fromjson | .kind)}'
# {"name":"message","format":"json","k":"test"}
[ "$(curl -s -b jar "$API/alert-groups/$G4/timeline" | jq '.items | length')" = "$BEFORE" ] && echo timeline-unchanged

# C-22.FR-3, FR-19: with nothing waiting, the Broken probe is checkChannel in the delivery client class
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
  AND ds.org_id = d.org_id WHERE ds.public_id = '$D' AND d.state = 'pending'"                     # 0
psql "$MUSTER_DATABASE_URL" -qc "UPDATE destinations SET health = 'broken', broken_since = now(), broken_cause = 'fatal',
  broken_reason = 'probe test', next_probe_at = now() WHERE public_id = '$D'"
curl -s -X DELETE $FB/requests; ADV 1; sleep 2
curl -s $FB/requests | jq -c '[.[] | .path]'                                       # ["/v1/channels/ch-alerts/check"]
curl -s -b jar $API/destinations/$D | jq -r .health.state                          # healthy

# C-22.FR-4: a template preview in CommonMark; C-22.AC-3: no token in the log after the faults above
curl -s "${H[@]}" $API/template-previews -d '{"kind":"root_message","template":"","format":"commonmark"}' | jq -r .format   # commonmark
grep -c 'muster-dev-bridge-token-b' dev.log                                       # 0
```

`test/e2e/bridge_test.go` covers the rest of C-22.AC-6 (replays off), AC-7 (`transient`, `invalid`, `gone` on an edit
and on a reply) and AC-10 against the fake bridge, with the development clock.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): deliver alert groups through bridges with idempotent posts`.
- S-074 was split by the plan review of the Bridge phase (D323), because it touched about 45 files: S-074 keeps the
  envelope, Publications, edits, Thread replies, error classes, test messages and the probe; S-081 takes the edit
  windows. The file name is kept.
- The commands line is laid out by the Bridge delivery, not the renderer, because it depends on the Connection's
  `info`.
- `published_at` from the first attempt matters only for bridges with an edit window: a replay found after a crash
  must not move the window later than the real post.
- The style hints of the buttons (D329) come from `buttons.StyleOf` of S-082, which Mattermost and Telegram already use;
  hence S-082 in `depends_on`.
- D338: a `407` of the Connection's proxy is `proxy_refused` in the Connection check and the Destination check, as in
  Telegram, and Transient in delivery; S-073 and S-080 reported it as `request_failed`.
- The Test and Preview card of the Destination page stays hidden for Bridges until S-077, which owns
  `destination-preview-panel.tsx`, `destination-test-panel.tsx` and the page.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-2 | partial | a `407` of the Connection's proxy is `proxy_refused` (D338) |
| C-22.FR-3 | partial | the Broken probe through `checkChannel`; `proxy_refused` for a `407` of the proxy (D338) |
| C-22.FR-4 | full | |
| C-22.FR-5 | full | presses on the buttons are S-075 |
| C-22.FR-6 | full | Mentions of linked people are S-076 |
| C-22.FR-7 | full | |
| C-22.FR-8 | partial | delivery's use of the classification of S-073 and a `407` of the proxy (D338); `edit_window_closed` is S-081 |
| C-22.FR-17 | partial | test messages and previews; the press on a test message is S-075 |
| C-22.FR-19 | full | |
| C-22.FR-23 | partial | Messengers → Bridge; edit windows are S-081, Account links S-076, "Write a bridge" S-079 |
| C-22.AC-3 | partial | the token on the delivery paths; the Connection check is S-073, the Destination check S-080, the event paths S-075 |
| C-22.AC-4 | full | |
| C-22.AC-5 | full | |
| C-22.AC-6 | full | |
| C-22.AC-7 | full | |
| C-22.AC-10 | full | |
| C-22.AC-11 | full | |
| C-22.AC-12 | partial | the press answer is S-075 |
