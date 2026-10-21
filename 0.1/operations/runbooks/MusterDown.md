# MusterDown

## Meaning

Prometheus cannot scrape a Muster replica: the target of the job `muster` is down. The chart rule is:

```promql
up{job="muster"} == 0
```

It fires after 2 minutes, with severity `critical` and one alert per scrape target (`instance`). It is a chart rule
only; Muster cannot raise it about itself.

!!! warning "Route it around Muster"
    Send this alert through an Alertmanager route that bypasses Muster, for example straight to a pager or a chat
    that Muster does not manage. While Muster is down it cannot deliver anything, this alert included.

## Impact

- With every replica down, nothing is ingested, no message is sent or edited, and the UI, the API and the buttons in
  the messengers do not answer. Alertmanager retries its failed notifications, and each later notification carries the
  whole Alertmanager group again, so Muster catches up once it is back.
- With one replica of several down, the others keep working: ingestion, delivery and timers run on every replica, and
  another replica takes over the Leader tasks if the stopped one was the Leader.
- After the downtime, the new Leader records it (`downtime_recorded`) and Muster recovers: see
  **Recovery after downtime** on the System status page.

## Diagnosis

1. Find out whether the process runs at all: the pod or the container, its restarts and its exit code.
2. Ask the internal listener (`MUSTER_LISTEN_INTERNAL`, `:8082` by default) directly. `GET /health/live` answers
   `200` while the process serves; `GET /health/ready` answers `503` while the database does not answer or the
   shutdown has begun.
3. Read the last log lines of the replica:
    - `startup_failed`: start-up stopped; its `error` says what to fix — the database cannot be reached, a migration
      failed or a listener cannot listen.
    - `listener_failed`: a listener stopped with an error and the process exits so that it is restarted.
    - `shutdown_requested`: the replica received SIGTERM, for example during a rollout.
4. When the process runs but the scrape fails, check the scrape configuration against `MUSTER_LISTEN_INTERNAL`: the
   port, the path `/metrics` and the network policy between Prometheus and the replica.
5. Run `muster doctor` with the replica's settings. The lines `database`, `session_connection` and `key_canary` name
   a database that cannot be reached, a session connection whose advisory locks or `LISTEN` do not work, and master
   keys that do not decrypt the stored data.

## Fix

- Fix what `startup_failed` or `muster doctor` names: the database URL or its credentials, the master keys, a port
  that another process holds.
- When the database is down, bring it back first; Muster becomes ready as soon as it answers.
- When only the scrape fails, correct the scrape target or the network policy.
- Once Muster is back, open **Recovery after downtime** on the System status page (`/admin/system-status`) and the
  Alert Group list: Alertmanager's next notifications bring the state up to date.
- Set up an outgoing heartbeat (Organization → **Outgoing heartbeat**, `/admin/organization/outgoing-heartbeat`) to an
  external dead man's switch, so that a stopped Muster is noticed even when Prometheus cannot report it.
