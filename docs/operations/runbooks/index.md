# Runbooks

One page for every alert Muster can raise: the rules of the Helm chart, evaluated by Prometheus or VictoriaMetrics
from Muster's metrics, and the Internal alerts that Muster raises itself. Each page says what the alert means, its
impact, how to diagnose it and how to fix it.

| Alert | Severity | Source |
|---|---|---|
| [MusterDown](MusterDown.md) | critical | chart rule |
| [MusterNoLeader](MusterNoLeader.md) | critical | chart rule |
| [MusterDeliveryFailing](MusterDeliveryFailing.md) | critical | chart rule |
| [MusterDestinationBroken](MusterDestinationBroken.md) | critical | both |
| [MusterHeartbeatLost](MusterHeartbeatLost.md) | critical | both |
| [MusterDeliverySlow](MusterDeliverySlow.md) | warning | chart rule |
| [MusterDeliveryQueueGrowing](MusterDeliveryQueueGrowing.md) | warning | chart rule |
| [MusterIngestBacklog](MusterIngestBacklog.md) | warning | chart rule |
| [MusterIngestRejected](MusterIngestRejected.md) | warning | chart rule |
| [MusterTemplateError](MusterTemplateError.md) | warning | both |
| [MusterClockSkew](MusterClockSkew.md) | warning | chart rule |
| [MusterLoginFailures](MusterLoginFailures.md) | warning | chart rule |
| [MusterSnapshotTruncated](MusterSnapshotTruncated.md) | warning | Internal alert |
| [MusterOIDCSecretExpiring](MusterOIDCSecretExpiring.md) | warning | Internal alert |

## Two sources

- **Chart rules** are evaluated by your monitoring from the metrics Muster serves at `/metrics`, and reach people
  through your Alertmanager like any other rule. Their thresholds, windows and job selector are values of the chart;
  the expressions on these pages are the defaults.
- **Internal alerts** are raised by Muster itself, as Alerts of the built-in Integration "Muster" that every
  Organization has. Ordinary Routes pick them by their labels and deliver them like any other Alert Group; see
  [Internal alerts](../../reference/internal-alerts.md).
- An alert listed as **both** is an Internal alert with a chart rule of the same name for the same condition. The
  chart rule is the backup for when Muster cannot deliver the Internal alert itself.

Send the critical chart rules through an Alertmanager route that bypasses Muster, to a pager or a chat that Muster
does not manage: when Muster is down or cannot deliver, it cannot deliver an alert about that either.

## Runbook links

Every Internal alert carries a `runbook_url` annotation that opens its page here, for the version of Muster that
raised it, and the chart rules link to the same pages:

```text
<MUSTER_RUNBOOK_BASE_URL>/<version>/operations/runbooks/<alertname>/
```

`MUSTER_RUNBOOK_BASE_URL` is `https://muster-io.github.io/muster` by default, the published site; point it at your own
copy of the site where people cannot reach the public one. `<version>` is the `major.minor` of the binary, such as
`1.2`, or `latest` for a development build. For example, `MusterOIDCSecretExpiring` of a development build links to
`https://muster-io.github.io/muster/latest/operations/runbooks/MusterOIDCSecretExpiring/`.
