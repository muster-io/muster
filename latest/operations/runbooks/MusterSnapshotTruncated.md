# MusterSnapshotTruncated

## Meaning

Alertmanager truncates the Snapshots of an Integration: it left Alerts out of a Snapshot because of the `max_alerts`
limit of its webhook receiver. Muster treats each Snapshot as the whole Alertmanager group, so a Truncated Snapshot
hides Alerts from it.

It is an **Internal alert** only, severity `warning`, raised by Muster as an Alert of the built-in "Muster"
Integration when the first Alertmanager group (`groupKey`) of the Integration arrives truncated. Its labels are
`integration` (the Integration's id) and `integration_name`. It is resolved when no group of the Integration is
truncated any more: each group's next untruncated Snapshot ends its truncation, and so does a group that has sent
nothing for as long as it takes an Alert to go Stale.

## Impact

- The Alerts that Alertmanager left out do not reach their Alert Groups: Muster may show fewer Alerts than fire, miss
  an Alert Group entirely, or keep an Alert open that Alertmanager no longer lists.
- Everything else of the Integration keeps working.

## Diagnosis

1. Open the Integration page (`/integrations/<id>`): the banner says "Alertmanager truncates Snapshots for N groups.
   Set `max_alerts: 0` on the Alertmanager receiver." Its **Alertmanager routes** list shows the **Truncated groups**.
2. **System status** (`/admin/system-status`) lists the Integration under **Integrations needing attention** with
   "Snapshots truncated".
3. `muster_ingest_truncated_snapshots_total` by `integration` counts the truncated Snapshots; the log line
   `internal_alert_raised` (`alertname`, `fingerprint`, `entity`) marks when the alert was raised.
4. Open a recent Stored Snapshot of the Integration (`/integrations/<id>/snapshots/<id>`): a Truncated Snapshot has
   `truncatedAlerts` above 0.
5. In the Alertmanager configuration, find the `webhook_configs` entry of the receiver that sends to Muster and its
   `max_alerts`.

## Fix

- Set `max_alerts: 0` on the Alertmanager receiver that sends to Muster, so that every Alert of the group is sent,
  and reload Alertmanager; see [Connect Alertmanager](../../integrations/alertmanager.md).
- The alert resolves once each truncated group has sent an untruncated Snapshot, at its next notification.
