---
id: S-078
title: "Example bridge under MIT and the conformance check `muster bridge check` (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-076]
covers: [C-22.FR-21, C-22.FR-22, C-22.AC-22, C-22.AC-23]
files_touched:
  - examples/bridge/LICENSE
  - examples/bridge/README.md
  - examples/bridge/go.mod
  - examples/bridge/.golangci.yml
  - examples/bridge/main.go
  - examples/bridge/chat.go
  - examples/bridge/protocol.go
  - examples/bridge/events.go
  - examples/bridge/idempotency.go
  - examples/bridge/web.go
  - examples/bridge/web/index.html
  - examples/bridge/web/app.js
  - examples/bridge/web/app.css
  - examples/bridge/bridge_test.go
  - examples/bridge/Dockerfile
  - internal/bridge/conformance.go
  - internal/bridge/conformance_test.go
  - internal/cli/bridge.go
  - internal/cli/bridge_test.go
  - internal/cli/cli.go
  - internal/archlint/secretleak.go
  - internal/tools/licensecheck/main.go
  - NOTICE
  - Makefile
  - .github/workflows/ci.yml
  - .github/workflows/edge.yml
  - .github/workflows/codeql.yml
  - AGENTS.md
  - test/e2e/bridge_example_test.go
acceptance:
  - "[C-22.FR-22] `examples/bridge/` is its own Go module with only the standard library, an MIT `LICENSE` and MIT SPDX headers, listed in the exclusion list of `NOTICE`, whose heading now names third-party and MIT-licensed files alike; the Muster binary does not import it, and `make licenses` lists none of its files."
  - "[C-22.FR-22] The example serves a chat web page — channels, Threads, buttons with their styles, direct messages, a user picker — and the Bridge protocol with every feature, each switchable by `BRIDGE_FEATURES`, both event delivery modes, an idempotency store per token, `edit_max_age_seconds` and `max_body_length` from flags, and users found by login; `go test ./...` in its module covers each operation, and its conformance to `api/bridge.yaml` is checked from the main module by `muster bridge check` in `test/e2e/bridge_example_test.go`."
  - "[C-22.FR-21] `muster bridge check` calls the protocol client directly from `internal/bridge/conformance.go`, outside delivery, which ADR-0017 permits; lint 3 checks calls of the `delivery.Adapter` methods, not of the protocol client, so it needs no entry and `make lint-arch` passes with the lint unchanged; the token read from `--token-file` appears in no output line, log line or returned error, the FAIL reasons taken from the bridge's answers included (`secretleak.go` probe `bridge_check`)."
  - "[C-22.FR-21, C-22.AC-22] `muster bridge check <url> --token-file f --channel alerts --forbidden-channel nobot` against the example prints one PASS line per step — info, channel check, post, repeated key replayed, edit, two Thread replies with `thread_ref`, the forbidden channel, polling, push to its own listener, user lookup, direct message, answer — and exits 0; with `BRIDGE_FEATURES=thread_messages` the buttons, direct-message and lookup steps print SKIP; against the fake bridge with replays off it prints FAIL for the repeated key and exits 1."
  - "[C-22.AC-23] On `muster dev` with a Bridge Connection to the example, a new Alert Group appears on the example's page; pressing Ack there as a linked user acknowledges it in Muster; with buttons switched off, `ack` written in its Thread acknowledges it (`test/e2e/bridge_example_test.go`)."
  - "[C-22.FR-22] `make ci` vets, lints (with the module's own `.golangci.yml`) and tests the example module without building an image; `make vulncheck` and CodeQL cover it too; each commit on `master` publishes `ghcr.io/muster-io/muster-example-bridge:sha-<7 hex digits>` and `edge`, unsigned like the Muster development image."
  - "[C-22.FR-22] The example bridge runs on the test environment as the maintainer deploys it, and a Muster there delivers to it, takes presses from its page and links an account by token (Notes)."
verify: "make ci e2e"
operator_attention: true
issue: 236
---

# S-078. Example bridge under MIT and the conformance check `muster bridge check` (BE)

## Scope

**IN**

- The example bridge: a small chat server with its own web page that speaks every part of the Bridge protocol, as a
  separate MIT-licensed Go module with its image.
- `muster bridge check <url>`: the conformance check of C-22.FR-21.
- The build, lint, test and image publication of the example in CI.

**OUT**

- "Write a bridge" and the protocol reference on the site (S-079); the pages for Admins (S-074, S-081, S-076).

## Contracts

- **Licence and layout** (ADR-0017, ADR-0001): `examples/bridge/` with `go.mod` (`module
  github.com/muster-io/muster/examples/bridge`, the Go version of the main module, no requirements), `LICENSE` (MIT,
  "Copyright The Muster Authors"), every source file headed `// SPDX-License-Identifier: MIT` and the copyright line;
  `NOTICE`, whose section "MIT-licensed parts" already names the example and `api/bridge.yaml`, gains
  `examples/bridge/**  the example bridge, MIT (ADR-0017)` in its exclusion list, and that list's heading, "Third-party
  files excluded from the licence header check:", becomes "Files excluded from the licence header check, third-party
  and MIT-licensed:", which `internal/tools/licensecheck` reads as the list's start; the AGENTS.md layout table gains
  `examples/bridge/` and its Make targets table `make example-bridge` (the stories README §2 already lists it).
- **Chat** (`chat.go`, `web.go`, `web/`): channels from `-channels` (default `alerts,ops`), users from `-users` (default
  `alice:Alice Smith,bob:Bob Brown`, found by login), messages with Threads, buttons rendered with their style hints,
  presses, Thread messages, direct messages between a user and the bot, and an event log; the page polls a small JSON
  API of the example (`/chat/...`) and lets the viewer pick who they are. It has no authentication of its own: the
  README says to put it behind one before exposing it.
- **Protocol** (`protocol.go`, `events.go`, `idempotency.go`): every operation of `api/bridge.yaml` with the token from
  `BRIDGE_TOKEN` or `-token-file`; the envelope rendered to HTML with a CommonMark renderer of its own, Mention markers
  as highlighted names; presses confirmed at once and queued as events; `pollEvents` and push with `BRIDGE_PUSH_URL` and
  `BRIDGE_PUSH_SECRET`; keys kept 24 hours; `edit_max_age_seconds` and `max_body_length` from flags; `BRIDGE_FEATURES`,
  a comma-separated list of the features of `info.capabilities`, all by default; a direct message to the bot whose text
  is a link token becomes an `account_link` event.
- **Conformance check** (`internal/bridge/conformance.go`, `internal/cli/bridge.go`): `muster bridge check <url>` with
  `--token-file`, `--channel`, `--forbidden-channel`, `--handle` (for the lookup and direct-message steps),
  `--push-listen` (default `127.0.0.1:0`) and `--push-url` (the address the bridge reaches it at), `--no-interactive`;
  the steps of C-22.FR-21 in order, each printed as `PASS <step>`, `FAIL <step>: <reason>` or `SKIP <step>: <why>`; exit
  code 1 on any FAIL. The answer step answers an event the bridge reported during the run, or, without one under
  `--no-interactive`, sends `answerEvent` for an unknown event id and expects `gone`. It reuses the client of S-073
  through the outbound package with a policy that allows loopback and private addresses — the person running it chose
  the target — and needs no database or master key. The push step opens its listener, generates a push secret and
  prints both for the bridge's configuration, then waits for a signed event, unless `--no-interactive`; it never
  changes the bridge itself.
- **The CLI exception** (ADR-0017, ADR-0016): the conformance check posts, edits and replies through the protocol
  client directly, outside delivery and the interactive path, which ADR-0017 permits because it runs outside any
  installation. Lint 3 checks calls of the `delivery.Adapter` methods (`Publish`, `Update`, `Reply`), not the
  messenger clients beneath them — the Mattermost and Telegram clients are not linted either — so the check needs no
  lint entry and `internal/archlint` does not change. AGENTS.md already states the exception.
- **Secrets** (lint 5): probe `bridge_check` runs the conformance check with a known token from a token file against
  a server that answers problems whose `detail` echoes the request, and fails when the token reaches the printed
  lines, a log line or a returned error.
- **CI** (`Makefile`, `ci.yml`, `edge.yml`, `codeql.yml`): `make example-bridge` runs `go vet`, golangci-lint with the
  module's own `examples/bridge/.golangci.yml` (the root configuration names the main module's packages) and `go test`
  in the module, and builds no image, so it needs no Docker; `make ci` runs it; `make vulncheck` also runs govulncheck
  in the module; CodeQL's manual Go build also builds the module; the edge workflow builds and publishes the image with
  the tags of the Muster development image and, like it, unsigned (releases are signed by `release.yml`). Renovate's Go
  manager finds `examples/bridge/go.mod` without configuration, and the Dockerfile pins its base image by digest.
- **Conformance from the main module** (`test/e2e/bridge_example_test.go`): the example's own tests use the standard
  library only and cannot load `api/bridge.yaml`; the end-to-end test starts the example and runs `muster bridge check`
  against it with every feature and with `BRIDGE_FEATURES=thread_messages`, then the flows of C-22.AC-23.

## Steps

1. Write the protocol side of the example with its tests. Check: `go test ./...` in `examples/bridge`.
2. Add the chat and its page. Check: the page by hand, then `bridge_example_test.go`.
3. Write the conformance check and its secret probe. Check: `conformance_test.go` against the fake bridge with
   faults, `make lint-arch`.
4. Wire CI, `NOTICE` and the layout tables. Check: `make ci`, `make licenses`.
5. Hand over to the maintainer for the test environment (Notes).

## Verification

```sh
make example-bridge
printf example-token > /tmp/bt
(cd examples/bridge && go run . -listen 127.0.0.1:18095 -token-file /tmp/bt) &
./bin/muster bridge check http://127.0.0.1:18095 --token-file /tmp/bt --channel alerts --forbidden-channel nobot \
  --handle alice --no-interactive
# PASS info · PASS channel_check · PASS post · PASS replayed_key · PASS edit · PASS thread_reply · PASS thread_ref
# PASS forbidden_channel · PASS poll · SKIP push: --no-interactive · PASS user_lookup · PASS direct_message · PASS answer
echo $?                                                                    # 0
curl -s -X PUT 127.0.0.1:18094/_fake/config -d '{"replays":false}' > /dev/null
printf muster-dev-bridge-token-b > /tmp/ft
./bin/muster bridge check http://127.0.0.1:18094 --token-file /tmp/ft --channel ch-alerts --forbidden-channel ch-nobot \
  --no-interactive > /tmp/fc; echo $?; grep replayed_key /tmp/fc
# 1
# FAIL replayed_key: the second request with the same Idempotency-Key created message m-… instead of returning m-…
make licenses | grep -c examples/bridge                                     # 0
make e2e                                                                   # TestBridgeExample passes
```

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): add an example bridge and the bridge conformance check`.
- **Maintainer** (operator attention): make the package `muster-example-bridge` public on GitHub Container Registry
  after its first publication; deploy the example on the test environment next to Muster with a token and, for the
  push mode, the callback address and a push secret; create a Bridge Connection and a Destination there, and check by
  eye that a new Alert Group, a press of Ack on the example's page, `ack` written in a Thread with buttons switched
  off, and an Account link by token work.
- The example is a reference, not a product: it keeps everything in memory, and a restart loses its messages, events
  and keys, which its README says.
- On the test environment the example usually runs at an address inside the cluster: when the Organization's outbound
  address policy denies private addresses, the maintainer allows the example's address there, and puts the example's
  page behind the cluster's authentication before exposing it, as its README asks.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-21 | full | |
| C-22.FR-22 | full | the deployment on the test environment is the maintainer's |
| C-22.AC-22 | full | |
| C-22.AC-23 | full | |
