---
id: S-085
title: "The example bridge on the Go bridge library, with its chat page and image (BE)"
capability: C-22
kind: be
layer: L1
repository: muster-io/muster-bridge-go
depends_on: [S-083]
covers: [C-22.FR-22, C-22.AC-25]
files_touched:
  - examples/chat/main.go
  - examples/chat/chat.go
  - examples/chat/chat_test.go
  - examples/chat/messenger.go
  - examples/chat/messenger_test.go
  - examples/chat/render.go
  - examples/chat/render_test.go
  - examples/chat/web.go
  - examples/chat/web_test.go
  - examples/chat/web/index.html
  - examples/chat/web/app.js
  - examples/chat/web/app.css
  - examples/chat/README.md
  - examples/chat/Dockerfile
  - .dockerignore
  - Makefile
  - README.md
  - .github/workflows/ci.yml
  - .github/workflows/image.yml
  - .github/workflows/codeql.yml
acceptance:
  - "[C-22.FR-22] `examples/chat` is a command of the module, MIT like the rest, built on the package `bridge` and the standard library only: a chat server with its own web page — channels, Threads, buttons shown with their style hints, direct messages between a person and the bot, a picker of who the viewer is, and an event log — served on one listener beside the Bridge protocol; `go test ./examples/chat/...` covers the chat, the messenger calls, the renderer and the page's JSON API."
  - "[C-22.FR-22] It offers every feature of the protocol, each switchable by `BRIDGE_FEATURES`, and both event delivery modes — push with `BRIDGE_PUSH_URL` and the secret from `BRIDGE_PUSH_SECRET` or `-push-secret-file`; `edit_max_age_seconds` and `max_body_length` come from flags; a press on the page is confirmed there at once and reported through the library; a message to the bot in its direct chat reaches the library as a direct message, so a pasted Account link token links the account (`messenger_test.go`, `web_test.go`)."
  - "[C-22.FR-22, C-22.AC-25] `make conformance` also runs the chat example: `muster bridge check` from `ghcr.io/muster-io/muster:edge` passes every step with all features but press and push, which it skips with `--no-interactive`, and skips the optional ones with `BRIDGE_FEATURES=thread_messages`; interactively, with the example pushing to the check's listener with the secret of `--push-secret-file`, the press, push and answer steps pass when the job presses the button that the `WAIT press` line names through the page's JSON API; the CI job `conformance` runs all three."
  - "[C-22.FR-22] Each push to `master` publishes `ghcr.io/muster-io/muster-example-bridge:sha-<7 hex digits>` and `edge`, and each release tag `vX.Y.Z` the image `vX.Y.Z`, for linux/amd64 and linux/arm64, from `examples/chat/Dockerfile`, whose base images are pinned by digest; the image runs as a non-root user, and `docker run <image> -help` lists its flags."
  - "[C-22.FR-22] The repository's README and the example's README show how to run the image next to `muster dev`, create a Bridge Connection and a Destination for it, and that the page has no authentication of its own; the example runs on the test environment as the maintainer deploys it, and a Muster there delivers to it, takes presses from its page and links an account by token (Notes)."
verify: "make ci conformance"
operator_attention: true
issue: null
---

# S-085. The example bridge on the Go bridge library, with its chat page and image (BE)

## Scope

**IN**

- The example bridge of C-22.FR-22, moved here from Muster's repository (D343) and built on the Go bridge library: a
  small chat server with its own web page that offers every feature of the protocol.
- Its image, published from this repository, and its conformance check in this repository's CI.

**OUT**

- The library and the minimal example (S-083); Muster's end-to-end test against a released image of this example,
  which covers C-22.AC-23 (S-086); the Python library (S-084).

## Contracts

- **Layout** (`examples/chat/`): the command `github.com/muster-io/muster-bridge-go/examples/chat` in the library's
  module, so it always builds against the library of the same commit and adds no dependency to the module. Every file
  carries the MIT header of S-083.
- **Chat** (`chat.go`, `web.go`, `web/`): channels from `-channels` (default `alerts,ops`; any other channel id is one
  the bot is not in, `forbidden`), people from `-users` (default `alice:Alice Smith,bob:Bob Brown`, found by login),
  messages with their edits and Threads, buttons rendered with their style hints, direct messages between a person and
  the bot, and an event log, all in memory. The page (`index.html`, `app.js`, `app.css`, embedded in the binary) polls
  the example's JSON API under `/chat/api/` — `GET messages?channel=`, `GET direct-messages?user=`, `POST press`
  `{message_id, user, button}`, `POST thread` `{message_id, user, text}`, `POST direct-message` `{user, text}` — and
  lets the viewer pick who they are. The page and its API have no authentication: the README says to put them behind
  some before exposing them. Muster's end-to-end test (S-086) and this repository's CI drive the same API as a person
  would.
- **Messenger** (`messenger.go`, `render.go`): implements `bridge.Messenger`, `UserFinder`, `DirectMessenger` and
  `Deleter` over the chat. The envelope is rendered to HTML by a small renderer of its own for the CommonMark Muster
  writes — paragraphs, hard line breaks, emphasis, strong emphasis, strikethrough, code spans, `http` and `https` links,
  bullet lists, block quotes and backslash escapes — with anything else shown as text and every text escaped; Mention
  markers become highlighted names through `bridge.ReplaceMentions`; the status colour, or the emoji without
  `status_color`, marks the message. A press is confirmed on the page at once and reported with `Press`; a Thread
  message with `ThreadMessage`; a deletion on the page with `MessageDeleted`; a message to the bot with
  `DirectMessage`, which the library turns into an `account_link` event when it carries a token. Answers are shown to
  their person only (`ephemeral`), in the Thread, or in the direct chat, as `answerEvent` asks.
- **Configuration** (`main.go`): `-listen` (default `127.0.0.1:18095`; `:8095` in the image), the token from
  `BRIDGE_TOKEN` or `-token-file`, push with `BRIDGE_PUSH_URL` and `BRIDGE_PUSH_SECRET` or `-push-secret-file`,
  `-edit-max-age` (seconds, none by default) and `-max-body-length` (default 4000), and `BRIDGE_FEATURES`, a
  comma-separated list of the members of `info.capabilities`, all by default; `user_lookup` is `login` while
  `direct_messages` is on and `none` without it. `info` names the bridge "Muster example bridge" with the library's
  version and the messenger "Example chat". The README says that the example keeps everything in memory, so a restart
  loses its messages, events and keys.
- **Conformance** (`Makefile`, `ci.yml`): `make conformance` runs, after S-083's three runs against `examples/minimal`,
  the same two non-interactive runs against `examples/chat`, and a third, interactive one: the example starts with
  `BRIDGE_PUSH_URL` pointing at the check's listener (`--push-listen 127.0.0.1:18096`) and the secret of
  `--push-secret-file`; the script reads the check's `WAIT press` line, presses that message's first button as `alice`
  through `POST /chat/api/press`, and expects PASS for press, push and answer.
- **Image** (`Dockerfile`, `.dockerignore`, `image.yml`): a two-stage build from the module root — the Go image of the
  module's newest supported release, then `gcr.io/distroless/static-debian12:nonroot`, both pinned by digest, which
  Renovate updates — that runs `/example-bridge` as the user 65532 and listens on `:8095`. `image.yml` builds it with
  Buildx for linux/amd64 and linux/arm64 and pushes `ghcr.io/muster-io/muster-example-bridge` with `sha-<7 hex digits>`
  and `edge` on each push to `master`, and `vX.Y.Z` on each release tag, which release-please creates with the App
  token of S-083 so that the tag starts the workflow. The images are not signed; Muster pins the one it tests by digest
  (S-086).
- **CodeQL** (`codeql.yml`): adds JavaScript for the page's script.
- **README** (`README.md`, `examples/chat/README.md`): the example's section — what it shows, the
  `docker run -p 8095:8095 -e BRIDGE_TOKEN=… ghcr.io/muster-io/muster-example-bridge:<version>` line, the Bridge
  Connection in Muster with the base URL and the token, the event delivery modes, `BRIDGE_FEATURES`, and the warning
  about authentication.

## Steps

1. Write the chat, its page and the JSON API with their tests. Check: `go test ./examples/chat/...`.
2. Write the messenger and the renderer on the library. Check: `messenger_test.go`, `render_test.go`, then
   Verification against the edge image.
3. Add the interactive conformance run, the Dockerfile and the image workflow. Check: `make conformance`; the image
   builds and answers `-help`.
4. Update the READMEs and CodeQL. Check: the pull request's checks, all green.
5. Hand over to the maintainer (Notes). Check: after the release,
   `docker pull ghcr.io/muster-io/muster-example-bridge:v0.1.0` succeeds without signing in and prints the digest that
   S-086 pins.

## Verification

In a clone of `muster-io/muster-bridge-go`:

```sh
make ci conformance                                       # passes, the chat example's three runs included
docker build -f examples/chat/Dockerfile -t example-bridge . && docker run --rm example-bridge -help | head -3
printf example-token > /tmp/bt
docker run -d --name eb --network host -v /tmp/bt:/token:ro example-bridge -listen 127.0.0.1:18095 -token-file /token
curl -s -H 'Authorization: Bearer example-token' 127.0.0.1:18095/v1/info | jq -r .messenger.name       # Example chat
docker run --rm --network host -v /tmp/bt:/token:ro ghcr.io/muster-io/muster:edge bridge check \
  http://127.0.0.1:18095 --token-file /token --channel alerts --forbidden-channel nobot --handle alice \
  --no-interactive; echo "exit=$?"                                                                     # exit=0
curl -s '127.0.0.1:18095/chat/api/messages?channel=alerts' | jq -r '.[].title' | grep -c '^muster bridge check: buttons$'   # 1
docker rm -f eb
```

Then, with `muster dev` running from a Muster checkout and the image above on `127.0.0.1:18095`: a Bridge Connection
in poll mode to `http://127.0.0.1:18095` with the token `example-token`, a Destination for `alerts`, and "Send test
message" make "🧪 Test message" appear in `#alerts` on the page at `http://127.0.0.1:18095/`; its screenshot goes into
the pull request.

## Open questions

None.

## Notes

- Suggested commit: `feat: add the example bridge and its image`.
- **Maintainer** (operator attention): after the first publication, make the package `muster-example-bridge` on GitHub
  Container Registry public and link it to this repository; merge the release pull request, which tags `v0.1.0` and
  publishes the image `v0.1.0` that S-086 pins; deploy the example on the test environment next to Muster with a token
  and, for the push mode, the callback address and a push secret; create a Bridge Connection and a Destination there,
  and check by eye that a new Alert Group, a press of Ack on the example's page, `ack` written in a Thread with buttons
  switched off, and an Account link by token work. On the test environment the example usually runs at an address
  inside the cluster: when the Organization's outbound address policy denies private addresses, the maintainer allows
  the example's address there, and puts its page behind the cluster's authentication, as its README asks.
- The example is a reference, not a product: it keeps everything in memory and has no authentication of its own.
- It lived in Muster's repository as `examples/bridge/` in the earlier contract of S-078; it never existed there, so
  nothing moves but the contract.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-22 | partial | the example, its image and its conformance here; Muster's test against its released image in S-086 |
| C-22.AC-25 | partial | the example bridge passes in the Go library's CI; the minimal example and the copy check in S-083, Python in S-084 |
