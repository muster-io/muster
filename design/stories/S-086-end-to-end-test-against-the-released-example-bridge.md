---
id: S-086
title: "End-to-end test against the released image of the example bridge"
capability: C-22
kind: infra
layer: L1
depends_on: [S-085]
covers: [C-22.FR-22, C-22.AC-22, C-22.AC-23]
files_touched:
  - test/e2e/bridge_example_test.go
  - .github/renovate.json5
  - CONTRIBUTING.md
acceptance:
  - "[C-22.FR-22] `test/e2e/bridge_example_test.go` runs `ghcr.io/muster-io/muster-example-bridge` at a released version pinned by digest, named once in a constant under a `// renovate:` comment; a regex manager in `.github/renovate.json5` updates its version and digest, so a release of the Go bridge library reaches Muster as a Renovate pull request that runs this test."
  - "[C-22.AC-22] `muster bridge check` against the example exits 0 three times: with all its features and `--no-interactive`, every step passes but press and push, which are skipped; with `BRIDGE_FEATURES=thread_messages`, buttons, user_lookup, direct_message and delete are skipped too; interactively, with the example pushing to the check's listener, the test presses the button named by the `WAIT press` line through the example's page API, and press, push and answer pass (`TestBridgeExampleCheck`)."
  - "[C-22.AC-23] On `muster dev` with a Bridge Connection in poll mode to the example and a Destination for its channel `alerts`, an alert sent to the Integration appears as a Root message `#N` in `alerts` on the example's page; Alice, signed in to the web UI, starts an Account link and sends its token to the bot through the page, which links her account; her press of Ack there acknowledges the Alert Group in Muster with the Transport `bridge`; with the example restarted with `BRIDGE_FEATURES=thread_messages` and the Connection checked again, `ack` written in the Thread of a new Alert Group acknowledges it and is answered \"Done: Acknowledged #N\"; with the Connection in push mode and the example pushing to its callback with the push secret, a press reaches Muster as a signed push (`TestBridgeExampleFlows`)."
  - "[C-22.FR-22] CONTRIBUTING.md says that `make e2e-go` pulls the example's image from GitHub Container Registry and runs it with host networking, so it needs Docker on Linux, as on the CI runners and the development VM."
verify: "make ci e2e-go"
operator_attention: false
issue: 259
---

# S-086. End-to-end test against the released image of the example bridge

## Scope

**IN**

- An end-to-end test that runs a released image of the example bridge, pinned by digest, and checks it with
  `muster bridge check` and with the flows of C-22.AC-23 on `muster dev`.
- The Renovate rule that keeps the pinned image current, so that each release of the Go bridge library is tested
  against Muster's `master` before it is taken.

**OUT**

- `muster bridge check` and its test against the fake bridge (S-078); the example and its image (S-085); the
  libraries (S-083, S-084); "Write a bridge" (S-079).

## Contracts

- **The pinned image** (`test/e2e/bridge_example_test.go`, `.github/renovate.json5`): one constant holds
  `ghcr.io/muster-io/muster-example-bridge:vX.Y.Z@sha256:<digest>`, the first release that S-085's maintainer step
  made, under the comment `// renovate: datasource=docker depName=ghcr.io/muster-io/muster-example-bridge`. A
  `customManagers` entry of type `regex` for `test/e2e/*.go` reads that line with `currentValue` and `currentDigest`, so
  Renovate moves the version and the digest together. The existing rules for container images merge the update once
  every check, `e2e-go` included, passes, but a `packageRules` entry for this image keeps it out of their group, in a
  pull request of its own, so that a red run names the release that broke. ADR-0017 is the reason: a change of Muster
  that breaks a released library fails here, and a library release that breaks against Muster's `master` never merges.
- **Running the example** (`bridge_example_test.go`): the test starts the image with `docker run -d --network host` on
  `127.0.0.1:18095` with a token file of mode 0644, which the image's user 65532 can read, mounted read-only, restarts
  it with other variables for the later cases, and removes it when the test ends; `docker` missing fails the test,
  naming the command. It uses the harness of `test/e2e/harness.go` in the mode `DevProcess`, the API with a Personal
  access token for the Admin's steps and Alice's web session, with its CSRF token, for the Account link, which an API
  token cannot start (C-18.FR-1), as `test/e2e/account_links_test.go` does, and the example's JSON API under
  `/chat/api/` (S-085) to read its channels and to press, write and send direct messages as a person would.
- **`TestBridgeExampleCheck`** (C-22.AC-22): `muster bridge check` from the binary under test against the example,
  `--no-interactive`, with all features and with `BRIDGE_FEATURES=thread_messages`, expecting the PASS and SKIP lines
  of S-078's table and exit 0; then interactively, the example restarted with `BRIDGE_PUSH_URL` at the check's listener
  (`--push-listen 127.0.0.1:18096`) and the secret of `--push-secret-file`, pressing the message of the `WAIT press`
  line through `POST /chat/api/press`.
- **`TestBridgeExampleFlows`** (C-22.AC-23): a Bridge Connection in poll mode to the example, a Destination for `alerts`
  and a Route to it; an alert through the Integration; the token of C-22.FR-15, started in Alice's web session, sent to
  the bot as Alice's direct message; Ack pressed as Alice; the example restarted with `BRIDGE_FEATURES=thread_messages`,
  then the Connection checked through the API so that Muster reads `info` again instead of waiting
  `bridge.info_max_age`, a second Alert Group and `ack` written in its Thread; then the Connection switched to push, the
  example restarted with all features and `BRIDGE_PUSH_URL` set to the Connection's callback address and its push
  secret, the Connection checked again, a third Alert Group and a press on it. A restart loses the example's messages,
  so every press and every Text command acts on an Alert Group posted after the last restart; Alice's Account link lives
  in Muster and survives. Each step is asserted through the API — the Alert Group's status, the Transport `bridge` in
  its Timeline — and on the example's page data.
- **CONTRIBUTING.md**: the end-to-end section names the example's image and the need for Docker with host networking.

## Steps

1. Add the constant, the Renovate manager and the check test. Check: `make e2e-go` runs `TestBridgeExampleCheck`;
   `npx --yes --package renovate -- renovate-config-validator .github/renovate.json5` passes.
2. Add the flows. Check: `make e2e-go`, then Verification.

## Verification

On Linux with Docker (the development VM):

```sh
make e2e-go                                       # TestBridgeExampleCheck and TestBridgeExampleFlows pass
grep -n 'muster-example-bridge:v[0-9.]*@sha256:[0-9a-f]\{64\}' test/e2e/bridge_example_test.go     # one line
npx --yes --package renovate -- renovate-config-validator .github/renovate.json5                    # valid
LOG_LEVEL=debug npx --yes renovate --platform=local --dry-run=lookup 2>&1 | grep -c muster-example-bridge   # 1 or more
```

By hand on `muster dev` with the pinned image on `127.0.0.1:18095`, as the test does: the Alert Group page of the
acknowledged Alert Group shows "Acknowledged" with the Transport `bridge` in its Timeline, and the example's page shows
the edited Root message with the buttons of the acknowledged status.

## Open questions

None.

## Notes

- Suggested commit: `test(e2e): run the released example bridge against muster dev`.
- The Renovate pull requests for the image are the compatibility check of ADR-0017 in Muster's direction: they run
  the whole end-to-end suite with the new release, and a red one shows which side broke the protocol.
- The pinned image is a release of `muster-bridge-go`, never `edge`: what Muster checks against is what bridge
  authors install. This story therefore starts once the maintainer's step of S-085 is done: the release `v0.1.0` is
  tagged and the package `muster-example-bridge` is public.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-22 | partial | Muster's test against the released image; the example and its image in S-085 |
| C-22.AC-22 | partial | against the released example bridge; against the fake bridge in S-078 |
| C-22.AC-23 | full | |
