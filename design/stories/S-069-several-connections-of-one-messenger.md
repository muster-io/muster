---
id: S-069
title: "Several Connections of one messenger: two Mattermost servers and two Telegram bots (BE)"
capability: C-18
kind: be
layer: L1
depends_on: [S-061, S-066, S-067, S-068, S-043, S-044, S-064, S-051, S-052]
covers: [C-18.FR-2, C-18.FR-4, C-18.FR-8, C-18.FR-10, C-18.AC-3, C-18.AC-7, C-11.FR-1, C-13.FR-4, C-14.FR-2, C-14.FR-3, C-14.FR-4, C-14.AC-13, C-17.FR-2, C-17.FR-4, C-01.FR-13]
files_touched:
  - AGENTS.md
  - CONTRIBUTING.md
  - design/facts.md
  - design/prd/l1/C-14-telegram.md
  - design/stories/README.md
  - design/stories/coverage.md
  - design/stories/S-069-several-connections-of-one-messenger.md
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/fakes/fakemattermost/posts.go
  - internal/fakes/fakemattermost/presses.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/copies.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/fakes/faketelegram/presses.go
  - internal/fakes/faketelegram/updates.go
  - test/e2e/harness.go
  - test/e2e/several_connections_test.go
  - web/e2e/several-connections.spec.ts
  - web/e2e/support.ts
acceptance:
  - "[C-01.FR-13] `muster dev` starts a second fake Mattermost server on `127.0.0.1:18066`, and the fake Telegram server serves a second bot (`@muster_dev_b_bot`, the published token `234567:dev-telegram-token-b`) with its own identity, chat memberships, private chats, update queue and budget of sends and edits per chat, while every other token keeps the first bot; the second fake Mattermost bot (`muster-dev-bot-b`, token `muster-dev-mattermost-token-b`) posts, edits, sends direct messages and presses as itself. `fakemattermost_test.go` and `faketelegram_test.go` check each."
  - "[C-01.FR-13] In the fake Telegram server the automatic copy of a channel post, its `edited_message` and people's comments reach every bot that is an admin of the discussion group, the bot that posted included, while a bot's own messages reach no other bot; `withhold_copies_from` keeps copies from the bots it names only; a press goes to the bot that sent the message pressed."
  - "[C-11.FR-1, C-13.FR-4, C-14.FR-4] A Route with a Mattermost Destination on each of two fake Mattermost servers, two Telegram Destinations through two Telegram Connections in one channel and an outgoing webhook gives every Destination its own Root message and its own events; Acknowledge and Resolve edit each Root message through its own Connection only — every `editMessageText` of a channel post comes from the token that posted it, every Mattermost patch from the bot that posted (`TestSeveralConnectionsRoute`)."
  - "[C-13.FR-4, C-18.FR-4, C-18.FR-8, C-18.AC-3] Two Mattermost Connections to one server, both in one channel, post two Root messages whose buttons call back their own Connection; a press through Connection B by a person linked only through Connection A is answered \"Your Mattermost account is not linked to Muster. Link it in your profile: {link}\" and changes nothing; after the person links through Connection B (the code comes as a direct message from bot B), the press runs as that User with the Transport `mattermost`; a callback to one Connection that carries the other Connection's post is refused like a bad signature (`mattermost_press` with the outcome `not_verified`) and changes nothing (`TestSeveralConnectionsMattermostSameChannel`)."
  - "[C-17.FR-2, C-17.FR-4, C-13.FR-4] In that channel, the ack timeout notices and the Reminders of each Destination are Thread replies to its own Root message, posted by its own bot, and a press on \"Still on it\" on bot B's Reminder by the Owner linked through Connection B records `reminder_answered` for that User (`TestSeveralConnectionsMattermostSameChannel`)."
  - "[C-14.FR-3, C-17.FR-2, C-17.FR-4] Two Telegram Connections in one channel and its discussion group post two channel posts; both copies reach both Connections and are buffered for both, yet each delivery's Thread attaches to the copy of its own post: the Thread replies about new Alerts, the ack timeout notices and the Reminders of each Destination reply to the copy of its own post, sent by its own bot. A copy withheld from one bot leaves only that Destination's Thread \"Thread not attached\" until a person comments under its post; a deleted copy of one post leaves the other Destination's Thread attached (`TestSeveralConnectionsTelegramSameChat`)."
  - "[C-14.FR-4, C-18.FR-4, C-18.FR-8, C-18.AC-3] A Telegram account linked through Connection A presses Ack on Connection B's Root message and \"Still on it\" on Connection B's Reminder; both run as its User with the Transport `telegram` and are answered by bot B (\"Done: Acknowledge\", \"Done: Still on it\"); a press on one bot's channel post or Reminder that reaches the other bot is answered \"This button could not be verified; nothing was changed.\" and changes nothing (`TestSeveralConnectionsTelegramSameChat`)."
  - "[C-18.FR-2, C-18.AC-7] The deep link of Connection B names `muster_dev_b_bot`; a `/start` to bot B with a token started for Connection A links nothing and is answered \"This link is invalid or expired. Start again from your Muster profile.\"; with Alice's Telegram account linked through Connection A only, bot B's Root message of an Alert Group she acknowledged names her Telegram username, and its Loud Mention of her is a `tg://user?id=` link (`TestSeveralConnectionsTelegramSameChat`)."
  - "[C-14.FR-2, C-14.AC-13] `createDestination` through Connection B on the channel `@no_comments` is refused with `422 destination_check_failed` and \"Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.\", and bot B posts nothing there (`TestSeveralConnectionsTelegramWithoutDiscussionGroup`)."
  - "[C-18.FR-10] `listLinkableConnections` lists every Mattermost and Telegram Connection that is not deleted, two or more of each messenger, and the profile offers each of them in Playwright (`several-connections.spec.ts`), which also shows the Connections and Destinations lists, a Destination page, the Route's Destinations section, the delivery section of an Alert Group, the \"Delivery problem\" filter and the Account links of the user page with two Connections of each messenger, at 1280 and 360 px wide."
verify: "make ci test-integration e2e"
operator_attention: true
issue: null
---

# S-069. Several Connections of one messenger: two Mattermost servers and two Telegram bots (BE)

## Scope

**IN**

- The fake Mattermost server serves a second bot account on the same server (its own token, user, channel
  memberships, posts, edits, direct messages and presses), and `muster dev` starts a second fake Mattermost server.
- The fake Telegram server serves a second bot (its own token, identity, chat memberships, private chats and update
  queue); the automatic copies of channel posts and people's comments reach every admin bot of the discussion group;
  a bot's own messages reach no other bot.
- The development mode, the end-to-end harness and the Playwright support learn the second fake Mattermost server and
  the second Telegram bot; their published tokens are constants of `internal/devmode`.
- Permanent end-to-end coverage (D305) of several Connections of one messenger: a Route across two Mattermost servers,
  two Telegram Connections and an outgoing webhook; two Mattermost Connections in one channel; two Telegram Connections
  in one channel and discussion group; a Telegram channel without a discussion group through the second Connection;
  Account links in their Identity spaces — per Connection in Mattermost, all of Telegram in Telegram.
- A Playwright spec over the existing pages with two Connections of each messenger, which saves the screenshots of the
  Verification.
- A Pending entry in `design/facts.md` for what the fake now assumes about two bots in one Telegram chat.
- Any product defect these tests find, fixed in the same pull request, with its files added to `files_touched` and the
  reason in the pull request (AGENTS.md, Working a story, step 5).

**OUT**

- S-070 (to be written), a UI story: the profile's wording for the Telegram link ("for all Telegram bots", "linked via
  {Connection}", "Relink"), the Route editor's warning about two Destinations in one chat, the labels that name a
  Destination's or a link's Connection across the UI, and the documentation of several Connections per messenger.
- New product behaviour: the contracts of C-11, C-13, C-14, C-17 and C-18 already allow several Connections of one
  messenger and two Destinations in one chat; this story proves it and changes nothing in them, apart from fixes of
  defects it finds.
- A Telegram channel whose comments are turned off after its Destination was saved: today's behaviour is kept and
  documented in C-14.FR-14 (D308), with no new test.
- New demo Connections in `muster dev`: the second fake server and the second bot only listen; tests and checks create
  their Connections through the API, so that the existing tests keep their demo configuration.

## Contracts

- **Operations** (all existing): `createConnection`, `createDestination`, `createRoute` (`destination_ids`),
  `acknowledgeAlertGroup` and the other Commands, `listAlertGroupDeliveries`, `mattermostAction` (ingest listener,
  `/api/v1/callbacks/mattermost/{connection_id}`), the Telegram updates of S-041 and S-067, `listLinkableConnections`,
  `startTelegramLink`, `startMattermostLink`, `confirmMattermostLink`, `listUserAccountLinks`.
- **Tables** (read by the tests): `deliveries` (`message_id`, `thread_state`, `thread_anchor_id`), `thread_replies`,
  `telegram_post_copies` (one row per Connection and post), `account_links` (`identity_space`: `telegram`, or
  `mattermost:<connection id>`). No migration.
- **Rules re-verified** (no change):
  - every Desired state, delivery, Root message and Thread is per Alert Group and Destination (C-11.FR-1), and two
    Destinations in one chat are two Destinations;
  - a Mattermost press is bound to a Root message or a Reminder of a Destination of the Connection whose callback it
    reached, and its account is looked up in `mattermost:<that Connection>` (C-13.FR-4, C-18.FR-4);
  - a Telegram copy is buffered per Connection, channel and post, and attaches only the deliveries of that Connection
    whose Root message is that post (C-14.FR-3, S-066); a Telegram press is bound to a channel post or a Reminder of a
    Destination of the Connection that received it, and its account is looked up in `telegram` (C-14.FR-4,
    C-18.FR-4);
  - a Telegram link token is bound to the Connection it was started for (C-18.FR-2); footers and Mentions use the
    Destination's Identity space (C-12.FR-12, C-18.AC-7).
- **Fake Mattermost** (C-01.FR-13, extending S-039 and S-061):
  - the second bot: user `muster-dev-bot-b` (`fakemattermost.SecondBotUserID`), token `muster-dev-mattermost-token-b`
    (`fakemattermost.SecondBotToken`); every other accepted token is the first bot, as before. The bot of the request's
    token is the one `GET /api/v4/users/me`, the channel lists and memberships, new posts (`user_id`), ephemeral posts,
    direct channels and `POST /api/v4/posts/{id}/actions/{action}` act as;
  - the second bot is a member of `ch-alerts` only; `PUT`/`DELETE /_fake/channels/{id}/members/{user_id}` change it;
  - a patch of another user's post is refused `403` unless the bot is a system admin (`bot_system_admin`), as for a
    bot with the role Member;
  - no notification is recorded for a bot, so a second bot in a channel changes no notification of people;
  - `GET /_fake/dms` names the bot of each direct message (`bot_id`);
  - the second server is another instance with the same fixtures: the same people with the same user ids, so that only
    the Connection tells their accounts apart.
- **Fake Telegram** (C-01.FR-13, extending S-042, S-066 and S-067):
  - the second bot: id `234567`, username `muster_dev_b_bot` (`faketelegram.SecondBotID`, `SecondBotUsername`), every
    token that starts with `234567:` (`faketelegram.SecondBotToken` is `234567:dev-telegram-token-b`); every other
    token is the first bot, as before. `getMe`, the rights checks of `sendMessage`, `editMessageText` and
    `getChatMember`, the sender of the bot's messages (`from`) and the budget of sends and edits per chat (F-016) follow
    the token's bot; both bots are admins of `@muster_alerts`, its discussion group and `@no_comments` from the start;
  - an edit records which bot made it (`edits[].by`); a bot edits a channel post as an admin allowed to edit messages
    and, in a group, only its own messages;
  - a private chat is a chat between one person and one bot: `POST /_fake/private` opens it with the bot of its token,
    `sendMessage` to it needs that bot, and `GET /_fake/messages?chat=<id>&bot=<bot id>` lists it;
  - fan-out: the `message` update of an automatic copy, the `edited_message` of a copy and a person's comment go to the
    token that posted and to every token of another bot that is an admin of the discussion group and receives updates
    (it called `getUpdates` or has a webhook); with `token` set, `POST /_fake/comment` still goes to that token only.
    A bot's own sends reach no other bot (Bot API FAQ, "Why doesn't my bot see messages from other bots?"); a press
    goes to the bot that sent the message pressed, also when `data_from` takes the data from another bot's message;
  - `PUT /_fake/config {"withhold_copies_from": [<bot id>, …]}` keeps the copies, and their edits, from the bots named;
    `withhold_copies` keeps them from every bot, as before.
- **Development mode** (`internal/devmode`, `internal/cli/dev.go`): `MattermostSecondAddr` `127.0.0.1:18066`, started
  with the other fakes, with `AllowedUntrustedInternalConnections` set as for the first, and printed as
  `muster dev: second fake Mattermost http://127.0.0.1:18066` before the Telegram line, which stays last; the published
  constants `MattermostSecondBotToken` and `TelegramSecondBotToken`. The demo configuration is unchanged.
- **Harness** (`test/e2e/harness.go`): `FakeURLs.MattermostSecond`, waited for like the others, in both modes;
  `devmode.Fakes.MattermostSecond` in the mode `FakesInProcess`. `web/e2e/support.ts` gains the second server's
  control URL and the second tokens.
- **Facts** (`design/facts.md`, Pending): the fakes assume, to be checked on the test stand with a second bot, that
  two admin bots of one channel and its discussion group each receive the automatic copies of the other's posts, their
  `edited_message` and people's comments, while neither receives the other's messages (the last part from the Bot API
  FAQ); that the budget of sends and edits per chat (F-016) is each bot's own; and that Mattermost refuses a bot with
  the role Member an edit of another bot's post, which the fake answers `403`.
- **Defaults**: `telegram.copy_wait`, `route.ack_timeout`, `route.reminders`, `delivery.interactive_budget`.

## Steps

1. Give the fake Mattermost server its second bot. Check: `fakemattermost_test.go` covers `users/me`, a post, a patch
   of the other bot's post (`403`), an ephemeral post, a direct message (`bot_id`) and a press as the bot of the token,
   and that a second bot in a channel records no notification.
2. Give the fake Telegram server its second bot, per-bot private chats, the fan-out and `withhold_copies_from`. Check:
   `faketelegram_test.go` covers `getMe` per token, `from` and `edits[].by`, the copy and its `edited_message` reaching
   both admin bots, a comment reaching both, a bot's reply reaching neither other bot, `withhold_copies_from`, two
   private chats of one person, a budget per bot and a press reaching the bot of the message pressed; the existing
   cases pass unchanged.
3. Start the second fake Mattermost server in `muster dev`, add the constants, the usage text, the harness URL and the
   Playwright support. Check: `devmode_test.go` and `dev_test.go` cover the address, the printed lines in order and a
   busy second port; `make e2e` passes before any new test is added.
4. Write `test/e2e/several_connections_test.go` (four tests, below). Check: each passes with `make e2e` and with
   `E2E_REPLICAS=2`; a test that fails on product code stops the story, the defect and its fix are described in the
   pull request, and the files join `files_touched`.
5. Write `web/e2e/several-connections.spec.ts`. Check: it passes in `make e2e`; with `MUSTER_E2E_SCREENSHOTS` set it
   saves the screenshots of the Verification.
6. Add the Pending entry to `design/facts.md` and the second server and bot to `AGENTS.md` and `CONTRIBUTING.md`.
   Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-049: the Admin's Personal access token in A, bob's in B (Responder), the Integration "lab" with NOTIFY, ADV,
# AG and PUTA, the On-call policy in P. bob links accounts through his web session (`HB`) as in S-051.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FTG=127.0.0.1:18081/_fake; GR=-1001000000002; CH=-1001000000001
FMM1=127.0.0.1:18065/_fake; FMM2=127.0.0.1:18066/_fake
MENTIONS=$(jq -cn '["new_alert_group","new_alerts","reopen","ack_timeout","snooze_ended","rise_to_urgent"]
  | map({(.): {everyone: "none", user_ids: [], groups: []}}) | add')
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"$2\"}}" > /dev/null
  PUTA $1 a "{\"labels\":{\"team\":\"$3\",\"host\":\"$1\"}}"; NOTIFY $1 '{"reason":"first notification"}'; AG "host%3D%22$1%22"; }
grep -c 'muster dev: second fake Mattermost http://127.0.0.1:18066' dev.log            # 1
curl -s $FMM2/requests > /dev/null && echo up                                         # up
CONN() { curl -s "${A[@]}" $API/connections -d "$1" | jq -r .id; }
MMC() { CONN "{\"type\":\"mattermost\",\"name\":\"$1\",\"server_url\":\"http://$2\",\"bot_token\":\"$3\",\"proxy\":{\"enabled\":false},\"limiter\":{\"limit\":10,\"per_seconds\":1}}"; }
TGC() { CONN "{\"type\":\"telegram\",\"name\":\"$1\",\"bot_token\":\"$2\",\"bot_api_base_url\":\"http://127.0.0.1:18081\",\"update_mode\":\"long_polling\",\"proxy\":{\"enabled\":false},\"limiter\":{\"limit\":30,\"per_seconds\":1}}"; }
MA=$(MMC mm-a 127.0.0.1:18065 muster-dev-mattermost-token)       # server 1, bot A
MB=$(MMC mm-b 127.0.0.1:18065 muster-dev-mattermost-token-b)     # server 1, bot B
M2=$(MMC mm-2 127.0.0.1:18066 muster-dev-mattermost-token)       # server 2
TA=$(curl -s "${A[@]}" "$API/connections?type=telegram" | jq -r '.items[] | select(.name == "Dev Telegram") | .id')
TB=$(TGC tg-b 234567:dev-telegram-token-b)
MMD() { curl -s "${A[@]}" $API/destinations -d "{\"type\":\"mattermost\",\"name\":\"$1\",\"connection_id\":\"$2\",\"team_id\":\"team-dev\",\"channel_id\":\"ch-alerts\",\"mentions\":$MENTIONS,\"limiter\":{\"limit\":10,\"per_seconds\":1}}" | jq -r .destination.id; }
TGD() { curl -s "${A[@]}" $API/destinations -d "{\"type\":\"telegram\",\"name\":\"$1\",\"connection_id\":\"$2\",\"channel_id\":\"$3\",\"mentions\":$MENTIONS,\"limiter\":{\"limit\":20,\"per_seconds\":60}}"; }
DA=$(MMD mm-a-alerts $MA); DB=$(MMD mm-b-alerts $MB); D2=$(MMD mm-2-alerts $M2)
TDA=$(TGD tg-a-alerts $TA @muster_alerts | jq -r .destination.id); TDB=$(TGD tg-b-alerts $TB @muster_alerts | jq -r .destination.id)
WH=$(curl -s "${A[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"hook\",\"mode\":\"events\",\"events\":{\"url\":\"http://127.0.0.1:18093/hook/s069\",\"headers\":[]},
  \"proxy\":{\"enabled\":false},\"mentions\":$MENTIONS,\"limiter\":{\"limit\":50,\"per_seconds\":1}}" | jq -r .destination.id)

# C-14.FR-2, C-14.AC-13: a channel without a discussion group through Connection B
TGD tg-b-nocomments $TB @no_comments | jq -r '.status, .errors[0].detail'
# 422
# Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.
curl -s "$FTG/messages?chat=@no_comments" | jq length                                   # 0

# C-11.FR-1: one Route, six Destinations: five Root messages and the webhook's event
curl -s "${A[@]}" $API/routes -d "{\"name\":\"multi\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"multi\"}],\"urgent\":false,
  \"group_key\":[\"alertname\",\"host\"],\"destination_ids\":[\"$DA\",\"$DB\",\"$D2\",\"$TDA\",\"$TDB\",\"$WH\"],\"policy\":$P}" > /dev/null
G=$(FIRE m1 DiskFull multi); sleep 3
curl -s "${A[@]}" $API/alert-groups/$G/deliveries | jq -c '[.items[] | select(.destination.type != "webhook") | .state]
  | group_by(.) | map({(.[0]): length}) | add'                                          # {"delivered":5}
curl -s 127.0.0.1:18093/_fake/received/s069 | jq 'length > 0'                           # true   (the webhook's events)
curl -s $FMM1/posts | jq -c '[.[] | select(.root_id == "") | .user_id] | sort'          # ["musterdevbotbuserfake00000","musterdevbotuserfake000000"]
curl -s $FMM2/posts | jq -c '[.[] | select(.root_id == "")] | length'                  # 1
curl -s "$FTG/messages?chat=$CH" | jq -c '[.[] | .from.id] | sort'                      # [123456,234567]
curl -s "$FTG/messages?chat=$GR" | jq -c '[.[] | select(.is_automatic_forward)] | length'   # 2
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM telegram_post_copies"              # 4   (both copies, for both Connections)
psql "$MUSTER_DATABASE_URL" -Atc "SELECT ds.name, d.thread_anchor_id = c.copy_message_id::text FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
  JOIN telegram_post_copies c ON c.connection_id = ds.connection_id AND c.channel_message_id::text = d.message_id WHERE ds.type = 'telegram' ORDER BY 1"
# tg-a-alerts|t
# tg-b-alerts|t

# Mattermost Identity space per Connection: bob linked through mm-a only
LINKMM() { R=$(curl -s "${HB[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$1\",\"username\":\"bob\"}" | jq -r .request_id)
  CODE=$(curl -s $2/dms | jq -r '[.[] | select(.user_id == "u-bob")] | last | .message' | grep -o '[2-9A-Z]\{8\}')
  curl -s "${HB[@]}" $API/me/account-links/mattermost/confirmation -d "{\"request_id\":\"$R\",\"code\":\"$CODE\"}" > /dev/null; }
LINKMM $MA $FMM1
ROOTB=$(curl -s $FMM1/posts | jq -r '.[] | select(.root_id == "" and .user_id == "musterdevbotbuserfake00000") | .id')
curl -s -X POST $FMM1/press -d "{\"post_id\":\"$ROOTB\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" | jq -r .answer.ephemeral_text
# Your Mattermost account is not linked to Muster. Link it in your profile: http://localhost:8080/profile
curl -s "${A[@]}" $API/alert-groups/$G | jq -r .status                               # firing
LINKMM $MB $FMM1
curl -s $FMM1/dms | jq -c '[.[] | select(.user_id == "u-bob") | .bot_id] | unique | sort'   # ["musterdevbotbuserfake00000","musterdevbotuserfake000000"]
curl -s -X POST $FMM1/press -d "{\"post_id\":\"$ROOTB\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 2
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"acknowledged","t":"mattermost","u":"bob"}

# The Acknowledge edited each Root message through its own Connection
curl -s "$FTG/messages?chat=$CH" | jq -c '[.[] | {from: .from.id, by: ([.edits[].by] | unique)}]'
# [{"from":123456,"by":[123456]},{"from":234567,"by":[234567]}]
curl -s $FMM1/requests | jq '[.[] | select(.method == "PUT" and (.path | endswith("/patch")) and .status != 200)] | length'   # 0

# Telegram Identity space is all of Telegram: bob links through Connection A, presses through bot B
TOK=$(curl -s "${HB[@]}" $API/me/account-links/telegram -d "{\"connection_id\":\"$TA\"}" | jq -r .deep_link | sed 's/.*start=//')
curl -s -X POST $FTG/private -d "{\"token\":\"234567:dev-telegram-token-b\",\"from\":{\"id\":5001,\"username\":\"bob_t\"},\"text\":\"/start $TOK\"}" > /dev/null; sleep 1
curl -s "$FTG/messages?chat=5001&bot=234567" | jq -r '.[-1].text'     # This link is invalid or expired. Start again from your Muster profile.
curl -s -X POST $FTG/private -d "{\"token\":\"123456:dev-telegram-token\",\"from\":{\"id\":5001,\"username\":\"bob_t\"},\"text\":\"/start $TOK\"}" > /dev/null; sleep 1
curl -s "${HB[@]}" $API/me/account-links/telegram -d "{\"connection_id\":\"$TB\"}" | jq -r .deep_link | grep -c 't.me/muster_dev_b_bot?start='   # 1
POSTB=$(curl -s "$FTG/messages?chat=$CH" | jq -r '.[] | select(.from.id == 234567) | .message_id')
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POSTB,\"from\":{\"id\":5001},\"button\":\"Unack\"}" > /dev/null; sleep 2
curl -s $FTG/answers | jq -r '.[-1].text'                                             # Done: Unacknowledge
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POSTB,\"from\":{\"id\":5001},\"button\":\"Ack\"}" > /dev/null; sleep 2
curl -s $FTG/answers | jq -r '.[-1].text'                                             # Done: Acknowledge
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"acknowledged","t":"telegram","u":"bob"}

# Reminders land in the right Threads
ADV 14400; sleep 2
curl -s "$FTG/messages?chat=$GR" | jq -c '[.[] | select(.from.is_bot and (.reply_markup.inline_keyboard != null))
  | {by: .from.id, to: .reply_parameters.message_id}]'
# [{"by":123456,"to":<copy of the post of 123456>},{"by":234567,"to":<copy of the post of 234567>}]
curl -s $FMM1/posts | jq -c '[.[] | select(.root_id != "" and ((.props.attachments[0].actions // []) | length) == 2) | {u: .user_id, root: .root_id}]'
# each Reminder's root is the Root message posted by the same bot

./bin/muster dev doctor | grep -c '^OK  *destination .*: ok'                           # 5   (mm-a, mm-b, mm-2, tg-a, tg-b alerts)
# the webhook Destination has no doctor line: `muster doctor` checks Mattermost and Telegram Destinations only
```

`test/e2e/several_connections_test.go` repeats these checks in four tests and adds what the Notes list under Tests.

**Screenshots** (live-verification artifacts, not committed): stop `make dev` (the Playwright global setup starts its
own `muster dev` on a fresh database), run `make build`, then from `web/`
`MUSTER_E2E_SCREENSHOTS=../bin/live-shots/s069 pnpm exec playwright test e2e/several-connections.spec.ts`; the spec
creates the setup above through the API and the fakes and saves, in English, at 1280 and at 360 px wide:
`connections`, `destinations`, `destination-tg-b`, `route-editor-destinations`, `alert-group-deliveries`,
`delivery-problem-filter`, `profile-messenger-accounts` and `admin-user-account-links`, each as `-desktop.png` and
`-360.png`, into `bin/live-shots/s069/`. The pull request attaches them.

**Live check on the test stand** (`operator_attention`): after the merge, the maintainer's deploy adds a second
Mattermost server, or a second bot on the same one, and a second Telegram bot in the test channel and its discussion
group, and checks by eye: two Root messages, each Thread under its own post, a press through each bot, a Telegram link
made through one bot working through the other, and the Pending fact of `design/facts.md` (both bots receive both
copies and people's comments, neither receives the other's messages).

## Open questions

None. The two questions of the first draft were settled by the maintainer (D308); see Notes.

## Notes

- D308 (resolved open questions): a Telegram channel without a discussion group stays refused as a Destination in L1
  (C-14.FR-2, FR-14 unchanged); a channel-only mode is an L2 backlog item. A channel that loses its discussion group,
  or the bot its rights there, after its Destination was saved keeps today's behaviour: Root messages, edits and
  presses go on, Thread replies go unattached with "Thread not attached" after `telegram.copy_wait` (S-066), the
  Destination check and `muster doctor` fail, and the Destination does not become Broken; C-14.FR-14 says so.
- Suggested commit: `test(e2e): cover several mattermost servers and telegram bots`.
- D305 (binding): several Connections of one messenger are a supported, normal setup; Mattermost Identity spaces stay
  per Connection, the Telegram Identity space is all of Telegram, and one chat reached through two Connections is two
  Destinations, which is allowed.
- **Capability.** C-18, because Identity spaces are the one rule that differs between the messengers when there are
  several Connections, and the story needs Account links made through the profile (S-051) and the profile and user
  pages (S-052) for its screenshots, so it follows S-052 in the Actions phase. Its delivery checks re-verify C-11,
  C-13, C-14 and C-17 in part; C-11 has no UI and belongs to the Shadow phase, which this story must follow.
- **Kind.** `be`: the changes are fakes, the development mode and tests; the Playwright spec checks existing pages and
  changes none.
- **Tests** (`test/e2e/several_connections_test.go`, `DevProcess` mode):
  - `TestSeveralConnectionsRoute` — the Route of the Verification: six Destinations, one Root message for each messenger
    Destination and events at the webhook; Acknowledge, a new Alert (each Thread reply to its own Root message or copy) and
    Resolve through every Connection, each edit from the token or bot that posted; `listLinkableConnections` lists the
    demo and new Connections of both messengers.
  - `TestSeveralConnectionsMattermostSameChannel` — `mm-a` and `mm-b` in `ch-alerts`: two Root messages by two bots,
    each button's `integration.url` ending in its own Connection; bob linked through `mm-a` only: "not linked" through
    `mm-b`, nothing changes; linked through `mm-b` (direct message from bot B): the press runs as bob; a callback to
    `mm-b` carrying `mm-a`'s post, and the reverse, is refused like a bad signature and changes nothing — Muster
    answers it with the empty object, since the post is no Root message of that Connection's Destinations, so there is
    no Destination to answer through (D284); ack timeout notices and Reminders under their own Root messages by their
    own bots; "Still on it" on bot B's Reminder records `reminder_answered` for bob; the Destination check of `mm-b`
    fails once bot B leaves the channel, while `mm-a`'s passes.
  - `TestSeveralConnectionsTelegramSameChat` — `Dev Telegram` and `tg-b` in `@muster_alerts`: two posts, two copies,
    four rows in `telegram_post_copies`, each Thread attached to its own copy; new Alerts, ack timeout notices and
    Reminders replying to their own copies from their own bots; a person's comment under post A changes neither
    Thread; `withhold_copies_from: [234567]` on a new Alert Group: only `tg-b`'s Thread "Thread not attached" after
    `telegram.copy_wait` (development clock), attached after a comment under post B; the copy of post A deleted: `tg-a`
    loses its Thread, `tg-b` keeps it; Account links and presses of the acceptance statements; Alice linked through
    `Dev Telegram` only: bot B's footer names `alice_t` and its Loud Mention is `tg://user?id=…`; a press on one bot's
    post or Reminder that reaches the other bot is not verified — the button data name the Alert Group and the Command,
    not the Destination, so bot A's data on bot B's Reminder of the same Alert Group would be a valid press, and the
    binding to a Connection is the Connection that receives the press; the Destination check of `tg-b-alerts` fails
    once bot B is only a member of the discussion group, while `tg-a-alerts`' passes.
  - `TestSeveralConnectionsTelegramWithoutDiscussionGroup` — the refusal of `@no_comments` through `tg-b`, nothing
    posted there.
- **What the fakes assume.** Telegram: a channel post's automatic copy is sent by Telegram (777000), not by a bot, so
  every admin bot of the discussion group receives it (F-003, F-005); a bot never sees another bot's messages (Bot API
  FAQ); the budget of F-016 is each bot's own in a chat, as Telegram's limits are a bot's — with one shared budget, two
  bots in one channel would hold each other back. Mattermost: a bot with the role Member cannot edit another user's
  post. These are recorded as Pending in
  `design/facts.md` until the test stand confirms them.
- **No new demo Connections.** `muster dev` keeps "Dev Mattermost" and "Dev Telegram" only; the second fake server and
  the second bot listen, and their published tokens are in `internal/devmode`, so the existing tests and specs that
  list Connections keep their counts.
- **Code read while planning, keyed correctly by Connection** (the tests make it permanent): the Mattermost callback
  takes the Connection from its path and binds the press and the Account link to it
  (`internal/mattermost/callback.go`); the Telegram copy buffer, the attach and the wake of replies filter by
  Connection (`internal/delivery/copies.go`, `query.sql` `AttachCopy`, `WakeCopyReplies`); Telegram press bindings
  filter by Connection and chat (`GetTelegramPressBinding`, `GetTelegramReplyPressBinding`); footers and Mentions use
  the Destination's Identity space (`internal/mentions`, the adapters' layouts); clients, limiters and the update lock
  are per Connection.
- `operator_attention`: the live check on the test stand after the merge, which the
  maintainer's deploy makes with a second Mattermost server or bot and a second Telegram bot; it also settles the
  Pending fact.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-18.FR-2 | partial | a token started for one Telegram Connection sent to another bot; the deep link names the bot of its Connection |
| C-18.FR-4 | partial | Identity spaces with two Connections of each messenger |
| C-18.FR-8 | partial | presses through each of several Connections |
| C-18.FR-10 | partial | the profile's list with several Connections of each messenger |
| C-18.AC-3 | partial | after linking through one Connection, a press through another in Telegram, through the same one in Mattermost |
| C-18.AC-7 | partial | the footer and the Mention through a Telegram Connection other than the one linked through |
| C-11.FR-1 | partial | one Root message per Destination with several Connections of one messenger and one chat |
| C-13.FR-4 | partial | presses on two Connections' posts in one channel, and a callback to the wrong Connection |
| C-14.FR-2 | partial | a channel without a discussion group through a second Connection |
| C-14.FR-3 | partial | two Connections' copies in one discussion group |
| C-14.FR-4 | partial | presses on two Connections' posts and Reminders in one chat |
| C-14.AC-13 | partial | the refusal through a second Connection |
| C-17.FR-2 | partial | ack timeout notices of two Destinations in one chat |
| C-17.FR-4 | partial | Reminders of two Destinations in one chat |
| C-01.FR-13 | partial | the second fake Mattermost server and bot, the second Telegram bot and the copy fan-out |
