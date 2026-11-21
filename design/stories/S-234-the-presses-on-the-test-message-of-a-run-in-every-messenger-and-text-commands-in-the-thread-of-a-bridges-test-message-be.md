---
id: S-234
title: The presses on the Test message of a run in every messenger, and Text commands in the Thread of a Bridge's Test message (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-231, S-232, S-233, S-067, S-075, S-100, S-061]
covers: [C-42.FR-5, C-42.FR-6, C-42.AC-3]
files_touched:
  - internal/db/migrations/00NN_destination_test_presses.up.sql
  - internal/db/migrations/00NN_destination_test_presses.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - api/openapi.yaml
  - internal/destinations/query.sql
  - internal/destinations/testruns.go
  - internal/destinations/testpresses.go
  - internal/destinations/testpresses_test.go
  - internal/destinations/testruns_integration_test.go
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/teststeps.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/mattermost/teststeps.go
  - internal/telegram/presses.go
  - internal/telegram/presses_test.go
  - internal/slack/presses.go
  - internal/slack/callback.go
  - internal/slack/presses_test.go
  - internal/bridge/presses.go
  - internal/bridge/commands.go
  - internal/bridge/presses_test.go
  - internal/fakes/fakemattermost/presses.go
  - internal/fakes/faketelegram/presses.go
  - internal/fakes/fakeslack/presses.go
  - internal/fakes/fakebridge/events.go
  - internal/logging/events.go
  - test/e2e/destination_test_presses_test.go
acceptance:
  - "[C-42.FR-5, C-42.AC-3] A press on any button of the Test message of a run, by anyone, in the fake Mattermost, Telegram, Slack and bridge servers, changes nothing — no Command is run, no Alert Group, Timeline, Note or Assignee is written — and is answered privately with \"This is a test message; nothing was changed\" (an ephemeral post in Mattermost and Slack, a callback answer in Telegram, the answer of the press event through a Bridge), the same answer a press on the one-message test of C-16 gets; the Test message is recognised by the signed data of its buttons and the run found by `message_ref`, so a press on a run that has ended or expired is answered the same way (`testpresses_test.go`, `presses_test.go` of each messenger, `test/e2e/destination_test_presses_test.go`)."
  - "[C-42.FR-5, C-42.AC-3] `getDestinationTestRun` lists each press that reached Muster in `presses`, with the person — the User when the press carries an Account link, else the messenger's own name of the person —, the button's action, the step whose message it was on and the time on the business clock; the press is stored in `destination_test_presses`, deleted with its run, and shown while the run lives, which is the proof that presses reach Muster for every messenger, a Slack and a Bridge Destination included, where the bot cannot press its own button (C-24.FR-13) (`testpresses_test.go`, `test/e2e/destination_test_presses_test.go`)."
  - "[C-42.FR-5] A Mattermost run presses the first button of its first step through the bot, as the test of C-16.FR-3 does, and records it as a press with the bot as the person; when the fake server answers \"Action integration error\" the step's result says what to do about `AllowedUntrustedInternalConnections`, as the one-message test says (`callback_test.go`, `teststeps.go` tests, `test/e2e/destination_test_presses_test.go`)."
  - "[C-42.FR-5] Text commands in the Thread of a Bridge Destination's Test message (C-22.FR-14) — `ack`, `resolve`, a Snooze preset — are answered as the press is, \"This is a test message; nothing was changed\", and appear in the run's presses with the command in the button's place; a Text command in the Thread of a real Root message is handled as before (`presses_test.go` of `bridge`, `test/e2e/destination_test_presses_test.go`)."
  - "[C-42.FR-6, C-42.AC-3] A press on a Test message counts in no `muster_commands_total` and writes no row of `commands` or `audit_log` beyond the Request log's record of the inbound request (C-23.FR-2); the Test message of a run and the one-message test are told apart in no log field that identifies a person beyond the login the press already logs (`testpresses_test.go`, `make lint-arch`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-234. The presses on the Test message of a run in every messenger, and Text commands in the Thread of a Bridge's Test message (BE)

## Scope

**IN**

- Recognising a press on the Test message of a run in Mattermost, Telegram, Slack and through a Bridge, answering it
  privately and recording it on the run (C-42.FR-5, AC-3).
- The bot's press of the first button of a Mattermost run's first step, and the Text commands of a Bridge's Test
  message.

**OUT**

- The run and its steps: S-231 to S-233. The page that shows the presses: S-237.
- The Commands themselves and the press attribution of real messages: unchanged (C-10, C-18).

## Contracts

- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.6):
  `destination_test_presses` — `id`, `org_id`, `run_id` (`destination_test_runs`, `ON DELETE CASCADE`), `step`,
  `action` (the button's action or a Text command), `person` (text: the User's login, or the messenger's name of the
  person), `user_id` (nullable), `pressed_at` (business clock); an index on `(org_id, run_id)`. No CHECK replaces another;
  down drops the table.
- **Spec** (`api/openapi.yaml`): `DestinationTestRun.presses` (introduced empty by S-231) becomes a list of
  `DestinationTestPress` — `person`, `user` (`EntityRef` or null), `action`, `step`, `pressed_at`; no new operation.
- **Recognising the Test message**: the buttons of a Test message already carry a signed marker that makes the press
  answered as a test (C-16.FR-3; `internal/delivery/presses.go`); this story extends the marker with the run's
  `message_ref` so that the press handler of each messenger (`internal/mattermost/callback.go`,
  `internal/telegram/presses.go`, `internal/slack/{presses,callback}.go`, `internal/bridge/{presses,commands}.go`) calls
  one function of `internal/destinations/testpresses.go` — which finds the run by `(destination, message_ref)`, writes the
  press, and returns "This is a test message; nothing was changed" — and nothing else. A press whose run is gone is
  answered the same and recorded nowhere.
- **Bot press** (`internal/mattermost/teststeps.go`): after the first step the stepper presses the first button of the
  Root message through `POST /api/v4/posts/{post_id}/actions/{action_id}` as a second interactive request, as
  `testmsg.go` does for C-16.FR-3, and the step's result carries the outcome.
- **Fake servers**: the `POST /_fake/press` of the four fakes and the fake bridge's `POST /_fake/thread-message` press a
  button or send a Text command in the Thread of a message they hold, by message and button, as they do for real
  messages; extended only where they lack it, for instance to name a Test message by its run.
- **Log events**: `destination_test_press` in `internal/logging/events.go`, regenerating the reference pages; no metric.

## Steps

1. Write the migration, `schema.md` and the spec part. Check: `make test-integration` runs up, down and up;
   `make generate-check`.
2. Write `testpresses.go` and the marker's extension. Check: `testpresses_test.go` (the run found, a gone run, the
   answer, no Command).
3. Wire the four messengers' handlers and the Bridge's Text commands. Check: `presses_test.go` of each,
   `callback_test.go`.
4. Add the bot's press to the Mattermost stepper. Check: its test (reached, "Action integration error").
5. Write `test/e2e/destination_test_presses_test.go`. Check: the Verification below; `make lint-arch`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; FSL=127.0.0.1:18097/_fake; FBR=127.0.0.1:18094/_fake
# A: the Admin's header array. A run on each Destination type, the first step made (as S-231 to S-233)
D=$(curl -s "${A[@]}" $API/destinations | jq -r '.items[] | select(.name=="infra-critical") | .id')
RID=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"critical"}' | jq -r .id)
# Mattermost: the bot's press of the first button is recorded at once
curl -s "${A[@]}" $API/destinations/$D/test-runs/$RID | jq -c '.presses[] | {p: .person, a: .action, s: .step}'
# C-42.AC-3: Ivan presses Acknowledge on the Test message in the fake Mattermost server
POST=$(curl -s $FMM/posts | jq -r '[.[] | select(.root_id=="")][0].id')
curl -s -X POST $FMM/press -d "{\"post_id\":\"$POST\",\"user\":\"ivan\",\"action\":\"ack\"}" | jq -c .
curl -s $FMM/ephemeral | jq -r '.[-1].message'        # This is a test message; nothing was changed
curl -s "${A[@]}" $API/destinations/$D/test-runs/$RID | jq -c '[.presses[] | .person]'   # ["<the bot>","ivan"]
psql "$MUSTER_DB" -c "select count(*) from alert_groups where title like '%test%'"        # 0
curl -s localhost:8082/metrics | grep -c '^muster_commands_total'                        # the same series and values as before
# Telegram, Slack and the bridge: the same press by their fakes' POST /_fake/press (the bodies of S-067, S-100 and S-075's tests)
curl -s -X POST $FTG/press -d "$TG_PRESS" ; curl -s $FTG/answers | jq -r '.[-1].text'      # This is a test message; nothing was changed
curl -s -X POST $FSL/press -d "$SL_PRESS" ; curl -s $FSL/ephemeral | jq -r '.[-1].text'    # the same, as an ephemeral message
curl -s -X POST $FBR/press -d "$BR_PRESS" ; curl -s $FBR/answers | jq -r '.[-1].text'      # the same, as the answer of the press event
# each is listed in the run's `presses` with the person and the action
# a Text command in the Thread of the Bridge's Test message
curl -s -X POST $FBR/thread-message -d "$BR_ACK_IN_THREAD"      # the text "ack" by ivan in the Thread of the Test message
curl -s $FBR/answers | jq -r '.[-1].text'                       # This is a test message; nothing was changed
# a press after the run ended or expired is answered the same
curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 3660}'
curl -s -X POST $FMM/press -d "{\"post_id\":\"$POST\",\"user\":\"ivan\",\"action\":\"ack\"}" ; curl -s $FMM/ephemeral | jq -r '.[-1].message'
make lint-arch
```

`test/e2e/destination_test_presses_test.go` repeats these for all four messengers with the exact bodies of each fake, and
for the Mattermost fake's "Action integration error" fault.

## Open questions

None.

## Notes

- Suggested commit: `feat(destinations): record and answer presses on the test message of a step-by-step run`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number
  is assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- The presses are the proof that buttons reach Muster for every messenger: a Slack bot cannot press its own button
  (C-24.FR-13), so a person presses and the page shows the press (S-237).
- Presses on the one-message test of C-16 are answered as before and are not recorded: only a run has a place to show
  them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-5 | full | the buttons themselves are S-231 to S-233's; the page shows the presses in S-237 |
| C-42.FR-6 | partial | a press writes nothing and counts in no command metric; the steps' own part is S-231 to S-233 |
| C-42.AC-3 | full | |
