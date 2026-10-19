# Monitoring

Muster exports Prometheus metrics, ships alert rules and a Grafana dashboard in its Helm chart, and raises Internal
alerts about its own problems. Use all three: the metrics and the rules tell you from outside, and the Internal alerts
reach you through Muster itself.

## Metrics

Muster serves `/metrics` on the **internal listener**, port 8082 by default (`MUSTER_LISTEN_INTERNAL`), next to
`/health/live` and `/health/ready`. It never pushes metrics. Do not expose that listener outside the cluster.

With the Prometheus Operator, set `serviceMonitor.enabled: true`. The chart then creates a `ServiceMonitor` for the
internal Service, with `serviceMonitor.interval` (default 30 seconds) and `serviceMonitor.scrapeTimeout` (default 10
seconds), and extra labels from `serviceMonitor.labels` for the selector of your Prometheus. Without the operator, scrape
the internal Service yourself, or the pods on port 8082.

Every metric is in the [metrics reference](../reference/metrics.md). Entities appear by their immutable id; their names are
in the `*_info` metrics, which the dashboard joins. No metric carries an alert label, a user or an Alert Group number.

## Alert rules

The chart renders one group of rules, named `muster`, when you enable one of:

| Value | Renders | Needs |
|---|---|---|
| `alerting.prometheusRule.enabled` | a `PrometheusRule` | the Prometheus Operator CRDs |
| `alerting.vmRule.enabled` | a `VMRule` | the VictoriaMetrics Operator CRDs |

Both take extra labels (`alerting.prometheusRule.labels`, `alerting.vmRule.labels`) for the rule selector of your
operator. Each rule has a `description` and a `runbook_url` that points to its [runbook](runbooks/index.md), built from
`alerting.runbookBaseURL` and the version of Muster the chart deploys.

What you can tune:

- `alerting.jobSelector` is the label matcher that selects Muster's series in every rule. By default it is
  `job="muster"`, the job the `ServiceMonitor` gives. Change it when your scrape job has another name.
- `alerting.rules.<Name>.enabled` switches one rule off, and `.for` sets how long its condition holds before it fires.
- Some rules have thresholds: `window` (the range of the `rate` or `increase`), `seconds` (`MusterDeliverySlow`: p95
  delivery latency; `MusterClockSkew`: the skew), `size` (`MusterDeliveryQueueGrowing`), `backlog` and `delaySeconds`
  (`MusterIngestBacklog`), `unauthorized` (`MusterIngestRejected`) and `failures` (`MusterLoginFailures`).

The defaults, with the expression of each rule, are in the [chart values reference](../reference/chart-values.md), and
each rule has its [runbook](runbooks/index.md). The rules are covered by unit tests in CI.

The five rules with severity `critical` are `MusterDown`, `MusterNoLeader`, `MusterDeliveryFailing`,
`MusterDestinationBroken` and `MusterHeartbeatLost`. The others are `warning`.

### Send the critical rules around Muster

!!! warning "Do not route the critical rules through Muster"
    A rule such as `MusterDown` or `MusterDeliveryFailing` fires when Muster cannot do its job. If Alertmanager sends
    it to Muster, the alert goes exactly where it cannot be delivered. Use an Alertmanager route that **bypasses** Muster
    for them, to a receiver you trust to work when Muster does not.

The chart rules carry the labels `alertname` (starting with `Muster`) and `severity: critical` or `warning`, plus the
labels Prometheus adds from the series. Match on both, and put the route **before** the route that sends everything to
Muster, with `continue: false` (the default) so that the alert stops there:

```yaml
route:
  receiver: muster
  group_by: [alertname, namespace]
  routes:
    - receiver: platform-direct          # not Muster
      matchers:
        - alertname=~"Muster.+"
        - severity="critical"
      continue: false
    - receiver: muster                   # everything else
      continue: false

receivers:
  - name: muster
    webhook_configs:
      - url: https://ingest.muster.example.org/api/v1/ingest
        send_resolved: true
        max_alerts: 0
        http_config:
          authorization:
            type: Bearer
            credentials: mstr_int_…                       # the Integration token, see Connect Alertmanager
  - name: platform-direct
    webhook_configs:
      - url: https://chat.example.org/hooks/<id>          # a chat, a pager or a mail gateway of your own
        send_resolved: true
```

The matchers use `alertname=~"Muster.+"` for the rules of this chart; `severity="critical"` limits the bypass to the ones
that need it, so the warnings still reach Muster. Point the receiver `platform-direct` at a path that does not depend on Muster. The `muster` receiver is the one from
[Connect Alertmanager](../integrations/alertmanager.md).

The Internal alerts of Muster itself do not go through Alertmanager, so this route does not see them.

## Dashboard

The chart ships one dashboard, "Muster", with the fixed uid `muster`, a data source variable, and the variables
`namespace`/`job`, `integration`, `route` and `destination`. It has four rows: Overview, Ingestion, Delivery and
Platform. You enable it in either or both ways, each behind its own flag:

| Value | Renders | Notes |
|---|---|---|
| `dashboard.configMap.enabled` | a `ConfigMap` with the dashboard JSON | for the Grafana sidecar; `dashboard.configMap.labels` defaults to `grafana_dashboard: "1"` |
| `dashboard.grafanaDashboard.enabled` | a `GrafanaDashboard` of the Grafana Operator | references that ConfigMap, so it needs `dashboard.configMap.enabled` too; `instanceSelector`, `folder` and `allowCrossNamespaceImport` are values |

## Internal alerts

Muster raises its own problems as **Internal alerts**: `MusterSnapshotTruncated`, `MusterHeartbeatLost`,
`MusterDestinationBroken`, `MusterTemplateError` and `MusterOIDCSecretExpiring`. They are Alerts of the built-in
"Muster" Integration, which has no tokens and cannot be changed. They go through Routes like any Alert, and they reach
nobody until a Route sends them to a Destination. The [Internal alerts reference](../reference/internal-alerts.md) lists
them with their labels.

Give them a Route:

1. Open **Routes**. At the top of the list, Muster suggests *Send Muster's own alerts to a Destination* when no Route
   other than the Default route matches them.
2. Choose a Destination, and press *Create the route*. The Route matches `alertname=~"Muster.*"`.

Use a Destination that does not depend on what the alert reports: not the one that is Broken. A second messenger, or an
[outgoing webhook](../outgoing-webhooks/events.md) to a chat of your own, serves well. When a Route for
`MusterHeartbeatLost` exists, the Internal alerts Route goes directly below it. Every Internal alert has a
[runbook](runbooks/index.md), and `MUSTER_RUNBOOK_BASE_URL` (set by the chart from `alerting.runbookBaseURL`) is the base
of its link.

Two Internal alerts have a chart rule with the same name, `MusterHeartbeatLost` and `MusterDestinationBroken`. The rule is
the backup for when Muster cannot deliver; the Internal alert is the one with the Integration's labels.

## Watching that Muster is alive

Metrics and rules need Prometheus. To notice that Muster, its cluster or its network is down altogether, configure the
[outgoing heartbeat](outgoing-heartbeat.md): an external service alerts you when the calls stop.

The **System status** page (**Organization → System status**, Admins only) shows the same from the inside: the Leader and
every live replica with the key ids it holds, recovery after downtime, delivery queues, Broken Destinations, Integrations
that need attention, template errors, Storms and the outgoing heartbeat.
