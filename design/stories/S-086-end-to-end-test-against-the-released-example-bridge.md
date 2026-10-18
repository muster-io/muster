---
id: S-086
title: "End-to-end test against the example bridge's image, pinned by digest"
capability: C-22
kind: infra
layer: L1
depends_on: [S-085]
covers: [C-22.FR-22, C-22.AC-22, C-22.AC-23]
files_touched:
  - test/e2e/bridge_example_test.go
  - .github/renovate.json5
  - CONTRIBUTING.md
  - design/prd/l1/C-22-bridge.md
  - design/stories/README.md
  - design/stories/S-078-example-bridge-and-the-bridge-conformance-check.md
  - design/stories/S-083-go-bridge-library.md
  - design/stories/S-084-python-bridge-library.md
  - design/stories/S-085-example-bridge-on-the-go-library.md
acceptance:
  - "[C-22.FR-22] `test/e2e/bridge_example_test.go` runs `ghcr.io/muster-io/muster-example-bridge:edge` pinned by digest, named once in a constant under a `// renovate:` comment; a regex manager in `.github/renovate.json5` updates its digest, so each new image of the example reaches Muster as a Renovate pull request of its own that runs this test. After the L1 release the pin moves to the release `v0.1.0`, and the same manager moves the version and the digest together."
  - "[C-22.AC-22] `muster bridge check` against the example exits 0 three times: with all its features and `--no-interactive`, every step passes but press and push, which are skipped; with `BRIDGE_FEATURES=thread_messages`, buttons, user_lookup, direct_message and delete are skipped too; interactively, with the example pushing to the check's listener, the test presses the button named by the `WAIT press` line through the example's page API, and press, push and answer pass (`TestBridgeExampleCheck`)."
  - "[C-22.AC-23] On `muster dev` with a Bridge Connection in poll mode to the example and a Destination for its channel `alerts`, an alert sent to the Integration appears as a Root message `#N` in `alerts` on the example's page; Alice, signed in to the web UI, starts an Account link and sends its token to the bot through the page, which links her account; her press of Ack there acknowledges the Alert Group in Muster with the Transport `bridge`; with the example restarted with `BRIDGE_FEATURES=thread_messages` and the Connection checked again, `ack` written in the Thread of a new Alert Group acknowledges it and is answered \"Done: Acknowledged #N\"; with the Connection in push mode and the example pushing to its callback with the push secret, a press reaches Muster as a signed push, which the Request log records as an inbound `push` of the Connection answered `204` (`TestBridgeExampleFlows`)."
  - "[C-22.FR-22] CONTRIBUTING.md says that `make e2e-go` pulls the example's image from GitHub Container Registry and runs it with host networking, so it needs Docker Engine on Linux, as on the CI runners."
verify: "make ci e2e-go"
operator_attention: false
issue: 259
---

# S-086. End-to-end test against the example bridge's image, pinned by digest

## Scope

**IN**

- An end-to-end test that runs the image of the example bridge, pinned by digest — the `edge` image until the libraries
  are released, then a release — and checks it with `muster bridge check` and with the flows of C-22.AC-23 on
  `muster dev`.
- The Renovate rule that keeps the pinned image current, so that each new image of the example — a new `edge` digest
  now, a release of the Go bridge library once there are releases — is tested against Muster's `master` before it is
  taken.

**OUT**

- `muster bridge check` and its test against the fake bridge (S-078); the example and its image (S-085); the
  libraries (S-083, S-084); "Write a bridge" (S-079).

## Contracts

- **The pinned image** (`test/e2e/bridge_example_test.go`, `.github/renovate.json5`): one constant holds
  `ghcr.io/muster-io/muster-example-bridge:edge@sha256:<digest>`, on the line under the comment
  `// renovate: datasource=docker depName=ghcr.io/muster-io/muster-example-bridge`. A `customManagers` entry of type
  `regex` for `test/e2e/*.go` reads that line with `currentValue` (`edge`) and `currentDigest`, so Renovate moves the
  digest when `edge` changes, and, once the pin is a release, the version and the digest together. `ignorePaths` keeps
  the list of `config:best-practices` without `**/test/**`, which would hide `test/e2e/` from every manager. The
  existing rules for container images merge the update once every check, `e2e-go` included, passes, but a
  `packageRules` entry for this image keeps it out of their group, in a pull request of its own (`groupName`
  `example bridge image`), so that a red run names the image that broke, and sets `minimumReleaseAge` to `0 days`: a
  new digest of `edge` has no release date, and the repository's `3 days` would hold it as pending for ever (Renovate's
  `minimumReleaseAgeBehaviour` `timestamp-required`). ADR-0017 is the reason: a change of Muster that breaks the
  library fails here, and a library change that breaks against Muster's `master` never merges.
- **Running the example** (`bridge_example_test.go`): the test starts the image with `docker run -d --network host` and
  `-listen 127.0.0.1:18095`, with a token file and, for push, a push secret file, each of mode 0644, which the image's
  user 65532 can read, mounted read-only and named by `-token-file` and `-push-secret-file`: neither secret is ever on a
  command line or in the container's environment. The container has the fixed name `muster-e2e-example-bridge` and the
  label `muster-e2e=example-bridge`, so that each start first removes a container that a killed run left holding the
  port. It waits until `GET /` answers `200`, restarts it — `docker rm -f`, then `docker run` again — with other
  variables for the later cases, removes it when the test ends, and adds the container's log, its secrets masked, to a
  failure; `docker` missing fails the test, naming the command. It uses the harness of `test/e2e/harness.go` in the mode
  `DevProcess`, the API with a Personal access token for the Admin's steps and Alice's web session, with its CSRF token,
  for the Account link, which an API token cannot start (C-18.FR-1), as `test/e2e/account_links_test.go` does, and the
  example's JSON API under `/chat/api/` (S-085) to read its channels and to press, write and send direct messages as a
  person would, every `POST` with `Content-Type: application/json`, without which the example answers `415`.
- **`TestBridgeExampleCheck`** (C-22.AC-22): `muster bridge check` from the binary under test against the example,
  which needs no harness, with `--channel alerts --forbidden-channel nobot --handle alice`: `--no-interactive`, with all
  features and with `BRIDGE_FEATURES=thread_messages`, expecting the PASS and SKIP lines of S-078's table and exit 0;
  then interactively, the example restarted with `BRIDGE_PUSH_URL` at the check's listener
  (`--push-listen 127.0.0.1:18096`, `PUSH http://127.0.0.1:18096/`) and the secret of `--push-secret-file`, pressing
  the first button of the message of the `WAIT press` line through `POST /chat/api/press`.
- **`TestBridgeExampleFlows`** (C-22.AC-23): a Bridge Connection in poll mode to the example, a Destination for `alerts`
  and a Route to it; an alert through the Integration; the token of C-22.FR-15, started in Alice's web session, sent to
  the bot as Alice's direct message; Ack pressed as Alice; the example restarted with `BRIDGE_FEATURES=thread_messages`,
  then the Connection checked through the API, which reads `info` again; a second Alert Group and `ack` written in its
  Thread; then the Connection switched to push, the example restarted with all features and `BRIDGE_PUSH_URL` set to the
  Connection's callback address and its push secret, the Connection checked again, a third Alert Group and a press on
  it, which the Request log shows as an inbound `push` of the Connection, answered `204`, with its `Webhook-Signature`
  masked: only the push path writes that record. A restart loses the example's messages, so every press and every Text
  command acts on an Alert Group posted after the last restart, found by its `#N`; Alice's Account link lives in Muster
  and survives. Each step is asserted through the API — the Alert Group's status, the Transport `bridge` in its Timeline
  — and on the example's page data.
- **CONTRIBUTING.md**: the end-to-end section names the example's image and the need for Docker with host networking.

## Steps

1. Add the constant, the Renovate manager and the check test. Check: `make e2e-go` runs `TestBridgeExampleCheck`;
   `npx --yes --package renovate -- renovate-config-validator .github/renovate.json5` passes.
2. Add the flows. Check: `make e2e-go`, then Verification.

## Verification

On Linux with Docker (the development VM):

```sh
make e2e-go                                       # TestBridgeExampleCheck and TestBridgeExampleFlows pass
grep -n 'muster-example-bridge:edge@sha256:[0-9a-f]\{64\}' test/e2e/bridge_example_test.go       # one line
npx --yes --package renovate -- renovate-config-validator .github/renovate.json5                    # valid
LOG_LEVEL=debug npx --yes renovate --platform=local --dry-run=lookup --include-paths='test/e2e/**' 2>&1 \
  | grep -c '"depName": "ghcr.io/muster-io/muster-example-bridge"'                                  # 1
```

By hand on `muster dev` with the pinned image on `127.0.0.1:18095`, as the test does: the Alert Group page of the
acknowledged Alert Group shows "Acknowledged" with the Transport `bridge` in its Timeline, and the example's page shows
the edited Root message with the buttons of the acknowledged status.

## Open questions

None.

## Notes

- Suggested commit: `test(e2e): run the example bridge's image against muster dev`.
- The Renovate pull requests for the image are the compatibility check of ADR-0017 in Muster's direction: they run
  the whole end-to-end suite with the new image, and a red one shows which side broke the protocol.
- The libraries are released when L1 is complete (D366), so until then the test pins the `edge` image of
  `muster-bridge-go` by digest, which is as reproducible as a release and is the library's `master`. After the L1
  release the pin moves to `v0.1.0`, a released image, which is what bridge authors install and what ADR-0017 intends.
- Contract corrections made while implementing: the Renovate file needed `ignorePaths` without `**/test/**` and
  `minimumReleaseAge` `0 days` for this image (Contracts); the push is proved by its Request log record; the
  Verification's dry run is limited to `test/e2e/` and counts the dependency, not every log line that names the image,
  which the configuration dump alone matched; the container has a fixed name, so that a container left by a killed run
  does not hold the port.
- With two replicas (`E2E_REPLICAS=2`, the nightly run) the test advances the development clock past
  `bridge.info_max_age` after each Connection check, because a check reads `info` again only on the replica that runs
  it, while another replica keeps its copy in memory until it is that old. With one replica, as on pull requests, the
  check alone makes delivery use the new `info`. This known behaviour is raised for the maintainer in the pull request.
- The file keeps its old name (`…-released-example-bridge`); the title says what the story delivers now.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-22 | partial | Muster's test against the pinned image; the example and its image in S-085 |
| C-22.AC-22 | partial | against the pinned image of the example bridge; against the fake bridge in S-078 |
| C-22.AC-23 | full | |
