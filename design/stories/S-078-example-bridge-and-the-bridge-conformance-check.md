---
id: S-078
title: "The conformance check `muster bridge check` against the fake bridge (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-076]
covers: [C-22.FR-21, C-22.AC-22]
files_touched:
  - internal/bridge/conformance.go
  - internal/bridge/conformance_test.go
  - internal/bridge/client.go
  - internal/bridge/client_test.go
  - internal/cli/bridge.go
  - internal/cli/bridge_test.go
  - internal/cli/cli.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - test/e2e/bridge_check_test.go
acceptance:
  - "[C-22.FR-21] `muster bridge check` calls the protocol client directly from `internal/bridge/conformance.go`, outside delivery, which ADR-0017 permits; lint 3 checks calls of the `delivery.Adapter` methods, not of the protocol client, so it needs no entry and `make lint-arch` passes with the lint unchanged; the token read from `--token-file`, and a push secret read from `--push-secret-file`, appear in no output line, log line or returned error, the FAIL reasons taken from the bridge's answers included (`secretleak.go` probe `bridge_check`); only a push secret the check generated itself is printed, for the bridge's configuration."
  - "[C-22.FR-21, C-22.AC-22] Against the fake bridge with every feature, `muster bridge check <fake> --token-file f --channel ch-alerts --forbidden-channel ch-nobot --handle alice --no-interactive` prints PASS for info, channel_check, post, replayed_key, buttons, edit, thread_reply, thread_ref, forbidden_channel, user_lookup, direct_message, delete, poll and answer, SKIP for press and push with `--no-interactive` as the reason, and exits 0 (`test/e2e/bridge_check_test.go`)."
  - "[C-22.FR-21, C-22.AC-22] With the fake offering only `thread_messages`, `user_lookup` `none` and polling, the buttons, user_lookup, direct_message, delete and push steps print SKIP naming the missing feature and the check exits 0; with replays off it prints FAIL for replayed_key and exits 1; with the fake answering `protocol_version` `2` it prints FAIL for info, runs no other step and exits 1 (`test/e2e/bridge_check_test.go`)."
  - "[C-22.FR-21] Without `--no-interactive`, the check opens its push listener and prints its `PUSH` line before it asks for a press; with the fake's push target set to that URL and secret, the press of the button named by the `WAIT press` line arrives by polling and as a signed push under the same id, the check answers it with `answerEvent`, and prints PASS for press, push and answer; the test presses through the fake's `/_fake/press` (`test/e2e/bridge_check_test.go`)."
  - "[C-22.FR-21] The protocol client gains `deleteMessage`, and the fake bridge, which declares `delete`, answers it with `204` for a message of the bot and, as the specification asks, also for one that is already deleted or that it does not know (`client_test.go`, `fakebridge_test.go` against `api/bridge.yaml`)."
verify: "make ci e2e-go"
operator_attention: false
issue: 236
---

# S-078. The conformance check `muster bridge check` against the fake bridge (BE)

## Scope

**IN**

- `muster bridge check <url>`: the conformance check of C-22.FR-21, which people who write a bridge run against it,
  and which the CI of the bridge libraries runs from Muster's development image (S-083, S-084).
- Its end-to-end test against the fake bridge, with every feature and with a reduced set.
- `deleteMessage` in the protocol client and in the fake bridge, which the check's `delete` step needs.

**OUT**

- The example bridge, which moved into the repository of the Go bridge library (D343): the library (S-083), the
  example on it with its image (S-085), and Muster's end-to-end test against that image, which completes C-22.AC-22
  and covers C-22.AC-23 (S-086).
- The Python bridge library (S-084); "Write a bridge" and the protocol reference on the site (S-079); the pages for
  Admins (S-074, S-081, S-076).

## Contracts

- **Conformance check** (`internal/bridge/conformance.go`, `internal/cli/bridge.go`): `muster bridge check <url>` with
  `--token-file`, `--channel`, `--forbidden-channel`, `--handle` (for the lookup and direct-message steps),
  `--push-listen` (default `127.0.0.1:0`), `--push-url` (the address the bridge reaches the listener at; by default the
  listener's own), `--push-secret-file` (a push secret to use instead of a generated one, so that a script can configure
  the bridge before the run) and `--no-interactive`. It runs the steps below in this order and prints each as
  `PASS <step>`, `FAIL <step>: <reason>` or `SKIP <step>: <why>`; it exits `1` when a step failed, `0` otherwise. A
  failed `info` ends the run, because every later step depends on what the bridge declares. The test messages are of
  the kind `test`, titled `muster bridge check: <step>`.

  | Step | Calls | Passes when | Skipped when |
  |---|---|---|---|
  | `info` | `getInfo` | the answer matches the protocol and `protocol_version` is `1` | never |
  | `channel_check` | `checkChannel` of `--channel` | `200` with the channel's name | never |
  | `post` | `postMessage` of a Quiet test envelope without buttons | `201` with a `message_id` | never |
  | `replayed_key` | the same request with the same `Idempotency-Key` | the same `message_id`, `replayed` true | never |
  | `buttons` | `postMessage` with one button of each style hint, at most `max_per_message`, each with 64 bytes of data | `201` | no `buttons` |
  | `edit` | `editMessage` of the `post` message | `200` | never |
  | `thread_reply` | `replyInThread` under the `post` message, `thread_ref` null | `201` with a `thread_ref` | never |
  | `thread_ref` | a second reply with the first one's `thread_ref` | `201` | never |
  | `forbidden_channel` | `checkChannel` of `--forbidden-channel` | `forbidden` | no `--forbidden-channel` |
  | `user_lookup` | `findUsers` with `--handle` | one person | `user_lookup` `none`, or no `--handle` |
  | `direct_message` | `sendDirectMessage` to that person | `201` | no `direct_messages`, `bot_can_start` false, or no person found |
  | `delete` | `deleteMessage` of the `post` message | `204` | no `delete` |
  | `poll` | `pollEvents` with `wait` 0, then again with its `next_cursor` | both answers match the protocol | no `poll` |
  | `press` | waits for a press of the `buttons` message, by polling when offered, else at the push listener | a `press` event with its data unchanged | no `buttons`; `--no-interactive` |
  | `push` | checks what the push listener received during `press` | a signed event — the press, under the id `press` saw — with a valid signature within `bridge.push_tolerance` | no `push`; `--no-interactive` |
  | `answer` | `answerEvent` for the event of `press` or `push`, else for an unknown event id | `200` for an event, `gone` for the unknown id | never |

  In an interactive run against a bridge that offers `push`, the check opens its push listener after `poll` and prints
  one line `PUSH <url> <secret>` with a secret it generates, or `PUSH <url>` when `--push-secret-file` gives the secret,
  so that the bridge can be pointed at it before anything is pressed. Before each wait for a person, which lasts up to
  five minutes, it prints one line `WAIT <step>: <what to do>` that names the message by its `message_id`, so that a
  script can act as the person. The check reuses the client of S-073 through the outbound package, with a policy that
  allows loopback and private addresses, because the person running it chose the target; it needs no database, master
  key or configuration file, and nothing in it writes to the file system, so it runs from the image as
  `docker run --network host -v <token file>:/token:ro ghcr.io/muster-io/muster:edge bridge check …`, which the
  libraries' CI does. It never changes the bridge's configuration.
- **The CLI exception** (ADR-0017, ADR-0016): the conformance check posts, edits and replies through the protocol
  client directly, outside delivery and the interactive path, which ADR-0017 permits because it runs outside any
  installation. Lint 3 checks calls of the `delivery.Adapter` methods (`Publish`, `Update`, `Reply`), not the
  messenger clients beneath them — the Mattermost and Telegram clients are not linted either — so the check needs no
  lint entry and `internal/archlint` does not change. AGENTS.md already states the exception.
- **Delete** (`client.go`, `fakebridge/messages.go`): the protocol client gains `DeleteMessage` for the `delete` step;
  Muster L1 still never deletes a message. The fake bridge, whose default configuration declares `delete`, answers
  `deleteMessage` with `204` for a message of the token's bot, which it marks deleted without queuing
  `message_deleted` (the bot deleted it itself), and with `204` too for a message that is already deleted or unknown,
  because the specification makes a message that no longer exists a success.
- **Secrets** (lint 5): probe `bridge_check` runs the conformance check with a known token and a known push secret
  from files against a server that answers problems whose `detail` echoes the request, and fails when either reaches
  the printed lines, a log line or a returned error.
- **End-to-end** (`test/e2e/bridge_check_test.go`): starts `muster dev` with the harness, builds the check's command
  line against `FakeURLs.Bridge` with the second bot's token (`muster-dev-bridge-token-b`, so that the demo Connection's
  poller does not take its events), and runs it three ways: with every feature and `--no-interactive`; with the
  features cut through `/_fake/config` and with replays off; and interactively: once the `PUSH` line appears, it sets
  the fake's push target to that URL and secret, and once the `WAIT press` line names the message, it presses a button
  of it through `/_fake/press`. The example bridge's image is S-086's.

## Steps

1. Add `DeleteMessage` to the client and `deleteMessage` to the fake. Check: `client_test.go`, `fakebridge_test.go`.
2. Write the conformance check, its command and its secret probe. Check: `conformance_test.go` against the fake bridge
   with faults, `bridge_test.go`, `make lint-arch`.
3. Write the end-to-end test. Check: `make e2e-go`, then Verification.

## Verification

```sh
make dev &                                                                  # the fake bridge listens on 127.0.0.1:18094
printf muster-dev-bridge-token-b > /tmp/ft
./bin/muster bridge check http://127.0.0.1:18094 --token-file /tmp/ft --channel ch-alerts \
  --forbidden-channel ch-nobot --handle alice --no-interactive; echo "exit=$?"
# PASS info · PASS channel_check · PASS post · PASS replayed_key · PASS buttons · PASS edit · PASS thread_reply
# PASS thread_ref · PASS forbidden_channel · PASS user_lookup · PASS direct_message · PASS delete · PASS poll
# SKIP press: --no-interactive · SKIP push: --no-interactive · PASS answer
# exit=0
curl -s -X PUT 127.0.0.1:18094/_fake/config \
  -d '{"capabilities":{"thread_messages":"all"},"user_lookup":"none","event_delivery":["poll"]}' > /dev/null
./bin/muster bridge check http://127.0.0.1:18094 --token-file /tmp/ft --channel ch-alerts --no-interactive | grep SKIP
# SKIP buttons: the bridge does not offer buttons · SKIP forbidden_channel: no --forbidden-channel
# SKIP user_lookup: the bridge looks up nobody · SKIP direct_message: … · SKIP delete: … · SKIP press: … · SKIP push: …
curl -s -X PUT 127.0.0.1:18094/_fake/config -d '{"replays":false}' > /dev/null
./bin/muster bridge check http://127.0.0.1:18094 --token-file /tmp/ft --channel ch-alerts --no-interactive > /tmp/fc
echo "exit=$?"; grep replayed_key /tmp/fc
# exit=1
# FAIL replayed_key: the second request with the same Idempotency-Key created message m-… instead of returning m-…
grep -c muster-dev-bridge-token-b /tmp/fc                                   # 0
make e2e-go                                                                 # TestBridgeCheck passes
```

## Open questions

None.

## Notes

- Suggested commit: `feat(cli): add the bridge conformance check`.
- The example bridge and its image left this story with D343: they live in `muster-io/muster-bridge-go`, built on the
  Go bridge library (S-083, S-085), and S-086 runs this check and the flows of C-22.AC-23 against a released image of
  it. The check comes first because the libraries' CI runs it from `ghcr.io/muster-io/muster:edge`.
- The file keeps its name from before the move; the title says what the story now delivers.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-21 | full | |
| C-22.AC-22 | partial | against the fake bridge; against the released example bridge in S-086 |
