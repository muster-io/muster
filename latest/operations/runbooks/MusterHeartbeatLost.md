# MusterHeartbeatLost

## Meaning

An Integration is **Heartbeat lost**: its Heartbeat is on, and no signal arrived within its Heartbeat timeout
(`integration.heartbeat_timeout`, 5 minutes by default). Something on the path from Prometheus through Alertmanager
and the network to Muster has stopped; see [Heartbeat](../../integrations/heartbeat.md).

This alert has two sources with the same name, severity `critical` and this page:

- The **Internal alert**, raised by the Leader's Heartbeat check, which runs every 10 seconds, as an Alert of the
  built-in "Muster" Integration. Its labels are `integration` (the Integration's id), `integration_name` and the
  Integration's Static labels, so a Route can send it to the team of that cluster. It is resolved by the next signal,
  by turning the Heartbeat off, and by deleting the Integration.
- The **chart rule**, the backup for when Muster itself cannot deliver the Internal alert:

    ```promql
    max by (integration) (muster_heartbeat_lost{job="muster"}) == 1
    ```

    It fires after 1 minute with the label `integration`. `muster_heartbeat_lost` is set by the Leader's Heartbeat
    check.

!!! warning "Route it around Muster"
    Send the chart rule through an Alertmanager route that bypasses Muster, so that it reaches people even when the
    path through Muster is the one that broke.

## Impact

- Alerts from that Alertmanager may not reach Muster: new problems in that cluster may go unnoticed.
- **Stale resolution pauses** for the Integration: the time without a live Heartbeat does not count towards an
  alert's time to go Stale, so nothing is resolved as Stale until the signal returns.
- Other Integrations, delivery and Commands keep working.

## Diagnosis

1. Open the Integration page (`/integrations/<id>`): the banner says "No contact with Alertmanager since HH:MM…",
   the time of the last signal. **System status** (`/admin/system-status`) lists the Integration under
   **Integrations needing attention** as "Heartbeat lost since …".
2. Find the log line `heartbeat_lost` (`integration`, `last_signal_at`). The signal returning logs `heartbeat_live`
   with `first` false.
3. Check Alertmanager: is it running, and does it still have the Heartbeat route and receiver? Its own metrics show
   failed notifications to the Heartbeat receiver.
4. Check Prometheus: the always-firing alert (`MusterHeartbeat`, or `Watchdog` in kube-prometheus-stack) must be
   firing, so the rule evaluation and the connection from Prometheus to Alertmanager work.
5. Check the path to Muster's ingest listener (`MUSTER_INGEST_URL`): DNS, ingress, certificates and network
   policies. Requests that arrive with a wrong or revoked token are refused with `401` and logged as
   `ingest_rejected`; see [MusterIngestRejected](MusterIngestRejected.md).
6. When many Integrations are Heartbeat lost at once, the problem is likely on Muster's side: its ingest listener or
   the network in front of it.

## Fix

- Restart or repair Alertmanager or Prometheus, or restore the Heartbeat route and receiver from the snippet
  ([The snippet](../../integrations/heartbeat.md#the-snippet)).
- Fix the network path or the ingress to `MUSTER_INGEST_URL`.
- Replace a revoked token in the Heartbeat receiver with a token of the Integration.
- When the cluster was retired on purpose, turn the Heartbeat off or delete the Integration; both resolve the alert.
- The next signal makes the Heartbeat live again and resolves the Internal alert; see
  [When the Heartbeat is lost](../../integrations/heartbeat.md#when-the-heartbeat-is-lost).
