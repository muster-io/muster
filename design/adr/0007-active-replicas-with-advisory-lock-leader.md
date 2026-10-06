# 0007. High availability: all replicas active, one Leader for singleton work

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — the OIDC client secret expiry check (`MusterOIDCSecretExpiring`) is a Leader task
- Amended: 2026-10-05 — pruning short-lived rows and the records of gone replicas are Leader tasks

## Context

Most of Muster's work can run on any replica: HTTP (API, UI, ingestion, Mattermost button callbacks) and the workers
that claim rows with `SKIP LOCKED` (ADR-0006) — ingestion, delivery and timers with deadlines (ack timeouts, Reminders,
Snooze ends, Grace periods). A few tasks must run exactly once across all replicas:

- Telegram `getUpdates` long polling — Telegram answers `409 Conflict` when two clients poll the same bot token;
- periodic checks over all Integrations and Alerts: Heartbeat lost and Stale Alerts;
- creating and dropping partitions, and pruning short-lived rows (ended sessions, expired requests and links) and the
  records of gone replicas;
- gauges computed from the database;
- a periodic "alive" mark that tells a restarted Muster how long it was down;
- Muster's own outgoing heartbeat to an external dead man's switch (ADR-0014);
- the check of the OIDC client secret's expiry date, which raises the Internal alert `MusterOIDCSecretExpiring`
  (ADR-0014).

Muster runs under docker-compose as well as on Kubernetes, often with PgBouncer in front of PostgreSQL. PgBouncer in
transaction pooling mode breaks session-level features: session advisory locks and `LISTEN/NOTIFY` do not survive
across transactions.

A session advisory lock is released when its session ends. If the Leader's process crashes, its connection closes and
the lock is released at once. If the Leader's node disappears or the network splits, PostgreSQL keeps the session until
it notices that the peer is gone — with default Linux TCP keepalive settings, about two hours — while the old Leader may
still be running and believe it leads.

## Decision

**All replicas are active.** Every replica serves HTTP and runs the ingestion, delivery and timer workers. Timer rows are
claimed like any other row, with `SKIP LOCKED` and a lease, so a timer fires on whichever replica is free and a stalled
replica delays nothing beyond its lease.

**One Leader** holds a **session advisory lock with a constant key** on a dedicated connection and runs only the tasks
listed in the Context: Telegram polling, the Heartbeat lost and Stale checks, partition maintenance, database gauges,
the "alive" mark, the outgoing heartbeat and the OIDC secret expiry check (later also polling Alertmanager in the `pull`
Connection mode). This list is closed: new singleton work is added to it by a reviewed change. Because only the Leader
sends the outgoing heartbeat, it also stops when no replica leads.

**A bounded lease on leadership.** The Leader proves it still holds the lock by pinging over the lock's own session at a
fixed interval. The lock session sets `idle_session_timeout` (which is why PostgreSQL 14 is the oldest supported
version, ADR-0006) and TCP keepalive settings, so PostgreSQL ends a session that has gone silent — and releases the lock
— within an explicit bound instead of hours. A Leader whose ping fails, or has not succeeded within a fencing deadline
shorter than that bound, stops all singleton work at once and closes the lock connection; it may compete for the lock
again later. So the old Leader normally stops before PostgreSQL can hand the lock to another replica. A process frozen
for longer than the bound can still overlap with its successor for a moment, so every singleton task must be safe to run
twice: Telegram rejects a second poller with `409 Conflict`, which the poller answers by backing off — it never counts
against the Connection or marks anything Broken; partition changes and Stale resolutions are idempotent, Internal alerts
are deduplicated by fingerprint, and an extra outgoing heartbeat is harmless. Defaults: a ping every 5 seconds, a
fencing deadline of 15 seconds and a server-side bound of 30 seconds, so a handover after a node loss takes well under a
minute.

**Downtime.** The Leader writes an "alive" mark every 30 seconds. After a full outage, the next Leader learns how long
Muster was down and adds a Timeline entry to open Alert Groups; overdue timer rows are claimed like any others and fire
once, collapsed (ADR-0004).

**A session-capable connection.** The Leader lock, `LISTEN` connections and migrations (ADR-0006) use a session
connection that must reach PostgreSQL directly or through session pooling. By default it is the main connection;
installations whose main connection goes through a transaction pooler give it a separate address (ADR-0010). At startup
Muster checks that these features work over it and stops with a clear error otherwise; `muster doctor` reports the
pooler mode.

**One replica by default.** The Helm chart defaults to `replicas: 1` and docker-compose runs one instance; two replicas
with a PodDisruptionBudget are an option. A nightly end-to-end test runs two replicas, so duplication bugs are found
without anyone having to run HA in production.

**Health.** Liveness performs no checks; readiness checks only the database. Unreachable messengers or Alertmanagers
raise alerts; they do not take replicas out of load balancing. The `muster_leader` metric shows which replica leads.
The chart's `MusterNoLeader` rule fires when no replica has led for 2 minutes (ADR-0014): Telegram polling, the
Heartbeat lost and Stale checks, partition maintenance and pruning, the database gauges, the outgoing heartbeat and the OIDC secret
expiry check have stopped, while ingestion, delivery and timers keep working.

## Consequences

- HTTP and all workers, timers included, scale horizontally; only the few singleton tasks do not, which is fine at the
  volumes of the first release.
- A Leader change pauses Telegram polling, the periodic checks and the outgoing heartbeat for at most the handover
  bound; no ack timeout or Reminder waits for it.
- The Leader's own database connection becomes a liveness signal: a Leader that cannot reach PostgreSQL stops leading
  within seconds, even if its process keeps running.
- Installations whose main connection goes through PgBouncer in transaction mode must provide a second, session-capable
  connection; this is checked at startup, not discovered in production.
- One replica by default keeps the minimal installation small; correctness with several replicas is guarded by the
  nightly test.
- Rolling updates are supported only between adjacent minor versions (ADR-0006), so replicas of very different versions
  never run side by side.

## Alternatives considered

- **Rely on the connection closing when the Leader dies.** True for a crash, not for a lost node or a network split,
  where PostgreSQL may keep the session for hours while timers and polling stand still.
- **Timers only on the Leader.** One replica would handle every deadline, and a stalled Leader would stop ack timeouts
  and Reminders across the installation; timer rows need no Leader once they are claimed with a lease.
- **Active/passive**, where only the Leader serves traffic. Wastes replicas and makes every failover visible to UI users
  and to Alertmanager.
- **One advisory lock per role, spreading roles across replicas.** Balances load, but "who does what" becomes harder to
  observe and debug, and first-release volumes do not need it.
- **Kubernetes Lease objects.** Muster must also run outside Kubernetes.
- **Locks in Redis.** Would add the component ADR-0006 avoids.
- **Requiring a direct database connection for everything.** Many PostgreSQL deployments put PgBouncer in front; a
  second connection setting for the few session connections is cheaper for users.
