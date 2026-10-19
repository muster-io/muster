---
id: S-093
title: Snooze presets on the buttons of Mattermost, Telegram and Bridges, and in Text commands (BE)
capability: C-10
kind: be
layer: L1
depends_on: [S-092, S-075, S-082]
covers: [C-10.FR-18, C-10.AC-23, C-22.FR-5, C-22.FR-14, C-22.AC-27]
files_touched:
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/messages/default.go
  - internal/messages/default_test.go
  - internal/messages/render.go
  - internal/messages/preview.go
  - internal/messages/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - internal/telegram/presses.go
  - internal/telegram/presses_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/commands.go
  - internal/bridge/commands_test.go
  - internal/bridge/presses.go
  - internal/bridge/presses_test.go
  - test/e2e/snooze_preset_buttons_test.go
  - docs/concepts/statuses-and-commands.md
  - docs/messengers/bridge.md
  - docs/messengers/write-a-bridge.md
acceptance:
  - "[C-10.FR-18, C-10.AC-23] On a Route with the On-call durations, the Root message of a firing Alert Group carries, in the fake Mattermost server, the actions `ack`, `resolve`, `snooze0`, `snooze1`, `snooze2`, `snoozetomorrow` and `snoozeweek` named \"Ack\", \"Resolve\", \"Snooze 1 h\", \"Snooze 4 h\", \"Snooze 24 h\", \"Until tomorrow 09:00\" and \"Until Monday 09:00\", and in the fake Telegram server the keyboard `[[\"Ack\",\"Resolve\"],[\"Snooze 1 h\",\"Snooze 4 h\",\"Snooze 24 h\"],[\"Until tomorrow 09:00\",\"Until Monday 09:00\"]]`; an acknowledged one carries the presets after its durations too, a snoozed one none; the presets have no `style`."
  - "[C-10.FR-18, C-10.AC-23] On a Route in Russian the presets read «До завтра 09:00» and «До понедельника 09:00»; with the week starting on any other day the label names it — \"Until Sunday 09:00\", «До воскресенья 09:00» — and the time is `organization.workday_start` as `HH:MM` (`default_test.go`, all seven days in both languages)."
  - "[C-10.FR-18, C-10.AC-23] At 02:00 UTC, a press on \"Until tomorrow 09:00\" in Mattermost by bob, linked, whose profile time zone is Asia/Tokyo, snoozes the Alert Group until 09:00 Tokyo time with the Transport `mattermost`, and the Audit log details name `preset` `tomorrow` and `time_zone` `Asia/Tokyo`; a press on it in Telegram by a linked User without a profile time zone snoozes another one until 09:00 UTC, the Organization's time zone."
  - "[C-10.FR-18, C-10.AC-23] A Route without Snooze durations offers neither preset in either messenger, and a press on a preset button of a Root message published before its Route lost its durations changes nothing and is answered \"This button could not be verified; nothing was changed.\" (`callback_test.go`, `presses_test.go`)."
  - "[C-10.FR-18, C-10.AC-23] After `workday_start` becomes `08:30`, the next re-render of an open Root message, on its next change of status, shows \"Until tomorrow 08:30\" in both messengers; a press on a button rendered before the change ends at 08:30."
  - "[C-22.FR-5, C-22.AC-27] A Bridge Root message carries the presets after the durations with the style hint `neutral`; the fake bridge with `max_per_message` 5 gets \"Ack\", \"Resolve\" and the three durations on a firing Root message, with 6 also \"Until tomorrow 09:00\", with 10 all seven (`envelope_test.go` covers every limit from 2 to 7)."
  - "[C-22.FR-14, C-22.AC-27] From a linked Responder, `snooze tomorrow`, `/Snooze Tomorrow` and `отложить завтра` snooze until the next start of the working day in the User's profile time zone, else the Organization's, and `snooze monday` and `отложить понедельник` until the start of the week, each answered \"Done: Snoozed #N\"; with the week starting on Monday, `snooze friday` is answered \"Snooze for one of: 1h, 4h, 24h, tomorrow, monday\", and on a Route in Russian «Отложить можно на: 1ч, 4ч, 24ч, завтра, понедельник»; on a Route without Snooze durations `snooze tomorrow` is answered \"This Alert Group's Route has no Snooze durations; nothing was changed.\""
  - "[C-10.FR-18] A Destination test message and the Destination preview carry the preset buttons of their status, and a press on a preset of a test message is answered \"This is a test message; nothing was changed.\" (`default_test.go`, `callback_test.go`, `presses_test.go`)."
  - "The documentation pages Concepts → Statuses and commands (the buttons and the Text commands), Messengers → Bridge and Write a bridge (the order in which Snooze buttons are dropped) describe the presets, and `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 296
---

# S-093. Snooze presets on the buttons of Mattermost, Telegram and Bridges, and in Text commands (BE)

## Scope

**IN**

- Two Snooze buttons after the durations on the Root messages of firing and acknowledged Alert Groups, test messages
  and previews included, on a Route that has Snooze durations: "Until tomorrow {start}" and "Until {day} {start}" in
  the Route's language (reference.md, C-10.FR-18).
- Their presses in Mattermost, Telegram and Bridges, dispatched as the Snooze of S-092 with a preset and no time zone,
  so that the linked User's profile time zone, else the Organization's, decides the end.
- The Telegram keyboard row of the presets, the Mattermost action ids, the Bridge drop order beyond
  `buttons.max_per_message` (C-22.FR-5), and the preset words of the Text commands (C-22.FR-14) with the answer that
  lists them.
- The documentation of the buttons, the Text commands and the drop order.

**OUT**

- The rule, the settings and the API: S-092. The UI: S-094.
- Asking a messenger for a person's time zone: Telegram and the Bridge protocol have none, so the profile decides
  (S-092, Notes).
- A re-render of every open Root message when the settings change: their buttons follow at their next re-render, as a
  new `organization.time_zone` does in message times (C-10.FR-18).

## Contracts

- **Buttons** (`internal/buttons`): `ForStatus` adds, for `firing` and `acknowledged` and only when the Route has at
  least one duration, two Snooze buttons after the durations whose arguments are reserved: `ArgumentTomorrow` (254) and
  `ArgumentWeekStart` (255). Durations keep the indices 0 to 253, so a Route offers at most its first 254 durations as
  buttons, two fewer than today's bound of the one-byte argument. The action id keeps its layout, version and 64-byte
  bound; the style of both is `neutral` (`StyleOf` of Snooze).
- **Labels** (`internal/messages`, `texts/en.json`, `texts/ru.json`): `button.snoozeTomorrow` "Until tomorrow {time}"
  («До завтра {time}») and `button.snoozeWeekStart` "Until {day} {time}" («До {day} {time}»), where `{time}` is
  `organization.workday_start` as `HH:MM` and `{day}` the name of `organization.week_start` — in Russian in the
  genitive: понедельника, вторника, среды, четверга, пятницы, субботы, воскресенья. The render source reads `workday_start` and
  `week_start` beside `time_zone`, in the read of an Alert Group for its message and in `GetRenderSettings` of previews
  (`query.sql`), so the labels are part of the Desired state and its hash: a Root message gets the presets, or new
  labels after a change of the settings, at its next re-render. Deploying the story changes no Desired state by itself.
- **Mattermost** (`layout.go`, `callback.go`): the action ids `snoozetomorrow` and `snoozeweek` (letters only, as
  Mattermost wants); a press dispatches `Snooze` with `SnoozeEnd{Preset}`; a preset press on a message whose binding
  has no Snooze durations is unverifiable, like a duration index out of range today.
- **Telegram** (`layout.go`, `presses.go`): the keyboard gets a third row, the two presets, under the row of the
  durations; every edit keeps carrying the whole keyboard (C-14.FR-15); a press dispatches as in Mattermost.
- **Bridges** (`envelope.go`, `presses.go`, `commands.go`): the buttons follow the durations with the hint `neutral`;
  beyond `max_per_message` Muster drops the `week_start` preset, then the `tomorrow` preset, then the durations from the
  longest (C-22.FR-5), whatever the length of the presets at that moment; the commands line keeps naming the first
  duration only. A Text command's `<duration>` may be `tomorrow` / `завтра` or a day of the week — `monday` …
  `sunday`, `понедельник`, `вторник`, `среда`, `четверг`, `пятница`, `суббота`, `воскресенье` — of which only the
  Organization's `week_start` is taken; any other day, like any duration the Route lacks, is answered with
  `bridge.snoozeDurations`, whose `{durations}` now ends with the two preset words in the Route's language; on a Route
  without durations the answer stays `bridge.noSnoozeDurations`. The dispatch is a Snooze with a preset, Transport
  `bridge`.
- **Answers**: unchanged — a preset press that runs is answered as any Snooze press, and a Text command with "Done:
  Snoozed #N".
- **Fake servers**: no change. They keep any number of buttons and keyboard rows, and their presses name a button by
  its label (Telegram, Bridge) or its action id (Mattermost).
- **Settings**: `organization.workday_start`, `organization.week_start`, `route.snooze_durations`,
  `buttons.max_per_message` of a bridge's `info`, `bridge.text_commands`, `bridge.info_max_age`.
- **Metrics, log events, schema, spec**: unchanged.

## Steps

1. Add the preset buttons and their labels. Check: `buttons_test.go` covers each status with and without durations;
   `default_test.go` covers the labels of the seven days in both languages and the Desired state of a Root message
   before and after a change of `workday_start`.
2. Lay them out and dispatch their presses in Mattermost and Telegram. Check: `layout_test.go` of both messengers
   asserts the actions and the three keyboard rows; `callback_test.go` and `presses_test.go` dispatch a preset press,
   refuse one without durations and answer one on a test message.
3. Add them to Bridges: the buttons, the drop order and the Text commands. Check: `envelope_test.go` for every limit
   from 2 to 7; `commands_test.go` for every preset word, day and answer in both languages; `presses_test.go`.
4. Write `test/e2e/snooze_preset_buttons_test.go` and the documentation. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-068: the Admin's token A, bob's session HB and token B, the Integration "lab" with FIRE, PUTA, NOTIFY, AG and
# ADV, the On-call policy in P, ROUTE of S-049, the Route "t" with the Mattermost Destination "alerts" (channel
# ch-alerts) and the Telegram Destination "alerts" (CH), bob's Mattermost link (u-bob) and Telegram link (5001), and the
# Admin's Telegram link (5002) set up the same way. As in S-075: the Bridge Connection "br" (C) with its Destination BD,
# a Route "br" to BD only, LINK and the fake bridge. ORG, NOW, AT and T as in S-092.
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; FB=127.0.0.1:18094/_fake; CH=-1001000000001
MMACT() { curl -s $FMM/posts | jq -c --arg g "$1" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g)))] | last | [.props.attachments[0].actions[] | {id, name, style}]'; }
MMROOT() { curl -s $FMM/posts | jq -r --arg g "$1" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g)))][0].id'; }
TGMSG() { curl -s "$FTG/messages?chat=$CH" | jq -c --arg g "$1" '[.[] | select(.text | test($g))] | last'; }
LINK bob u-bob AK0000000000B7
curl -s "${HB[@]}" -X PUT $API/me -d '{"name":"bob","time_zone":"Asia/Tokyo"}' > /dev/null

# C-10.AC-23: the buttons in both messengers
G=$(FIRE r1 HostDown t); sleep 1
MMACT $G
# [{"id":"ack","name":"Ack","style":"primary"},{"id":"resolve","name":"Resolve","style":"success"},
#  {"id":"snooze0","name":"Snooze 1 h","style":null},{"id":"snooze1","name":"Snooze 4 h","style":null},
#  {"id":"snooze2","name":"Snooze 24 h","style":null},{"id":"snoozetomorrow","name":"Until tomorrow 09:00","style":null},
#  {"id":"snoozeweek","name":"Until Monday 09:00","style":null}]
TGMSG HostDown | jq -c '.reply_markup.inline_keyboard | map(map(.text))'
# [["Ack","Resolve"],["Snooze 1 h","Snooze 4 h","Snooze 24 h"],["Until tomorrow 09:00","Until Monday 09:00"]]
RU=$(ROUTE ru "$(jq -c '.language = "ru"' <<<"$P")"); GR1=$(FIRE r2 DiskFull ru); sleep 1
MMACT $GR1 | jq -c '[.[-2:][] | .name]'                       # ["До завтра 09:00","До понедельника 09:00"]
NOD=$(ROUTE nod "$(jq -c '.snooze_durations_seconds = []' <<<"$P")"); GN=$(FIRE r3 NoDur nod); sleep 1
MMACT $GN | jq -c '[.[].id]'                                  # ["ack","resolve"]
TGMSG NoDur | jq -c '.reply_markup.inline_keyboard | map(map(.text))'   # [["Ack","Resolve"]]

# presses at 02:00 UTC: bob (Asia/Tokyo) in Mattermost, the Admin (no profile time zone) in Telegram
AT 7200
curl -s -X POST $FMM/press -d "{\"post_id\":\"$(MMROOT $G)\",\"action_id\":\"snoozetomorrow\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s "${A[@]}" $API/alert-groups/$G | jq -r .snooze_until; T 86400        # both lines: tomorrow 00:00:00Z (09:00 in Tokyo)
curl -s "${A[@]}" "$API/audit-log?action=alert_group.snoozed&limit=1" | jq -c '.items[0] | {transport, d: (.details | {preset, time_zone})}'
# {"transport":"mattermost","d":{"preset":"tomorrow","time_zone":"Asia/Tokyo"}}
G2=$(FIRE r4 Latency t); sleep 1
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$(TGMSG Latency | jq .message_id),\"from\":{\"id\":5002},\"button\":\"Until tomorrow 09:00\"}" > /dev/null; sleep 1
curl -s "${A[@]}" $API/alert-groups/$G2 | jq -r .snooze_until; T 32400       # both lines: today 09:00:00Z

# a change of the start of the working day shows at the next re-render
ORG '.workday_start = "08:30"' > /dev/null
G3=$(FIRE r5 Queue t); sleep 1; MMACT $G3 | jq -c '[.[-2:][] | .name]'      # ["Until tomorrow 08:30","Until Monday 08:30"]
MMACT $GR1 | jq -c '[.[-2:][] | .name]'                       # ["До завтра 09:00","До понедельника 09:00"] (not re-rendered yet)
curl -s "${A[@]}" -X POST $API/alert-groups/$GR1/acknowledge > /dev/null; sleep 1
MMACT $GR1 | jq -c '[.[0].name, .[-2].name]'                  # ["Снять подтверждение","До завтра 08:30"]
ORG '.workday_start = "09:00"' > /dev/null

# C-22.AC-27: Bridges — the drop order and the Text commands
CAPS() { curl -s $FB/config | jq -c ".capabilities | .buttons.max_per_message = $1"; }
BTN() { curl -s $FB/messages | jq -c --arg t "$1" '[.[] | select(.envelope.title | endswith($t))][0].envelope | [.buttons[] | "\(.label):\(.style)"]'; }
curl -s -X PUT $FB/config -d "{\"capabilities\":$(CAPS 5)}" > /dev/null; ADV 601
FIRE b5 Five br > /dev/null; sleep 2; BTN Five
# ["Ack:primary","Resolve:success","Snooze 1 h:neutral","Snooze 4 h:neutral","Snooze 24 h:neutral"]
curl -s -X PUT $FB/config -d "{\"capabilities\":$(CAPS 6)}" > /dev/null; ADV 601
FIRE b6 Six br > /dev/null; sleep 2; BTN Six | jq -c '.[-1]'             # "Until tomorrow 09:00:neutral"
curl -s -X PUT $FB/config -d "{\"capabilities\":$(CAPS 10)}" > /dev/null; ADV 601
GB=$(FIRE b7 Seven br); sleep 2; BTN Seven | jq -c '.[-2:]'              # ["Until tomorrow 09:00:neutral","Until Monday 09:00:neutral"]
ROOT=$(curl -s $FB/messages | jq -r '[.[] | select(.envelope.title | endswith(" Seven"))][0].id')
TC() { curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"$1\"}" > /dev/null; sleep 2; curl -s $FB/answers | jq -r '.[-1].text'; }
TC 'snooze friday'                                            # Snooze for one of: 1h, 4h, 24h, tomorrow, monday
TC '/Snooze Tomorrow'                                         # Done: Snoozed #<N>
curl -s "${A[@]}" $API/alert-groups/$GB | jq -r '.snooze_until | fromdate | strftime("%H:%M")'   # 00:00 (09:00 in Tokyo)
TC 'отложить понедельник'                                     # Done: Snoozed #<N>
curl -s "${A[@]}" $API/alert-groups/$GB | jq -r '.snooze_until | fromdate | strftime("%A %H:%M")'   # Monday 00:00 (09:00 in Tokyo)
```

`test/e2e/snooze_preset_buttons_test.go` repeats these steps and adds the acknowledged Root message in both messengers,
a press on a preset of a Destination test message, a press on a preset of a message whose Route lost its durations
after it was published, and `snooze tomorrow` on a Route without durations.

## Open questions

None.

## Notes

- Suggested commit: `feat(delivery): offer the snooze presets on messenger buttons and in text commands`.
- Button counts: a firing Root message on an On-call Route now carries seven buttons. Mattermost showed seven on one
  post on the test stand (F-068); a Telegram keyboard of three rows and seven buttons is not among the facts, and no
  documented limit of either messenger comes near it. A Bridge declares its own limit, and the drop order keeps the
  short durations, which a person on call needs most, longest.
- The presets come only with Snooze durations because an empty `route.snooze_durations` has meant "no Snooze from
  messengers" since S-061; the UI offers them on every Route (S-094).
- The day of the week in Russian labels is in the genitive after «До»; the Text commands take the nominative, the form a
  person types alone.
- Added after all stories of L1 had merged (D379); see S-092.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-18 | partial | the buttons, presses and Text commands; the API is S-092, the dialog S-094 |
| C-10.AC-23 | full | |
| C-22.FR-5 | partial | the presets and the new drop order; the rest of the requirement was covered by S-074 |
| C-22.FR-14 | partial | the preset words and the answer that lists them; the rest was covered by S-075 |
| C-22.AC-27 | full | |
