---
id: S-099
title: "Slack delivery: messages in blocks, mrkdwn, buttons and the Snooze menu, edits, Thread replies, deleted Root messages, error classes and test messages (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-098, S-093]
covers: [C-24.FR-3, C-24.FR-4, C-24.FR-5, C-24.FR-6, C-24.FR-7, C-24.FR-8, C-24.FR-13, C-24.FR-14, C-24.FR-15, C-24.AC-5, C-24.AC-6, C-24.AC-7, C-24.AC-8, C-24.AC-9, C-24.AC-15, C-24.AC-16, C-12.FR-7, C-16.FR-3]
files_touched:
  - api/openapi.yaml
  - internal/slack/adapter.go
  - internal/slack/adapter_test.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/slack/testmsg.go
  - internal/slack/testmsg_test.go
  - internal/slack/client.go
  - internal/slack/classify.go
  - internal/slack/classify_test.go
  - internal/messages/message.go
  - internal/messages/safe.go
  - internal/messages/safe_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/preview.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/delivery/delivery.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/live_test.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/query.sql
  - internal/destinations/test.go
  - internal/destinations/preview.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/api/templates.go
  - internal/api/templates_test.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/messages.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/runtime/runtime.go
  - internal/archlint/secretleak.go
  - test/e2e/slack_test.go
  - web/src/components/template-preview.tsx
acceptance:
  - "[C-24.FR-4, C-24.AC-5] A new Alert Group creates, through the delivery worker, one `chat.postMessage` whose `text` and top `section` are the summary line — the status emoji, `#N` and the title — with one attachment in the firing colour whose blocks are a `section` with the heading linked to the Alert Group page, a `section` with the body whose line of links starts with \"Open in Muster\", a `context` with \"Muster v<version>\" and no image, and an `actions` block; the request names `blocks` and `attachments` and sets `unfurl_links` and `unfurl_media` to `false`; no block is `rich_text` or `markdown` (`layout_test.go`, a golden request per status)."
  - "[C-24.FR-5, C-24.AC-5] A firing Root message carries Ack (`primary`), Resolve and the `static_select` \"Snooze…\" whose options are the Route's durations and then the Snooze presets; an acknowledged one Unack, Resolve (`primary`) and the menu; a snoozed one Ack (`primary`), Unsnooze and Resolve; a resolved one no `actions` block; a Reminder \"Still on it\" (`primary`) and Unack; no message has two `primary` buttons or any `danger`; every `value` is a signed action id of at most 150 bytes and every `action_id` is unique in its block (`layout_test.go`)."
  - "[C-24.FR-6, C-24.AC-5] Acknowledge through the API edits the Root message with `chat.update` of its `ts`, naming both `blocks` and `attachments`, Quietly and without Mentions; a new Alert creates a Thread reply with the Root message's `ts` as `thread_ts` and no `reply_broadcast`; a Thread reply and a Storm summary have their text in a `section`, a short `text` and `attachments: []`, and a test message is laid out as a Root message."
  - "[C-24.FR-7, C-24.AC-6, C-12.FR-7] A label value `@channel <!here> <b>x</b> *y* <https://evil.example|z> &` reaches the fake server as `@​channel &lt;!here&gt; &lt;b&gt;x&lt;/b&gt; *​y*​ &lt;https://evil.example|z&gt; &amp;`, with no Mention, link or emphasis; `previewTemplate` takes the markup `mrkdwn` and renders a Route's Root message template and line template in it with the same escaping (`safe_test.go`, `templates_test.go`)."
  - "[C-24.FR-4, C-24.AC-7] An Alert Group with 40 Alerts reaches the fake server with a body `section` of at most 3,000 code points, counted on the mrkdwn as sent, that ends with the line of links, the notices and the footer and cuts the Alert list with \"…and N more\"; an Alert Group whose title is 200 emoji gets a `text` whose title is cut to 60 code points and which stays under 4,000 bytes when every emoji is counted as its shortcode; Mentions that do not fit in 1,000 bytes of `text` stay in the top `section` (`layout_test.go`)."
  - "[C-24.FR-8, C-24.AC-8] Against the fake server: a `429` with `Retry-After: 1` delays the next request through that Connection, to any of its Destinations, by 1 to 2 seconds and is not counted as an attempt; `not_in_channel` makes the Destination Broken at once with the reason named; `invalid_blocks` on a Publication sends the same message as plain `text` within 3,000 bytes and records the delivery event `markup_rejected`; `edit_window_closed` on an edit ends it as Not delivered and leaves the Destination healthy."
  - "[C-24.FR-8, C-24.AC-8] An edit answered `message_not_found` while `conversations.info` shows the bot a member of the channel publishes the Alert Group again once, Quietly, with \"The previous message was deleted at HH:MM\", and leaves the Destination healthy; with the bot no longer a member it makes the Destination Broken; a Thread reply answered `ok` without `message.thread_ts` is deleted with `chat.delete` at once, and the Alert Group is published again once; a second deletion marks the pair \"deleted in the messenger\" (`adapter_test.go`, `live_test.go`)."
  - "[C-24.FR-3, C-24.FR-15, C-24.AC-9] A new Alert Group meets `not_in_channel` because the bot was removed in the fake server, and resolves while the Destination is Broken; after the bot is added back, the next probe — the Destination check of S-098 — ends the Broken state and resolves `MusterDestinationBroken` with no new Alert Group, its `conversations.info` counted in `muster_client_requests_total` under the delivery client class."
  - "[C-24.FR-6, C-24.AC-15] A Destination that mentions `here` for new Alert Groups posts a Loud Root message with `<!here>` in `text` and in the top `section` and none in the attachment; a chosen User without a Slack Account link is named by their display name as text; a Quiet Thread reply carries no Mention."
  - "[C-24.FR-13, C-24.AC-16, C-16.FR-3] `testDestination` of a Slack Destination posts one message marked \"🧪 Test message\" with the buttons of its status through the interactive path, changes no Alert Group's Timeline, and returns the one step `message`; `previewDestination` returns the request Muster would send, the token masked."
  - "[C-24.FR-14] The Request log records each Publication, edit, Thread reply, deletion of a misplaced reply, test message and probe with the method as its operation, the Destination and the Alert Group linked, and the bot token masked."
verify: "make ci test-integration e2e-go"
operator_attention: true
issue: null
---

# S-099. Slack delivery: messages in blocks, mrkdwn, buttons and the Snooze menu, edits, Thread replies, deleted Root messages, error classes and test messages (BE)

## Scope

**IN**

- The Slack adapter of the delivery worker: Publications, edits, Thread replies, Storm summaries and final edits, laid
  out as C-24.FR-4 says, with the buttons and the Snooze menu of C-24.FR-5 — rendered and signed, not yet pressable.
- The markup `mrkdwn` with its escaping (C-24.FR-7), in delivery and in template previews.
- The response mapping of C-24.FR-8 in delivery, with deleted Root messages and the misplaced reply, the plain-text
  fallback for rejected markup and the Broken probe.
- Mentions of everyone in the chat and of Users without an Account link; test messages and previews.

**OUT**

- Presses over HTTP, the answers to people and the press on a test message (S-100); Socket Mode (S-101).
- Mentions of linked Users and the footer's names of linked Users (S-102).
- The Destination page's form and the test panel's note about presses (S-103); the `mrkdwn` option in the Route
  editor's preview (S-103).

## Contracts

- **Spec** (`api/openapi.yaml`): the markup enums of `TemplatePreviewRequest` and of the preview's answer gain
  `mrkdwn`, described as Slack's; `testDestination` and `previewDestination` describe the Slack type — the step
  `message` alone, and the request as the JSON body of `chat.postMessage`.
- **Operations**: `testDestination` and `previewDestination` for the type (`destinations:write`, interactive for the
  test); `previewTemplate` with `mrkdwn`.
- **Adapter** (`internal/slack/adapter.go`): implements `delivery.Adapter` — `Publish` (`chat.postMessage`), `Update`
  (`chat.update`), `Reply` (`chat.postMessage` with `thread_ts`) and `LengthLimit` 3,000 — with the message id the
  Root message's `ts`; the outcome of each answer by `Classify` of S-097 and these rules: `message_not_found` on an
  edit reads `conversations.info` in the same client class, then Gone (member, not archived) or Fatal; a reply answered
  without `message.thread_ts` is deleted with `chat.delete` and returned as Gone; MarkupRejected sends the message again
  as `text` alone, rendered plain and cut to 3,000 bytes, and returns MarkupRejected with the outcome of that send;
  `edit_window_closed` and `cant_update_message` are Unknown. The delivery's `message_url` is
  `<workspace url>archives/<channel id>/p<ts without its dot>`, from the `slack_team_url` the check stored, without a
  request; none while the check has not run. Every send and edit goes through the delivery worker or the interactive
  path, as lint 3 checks.
- **Layout** (`internal/slack/layout.go`): the structure of C-24.FR-4 and C-24.FR-5 — `text` and the top `section`
  with the summary line and, for a Loud message, the Mentions; the attachment's colour from `messages.StatusColour`;
  the heading `section`; the body `section` of items 3 to 11 in `mrkdwn`; the `context` with "Muster v<version>" and no
  image; the `actions` block with `block_id`
  `muster_actions`, the `action_id`s `ack`, `unack`, `resolve`, `unsnooze`, `snooze` (the menu) and `still_on_it`, and
  the labels in the Route's language, the menu's placeholder "Snooze…" («Отложить…»). The title in `text` is cut to 60
  code points, and Mentions that do not fit in 1,000 bytes of `text` are left out of it.
- **Messages** (`internal/messages`): `MarkupMrkdwn` beside the other markups, with its layout of items 3 to 11 and
  `EscapeMrkdwn` — `&`, `<`, `>` as entities, a zero-width space after `@`, `*`, `_`, `~` and `` ` `` in alert data —
  used by the default message, the Route's templates and the Fallback template; the length counted on the escaped
  string. `delivery/render.go` renders a Slack Destination in `mrkdwn`.
- **Mentions** (`internal/mentions/mentions.go`): for a Slack Destination, `channel` and `here` render as `<!channel>`
  and `<!here>`; a chosen User without a Slack link for the Connection renders as their display name, escaped.
- **Connections** (`internal/connections/slack.go`, `query.sql`): `SlackTarget` — the client, the channel and the
  bot's name of a Destination for the adapter, cached per Connection version as the other types do.
- **Tests and previews** (`internal/slack/testmsg.go`, `internal/destinations/test.go`, `preview.go`): the test message
  of C-24.FR-13 through the interactive path, which never presses anything; the preview as the request body.
- **Fake Slack server** (`internal/fakes/fakeslack/messages.go`): `chat.postMessage`, `chat.update`, `chat.delete`,
  `chat.postEphemeral` (answered here, used by S-100) and `chat.getPermalink`, keeping each channel's messages and
  Threads; the rules of F-074 to F-091 that Muster relies on — the block limits with `invalid_blocks`, a `text` over
  4,000 bytes on an edit with `msg_too_long`, `primary` and `danger` only, `block_mismatch`, a reply to a deleted root
  without replies posted into the channel without `thread_ts`, a tombstone for a deleted root with replies, and
  `not_in_channel` for a channel the bot left; `/_fake/messages` lists them, and `/_fake/delete` deletes one as a
  person would.
- **Template preview** (`web/src/components/template-preview.tsx`): a `mrkdwn` branch wherever the type check asks
  for one after the spec change; the option in the Route editor comes with S-103.
- **Secrets** (lint 5): the probe `slack_connections` gains a Publication and an edit that fail.
- **Defaults**: `destination.slack.limiter`, `connection.slack.limiter`, `delivery.broken_probe_interval`,
  `delivery.transient_budget`, `message.alerts_listed`.

## Steps

1. Add `mrkdwn` to messages and template previews. Check: `safe_test.go`, `render_test.go`, `templates_test.go`.
2. Write the layout with its buttons and limits. Check: `layout_test.go` with golden requests per status, for a
   Reminder, a Storm summary, a test message and the Loud and Quiet variants.
3. Write the adapter with the response mapping and the deleted-message rules, and register it with the worker. Check:
   `adapter_test.go`, `classify_test.go`, `go test -tags integration -run TestLive ./internal/delivery/`.
4. Extend the fake server, the test message and the preview, and the end-to-end test. Check: `fakeslack_test.go`,
   `make e2e-go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; an Integration "lab" with its fake receiver, NOTIFY, ADV and AG as in
# S-020 and S-028; the On-call policy in P as in S-025, with its ack timeout and Reminders off; the Connection "sl" (C)
# and the Destination "alerts" on C0FAKEALERTS (D) as in S-098. Messages are picked by their summary line, so the
# database need not be fresh.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FS=127.0.0.1:18095/_fake
R=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"sl\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"sl\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" | jq -r .id)
# FIRE <group> <alertname> <pod> [note]: one firing Alert in a new Alertmanager group, notified at once
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"$2\"}}" > /dev/null
  curl -s -X PUT $FAM/groups/$1/alerts/$3 -d "$(jq -cn --arg p "$3" --arg n "${4:--}" '{labels: {team: "sl", pod: $p, note: $n}}')" > /dev/null
  NOTIFY $1 '{"reason":"first notification"}'; sleep 1; }
MSG() { curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -c "[.[] | select(.text | contains(\"$1\"))][0] | $2"; }
# FAULT <method> <status> <error> [retry-after]: one answer of the fake as Slack gives it
FAULT() { jq -cn --arg p "/api/$1" --argjson s "$2" --arg e "$3" --argjson ra "${4:-0}" '{path: $p, status: $s,
  retry_after_seconds: $ra, content_type: "application/json", body: ({ok: false, error: $e} | tojson), times: 1}' |
  curl -s -X POST $FS/faults -d @- > /dev/null; }

# C-24.FR-4, FR-5, AC-5: the Root message
FIRE s1 DiskFull i1 '@channel <!here> <b>x</b> *y* <https://evil.example|z> &'; G=$(AG 'pod%3D%22i1%22')
MSG DiskFull '{top: .blocks[0].type, colour: .attachments[0].color, b: [.attachments[0].blocks[].type],
  a: [.attachments[0].blocks[-1].elements[] | {type, style, t: (.text.text // .placeholder.text)}], u: [.unfurl_links, .unfurl_media]}'
# {"top":"section","colour":"#d32f2f","b":["section","section","context","actions"],
#  "a":[{"type":"button","style":"primary","t":"Ack"},{"type":"button","style":null,"t":"Resolve"},{"type":"static_select","style":null,"t":"Snooze…"}],"u":[false,false]}
MSG DiskFull '[.attachments[0].blocks[-1].elements[2].options[].text.text]'
# ["1h","4h","24h","Until tomorrow 09:00","Until Monday 09:00"]
MSG DiskFull '.attachments[0].blocks[1].text.text | [contains("Open in Muster"), contains("&lt;!here&gt;"), contains("<https://evil")]'
# [true,true,false]

# C-24.FR-6: an edit and a Thread reply
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
curl -s $FS/requests | jq -c '[.[] | select(.path == "/api/chat.update")][-1].body | fromjson | {b: has("blocks"), a: has("attachments"), m: (.text | contains("<"))}'
# {"b":true,"a":true,"m":false}
MSG DiskFull '[.attachments[0].blocks[-1].elements[] | {t: (.text.text // .placeholder.text), style}]'
# [{"t":"Unack","style":null},{"t":"Resolve","style":"primary"},{"t":"Snooze…","style":null}]
curl -s -X PUT $FAM/groups/s1/alerts/i2 -d '{"labels":{"team":"sl","pod":"i2"}}' > /dev/null
NOTIFY s1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
MSG DiskFull '.replies | length'                                                # 1

# C-24.FR-8, AC-8: a Root message deleted in the client, found by the next edit
TS=$(MSG DiskFull '.ts' | tr -d '"'); curl -s -X POST $FS/delete -d "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\"}" > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null; sleep 2
MSG DiskFull '.attachments[0].blocks[1].text.text | test("The previous message was deleted at [0-9]{2}:[0-9]{2}")'   # true
curl -s -b jar $API/destinations/$D | jq -r .health.state                      # healthy
# a Root message deleted before any reply: the reply lands in the channel and is deleted at once
FIRE s2 CpuHigh j1; G2=$(AG 'pod%3D%22j1%22'); TS=$(MSG CpuHigh '.ts' | tr -d '"')
curl -s -X POST $FS/delete -d "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\"}" > /dev/null
curl -s -X PUT $FAM/groups/s2/alerts/j2 -d '{"labels":{"team":"sl","pod":"j2"}}' > /dev/null
NOTIFY s2 '{"reason":"new alerts added"}'; ADV 61; sleep 2
curl -s $FS/requests | jq '[.[] | select(.path == "/api/chat.delete")] | length >= 1'   # true
curl -s -b jar "$API/alert-groups/$G2/timeline" | jq '[.items[] | select(.kind == "delivery" and .delivery_event == "republished")] | length'   # 1

# C-24.FR-8, AC-8: 429 for the Connection, markup rejected, an edit window
FAULT chat.update 429 ratelimited 1
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
sleep 3; curl -s $FS/requests | jq -c '[.[] | select(.path == "/api/chat.update")] | (map(.status) | rindex(429)) as $i | .[$i].at_ms as $t |
  [.[$i + 1:][] | (.at_ms - $t) / 1000] | length > 0 and all(. >= 1 and . < 2)'   # true
FAULT chat.postMessage 200 invalid_blocks
FIRE s3 MemHigh k1; G3=$(AG 'pod%3D%22k1%22'); MSG MemHigh '{blocks: (.blocks | length), t: (.text | length > 0)}'   # {"blocks":0,"t":true}
curl -s -b jar "$API/alert-groups/$G3/timeline" | jq '[.items[] | select(.delivery_event == "markup_rejected")] | length'   # 1
FAULT chat.update 200 edit_window_closed
curl -s "${H[@]}" -X POST $API/alert-groups/$G3/acknowledge > /dev/null; sleep 2
curl -s -b jar "$API/alert-groups/$G3/deliveries" | jq -r '.items[0].state'    # not_delivered

# C-24.FR-15, AC-9: Broken and the probe
curl -s -X PUT $FS/config -d '{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":false}}}}' > /dev/null
FIRE s4 NetDown l1; sleep 1; curl -s -b jar $API/destinations/$D | jq -r .health.state   # broken
G4=$(AG 'pod%3D%22l1%22'); curl -s "${H[@]}" -X POST $API/alert-groups/$G4/resolve -d '{}' > /dev/null
curl -s -X PUT $FS/config -d '{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":true}}}}' > /dev/null
D0=$(curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk '{print $2}')
ADV 360; sleep 2
curl -s -b jar $API/destinations/$D | jq -r .health.state                      # healthy
curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk -v d=$D0 '{print (($2 - d) >= 1)}'   # 1
MSG NetDown '.ts' | grep -c null                                                # 1: never published

# C-24.FR-13, AC-16: the test message
curl -s "${H[@]}" -X POST $API/destinations/$D/tests -d '{}' | jq -c '[.steps[].name, .steps[0].error_class]'   # ["message","none"]
MSG 'Test message' '.blocks | length > 0'                                       # true
```

Live, on the maintainer's test workspace, with `operator_attention`: the Connection "test workspace" (L) and the
Destination "live-alerts" on `#alerts` (LD) as in S-098, added to the Route "sl":

```sh
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/routes/$R | jq -r .etag)" $API/routes/$R \
  -d "$(curl -s -b jar $API/routes/$R | jq -c --arg d "$LD" '{name, matchers, urgent, group_key, policy, destination_ids: (.destination_ids + [$d])}')" > /dev/null
FIRE live1 LiveCheck m1 'literal *not bold* <!here> &'; LG=$(AG 'pod%3D%22m1%22')
curl -s -b jar "$API/request-log?destination=$LD&operation=chat.postMessage&limit=1" | jq -c '.items[0] | {status, error_class}'   # {"status":200,"error_class":null}
curl -s "${H[@]}" -X POST $API/alert-groups/$LG/acknowledge > /dev/null; sleep 2
curl -s -b jar "$API/request-log?destination=$LD&operation=chat.update&limit=1" | jq -c '.items[0].status'   # 200
```

The operator looks at `#alerts` in a Slack client, or sends a screenshot: a red bar that turns orange; Ack green
before and Resolve green after the acknowledgement; the menu "Snooze…" with the durations and the presets; the label
value shown literally, not bold, mentioning nobody. The operator then deletes the Root message in the client; the next
change of the Alert Group posts it again with the deletion notice. At the end the Alert Group is resolved, every
message the check posted is deleted with the bot token (`chat.delete`), and `LD` leaves the Route.

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): deliver alert groups to slack channels`.
- `operator_attention` is true for the live look: whether the colour bar, the green buttons, the menu and the literal
  label value look as F-074, F-075 and F-082 describe is seen only in a Slack client, and whether the zero-width space
  after `*`, `_`, `~` and `` ` `` keeps mrkdwn from pairing them is a Pending fact (C-24.FR-7). If it does not, the
  escaping changes in this pull request — to a look-alike character, for example — and C-24.FR-7 with it.
- The probe needs no code here: the Destination check of S-098 is the probe of every type that has one, run in the
  delivery class by `delivery/broken.go`.
- `chat.getPermalink` is served by the fake for completeness and not used: it answers `ok` for a tombstone (F-088).
- `text` is cut so that it never reaches the 4,000 bytes of F-078 even when Slack stores each character as an entity
  or a shortcode, which costs a long title in the notification only; the top `section` keeps the whole title.
- Muster sends only blocks of the types `section`, `context` and `actions`, so `block_mismatch` cannot happen; the
  fake answers it anyway, as a guard for a later change.
- The live check posts to the maintainer's `#alerts` and deletes what it posted afterwards (F-087: the bot deletes its
  own messages); a Thread with replies leaves a tombstone, which the operator may delete in the client.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-3 | partial | the Destination check as the Broken probe, in the delivery class |
| C-24.FR-4 | full | |
| C-24.FR-5 | partial | the buttons and the menu, signed; presses are S-100 and S-101 |
| C-24.FR-6 | partial | edits, Threads, Loud and Quiet, everyone and unlinked names; linked Mentions and the footer's names S-102 |
| C-24.FR-7 | partial | delivery and template previews; the Route editor's option is S-103 |
| C-24.FR-8 | partial | the mapping in delivery; the client's classification is S-097 |
| C-24.FR-13 | partial | the test message and the preview; a press on a test message is S-100, the panel's note S-103 |
| C-24.FR-14 | partial | delivery's requests; presses S-100, the socket S-101 |
| C-24.FR-15 | full | |
| C-24.AC-5 | full | |
| C-24.AC-6 | full | |
| C-24.AC-7 | full | |
| C-24.AC-8 | full | |
| C-24.AC-9 | partial | the probe; "Check" on the page is S-103 |
| C-24.AC-15 | partial | `<!here>` and unlinked names; `all` and groups refused in S-098, linked Mentions S-102 |
| C-24.AC-16 | partial | the test message; the press on it S-100, the note on the panel S-103 |
| C-12.FR-7 | partial | mrkdwn in Slack |
| C-16.FR-3 | partial | the test of a Slack Destination, which presses nothing |
