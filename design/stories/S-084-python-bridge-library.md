---
id: S-084
title: "The Python bridge library `muster-bridge` with its CI and PyPI releases (BE)"
capability: C-22
kind: be
layer: L1
repository: muster-io/muster-bridge-python
depends_on: [S-079]
covers: [C-22.FR-25, C-22.AC-25]
files_touched:
  - LICENSE
  - README.md
  - CONTRIBUTING.md
  - SECURITY.md
  - AGENTS.md
  - pyproject.toml
  - Makefile
  - .gitignore
  - .python-version
  - release-please-config.json
  - .release-please-manifest.json
  - .github/renovate.json5
  - .github/pull_request_template.md
  - .github/workflows/ci.yml
  - .github/workflows/release-please.yml
  - .github/workflows/codeql.yml
  - .github/workflows/scorecard.yml
  - api/bridge.yaml
  - src/muster_bridge/__init__.py
  - src/muster_bridge/py.typed
  - src/muster_bridge/config.py
  - src/muster_bridge/messenger.py
  - src/muster_bridge/app.py
  - src/muster_bridge/problems.py
  - src/muster_bridge/store.py
  - src/muster_bridge/events.py
  - src/muster_bridge/push.py
  - src/muster_bridge/mentions.py
  - examples/memory_bridge.py
  - tests/conftest.py
  - tests/test_config.py
  - tests/test_app.py
  - tests/test_problems.py
  - tests/test_store.py
  - tests/test_events.py
  - tests/test_push.py
  - tests/test_mentions.py
  - tests/test_spec.py
acceptance:
  - "[C-22.FR-25] The repository `muster-io/muster-bridge-python` has the default branch `master`, whose first commit adds only the MIT `LICENSE` (\"Copyright The Muster Authors\"); `pyproject.toml` names the distribution `muster-bridge` with the licence expression `MIT`, `requires-python = \">=3.11\"` and the runtime dependencies Starlette, httpx and pydantic 2 only; every Python file, workflow and Makefile carries `SPDX-License-Identifier: MIT` and the copyright line, which `make lint` checks; CONTRIBUTING.md takes contributions under MIT without a CLA; `make licenses` fails on a runtime dependency whose licence is not on its list."
  - "[C-22.FR-25] `muster_bridge.BridgeApp(config, messenger)` is an ASGI application on Starlette that uvicorn serves and that mounts under a path of a Starlette or FastAPI application; a bridge implements the `Messenger` protocol with `async` methods — post, edit, reply in a Thread, check a channel, answer — and, for the features it declares, `UserFinder`, `DirectMessenger` and `Deleter`; a configuration whose features need a protocol the messenger lacks, or whose `info` would not match `api/bridge.yaml`, raises `ConfigError` naming the reason (`test_config.py`, `test_app.py`)."
  - "[C-22.FR-25] The application serves every operation of `api/bridge.yaml` under `/v1/`, answers `401` to a missing or wrong token and calls the messenger with the bot of the token; before calling the messenger it refuses as `invalid` a request that does not match the models generated from the specification, a request body over the library's cap of 1 MiB, a `body` longer than `max_body_length` code points, buttons sent without `buttons`, more buttons than `max_per_message` and data longer than `data_max_bytes`; every request and answer of `test_spec.py`, one per operation and per problem, validates against the copy of `api/bridge.yaml` with openapi-core."
  - "[C-22.FR-25] A repeated `Idempotency-Key` under the same token answers the first result with `replayed` true without calling the messenger again, whatever the repeated body; under another token it is a new request; a key whose first request is still running is `transient`; a failed request stores nothing; keys live 24 hours (`test_store.py`, `test_app.py`)."
  - "[C-22.FR-25] A `BridgeError` of each class — `Transient`, `RateLimited` with the wait and the scope, `Gone` with `edit_window_closed`, `Forbidden`, `Invalid` — becomes its problem with the bridge's `detail`, a class the operation does not declare is mapped as in S-083, and any other exception becomes `transient` with a generic `detail` that carries none of its text, logged through `logging` without the token (`test_problems.py`, `test_spec.py`)."
  - "[C-22.FR-25] Reported presses, Thread messages, deleted messages and direct messages become events of their bot, a direct message that carries an Account link token becoming `account_link` and any other being ignored; an event reported twice with the same messenger id is queued once; `pollEvents` answers at once or within `wait` without blocking the event loop, keeps events at least 24 hours and refuses an unknown cursor as `invalid`; `answerEvent` reaches the messenger with its event and is `gone` for an id the library did not issue (`test_events.py`)."
  - "[C-22.FR-25] With a push target set for a bot, its events are posted with httpx to the callback one at a time and in order, signed as Standard Webhooks specifies, retried with back-off under the same `webhook-id` for at least an hour, dropped on `400` and kept on `401`; the pushers run as tasks of the running event loop, stop on `aclose()`, and `lifespan` stops them for a host application (`test_push.py`)."
  - "[C-22.FR-25, C-22.AC-25] `make spec-check` compares `api/bridge.yaml` with the one on Muster's `master` and fails naming the difference — a copy with one byte changed fails it — and `make generate-check` fails when `src/muster_bridge/models.py` is not what datamodel-code-generator makes of the copy; the CI jobs `spec` and `generate` run them on every pull request, on every push to `master` and daily."
  - "[C-22.AC-25] `make conformance` builds the wheel, installs it with `pip install` into a fresh virtual environment beside uvicorn, runs `examples/memory_bridge.py` from it with `python -I`, and runs `muster bridge check` from `ghcr.io/muster-io/muster:edge` against it three times, each exiting 0: with every feature and `--no-interactive`, every step passes but press and push, which are skipped; with `BRIDGE_FEATURES=thread_messages`, buttons, user_lookup, direct_message and delete are skipped too; interactively, with the example pushing to the check's listener, a script presses the button named by the `WAIT press` line through the example's `/demo/press`, and press, push and answer pass. The CI job `conformance` runs it on every pull request, on every push to `master` and daily."
  - "[C-22.FR-25] CI runs `lint` (ruff, the format check, mypy in strict mode, the header check), `test` (pytest with a coverage gate of 90 % on every Python release from 3.11 to the newest stable one), `generate`, `spec`, `licenses`, `build` and `conformance`, joined in the check `ci`; release-please keeps a release pull request with `0.x` versions, and a release builds the sdist and the wheel and publishes them to PyPI by trusted publishing from the environment `pypi`, with attestations and no stored token; Renovate, CodeQL and OpenSSF Scorecard run as in Muster's repository; every Action is pinned by commit SHA."
  - "[C-22.FR-25] The first release pull request stays open until L1 is complete, when the maintainer releases the libraries (D366): after the maintainer's PyPI setup, merging it publishes `muster-bridge` 0.1.0, and `pip install muster-bridge==0.1.0` in a fresh virtual environment imports `muster_bridge` with `__version__` `0.1.0` (Notes)."
verify: "make ci conformance"
operator_attention: true
issue: 257
---

# S-084. The Python bridge library `muster-bridge` with its CI and PyPI releases (BE)

## Scope

**IN**

- The repository `muster-io/muster-bridge-python`, from its first commit: the MIT licence, the distribution
  `muster-bridge` with the module `muster_bridge`, the contributor files and the agent guide.
- The library: the protocol's part of a bridge (C-22.FR-25), asynchronous, as an ASGI application on Starlette, with
  pydantic v2 models generated from its copy of `api/bridge.yaml` and httpx for pushes, for Python 3.11 or newer.
- An in-memory example that the README's quick start shows and the CI runs from the built wheel.
- The copy check, the conformance check from Muster's development image, and the CI, release, PyPI publication,
  Renovate, CodeQL and Scorecard workflows.

**OUT**

- The Go library and the example bridge (S-083, S-085); Write a bridge's section on this library and the README of
  Muster naming both libraries (S-060).
- `muster bridge check` itself (S-078).

## Contracts

- **Repository** (D343): as S-083's — public, default branch `master`, squash merges only, no wiki; the first commit,
  `chore: add the MIT licence`, adds only `LICENSE` and is pushed to `master` to create the branch; everything else
  goes through this story's pull request. Every Python file, workflow and Makefile starts with
  `SPDX-License-Identifier: MIT` and `Copyright The Muster Authors`; `make lint` fails naming a file without them
  (generated files, `uv.lock` and `api/bridge.yaml` excepted). CONTRIBUTING.md, SECURITY.md, AGENTS.md and the pull
  request template say what S-083's do, for this repository.
- **Package** (`pyproject.toml`, `.python-version`): the build backend hatchling; the distribution `muster-bridge`, the
  import package `muster_bridge` under `src/`, `py.typed`; `requires-python = ">=3.11"`; the runtime dependencies
  Starlette, httpx and pydantic 2, with lower bounds and no upper ones; the development dependencies — pytest, the
  anyio pytest plugin, coverage, ruff, mypy, datamodel-code-generator, openapi-core, uvicorn and pip-licenses — in a
  dependency group, locked by uv in `uv.lock`. `.python-version` names the Python of development and CI's single-version
  jobs. Each dependency is stated in the pull request with its licence, latest release and maintenance status, as
  Muster's ADR-0001 asks. `make licenses` (pip-licenses over an environment with the runtime dependencies only) allows
  MIT, MIT-0, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, PSF-2.0 and MPL-2.0, which the TLS certificates of httpx come
  under unmodified.
- **Types** (`api/bridge.yaml`): the copy of Muster's `api/bridge.yaml`, byte for byte; datamodel-code-generator writes
  `src/muster_bridge/models.py`, pydantic v2 models of the protocol's schemas with their constraints, which the
  library validates requests with and builds answers from.
- **API of the package** (`__init__.py`, `config.py`, `messenger.py`, `app.py`, `problems.py`, `store.py`,
  `mentions.py`), the names the bridge author sees, mirroring S-083's in Python's style:
  - `Messenger`, a `typing.Protocol` of `async` methods, each given the bot whose token Muster sent: `post_message`,
    `edit_message`, `reply_in_thread`, `check_channel` and `answer_event`; `UserFinder` (`find_user`),
    `DirectMessenger` (`send_direct_message`) and `Deleter` (`delete_message`) for the features that need them.
  - `Config` with `Bot` and `PushTarget`: the bridge's name and version, the messenger's name, the bots with their
    tokens and optional push targets, the capabilities, the limits, the user lookup, the event delivery modes (both by
    default), the optional `link_url_template`, a `Store` (`MemoryStore` by default) and a `logging.Logger`;
    `ConfigError` names what is wrong, with the checks of S-083's `New`.
  - `BridgeApp(config, messenger)`, the ASGI application, with `press`, `thread_message`, `message_deleted` and
    `direct_message` (coroutines that queue an event and return at once), `set_push(bot, target)`, `aclose()` and
    `lifespan`, an async context manager for a host application's lifespan — Starlette runs no lifespan of a mounted
    application, so the README's FastAPI example passes it on.
  - `BridgeError` and its classes `Transient`, `RateLimited` (with the wait and the scope), `Gone` (with
    `edit_window_closed` as a flag), `Forbidden` and `Invalid`, each with a `detail` for the Admin.
  - `Store`, a protocol of `async` methods for idempotency results and events, and `MemoryStore`.
  - `replace_mentions(body, mentions, render)`.

  The names are the contract; their exact signatures are the implementation's, and a renamed one is corrected in this
  file in the same pull request.
- **Protocol behaviour** (`app.py`, `events.py`, `push.py`, `store.py`): exactly S-083's — tokens compared in constant
  time; validation and limits; idempotency for `postMessage`, `replyInThread`, `sendDirectMessage` and `answerEvent` per
  token for 24 hours, `transient` while the first request runs; `info` built once; `deleteMessage` `204` also for a
  message the `Deleter` reports `gone`; problems with the status of their class and `Retry-After`, an undeclared class
  mapped as S-083 maps it; the request body capped at 1 MiB and `max_body_length` at 100,000; events with ids hashed
  from the bot and the messenger's id, kept at least 24 hours, long polling on an `asyncio` condition so that waiting
  requests block nothing; `account_link` only for a direct message whose whole text, trimmed and after an optional
  `/start `, is 43 characters of base64url; `answerEvent` `gone` for an unknown id; pushes signed with `hmac` and
  `hashlib`, sent with one `httpx.AsyncClient` per bridge, in order, with exponential back-off up to five minutes, for
  at least an hour, the event staying queued for polling afterwards. The pushers are tasks started on the first event of
  a bot with a push target, in the running loop.
- **Example** (`examples/memory_bridge.py`): S-083's minimal example in Python — channels from `--channels` (default
  `alerts,ops`; any other id is `forbidden`), people from `--users` (default `alice:Alice Smith,bob:Bob Brown`, found by
  login), messages written to the log, the token from `--token-file`, `--listen` (default `127.0.0.1:18095`), every
  feature unless `BRIDGE_FEATURES` lists some, `user_lookup` `login` while `direct_messages` is on and `none`
  otherwise, push with `--push-url` and `--push-secret-file`, and S-083's `POST /demo/press` for a script that plays the
  person — served with `uvicorn.run`.
- **Copy check, conformance and CI** (`Makefile`, `ci.yml`): `make spec-check` and `make spec-sync` as in S-083, the
  latter also running `make generate`; `make build` runs `uv build` into `dist/`; `make conformance` creates a fresh
  virtual environment, installs the built wheel and uvicorn with `pip install`, starts the example with `python -I`, so
  that it imports the installed package and not `src/`, and runs S-083's three `muster bridge check` runs — two with
  `--no-interactive`, one interactive through `/demo/press` — from `$(MUSTER_IMAGE)`, by default
  `ghcr.io/muster-io/muster:edge`, with a token file of mode 0644. `ci.yml` runs on pull requests, pushes to `master`,
  daily and by hand, with the jobs `lint`, `test` (every Python release from 3.11 to the newest stable one), `generate`,
  `spec`, `licenses`, `build` and `conformance`, and the job `ci` that passes only when all of them did. Tools are
  pinned in `uv.lock` and the workflows, and uv itself in the workflows, all updated by Renovate.
- **Releases** (`release-please.yml`, `release-please-config.json`, `.release-please-manifest.json`): release-please
  with the release type `python`, `bump-minor-pre-major`, `include-component-in-tag: false` and the manifest at `0.0.0`,
  run with the GitHub App's token; it updates the version in `pyproject.toml` and `__init__.py`. When it creates a
  release, a job builds the sdist and the wheel, and a second job in the GitHub environment `pypi`, with
  `id-token: write` and nothing else, publishes them with `pypa/gh-action-pypi-publish` by trusted publishing, with
  attestations.
  Versions stay `0.x` until Muster's first release, then `1.0.0` for protocol version 1 (ADR-0017).
- **Renovate, CodeQL, Scorecard** (`.github/renovate.json5`, `codeql.yml`, `scorecard.yml`): as in S-083, with
  Renovate's managers for `pyproject.toml`, `uv.lock`, `.python-version` and the workflows, and CodeQL for Python and
  Actions.

## Steps

1. Push the licence commit to create `master`; branch `s-084-python-bridge-library` from it. Check: `git ls-remote`
   shows `master` with one commit.
2. Add `pyproject.toml`, the Makefile, the copy of `api/bridge.yaml` and the generated models. Check:
   `make generate-check`, `make spec-check`.
3. Write the configuration, the application, the problems, the store and the idempotency with their tests. Check:
   `test_config.py`, `test_app.py`, `test_problems.py`, `test_store.py`.
4. Write the events, polling and the pushers. Check: `test_events.py`, `test_push.py`.
5. Write the example, the README with the quick start and the FastAPI example, then `test_spec.py`. Check: `make ci`,
   `make conformance`.
6. Add the workflows, Renovate, release-please and the contributor files. Check: the pull request's checks, all green.
7. Hand over to the maintainer for PyPI (Notes); after the first release, the last check of Verification.

## Verification

In a clone of `muster-io/muster-bridge-python`:

```sh
make ci                                          # lint, generate-check, spec-check, licenses, test, build pass
python3 -m venv /tmp/mb && /tmp/mb/bin/pip install -q dist/muster_bridge-*.whl uvicorn
/tmp/mb/bin/python -I -c 'import muster_bridge; print(muster_bridge.BridgeApp.__module__)'   # muster_bridge.app
printf example-token > /tmp/bt
/tmp/mb/bin/python -I examples/memory_bridge.py --token-file /tmp/bt &
curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:18095/v1/info                              # 401
docker run --rm --network host -v /tmp/bt:/token:ro ghcr.io/muster-io/muster:edge bridge check \
  http://127.0.0.1:18095 --token-file /token --channel alerts --forbidden-channel nobot --handle alice \
  --no-interactive; echo "exit=$?"
# PASS info · PASS channel_check · PASS post · PASS replayed_key · PASS buttons · PASS edit · PASS thread_reply
# PASS thread_ref · PASS forbidden_channel · PASS user_lookup · PASS direct_message · PASS delete · PASS poll
# SKIP press: --no-interactive · SKIP push: --no-interactive · PASS answer
# exit=0
kill %1; BRIDGE_FEATURES=thread_messages /tmp/mb/bin/python -I examples/memory_bridge.py --token-file /tmp/bt &
docker run --rm --network host -v /tmp/bt:/token:ro ghcr.io/muster-io/muster:edge bridge check \
  http://127.0.0.1:18095 --token-file /token --channel alerts --handle alice --no-interactive | grep -c SKIP   # 7
kill %1
cp api/bridge.yaml /tmp/spec.yaml && printf ' ' >> api/bridge.yaml
make spec-check; echo "exit=$?"                  # the difference is printed … exit=2
cp /tmp/spec.yaml api/bridge.yaml
make conformance                                 # passes
```

After the maintainer's PyPI setup and the merge of the first release pull request:

```sh
python3 -m venv /tmp/pypi && /tmp/pypi/bin/pip install -q muster-bridge==0.1.0
/tmp/pypi/bin/python -I -c 'import muster_bridge; print(muster_bridge.__version__)'          # 0.1.0
```

## Open questions

None.

## Notes

- Suggested commit: `feat: add the bridge library`.
- **Maintainer** (operator attention), before the first release:
  1. On PyPI, signed in as the account that will own the project, add a **pending trusted publisher** (Account
     settings → Publishing → Add a new pending publisher → GitHub): PyPI project name `muster-bridge`, owner
     `muster-io`, repository `muster-bridge-python`, workflow `release-please.yml`, environment `pypi`. The name was
     free on 2026-10-17; the first publication creates the project and makes that account its owner.
  2. In the repository's settings, create the environment `pypi`, limited to the branch `master`, and optionally with
     the maintainer as a required reviewer, so that each publication waits for an approval.
  3. As for S-083: install the release-please GitHub App and give the repository `RELEASE_PLEASE_APP_CLIENT_ID` and
     `RELEASE_PLEASE_APP_PRIVATE_KEY`; enable Renovate; after the first push, a ruleset on `master` that requires the
     check `ci` and pull requests; private vulnerability reporting.
  4. Leave the release pull request that release-please opens after this story open until L1 is complete (D366);
     merging it then tags `v0.1.0` and publishes `muster-bridge` 0.1.0; then run the last check of Verification.
- The repository was created empty by the maintainer (D343); nothing is pushed to it before this story.
- The library follows S-083's design so that a bridge author can move between the two, and is written for Python, not
  translated: `async` methods, `typing.Protocol`, exceptions for problems. Muster's code is AGPL and is never copied.
- Starlette runs the lifespan of the top-level application only; the pushers therefore start lazily, and `lifespan`
  lets a host application stop them on shutdown.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-25 | partial | the Python library; the Go library in S-083 |
| C-22.AC-25 | partial | the Python example from the built wheel and the copy check; Go in S-083 and S-085 |
