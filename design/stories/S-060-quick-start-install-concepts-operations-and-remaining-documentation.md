---
id: S-060
title: "Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations"
capability: C-21
kind: docs
layer: L1
depends_on: [S-055, S-071, S-059, S-054, S-077, S-079]
covers: [C-21.FR-2, C-21.FR-5, C-21.AC-2, C-19.FR-3]
files_touched:
  - docs/quick-start.md
  - docs/install/helm.md
  - docs/install/database.md
  - docs/integrations/alertmanager.md
  - docs/messengers/proxies.md
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
  - docs/index.md
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
verify: "make ci docs-check"
operator_attention: false
issue: 60
---

# S-060. Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations

## Scope

**IN**

- Quick start with docker-compose, and its nightly check on a clean machine.
- Install, the missing parts of Alertmanager and Sign-in, proxies, Concepts and Operations.

**OUT**

- Pages written with their capabilities: the Alertmanager receiver and Heartbeat (S-018, S-023), Mattermost and Telegram
  (S-061, S-042), Account links (S-051), outgoing webhooks (S-044, S-045), OIDC with Keycloak (S-013), the runbooks and
  "Telegram in restricted networks" (S-058), the Reference section (S-057), Bridge and Write a bridge with the protocol
  reference (S-074, S-081, S-076, S-079).

## Contracts

- **Origin**: split from S-058 when the contracts were written.
- **Pages** (C-21.FR-2), each linked from `mkdocs.yml` and the home page:

  | Section | Page | Topics |
  |---|---|---|
  | Quick start | `quick-start.md` | the compose example downloaded from the release tag, with a generated master key, the first sign-in, an Integration, the snippet for Alertmanager, a test alert, the next steps |
  | Install | `install/helm.md` | Helm values, an existing Secret for the master keys and passwords, ingress and Gateway API routes, the two public addresses (`MUSTER_PUBLIC_URL`, `MUSTER_INGEST_URL`), replicas and the PodDisruptionBudget |
  | Install | `install/database.md` | the database as a URL or as fields, external PostgreSQL with a CloudNativePG `Cluster` example, notes for managed PostgreSQL, the session connection for PgBouncer (and, for the main connection in transaction mode, PgBouncer 1.21 or newer with `max_prepared_statements` above 0), TLS modes |
  | Alertmanager | `integrations/alertmanager.md` (extended) | repeat intervals of 5–15 minutes, `max_alerts: 0`, Static labels, advice against Instance labels in informational alerts, besides the existing receiver, route and HA pair settings |
  | Messengers | `messengers/proxies.md` | the proxy of each client, the three types, authentication, what the outbound address policy checks through a proxy |
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
  with the password the page sets. `make quickstart-check` takes `MUSTER_IMAGE` and `QUICKSTART_REF`: the image the
  compose file runs and the Git ref the download reads in place of the release tag. The nightly job runs
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

## Steps

1. Write Quick start and its check. Check: `make quickstart-check` passes on a machine with only Docker.
2. Write Install and the additions to Alertmanager, Messengers and Sign-in. Check: `make docs-check`.
3. Write Concepts and Operations. Check: `make docs-check`; the list in Verification.

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
#                                                                       (no output)
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
