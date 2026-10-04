# 0006. PostgreSQL as the only stateful dependency

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — a trigram index over the `summary` annotation of Alert Groups, next to the one over titles;
  Notes are kept with the summary rows of Alert Groups, not with the details

## Context

Muster needs durable storage for configuration, Alert Groups and their Alerts, Timelines and the Audit log; a queue for
ingestion; a queue with retries and leases for delivery; timers with deadlines (ack timeouts, Reminders, Snooze ends,
Grace periods); a way for replicas to agree on work that must run only once; and a way to push live updates to browsers.
The minimal installation should be one binary plus one database, small enough for a home lab — one replica and
PostgreSQL within 256 MB of RAM — while the first release (L1) must handle a burst of 50 webhooks per second for a
minute, 10,000 active Alerts and 1,000 open Alert Groups.

The usual answer adds Redis and a job-queue library. The established PostgreSQL job queue for Go, River (MPL-2.0,
actively developed), was evaluated: ordering per key and concurrency limits per key — exactly what ingestion (order per
Integration) and delivery (limit per Destination) need — exist only in its commercial Pro edition, distributed as a
private Go module, which a self-built AGPL product cannot depend on.

## Decision

**PostgreSQL is the only stateful dependency.** No Redis, no message broker. The oldest supported version is
PostgreSQL 14, the first with `idle_session_timeout`, on which the Leader lease relies (ADR-0007); Muster checks the
server version at startup and stops with a clear error on an older one.

**Data access.** `pgx` with `sqlc`: SQL is written by hand and typed Go code is generated; generated code is checked in
and CI verifies it is current. Every query on a table with `org_id` must filter by it, which an architecture lint checks
by parsing the queries (ADR-0016); row-level security may be added later. Business queries never call SQL `now()`: the
current time is passed from Go, so tests can drive timers with a virtual clock.

**Three thin mechanisms on `FOR UPDATE SKIP LOCKED`.** They are rows with state, not generic jobs:

- **Ingestion** — the queue is the partitioned table of Stored Snapshots itself; at most one Stored Snapshot per
  Integration is in processing (ADR-0002).
- **Delivery** — reconciliation rows Alert Group × Destination with `next_attempt_at`, limiters per Destination and per
  Connection, and error classes (ADR-0005).
- **Timers** — rows with a deadline. Like delivery rows, they are claimed by whichever replica is free; no single
  replica owns them.

The shared parts are written once: claiming a row in a short transaction with a lease that runs out (stuck rows are
picked up again), exponential backoff with jitter, and making slow external calls outside the transaction. Workers are
woken by `LISTEN/NOTIFY`, which also feeds live updates to the UI. A session advisory lock elects the Leader for the
few tasks that must run only once, such as partition maintenance (ADR-0007).

**Partitions and retention.** High-volume tables are partitioned by time and cleaned by dropping whole partitions, never
by `DELETE`: Stored Snapshots daily (kept 14 days), Timeline entries and other events monthly (kept 90 days). The
Leader creates partitions ahead of time; `pg_partman` is not used. Other retention tiers: Alerts inside Alert Groups
90 days, summary rows of Alert Groups with their Notes 2 years (for time-to-acknowledge and time-to-resolve
statistics), Audit log 1 year. All periods are shown and editable in the UI. A search over past Alert Groups always has
a time range (default 7 days) so that partitions can be pruned; labels are indexed with GIN over `jsonb`, titles and
`summary` annotations with trigrams.

**Migrations.** Hand-written SQL in golang-migrate format; the same directory is the schema `sqlc` reads; `squawk` lints
dangerous DDL in CI. Migrations run through `muster migrate` or `MUSTER_MIGRATE_ON_START` (on in docker-compose, off in
the Helm chart, where an init container runs `muster migrate`), always under a session advisory lock. That lock is taken
over the session-capable connection (ADR-0007), because a transaction pooler would lose it between statements. Every
migration follows expand/contract and is compatible with the previous release; `up → down → up` must pass as an
acceptance check. A binary refuses to start on a schema newer than it knows. Rolling updates are supported only between
adjacent minor versions; downgrades are not supported, and the documentation says to back up before upgrading.

**Deployment.** The Helm chart does not include PostgreSQL: Muster takes the connection settings of an external database
from an existing Secret (ADR-0010). The documentation shows a CloudNativePG `Cluster` example and notes for managed
PostgreSQL; docker-compose runs PostgreSQL as a separate container.

## Consequences

- One stateful component to run, back up and monitor. Backups cover everything except the master keys, which are kept
  separately (ADR-0011).
- Queue semantics — order per Integration, limits per Destination and Connection, leases — are Muster's own code and
  need thorough tests, including tests with a virtual clock and with two replicas.
- Throughput is bounded by PostgreSQL; the L1 objectives fit well within it. An external queue for very large
  installations remains possible behind the ingestion interface.
- Daily partitions keep the 14-day promise for Stored Snapshots. With monthly partitions, a whole month is dropped at
  once and data would live up to about 45 days — at an estimated 2 GB per day, about 85 GB instead of about 27 GB.
- Repeated Snapshots are usually byte-identical; storing them by reference to a hash is left to the schema design.

## Alternatives considered

- **Redis or Valkey for queues and locks.** Another stateful component in every installation, with its own persistence
  and high-availability story.
- **River.** The features Muster needs are available only in the commercial edition (see Context).
- **A message broker (NATS, Kafka).** Too heavy for the minimal installation; kept as a possible option for very large
  installations.
- **An ORM such as GORM.** Hides the SQL that the queue mechanics depend on.
- **Monthly partitions for Stored Snapshots.** See Consequences.
- **`pg_partman`.** One more extension to install; creating partitions ahead of time is simple enough to do in Muster.
- **Bundling PostgreSQL in the chart, for example through a Bitnami subchart.** Bitnami's free images moved to a legacy
  repository and part of its catalogue now requires a commercial subscription; rendering a CloudNativePG `Cluster` from
  Muster's chart would tie the chart to another project's CRD versions.
