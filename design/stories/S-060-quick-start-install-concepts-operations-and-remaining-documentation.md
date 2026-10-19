---
id: S-060
title: "Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations"
capability: C-21
kind: docs
layer: L1
depends_on: [S-055, S-071, S-059, S-054, S-077, S-079, S-084]
covers: [C-21.FR-2, C-21.FR-5, C-21.AC-2, C-19.FR-3, C-22.FR-23]
files_touched:
  - docs/quick-start.md
  - docs/install/helm.md
  - docs/install/database.md
  - docs/integrations/alertmanager.md
  - docs/integrations/grafana.md
  - docs/messengers/index.md
  - docs/messengers/proxies.md
  - docs/outgoing-webhooks/recipes.md
  - docs/sign-in/index.md
  - docs/sign-in/totp.md
  - docs/sign-in/closed-networks.md
  - docs/concepts/alert-groups.md
  - docs/concepts/routes-and-group-keys.md
  - docs/concepts/snapshot-semantics.md
  - docs/concepts/loud-and-quiet.md
  - docs/concepts/statuses-and-commands.md
  - docs/concepts/broken-destinations.md
  - docs/operations/backup-and-restore.md
  - docs/operations/upgrades.md
  - docs/operations/key-rotation.md
  - docs/operations/high-availability.md
  - docs/operations/monitoring.md
  - docs/operations/outgoing-heartbeat.md
  - docs/operations/cli.md
  - docs/messengers/write-a-bridge.md
  - docs/index.md
  - README.md
  - AGENTS.md
  - mkdocs.yml
  - test/docs/quickstart.sh
  - Makefile
  - release-please-config.json
  - .github/workflows/nightly.yml
acceptance:
  - "[C-21.FR-2] The site has the sections Quick start, Install, Alertmanager, Messengers, Outgoing webhooks, Sign-in, Concepts, Operations, Runbooks and Reference, each with the topics C-21.FR-2 lists, checked against the list in Verification."
  - "[C-21.AC-2] The nightly workflow runs the commands of Quick start, exactly as the page shows them, on a fresh runner and reaches a running Muster: `/health/ready` answers `200` and the bootstrap Admin signs in — against the `edge` image of `master` always, and against the last release once one exists."
  - "[C-19.FR-3] Operations → Monitoring tells users to send the critical chart rules through an Alertmanager route that bypasses Muster, with an example route, and explains the rule values and the dashboard flags."
  - "[C-21.FR-5] Every new page is in English and draws its diagrams as Mermaid."
  - "[C-22.FR-23] Write a bridge has the Python bridge library beside the Go one — `pip install muster-bridge` (until the libraries are released with L1, `pip install .` in a clone of its repository, D366), a `Messenger` for the messenger, `BridgeApp` served by uvicorn or mounted into a FastAPI application, and `muster bridge check` against it — and Quick start's next steps and the README's roadmap and licence sections name both libraries and the example bridge, checked against the list in Verification."
verify: "make ci docs-check"
operator_attention: false
issue: 60
---

# S-060. Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations

## Scope

**IN**

- Quick start with docker-compose, and its nightly check on a clean machine.
- Install, the missing parts of Alertmanager and Sign-in, proxies, Concepts and Operations.
- The Python bridge library's section of Write a bridge, and the bridge libraries in Quick start's next steps and in
  the README's roadmap and licence sections.
- Maintainer queue (D375): the statement that the web UI and API work only after sign-in, in the README and Sign-in; a
  table of what each Destination type can do, on the Messengers page; outgoing-webhook recipes; Grafana Alerting through
  an external Alertmanager.

**OUT**

- Pages written with their capabilities: the Alertmanager receiver and Heartbeat (S-018, S-023), Mattermost and Telegram
  (S-061, S-042), Account links (S-051), outgoing webhooks (S-044, S-045), OIDC with Keycloak (S-013), the runbooks and
  "Telegram in restricted networks" (S-058), the Reference section (S-057), Bridge and Write a bridge with the protocol
  reference (S-074, S-081, S-076, S-079), apart from Write a bridge's section on the Python library, which this story
  adds; the libraries' own READMEs (S-083, S-084, S-085).

## Contracts

- **Origin**: split from S-058 when the contracts were written.
- **Pages** (C-21.FR-2), each linked from `mkdocs.yml` and the home page:

  | Section | Page | Topics |
  |---|---|---|
  | Quick start | `quick-start.md` | the compose example downloaded from the release tag, with a generated master key, the first sign-in, an Integration, the snippet for Alertmanager, a test alert, the next steps |
  | Install | `install/helm.md` | Helm values, an existing Secret for the master keys and passwords, ingress and Gateway API routes, the two public addresses (`MUSTER_PUBLIC_URL`, `MUSTER_INGEST_URL`), replicas and the PodDisruptionBudget |
  | Install | `install/database.md` | the database as a URL or as fields, external PostgreSQL with a CloudNativePG `Cluster` example, notes for managed PostgreSQL, the session connection for PgBouncer (and, for the main connection in transaction mode, PgBouncer 1.21 or newer with `max_prepared_statements` above 0), TLS modes |
  | Alertmanager | `integrations/alertmanager.md` (extended) | repeat intervals of 5–15 minutes, `max_alerts: 0`, Static labels, advice against Instance labels in informational alerts, besides the existing receiver, route and HA pair settings |
  | Alertmanager | `integrations/grafana.md` | Grafana Alerting: Grafana-managed alert rules sent to an external Alertmanager that has the Muster receiver, why not Grafana's webhook contact point, and the labels Grafana adds; maintainer queue (D375) |
  | Messengers | `messengers/index.md` | the Destination types and Connections, and a table of what each Destination type can do: buttons, Threads, Loud and Quiet, Mentions, Account links, rate limits, edit windows, Storms, proxies and checks; maintainer queue (D375) |
  | Messengers | `messengers/proxies.md` | the proxy of each client, the three types, authentication, what the outbound address policy checks through a proxy |
  | Outgoing webhooks | `outgoing-webhooks/recipes.md` | recipes built only from the events and template modes: a phone call or an SMS, a status page, a second chat, an issue tracker; maintainer queue (D375) |
  | Sign-in | `sign-in/index.md` | the web UI and the API work only after sign-in, the endpoints open without it, the Roles, the ways to sign in, and the Account links that people make themselves; maintainer queue (D375) |
  | Sign-in | `sign-in/totp.md`, `sign-in/closed-networks.md` | TOTP and its policy, recovery codes, resets; installations whose IdP Muster cannot reach: local users with TOTP |
  | Concepts | `concepts/*.md` | Alert Groups; Routes and Group keys; Snapshot semantics (Gone, Stale, Heartbeat, truncation); Loud and Quiet; statuses and commands with ack timeouts, Reminders and auto-unacknowledge; Broken Destinations and recovery |
  | Operations | `operations/backup-and-restore.md` | backing up the database and the master keys separately, the restore order, `muster doctor` after a restore |
  | Operations | `operations/upgrades.md` | semantic versions, adjacent minor versions only for rolling updates, backups first, no downgrades, migrations |
  | Operations | `operations/key-rotation.md` | the two-step rotation of S-071, the Keyring page and "Not active yet", removing the old key, expiring Reminder buttons, and web pages open across the removal: a CSRF token carries the id of the key that signed it, so a page open since before the rotation has its next change refused once the old key is gone and works again after a reload |
  | Operations | `operations/high-availability.md` | active replicas and the Leader, what stops without one, the session connection, two replicas with a PodDisruptionBudget, NTP on every node and what a clock skew does (NFR-9) |
  | Operations | `operations/monitoring.md` | the chart rules and their values, the dashboard and its flags, `/metrics` on the internal listener, the Alertmanager route that bypasses Muster for critical rules with an example, the Internal alerts Route |
  | Operations | `operations/outgoing-heartbeat.md` | dead man's switch services, the URL as a Secret, the proxy, what the System status shows |
  | Operations | `operations/cli.md` | `muster doctor`, `muster migrate`, `muster admin`, `muster ingest replay`, `muster secrets rotate-key`, `muster bridge check` (with a link to Write a bridge, S-079), `muster version`; `--actor` on the commands that change data |

- **Facts and requirements**: every behaviour a page describes is cited from the PRD and the facts, never invented;
  pages link to the Reference section for names of metrics, log events, variables and chart values instead of copying
  them.
- **Quick start before the first release** (C-21.AC-2): nothing is released before L1 is complete, so the page must
  work while no release exists. It downloads `deploy/compose/docker-compose.yml` from the release tag
  (`https://raw.githubusercontent.com/muster-io/muster/v<version>/deploy/compose/docker-compose.yml`), and the version
  in that command carries the `x-release-please-version` marker, with `docs/quick-start.md` added to the `extra-files`
  of `release-please-config.json`, so each release updates it as it updates the compose file. The compose file already
  reads its image from `MUSTER_IMAGE` (default: the release image).
- **Quick start check** (C-21.AC-2; `test/docs/quickstart.sh`, `make quickstart-check`, `.github/workflows/nightly.yml`):
  the script extracts the shell blocks of `docs/quick-start.md` marked `<!-- quickstart -->` and runs them in order in an
  empty directory on a fresh runner with only Docker, then waits for `/health/ready` and signs in as the bootstrap Admin
  with the password the page sets. `/health/ready` is served on the internal listener (`:8082`), which the compose
  example does not publish, and the Muster image has no shell, so the script probes it from the compose network: it runs
  busybox `wget` in the `postgres` container of the same compose project against `http://muster:8082/health/ready`
  until it answers `200`, within `QUICKSTART_READY_TIMEOUT` (180 seconds by default). It then signs in with
  `POST /api/v1/sessions` on `MUSTER_PUBLIC_URL`, with the login and password it reads from the `.env` the page wrote,
  and expects `201` and an active Admin session. It always removes the compose project with its volume, prints the
  containers' logs when it fails, and never prints the passwords or the key. `make quickstart-check` takes
  `MUSTER_IMAGE` and `QUICKSTART_REF`: the image the compose file runs and the Git ref the download reads in place of
  the release tag; `AGENTS.md` lists the target in its Make targets table. The nightly job runs
  `scripts/ci/docker-hub-mirror.sh` first, as the other Docker jobs do, then two legs: `master` — `MUSTER_IMAGE`
  `ghcr.io/muster-io/muster:edge` and the ref `master`; release — the page as written, skipped with a notice while the
  repository has no release (`gh release view` finds none).
- **Mermaid** (C-21.FR-5): Concepts draws the Alert Group states and the path from Snapshot to message; High
  availability draws the Leader handover.
- **Bridges** (C-22, D317): this story follows the Bridge stories, so that the pages name Bridges wherever they list
  messengers — Quick start's next steps, the Destination types in Concepts, Text commands beside buttons in "statuses and
  commands", republication past an edit window in "Broken Destinations", the Leader's Bridge polling in "High
  availability" and `muster bridge check` in the CLI page — and link to Messengers → Bridge and Write a bridge (S-074,
  S-081, S-079) instead of repeating them.
- **Bridge libraries** (C-22.FR-23, C-22.FR-25, D343): both libraries are merged in their repositories when this story
  starts: S-079 wrote Write a bridge with the Go library, and S-084 built the Python library beside it, after S-078 and
  S-083 (D369). Neither is released before L1 is complete (D366), so PyPI has no `muster-bridge` until then. Write a
  bridge gains the Python library's section beside the Go one — `pip install muster-bridge` (until the release,
  `pip install .` in a clone of the repository, as the library's README says), a `Messenger` of `async` methods,
  `BridgeApp` served by uvicorn or mounted into a FastAPI application with its `lifespan`, the reporting calls, and
  `muster bridge check` against it — taken from the library's README; Quick start's next steps name both libraries for a
  messenger Muster does not support; the README's L1 roadmap names Bridges with the libraries for Go and Python, and its
  licence section says that `api/bridge.yaml` is MIT and that the libraries are MIT in repositories of their own, as is
  the example bridge, which is not a repository of its own but `examples/chat` in `muster-bridge-go`.

## Steps

1. Write Quick start and its check. Check: `make quickstart-check` passes on a machine with only Docker.
2. Write Install and the additions to Alertmanager, Messengers and Sign-in. Check: `make docs-check`.
3. Write Concepts and Operations. Check: `make docs-check`; the list in Verification.
4. Add the Python library to Write a bridge and the libraries to Quick start and the README. Check: `make docs-check`;
   the bridge library lines of Verification.

## Verification

```sh
make docs-check; echo "exit=$?"                                         # exit=0
MUSTER_IMAGE=ghcr.io/muster-io/muster:edge QUICKSTART_REF=master make quickstart-check; echo "exit=$?"
# … ready, signed in as admin@example.org … exit=0
grep -c 'x-release-please-version' docs/quick-start.md                  # 1

# C-21.FR-2: every topic of the list is on its page
T() { grep -qiF -- "$2" docs/$1 || echo "missing in $1: $2"; }
T install/helm.md existingSecret; T install/helm.md 'Gateway API'; T install/helm.md MUSTER_INGEST_URL
T install/database.md 'kind: Cluster'; T install/database.md 'managed PostgreSQL'; T install/database.md PgBouncer
T integrations/alertmanager.md 'max_alerts: 0'; T integrations/alertmanager.md 'Static labels'; T integrations/alertmanager.md 'Instance labels'
T integrations/alertmanager.md 'dispatch.start-delay'; T messengers/proxies.md SOCKS5; T sign-in/closed-networks.md TOTP
T concepts/snapshot-semantics.md Stale; T concepts/loud-and-quiet.md Mention; T concepts/statuses-and-commands.md Reminder
T concepts/broken-destinations.md 'Broken'; T concepts/statuses-and-commands.md 'Text command'; T operations/high-availability.md Bridge
T operations/backup-and-restore.md 'muster doctor'; T operations/upgrades.md downgrade
T operations/key-rotation.md 'rotate-key'; T operations/key-rotation.md 'Not active yet'; T operations/key-rotation.md CSRF
T operations/high-availability.md Leader; T operations/monitoring.md 'bypass'
T operations/outgoing-heartbeat.md 'dead man'; T operations/cli.md -- '--actor'; T operations/cli.md 'muster bridge check'
T messengers/write-a-bridge.md 'pip install muster-bridge'; T messengers/write-a-bridge.md 'pip install .'
T messengers/write-a-bridge.md BridgeApp
T quick-start.md muster-bridge-go; T quick-start.md muster-bridge-python
T messengers/index.md 'What each Destination type can do'; T messengers/index.md 'Edit window'
T integrations/grafana.md 'external Alertmanager'; T integrations/grafana.md grafana_folder
T outgoing-webhooks/recipes.md 'A status page'; T outgoing-webhooks/recipes.md 'An issue tracker'
T sign-in/index.md 'works only after sign-in'; T sign-in/index.md Responder
#                                                                       (no output)
grep -c 'muster-bridge-go\|muster-bridge-python' README.md                 # 2 or more
grep -c 'example bridge' README.md                                          # 1 or more
for s in 'Quick start' Install Alertmanager Messengers 'Outgoing webhooks' Sign-in Concepts Operations Runbooks Reference; do
  grep -q "$s" site/index.html || echo "missing section $s"; done      # (no output)
grep -l 'class="mermaid"' site/concepts/*/index.html site/operations/high-availability/index.html | wc -l   # 3
```

## Open questions

None.

## Notes

- Suggested commit: `docs: add quick start, install, concepts and operations`.
- Quick start is the only page whose commands CI runs; keep it short and keep every command in a marked block.
- Until the first release the release leg of the nightly check is skipped; the first nightly run after the first
  release checks the page exactly as users read it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-2 | partial | the remaining sections; with S-057 and S-058 complete |
| C-21.FR-5 | partial | the pages of this story; with S-057 and S-058 complete |
| C-21.AC-2 | full | |
| C-19.FR-3 | partial | the documentation of the bypass route; with S-059 complete |
| C-22.FR-23 | partial | the Python library in Write a bridge; with S-074, S-081, S-076 and S-079 complete |
