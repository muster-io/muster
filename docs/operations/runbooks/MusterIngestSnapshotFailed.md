# MusterIngestSnapshotFailed

## Meaning

Stored Snapshots of an Integration failed in processing.

Within 15 minutes Muster could not process one: the body was not JSON, lacked a field that an Alertmanager webhook
has, or processing failed. The chart rule is:

```promql
sum by (integration) (increase(muster_ingest_failed_snapshots_total{job="muster"}[15m])) > 0
```

It fires at once (`for: 0m`), with severity `warning` and the label `integration`, the id of the Integration. It is a
chart rule only. The sender got `202`, because ingestion never answers `400`; the Integration page shows the warning
`snapshot_failed` instead.

## Impact

- The failed Snapshots create or change no Alert and no Alert Group, so nothing is sent for them, and they are not
  retried by themselves.
- Snapshots that arrive later are processed as usual, and the Integration page stops warning once one is processed,
  also one from another sender of the same Integration. The warning is not a durable signal; this rule is, and it
  resolves 15 minutes after the last failure.
- The warning shows the error without the values the sender chose, such as a quoted status. The Stored Snapshot has
  the full error.

## Diagnosis

1. Open the Integration page (`/integrations/<id>`): the warning `snapshot_failed` shows the error of its newest
   Stored Snapshot, for example `the webhook has no groupKey`. `GET /api/v1/integrations/<id>` carries the same as
   `warnings`.
2. Open **Stored Snapshots** of the Integration, or `GET /api/v1/stored-snapshots?integration=<id>`: the Snapshots in
   state `failed` keep their body as received and the `processing_error`.
3. Read the log lines `snapshot_failed` for the Integration.
4. Compare the body with the [fields a body needs](../../integrations/alertmanager.md#what-the-body-needs): a
   custom sender must send `groupKey`, `status` and `alerts`, and in each Alert `labels`, `status` and `startsAt`.

## Fix

- Correct the sender so that its body has the required fields, and send it again; the next Snapshot that is processed
  clears the warning.
- Run `muster ingest replay --since <duration> --integration <name> --actor <name>` to process the failed Snapshots
  again after a fix on the Muster side.
