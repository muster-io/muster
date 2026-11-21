---
id: S-232
title: The steps of a test run for Telegram and Slack Destinations (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-231, S-042, S-066, S-099, S-171, S-177]
covers: [C-42.FR-2, C-42.FR-3, C-42.FR-4, C-42.FR-6, C-42.AC-4]
files_touched:
  - internal/telegram/teststeps.go
  - internal/telegram/teststeps_test.go
  - internal/telegram/testmsg.go
  - internal/slack/teststeps.go
  - internal/slack/teststeps_test.go
  - internal/slack/testmsg.go
  - internal/destinations/testruns.go
  - internal/destinations/testruns_test.go
  - internal/destinations/testmentions.go
  - internal/destinations/testmentions_test.go
  - internal/delivery/teststeps.go
  - internal/delivery/teststeps_test.go
  - internal/runtime/runtime.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/fakes/fakeslack/messages.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - test/e2e/destination_test_runs_telegram_test.go
  - test/e2e/destination_test_runs_slack_test.go
acceptance:
  - "[C-42.FR-2, C-42.FR-3] A step-by-step test of a Telegram Destination makes the eight steps of S-231 in order — the Root message in the channel marked \"🧪 Test message\", its edits, and the Thread replies as comments in the discussion group, waiting for the copy of the post as delivery does (S-066) — each through the interactive path under `delivery.interactive_budget` with the tokens of the Destination's limiter, with the compact layout and the Destination's button set; a request that finds no limiter token in time ends `limited` and may be tried again, and a failed step stops the run (`teststeps_test.go` of `telegram`, `test/e2e/destination_test_runs_telegram_test.go`)."
  - "[C-42.FR-2, C-42.FR-3] The same for a Slack Destination: the Root message through `chat.postMessage` as blocks with its attachment and the status colour, its edits through `chat.update`, and the Thread replies with the Root message's `thread_ts`, the Snooze menu among the buttons of the statuses that show it; the requests show the bot token masked, the answer truncated and masked, the duration and the error class of Slack's error mapping, and a Slack error such as `ratelimited` or `channel_not_found` is the step's class as in delivery (`teststeps_test.go` of `slack`, `test/e2e/destination_test_runs_slack_test.go`)."
  - "[C-42.FR-4, C-42.AC-4] Each Loud step of a Telegram or a Slack run lists whom the Destination's Mention entries would mention, with the reason of each that would mention nobody — a person with no Account link in that messenger's Identity space, a user group the Connection cannot read (`usergroups:read`), a Telegram discussion group where the person is not a member — and writes the names as plain text after \"Would mention:\"; the Test message carries no Mention markup, no `tg://user` link, no `<@U…>` and no `<!subteam^…>`, no \"Mention not delivered\" is recorded and no warning of the Destination changes (`testmentions_test.go`, `teststeps_test.go`, the e2e tests)."
  - "[C-42.FR-6] A Telegram or Slack run writes no Alert Group, Alert, Timeline, Note or Assignee, adds no row of `deliveries` or `thread_replies`, never reconciles its messages, and counts only in `muster_client_requests_total` with the client `interactive`; its Test messages stay in the channel when the run ends and Muster deletes none (`teststeps_test.go`, the e2e tests, `make lint-arch`)."
  - "[C-42.FR-2] The built-in texts of the Test message of a Telegram or Slack run are in the language of the source's Route, for the built-in example that of the first Route, by name, that names the Destination; the Slack message stays within the limits of its blocks and Telegram's within its message length, with the shortening of the compact layout (`teststeps_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-232. The steps of a test run for Telegram and Slack Destinations (BE)

## Scope

**IN**

- The stepper of S-231 for Telegram and for Slack: the eight steps with each messenger's requests, their masking and
  classes, the Mention entries' "Would mention:" with the reasons these messengers have, and no Mention markup
  (C-42.FR-2, FR-3, FR-4, FR-6).

**OUT**

- The run, its operations, the steps' order, the expiry and the Audit log: S-231. A Bridge and an outgoing webhook:
  S-233. The presses on the Test message and the text commands: S-234.
- The one-message test of C-16 for both messengers, which stays as S-047 and S-099 made it.

## Contracts

- **Stepper**: `delivery.Stepper` of S-231, implemented by `*telegram.Tester` in `internal/telegram/teststeps.go` and
  `*slack.Tester` in `internal/slack/teststeps.go`, beside their existing `testmsg.go`, which they reuse for the marker,
  the masking and the limiter. They build the request of each step from the `messages.Source` that
  `internal/messages/teststeps.go` (S-231) provides, with the adapters' own layout (`layout.go` of each) and button
  set; the Test message is never reconciled and never read back by delivery. The registry of steppers in
  `internal/destinations/testruns.go` gains the two types, and `internal/runtime/runtime.go` wires them.
- **Telegram**: the Root message is a channel post; the Thread replies are comments in the linked discussion group, so a
  Thread reply waits for the copy of the post as `internal/telegram` does for delivery, within the interactive budget,
  and ends `limited`, `transient` or the class of the answer when it does not appear; a Destination with no discussion
  group makes the Thread replies a step result that says "no discussion group" and is not an error, as delivery does not
  send them either.
- **Slack**: `chat.postMessage` with `blocks` and one attachment, `chat.update` for the edits, replies with `thread_ts`;
  the bot token is masked in every request and the answer is shown as untrusted text.
- **Mentions** (`internal/destinations/testmentions.go`, S-231): the reasons specific to these messengers are added to
  the function's table — no Account link in the Identity space, the scope `usergroups:read` missing, a person who is not a
  member of the discussion group (S-132's `getChatMember`) — and the names are plain text.
- **Fake servers**: `internal/fakes/faketelegram/chats.go` and `internal/fakes/fakeslack/messages.go` record the edits and
  replies of a message in order, and the discussion group's copy of a post, if they do not already, so that the e2e
  tests read the eight steps; their `/_fake/requests` and faults are unchanged.
- **Log events, metrics, settings**: those of S-231; none are added.

## Steps

1. Write the Telegram stepper. Check: `teststeps_test.go` of `telegram` (the eight requests, the copy wait, no
   discussion group, masking, `limited`).
2. Write the Slack stepper. Check: `teststeps_test.go` of `slack` (blocks, the attachment colour per status, `thread_ts`,
   the Snooze menu, Slack error classes).
3. Add the two types to the registry and the Mention reasons. Check: `testruns_test.go`, `testmentions_test.go`.
4. Extend the two fake servers and write the e2e tests. Check: the Verification below; `make lint-arch`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FTG=127.0.0.1:18081/_fake; FSL=127.0.0.1:18097/_fake
# A: the Admin's header array, as in S-231; the Destinations "tg-alerts" (the channel and its discussion group) and "slack-alerts"
for NAME in tg-alerts slack-alerts; do
  D=$(curl -s "${A[@]}" $API/destinations | jq -r --arg n $NAME '.items[] | select(.name==$n) | .id')
  RID=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"warning"}' | jq -r .id)
  for i in 2 3 4 5 6 7 8; do curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps | jq -r '.name + " " + (.requests[0].status|tostring)'; done
done
# the eight steps of each, in order: created, new_alert, acknowledge, reminder, takeover, snooze, resolve, unresolve
curl -s $FTG/requests | jq -r '[.[] | select(.method=="editMessageText" or .method=="sendMessage") | .method] | join(" ")'   # sendMessage editMessageText ... in order
curl -s $FSL/requests | jq -r '[.[] | .method] | join(" ")'                                                          # chat.postMessage chat.update ... in order
# C-42.AC-4: an entry naming a person without a Telegram or Slack Account link
curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"}}' | jq -c '.steps[0].would_mention.entries[] | select(.reason != null)'
# {"kind":"person","name":"olga","reason":"has no linked Slack account"}
curl -s $FSL/messages | jq -r '.[].text' | grep -c '<@'        # 0: no Mention markup
curl -s "${A[@]}" $API/destinations/$D | jq -c '.warnings'      # unchanged
# C-42.FR-6: nothing else changed
psql "$MUSTER_DB" -c "select (select count(*) from deliveries where destination_id=(select id from destinations where public_id='$D')), (select count(*) from thread_replies)"   # as before the run
curl -s localhost:8082/metrics | grep -E '^muster_client_requests_total\{.*interactive' | head -2                                                                               # these two grow
# a scripted fault: Slack answers ratelimited once; the step's class is the one delivery gives it, and the same step tried again succeeds
curl -s -X POST $FSL/faults -d '{"method":"chat.update","error":"ratelimited","times":1}'
curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps | jq -c '.requests[0].error_class'   # "rate_limited"
curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps | jq -c '.requests[0].error_class'   # "none": the retry made the step
```

`test/e2e/destination_test_runs_telegram_test.go` and `…_slack_test.go` repeat these through the generated client,
including the language of the built-in texts (a Route in Russian, "🧪 Тестовое сообщение").

## Open questions

None.

## Notes

- Suggested commit: `feat(telegram): add step-by-step test runs for telegram and slack destinations`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- A Telegram run whose Destination has no discussion group shows the Thread replies of the steps as not sent, which
  mirrors delivery: that Destination never posts Thread replies.
- Slack cannot press its own bot's button, so a Slack run's buttons are checked by a person's press (S-234); this story
  only sends them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-2 | partial | the steps for Telegram and Slack; Mattermost is S-231, a Bridge and a webhook S-233 |
| C-42.FR-3 | partial | the interactive path, masking and classes for the two; the others are S-231, S-233 |
| C-42.FR-4 | partial | "Would mention" and the plain text for the two; the others are S-231, S-233 |
| C-42.FR-6 | partial | no writes and no counts for the two; the others are S-231, S-233 |
| C-42.AC-4 | partial | the entry that mentions nobody in Telegram and Slack; Mattermost is S-231, a Bridge S-233 |
