---
id: S-083
title: "The Go bridge library `muster-bridge-go` with its CI and releases (BE)"
capability: C-22
kind: be
layer: L1
repository: muster-io/muster-bridge-go
depends_on: [S-078]
covers: [C-22.FR-25, C-22.AC-25]
files_touched:
  - LICENSE
  - README.md
  - CONTRIBUTING.md
  - SECURITY.md
  - AGENTS.md
  - go.mod
  - Makefile
  - .golangci.yml
  - .gitignore
  - release-please-config.json
  - .release-please-manifest.json
  - .github/renovate.json5
  - .github/pull_request_template.md
  - .github/workflows/ci.yml
  - .github/workflows/release-please.yml
  - .github/workflows/codeql.yml
  - .github/workflows/scorecard.yml
  - api/bridge.yaml
  - api/codegen.yaml
  - doc.go
  - bridge.go
  - bridge_test.go
  - server.go
  - server_test.go
  - problem.go
  - problem_test.go
  - store.go
  - store_test.go
  - events.go
  - events_test.go
  - push.go
  - push_test.go
  - mention.go
  - mention_test.go
  - spec_test.go
  - example_test.go
  - examples/minimal/main.go
acceptance:
  - "[C-22.FR-25] The repository `muster-io/muster-bridge-go` has the default branch `master`, whose first commit, `chore: add the licence`, adds the MIT `LICENSE` (\"Copyright The Muster Authors\") and a README stub and nothing else; `go.mod` declares `github.com/muster-io/muster-bridge-go`; every Go file and workflow carries `SPDX-License-Identifier: MIT` and the copyright line, which `make lint` checks; CONTRIBUTING.md takes contributions under MIT without a CLA; `make licenses` fails on a dependency whose licence is not on the list of the Contracts."
  - "[C-22.FR-25] A bridge implements `bridge.Messenger` — post, edit, reply in a Thread, check a channel, answer — and, for the features it declares, `UserFinder`, `DirectMessenger` and `Deleter`; `bridge.New` refuses a configuration whose features need an interface the messenger lacks, or whose `info` would not match `api/bridge.yaml`, naming the reason (`bridge_test.go`)."
  - "[C-22.FR-25] The handler serves every operation of `api/bridge.yaml` under `/v1/`, answers `401` to a missing or wrong token and calls the messenger with the bot of the token; before calling the messenger it refuses with `400` `invalid` a request that does not match the specification, a request body over the library's cap of 1 MiB, a `body` longer than `max_body_length` code points, buttons sent without `buttons`, more buttons than `max_per_message` and data longer than `data_max_bytes`; every request and answer of `spec_test.go`, one per operation and per problem, validates against the embedded copy of `api/bridge.yaml`."
  - "[C-22.FR-25] A repeated `Idempotency-Key` under the same token answers the first result with `replayed` true without calling the messenger again, whatever the repeated body; under another token it is a new request; a key whose first request is still running is `transient`; a failed request stores nothing; keys live 24 hours (`store_test.go`, `server_test.go`)."
  - "[C-22.FR-25] An `Error` of each class becomes its problem with the bridge's `detail` and the status the specification gives it — `transient` (`500`), `rate_limited` with `Retry-After` and `scope`, `gone` with `reason` `edit_window_closed`, `forbidden`, `invalid` — a class the operation does not declare is mapped as the Contracts say, and any other error or a panic becomes `transient` with a generic `detail` that carries none of the error's text, which the library logs without the token (`problem_test.go`, `spec_test.go`)."
  - "[C-22.FR-25] Reported presses, Thread messages, deleted messages and direct messages become events of their bot: a direct message whose whole text, trimmed and after an optional `/start `, is 43 characters of base64url becomes `account_link`, any other is ignored; an event reported twice with the same messenger id is queued once; `pollEvents` answers the events after the cursor at once or within `wait`, keeps them at least 24 hours and refuses a cursor it does not know as `invalid`; `answerEvent` reaches the messenger with the event it answers and is `gone` for an id the library did not issue (`events_test.go`)."
  - "[C-22.FR-25] With a push target set for a bot, its events are posted to the callback one at a time and in order, signed as Standard Webhooks specifies with the push secret, retried with back-off under the same `webhook-id` for at least an hour, dropped on `400` and kept on `401`; a test receiver verifies every signature and timestamp (`push_test.go`)."
  - "[C-22.FR-25, C-22.AC-25] `make spec-check` compares `api/bridge.yaml` with the one on Muster's `master` and fails naming the difference — a copy with one byte changed fails it — and `make generate-check` fails when `models.gen.go` is not what oapi-codegen makes of the copy; the CI jobs `spec` and `generate` run them on every pull request, on every push to `master` and daily."
  - "[C-22.AC-25] `make conformance` runs `examples/minimal` and `muster bridge check` from `ghcr.io/muster-io/muster:edge` against it three times, each exiting 0: with every feature and `--no-interactive`, every step passes but press and push, which are skipped; with `BRIDGE_FEATURES=thread_messages`, the buttons, user_lookup, direct_message and delete steps are skipped too; interactively, with the example pushing to the check's listener, a script presses the button named by the `WAIT press` line through the example's `/demo/press`, and press, push and answer pass. The CI job `conformance` runs it on every pull request, on every push to `master` and daily."
  - "[C-22.FR-25] CI runs `lint`, `test` with the race detector on the two supported Go releases and a coverage gate of 90 % with `models.gen.go` excluded, `generate`, `spec`, `licenses`, `vulncheck` and `conformance`, joined in the check `ci`; release-please keeps a release pull request with `0.x` versions; Renovate, CodeQL and OpenSSF Scorecard run as in Muster's repository; every Action is pinned by commit SHA."
verify: "make ci conformance"
operator_attention: true
issue: 256
---

# S-083. The Go bridge library `muster-bridge-go` with its CI and releases (BE)

## Scope

**IN**

- The repository `muster-io/muster-bridge-go`, from its first commit: the MIT licence, the Go module
  `github.com/muster-io/muster-bridge-go` with the package `bridge`, the contributor files and the agent guide.
- The library: the protocol's part of a bridge (C-22.FR-25), with types generated from its copy of `api/bridge.yaml`.
- A minimal example, an in-memory bridge of one file, which the README's quick start shows and the CI checks.
- The copy check, the conformance check from Muster's development image, and the CI, release, Renovate, CodeQL and
  Scorecard workflows.

**OUT**

- The example bridge with its chat page and its image (S-085), and Muster's end-to-end test against that image (S-086).
- The Python library (S-084); "Write a bridge" on Muster's documentation site (S-079).
- `muster bridge check` itself, in Muster's repository (S-078).

## Contracts

- **Repository** (D343): public, default branch `master`, squash merges only, branches deleted after merge, no wiki — as
  the maintainer created it. The repository is empty, so the first commit, `chore: add the licence`, adds only `LICENSE`
  (MIT, "Copyright The Muster Authors") and a README stub of a few lines, and is pushed to `master` to create the
  branch; everything else goes through this story's pull request, from the branch `feat-bridge-library`. Every Go file,
  workflow and Makefile starts with `SPDX-License-Identifier: MIT` and `Copyright The Muster Authors` in its comment
  syntax; `make lint` fails naming a file without them (generated files, lock files and `api/bridge.yaml`, which states
  its licence in `info.license`, excepted). No CLA (ADR-0017): CONTRIBUTING.md says contributions are accepted under
  MIT, asks for conventional commits on one line without AI attribution, and points to Muster's `design/stories/` for
  the stories of this repository. SECURITY.md asks for private reports through GitHub's private vulnerability reporting.
  AGENTS.md is the guide for agents and people working here: the Makefile as the entry point, the copy of
  `api/bridge.yaml` that is never edited by hand, the stories in Muster's repository with `repository:
  muster-io/muster-bridge-go`, the pull request with `Part of muster-io/muster#<issue>` — a pull request here does not
  close an issue in Muster's repository, so the issue there is closed by hand once it merges — `Story: S-NNN` and its
  evidence table, and the rules above.
- **Module** (`go.mod`): `github.com/muster-io/muster-bridge-go` with `go 1.26.0` and no `toolchain` line — the older
  of the two supported Go releases, 1.26 and 1.27, which CI tests both of with `GOTOOLCHAIN: local`, so neither run
  downloads another toolchain. Dependencies: kin-openapi (MIT) for the validation against the specification, with
  its own permissive dependencies, and what the generated models need; each is stated in the pull request with its
  licence, latest release and maintenance status, as Muster's ADR-0001 asks. `make licenses` (go-licenses) allows MIT,
  MIT-0, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, 0BSD, Unlicense and CC0-1.0.
- **Types** (`api/bridge.yaml`, `api/codegen.yaml`): the copy of Muster's `api/bridge.yaml`, byte for byte; oapi-codegen
  (`models` only, package `bridge`) writes `models.gen.go`, the protocol's schemas as Go types — `Info`, `Capabilities`,
  `Limits`, `MessageInput`, `MessageContent`, `ThreadReplyInput`, `Person`, the events and `Problem` among them —
  which the library's own API uses, and the constants of their enums, such as `ProblemReasonEditWindowClosed`. The
  package embeds the copy for its validation.
- **API of the package** (`bridge.go`, `doc.go`), the names the bridge author sees:
  - `Messenger`, the calls every bridge implements, each given the bot whose token Muster sent and returning the
    protocol's answer or an error: `PostMessage`, `EditMessage`, `ReplyInThread` (with the `thread_ref` Muster sent,
    returning the one to keep), `CheckChannel` (the channel's name) and `AnswerEvent` (given the event it answers as a
    `ReportedEvent` — its id, `EventType`, message, `thread_ref`, person, time and the bridge's own `Chat` — so the
    bridge knows where to answer; returning where the answer was shown). `IdempotencyKeyFrom(ctx)` gives a call the
    `Idempotency-Key` of its request, for a messenger that takes one of its own.
  - The optional interfaces, needed by the features that use them: `UserFinder` (`FindUser`, for a `user_lookup`
    other than `none`), `DirectMessenger` (`SendDirectMessage`, for `direct_messages`) and `Deleter` (`DeleteMessage`,
    for `delete`).
  - `Config`: the bridge's name and version (`Name`, `Version`), the messenger's name (`MessengerName`; `Messenger` is
    the interface above), the bots (`Bots`, each a `Bot`: a name and a token, and optionally a push target,
    `PushTarget`: the callback URL and the push secret), the capabilities, the limits, the user lookup, the event
    delivery modes (both by default), the optional `link_url_template`, a `Store` (the memory store by default) and a
    `*slog.Logger`.
  - `New(Config, Messenger) (*Bridge, error)`: checks the configuration — every bot with a non-empty token of its own,
    features backed by the interfaces they need, the resulting `info` valid against the specification (for example
    `max_body_length` from 500 to 100,000, `data_max_bytes` at least 64, a `link_url_template` with `{token}` and an
    `http` or `https` scheme) — and names what is wrong.
  - `(*Bridge).Handler() http.Handler`, mountable under a path prefix and served by an `http.Server` with no
    `WriteTimeout`, or one above 75 seconds, because `pollEvents` holds a request for up to 60 seconds (Muster asks for
    25 and waits 15 more) — the README, `doc.go` and the minimal example say so; `SetPush(bot, *PushTarget)` changes a
    bot's push target at run time, `ErrUnknownBot` for a bot not in `Config.Bots` and `ErrClosed` after `Close`;
    `Close(ctx)` stops the pushers.
  - The reporting calls, which a bridge makes when its messenger tells it something: `Press`, `ThreadMessage`,
    `MessageDeleted` and `DirectMessage`, each with the bot and a struct of the same name — the messenger's own id for
    the update (`UpdateID`, empty when it has none), the message, the person and the text or data, and the time (`At`,
    zero for now); every one but `MessageDeleted` also carries `Chat`, an opaque string of the bridge — a chat or a
    callback id — that the library keeps with the event, gives back in `ReportedEvent.Chat` and never sends to Muster.
    Each returns once the event is queued, so a bridge confirms a press to its messenger within its deadline
    independently of Muster; a report for a bot not in `Config` is `ErrUnknownBot`, one whose event needs a feature the
    bridge does not offer `ErrNotOffered`, one that would not match `api/bridge.yaml` `ErrInvalidEvent`. The event types
    are the constants of `EventType` (`EventTypePress`, `EventTypeThreadMessage`, `EventTypeAccountLink`,
    `EventTypeMessageDeleted`).
  - `Error` with its `Class` — the constants `ClassTransient`, `ClassRateLimited`, `ClassGone`, `ClassForbidden` and
    `ClassInvalid` — and the constructors `TransientError`, `RateLimitedError` (with the wait and the scope),
    `GoneError` (optionally with `ReasonEditWindowClosed`), `ForbiddenError` and `InvalidError`, each with a `detail`
    for the Admin.
  - `Store`, the storage of idempotency results and events, and `NewMemoryStore`; a bridge that must keep its keys and
    queued events across restarts implements `Store` over its own database. Its methods: `ClaimKey`, which answers a
    `KeyClaim` — a `KeyState` (`KeyClaimed`, `KeyRunning`, `KeyAnswered`), the `StoredAnswer` of an answered key and the
    id of a new claim; `SaveKey` and `ReleaseKey`, which take that claim id, so that a request whose claim expired can
    neither overwrite nor release the claim of a newer request with the same key; `AppendEvent`, `EventsAfter`, which
    answers `ErrUnknownCursor` for a cursor it did not issue for the bot, and `FindEvent`, over `StoredEvent`s.
  - `ReplaceMentions(body, mentions, render)`, which replaces each Mention marker `[@name](muster-mention:ref)` of a
    body by what `render` makes of its entry in `mentions` — a native mention or the plain name.

  The names are the contract; their exact signatures are the implementation's, and a renamed one is corrected in this
  file in the same pull request.
- **Protocol behaviour** (`server.go`, `problem.go`, `store.go`, `events.go`, `push.go`), as `api/bridge.yaml` specifies
  it:
  - **Tokens**: `Authorization: Bearer` compared in constant time with every bot's token; `401` with a problem
    otherwise. Each request is the bot's whose token it carries, and its keys and events are that bot's.
  - **Validation and limits**: requests are validated against the embedded specification; a request body over 1 MiB —
    the library's own cap, which leaves room for any `max_body_length` up to 100,000 code points, the most `New` accepts
    — a `body` longer than `max_body_length` code points, buttons without `buttons`, more than `max_per_message` buttons
    and button data longer than `data_max_bytes` are `invalid`, and the messenger is not called. `wait` above 60 is
    `invalid`. Every refusal before the messenger is called is `400` with the class `invalid`; an operation of a feature
    the bridge does not offer — `deleteMessage` without `delete`, `findUsers` without a user lookup,
    `sendDirectMessage` without `direct_messages`, `pollEvents` without the poll mode — answers `404` with the class
    `invalid`.
  - **Idempotency**: for `postMessage`, `replyInThread`, `sendDirectMessage` and `answerEvent`, the first successful
    result of a key is stored per token for 24 hours and answered again, marked `replayed`, without calling the
    messenger; a key whose first request is still running is `transient`; a failed request stores nothing.
  - **`info`**: built once by `New` from `Config` and served as it is; a nil `buttons.styles` is sent as `[]`.
  - **Deletion**: `deleteMessage` answers `204` also when the `Deleter` reports the message `gone`, because the
    specification makes a message that no longer exists a success.
  - **Problems**: an `Error` becomes `application/problem+json` with the status the specification gives its class —
    `transient` `500`, `rate_limited` `429`, `gone` `404`, `forbidden` `403`, `invalid` `400` — and `Retry-After` and
    `scope` for `rate_limited`; `reason` is sent only as `edit_window_closed`, the one reason the protocol defines. A
    class the operation does not declare is mapped to one it does: `gone` from `deleteMessage` is `204` (above); `gone`
    from `postMessage`, `checkChannel` or `sendDirectMessage`, and `invalid` from `checkChannel`, are `forbidden`,
    because a channel or an account that is missing or unusable is `forbidden`, never `gone`; any other undeclared class
    is `transient`, and the library logs it as a mistake of the bridge. Any other error and a recovered panic become
    `transient` with the detail "The bridge could not reach its messenger.", and the error is logged at level error,
    never with a token, a push secret or an Account link token.
  - **Events**: each reported event gets an `id` — when the messenger's own id for the update is given, the base64url
    of a SHA-256 over the bot, the event type and that id, so that a messenger that delivers an update twice queues one
    event, two kinds of report about one update stay two events, and the id always fits the protocol's `webhook-id`;
    random otherwise — and `occurred_at`, the given time or the library's
    clock; it is appended to its bot's queue in the `Store`. `pollEvents` answers the events after the cursor at once,
    or waits up to `wait` seconds for one; the cursor is opaque; events before a cursor Muster sent may be dropped, and
    events are kept at least 24 hours. A direct message is an `account_link` event only when its whole text, trimmed and
    after an optional `/start `, is 43 characters of base64url; any other direct message is ignored. `answerEvent` for
    an id the library did not issue, or no longer holds, is `gone` without calling the messenger.
  - **Push**: with a push target, each event of the bot is posted, in order and one at a time, to the callback with
    `webhook-id` (the event's `id`), `webhook-timestamp` and `webhook-signature` — `v1,` and the base64 HMAC-SHA256 of
    `<id>.<timestamp>.<body>` with the decoded `whsec_` secret; `204` acknowledges it, `400` drops it, `401` and every
    other answer or a timeout retry it with exponential back-off up to five minutes between attempts, under the same
    `webhook-id`, for at least an hour, after which the event stays in the queue for polling. The pushers stop on
    `Close`.
- **Minimal example** (`examples/minimal/main.go`): an in-memory bridge — channels from `-channels` (default
  `alerts,ops`; any other channel id is `forbidden`), people from `-users` (default `alice:Alice Smith,bob:Bob Brown`,
  found by login), messages written to the log — with the token from `-token-file`, listening on `-listen` (default
  `127.0.0.1:18095`), every feature by default and the features listed in `BRIDGE_FEATURES` otherwise; `user_lookup` is
  `login` while `direct_messages` is on and `none` without it; push with `-push-url` and `-push-secret-file`. Outside
  the protocol it serves `POST /demo/press` `{message_id, user}`, which presses the first button of that message as that
  person, so that a script can play the person of an interactive `muster bridge check`; like the rest of the example it
  has no authentication and listens on loopback by default. The README's quick start is this file.
- **Copy check** (`Makefile`, `ci.yml`, ADR-0017): `make spec-check` downloads `api/bridge.yaml` from
  `https://raw.githubusercontent.com/muster-io/muster/master/api/bridge.yaml` (`MUSTER_SPEC_URL` overrides it) and
  compares it byte for byte with the copy, printing the difference; `make spec-sync` replaces the copy and runs
  `make generate`. The CI job `spec` runs `make spec-check`.
- **Conformance** (`Makefile`, `ci.yml`): `make conformance` builds and starts `examples/minimal` with a generated token
  file of mode 0644, so that the image's user 65532 can read it, and runs, with `MUSTER_IMAGE` defaulting to
  `ghcr.io/muster-io/muster:edge`:

  ```sh
  docker run --rm --network host -v <token file>:/token:ro $(MUSTER_IMAGE) bridge check http://127.0.0.1:18095 \
    --token-file /token --channel alerts --forbidden-channel nobot --handle alice --no-interactive
  ```

  then again with `BRIDGE_FEATURES=thread_messages`, and a third time interactively:
  the example starts with `-push-url` pointing at the check's listener (`--push-listen 127.0.0.1:18096`) and the secret
  that the check reads from `--push-secret-file`; the script reads the check's `WAIT press` line and presses that
  message through `POST /demo/press` as `alice`. It fails on an exit code other than 0 or on a step other than the
  expected skips. The edge image is the point: a library that Muster's newest check fails shows it on the next run.
- **CI** (`.github/workflows/ci.yml`): on pull requests, pushes to `master`, daily at a fixed hour and by hand, with
  `GOTOOLCHAIN: local`; jobs `lint` (golangci-lint with `.golangci.yml`, the header check), `test` (`go test -race` with
  the coverage gate of 90 % of the package's statements, `models.gen.go` excluded, on Go 1.26.x and 1.27.x), `generate`,
  `spec`, `licenses`, `vulncheck` (govulncheck) and `conformance`, and the job `ci` that passes only when all of them
  did, which the ruleset of `master` requires. The tools are pinned in the Makefile under `# renovate:` comments and
  built into `bin/tools/`.
- **Releases** (`release-please.yml`, `release-please-config.json`, `.release-please-manifest.json`): release-please
  with the release type `go`, `bump-minor-pre-major`, `include-component-in-tag: false` and the manifest at `0.0.0`, run
  with the GitHub App's token as in Muster's repository, so that its tag starts workflows; a release is the tag
  `vX.Y.Z`, and the workflow then asks `proxy.golang.org` for the new version so that `go get` finds it at once.
  Versions stay `0.x` until Muster's first release, then `1.0.0` for protocol version 1 (ADR-0017).
- **Renovate, CodeQL, Scorecard** (`.github/renovate.json5`, `codeql.yml`, `scorecard.yml`): the policy of Muster's
  `.github/renovate.json5` — `config:best-practices`, pinned versions and digests, weekly grouped updates that merge
  themselves on green checks, majors through the Dependency Dashboard, the regex manager for the Makefile's tool
  versions; CodeQL for Go and Actions; Scorecard weekly and on pushes to `master`. Every Action is pinned by commit SHA.

## Steps

1. Push the licence commit to create `master`; branch `feat-bridge-library` from it. Check: `git ls-remote` shows
   `master` with one commit.
2. Add the module, the Makefile, the copy of `api/bridge.yaml` and the generated models. Check: `make generate-check`,
   `make spec-check`.
3. Write the server, the problems, the store and the idempotency with their tests. Check: `server_test.go`,
   `store_test.go`, `problem_test.go`.
4. Write the events, polling and the pushers. Check: `events_test.go`, `push_test.go`.
5. Write the minimal example, the README's quick start, `doc.go` and `example_test.go`; then `spec_test.go`. Check:
   `make ci`, `make conformance`.
6. Add the workflows, Renovate, release-please and the contributor files. Check: the pull request's checks, all green,
   with `conformance` running the edge image.

## Verification

In a clone of `muster-io/muster-bridge-go`:

```sh
make ci                                                  # lint, generate-check, spec-check, licenses, test, build pass
printf example-token > /tmp/bt
go run ./examples/minimal -token-file /tmp/bt &
curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:18095/v1/info                       # 401
curl -s -H 'Authorization: Bearer example-token' 127.0.0.1:18095/v1/info | jq -c '{protocol_version, user_lookup}'
# {"protocol_version":"1","user_lookup":"login"}
docker run --rm --network host -v /tmp/bt:/token:ro ghcr.io/muster-io/muster:edge bridge check \
  http://127.0.0.1:18095 --token-file /token --channel alerts --forbidden-channel nobot --handle alice \
  --no-interactive; echo "exit=$?"
# PASS info · PASS channel_check · PASS post · PASS replayed_key · PASS buttons · PASS edit · PASS thread_reply
# PASS thread_ref · PASS forbidden_channel · PASS user_lookup · PASS direct_message · PASS delete · PASS poll
# SKIP press: --no-interactive · SKIP push: --no-interactive · PASS answer
# exit=0
kill %1; BRIDGE_FEATURES=thread_messages go run ./examples/minimal -token-file /tmp/bt &
docker run --rm --network host -v /tmp/bt:/token:ro ghcr.io/muster-io/muster:edge bridge check \
  http://127.0.0.1:18095 --token-file /token --channel alerts --handle alice --no-interactive | grep -c SKIP   # 7
# 7: buttons, user_lookup, direct_message, delete, press, push and forbidden_channel, which this run leaves out;
# make conformance passes --forbidden-channel and expects 6
kill %1
cp api/bridge.yaml /tmp/spec.yaml && printf ' ' >> api/bridge.yaml
make spec-check; echo "exit=$?"                          # the difference is printed … exit=2
cp /tmp/spec.yaml api/bridge.yaml && make spec-check     # passes
grep -L 'SPDX-License-Identifier: MIT' $(git ls-files '*.go' ':!models.gen.go')     # (nothing)
```

The pull request's checks show `ci` green with `conformance` against `ghcr.io/muster-io/muster:edge`; its evidence
table carries the output above, and its body says `Part of muster-io/muster#256`.

## Open questions

None.

## Notes

- Suggested commit: `feat: add the bridge library`.
- **Maintainer** (operator attention): install on this repository the GitHub App that release-please uses in Muster's
  repository, and give the repository the variable `RELEASE_PLEASE_APP_CLIENT_ID` and the secret
  `RELEASE_PLEASE_APP_PRIVATE_KEY` (or allow the organization's to it); enable Renovate for the repository; after the
  first push, add a ruleset on `master` that requires the check `ci` and pull requests; turn on private vulnerability
  reporting. Leave the first release pull request open until L1 is complete, when the maintainer releases the
  libraries (D366); `v0.1.0` then carries the example and its image.
- The repository was created empty by the maintainer (D343); nothing is pushed to it before this story.
- As merged (muster-io/muster-bridge-go#1, D355): the first commit is `chore: add the licence` with `LICENSE` and a
  README stub, the branch was `feat-bridge-library`, and the names of the Contracts are those of the code; this file was
  corrected after the merge to match it.
- The library is a server-side counterpart of Muster's protocol client, written for bridge authors, not copied from
  Muster: Muster's code is AGPL and cannot move into an MIT repository. Muster's fake bridge and `muster bridge check`
  are references for behaviour only.
- The library keeps nothing about messages: Muster sends `thread_ref` back with every reply and the full envelope with
  every edit, so a bridge needs no message store of its own (ADR-0017).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-25 | partial | the Go library; the Python library in S-084 |
| C-22.AC-25 | partial | the minimal Go example and the copy check; the example bridge in S-085, Python in S-084 |
