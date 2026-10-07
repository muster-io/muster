# Heartbeat

A **Heartbeat** proves, for one Integration, that the path from your cluster to Muster works: Prometheus evaluates
rules, Alertmanager sends notifications, and the network carries them to Muster. Alertmanager sends Muster a signal
about once a minute; when the signals stop, Muster calls people. It is part of the recommended setup of every
Integration.

In the examples, `MUSTER_INGEST_URL` is `https://ingest.muster.example.org`.

## Why it matters

Without a Heartbeat, a silent Alertmanager looks exactly like a healthy cluster with nothing to report. With one:

- Muster notices when Alertmanager goes quiet. After the Heartbeat timeout without a signal, the Integration is
  **Heartbeat lost** and Muster raises the Internal alert `MusterHeartbeatLost` (severity `critical`). Its labels are
  `integration` (the Integration's id), `integration_name` and the Integration's Static labels, so a Route can send it
  to the team of that cluster.
- Muster can resolve alerts as **Stale**. An Alertmanager group that is silenced, inhibited or muted as a whole sends
  nothing at all, not even its resolves, so Muster resolves its alerts once it has not seen them for three learned
  repeat intervals. It does that only while it knows that it would have heard from Alertmanager: while the Heartbeat is
  live. An Integration without a Heartbeat never resolves alerts as Stale.

## Turn it on

On the Integration page, turn the Heartbeat on. The timeout is 5 minutes by default (`integration.heartbeat_timeout`);
keep it a few times the interval at which Alertmanager signals. The Heartbeat then **waits for the first signal**:
nothing is raised and nothing goes Stale until it arrives, however long that takes. The first signal makes it
**live**.

While the Heartbeat is off, Muster answers a signal with `204` and records nothing, so turning it on later always
starts from waiting. Turning it off resolves an open `MusterHeartbeatLost`.

## The Heartbeat URL

Each Integration has its Heartbeat URL on the ingest listener:

```text
https://ingest.muster.example.org/api/v1/heartbeat
```

It accepts `GET` and `POST` with one of the Integration's tokens as `Authorization: Bearer <token>`, or appended to the
path as `/api/v1/heartbeat/<token>`, and answers `204`. The body is ignored. A missing, wrong or revoked token, or an
API token, is refused with `401`. Muster's logs name only the route, never a path with a token.

## The snippet

With the Heartbeat on, **Create token** also shows a Heartbeat snippet, **once**, with the token in it. It has three
parts.

An alert that always fires, as a Prometheus rule:

```yaml
groups:
  - name: muster-heartbeat
    rules:
      - alert: MusterHeartbeat
        expr: vector(1)
```

kube-prometheus-stack already has such an alert, **`Watchdog`**. With it, skip the rule and match
`alertname="Watchdog"` in the route instead.

A child route for it, first under the top-level route:

```yaml
route:
  routes:
    - receiver: muster-heartbeat-prod-eu
      matchers: ['alertname="MusterHeartbeat"']
      continue: true
      group_wait: 0s
      group_interval: 1m
      repeat_interval: 1m
```

`repeat_interval: 1m` (`snippet.heartbeat_repeat_interval`) makes Alertmanager send the always-firing alert about once
a minute, and `continue: true` passes it on to your other routes, such as a dead man's switch.

A receiver that sends it to the Heartbeat URL, without resolves:

```yaml
receivers:
  - name: muster-heartbeat-prod-eu
    webhook_configs:
      - url: https://ingest.muster.example.org/api/v1/heartbeat
        send_resolved: false
        http_config:
          authorization:
            type: Bearer
            credentials: mstr_int_…
```

Reload Alertmanager. Within a minute or two the Integration's Heartbeat becomes live.

## A cron job also works, but proves less

Any client that requests the Heartbeat URL regularly keeps the Heartbeat live, for example a cron job:

```sh
curl -fsS -H "Authorization: Bearer $MUSTER_TOKEN" https://ingest.muster.example.org/api/v1/heartbeat
```

It proves only that the network from that host to Muster works. The Alertmanager route proves much more: that
Prometheus evaluates rules, that Alertmanager routes and sends notifications, and that they reach Muster, which is
exactly the path your alerts take. Prefer it.

## When the Heartbeat is lost

No signal within the timeout makes the Integration **Heartbeat lost**:

- Muster raises `MusterHeartbeatLost` as a firing Alert of the built-in "Muster" Integration, and the Integration shows
  since when Muster has had no contact: the time of the last signal.
- **Stale resolution pauses.** The time without a live Heartbeat, from the last signal before the loss until the
  signal returns, does not count towards an alert's time to go Stale, so a broken path never resolves alerts that may
  still fire.
- The chart's rule `MusterHeartbeatLost` on `muster_heartbeat_lost` is the backup when Muster itself cannot deliver.

The next signal makes the Heartbeat live again and resolves `MusterHeartbeatLost`. Deleting the Integration resolves it
too.

After Muster itself was down, the timeout counts from the moment the Leader started leading again, so that Muster's own
outage does not make every Integration Heartbeat lost. A downtime longer than the timeout does not count towards
staleness either; a shorter one does, which is at most the timeout against at least three repeat intervals.
