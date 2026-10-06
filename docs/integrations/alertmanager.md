# Connect Alertmanager

Muster receives alerts from Alertmanager through an Alertmanager receiver with a webhook. Each delivery is a
**Snapshot** of one Alertmanager group: every alert of the Alertmanager group that still fires, and its resolves. Muster stores each request exactly as
received, answers `202` once it is written, and processes it afterwards.

In the examples, `MUSTER_INGEST_URL` is `https://ingest.muster.example.org`.

## One Integration per Alertmanager cluster

Create one **Integration** for each Alertmanager cluster that sends to Muster. An HA pair — two or more Alertmanager
instances that gossip with each other — is **one** cluster and gets **one** Integration: both instances send the same
notifications, and Muster folds their copies together within the Integration's duplicate window. Two independent
Alertmanagers, such as one per region, get one Integration each, so that each has its own repeats, its own Static
labels and its own health.

Static labels are added to every alert of the Integration before routing, for example `cluster=prod-eu`, so that Routes
and messages can tell the clusters apart.

## Create a token and paste the snippet

In **Integrations**, create the Integration, then **Create token**. Muster shows the token and a ready Alertmanager
snippet **once**; copy it before you close the dialog. Only a hash of the token is kept, so a lost token cannot be shown
again: create a new one.

The snippet has two parts. The Alertmanager receiver goes into `receivers`:

```yaml
receivers:
  - name: muster-prod-eu
    webhook_configs:
      - url: https://ingest.muster.example.org/api/v1/ingest
        send_resolved: true
        max_alerts: 0
        http_config:
          authorization:
            type: Bearer
            credentials: mstr_int_…
```

- `send_resolved: true` sends resolves, which close alerts in Muster at once.
- `max_alerts: 0` sends every alert of the Alertmanager group. Muster treats each Snapshot as the whole Alertmanager
  group; a Truncated Snapshot makes it warn.
- The token travels as `Authorization: Bearer`. A sender that cannot set a header may append the token to the URL
  instead, as `/api/v1/ingest/<token>`; Muster's logs name only the route, never such a path.

The route goes first under the top-level `route`, before your other child routes:

```yaml
route:
  routes:
    - receiver: muster-prod-eu
      continue: true
      repeat_interval: 10m
    # ... your existing child routes
    # The last child route: a catch-all without matchers for the receiver of the top-level route.
    # - receiver: <your default receiver>
```

- With no matchers, the route takes every alert, and `continue: true` passes each alert on to the child routes after
  it, so Muster runs beside your current Alertmanager receivers while you try it.
- **Keep a catch-all child route last.** Alertmanager sends an alert to the receiver of the top-level `route` only when
  no child route matches it. The Muster route matches every alert, so without a catch-all, an alert that none of your
  other child routes matches goes to Muster alone and your default receiver stops getting it. Add a last child route
  without matchers for your default receiver, or make sure an existing later child route already catches everything.

For example, a configuration that sends critical alerts to `oncall` and everything else to the top-level `default`:

```yaml
route:
  receiver: default
  routes:
    - receiver: oncall
      matchers: ['severity="critical"']
```

becomes:

```yaml
route:
  receiver: default
  routes:
    - receiver: muster-prod-eu
      continue: true
      repeat_interval: 10m
    - receiver: oncall
      matchers: ['severity="critical"']
    - receiver: default
```

A critical alert goes to `muster-prod-eu` and `oncall`, any other alert to `muster-prod-eu` and `default`. Without the
last line, a warning would reach only Muster. `amtool config routes test --config.file=alertmanager.yml
severity=warning` prints the receivers an alert reaches; it should list Muster and your default receiver.

- Keep `repeat_interval` between **5 and 15 minutes** (10 minutes in the snippet). Muster learns from the repeats that
  alerts still fire, and resolves an alert as Gone once its Alertmanager group stopped listing it for up to two repeat
  intervals. A long repeat interval does not reduce noise in Muster — Muster controls noise itself, with one message
  per Alert Group that it edits — it only makes Muster slower to notice that an alert is Gone.

Reload Alertmanager (`POST /-/reload` or `SIGHUP`). The Integration page then shows the time of the last Snapshot and
the Stored Snapshots as received.

## Start delay

When Alertmanager starts, it does not know the alerts that were firing before. It learns them again as the rule
evaluators (Prometheus, vmalert) send them, and until then its Alertmanager groups list only part of their alerts. A
Snapshot sent in that time is **partial**, and Muster would take the missing alerts as Gone.

Set `--dispatch.start-delay` so that Alertmanager waits before it sends. It must be at least **twice** the longer of the
rule evaluation interval and the rule evaluator's resend delay, plus a margin. Prometheus re-sends a firing alert only
every second evaluation at its defaults, so with a 1-minute evaluation interval use **about 2.5 minutes or more**:

```sh
alertmanager --dispatch.start-delay=2m30s ...
```

With 90 seconds, the first Snapshot after a start was still partial in tests.

## HA pairs need a persistent volume

Give every instance of an HA pair a persistent volume for its data directory (`--storage.path`). Without one, a
restarted instance loses its silences and its notification log: it can notify silenced alerts and send partial
Snapshots until the other instance catches it up.

## Token rotation

An Integration may hold several tokens. To rotate one without losing a webhook:

1. Create a second token and paste its snippet (or only the new `credentials`) into the Alertmanager configuration.
2. Reload Alertmanager and check that the token list shows a recent use of the new token.
3. Revoke the old token. It is refused from the next request.

A request with a revoked token is refused with `401` and counted under the Integration in
`muster_ingest_requests_total{outcome="unauthorized"}`, so the chart rule `MusterIngestRejected` shows an Alertmanager
that still uses an old token.

## Behind a reverse proxy

Muster serves ingestion on its own listener, `MUSTER_LISTEN_INGEST` (`:8081` by default), apart from the UI and API on
`MUSTER_LISTEN_APP`. Set `MUSTER_INGEST_URL` to the address Alertmanager uses; it defaults to `MUSTER_PUBLIC_URL`, and
the snippets are built from it.

With separate listeners, route `/api/v1/ingest` and `/api/v1/heartbeat` (with everything below them) to the ingest
port. A reverse proxy that serves both on one host name, for example, sends these two paths to `:8081` and everything
else to `:8080`. Keep the proxy's body size limit at **16 MB** or more: Muster accepts bodies up to `ingest.body_limit`
(16 MB) and answers `413` above it. Do not let the proxy log request paths with the token, or prefer the
`Authorization` header, which proxies do not log by default.

With one listener for both (`MUSTER_LISTEN_INGEST` equal to `MUSTER_LISTEN_APP`), the same port serves ingestion and
the API.

## What Muster answers

| Answer | When | Alertmanager |
|---|---|---|
| `202` | The body was stored, whatever it holds | done |
| `401` | No token, an unknown or revoked token, a token of a deleted Integration, or an API token | does not retry |
| `413` | The body is larger than 16 MB | does not retry |
| `500` | The body could not be written | retries |

Muster never answers `400`: a body that is not JSON, or not an Alertmanager payload, is stored and fails later in
processing, where the Stored Snapshot shows the error.
