# MusterIngestBacklog

## Meaning

Processing falls behind ingestion: more than 1,000 Stored Snapshots wait to be processed, or the 95th percentile of
the time from receiving a Snapshot to the end of its processing is above 60 seconds. The chart rule is:

```promql
max(muster_ingest_backlog{job="muster"}) > 1000 or histogram_quantile(0.95, sum by (le) (rate(muster_ingest_processing_delay_seconds_bucket{job="muster"}[5m]))) > 60
```

It fires after 5 minutes, with severity `warning`. It is a chart rule only. `muster_ingest_backlog` is counted by the
Leader over every Organization. Ingestion itself keeps answering Alertmanager: Muster stores each Snapshot first and
processes it afterwards, in arrival order per Alertmanager group.

## Impact

- New Alerts reach their Alert Groups late, and so do resolves: Alert Groups appear, change and resolve in the UI and
  in the messengers later than Alertmanager sent them.
- Nothing is lost: the Stored Snapshots wait and are processed in order.

## Diagnosis

1. Find the slow Integrations:
   `histogram_quantile(0.95, sum by (le, integration) (rate(muster_ingest_processing_delay_seconds_bucket{job="muster"}[5m])))`.
2. List what waits for one Integration: `GET /api/v1/stored-snapshots?integration=<id>&state=pending`, or the
   Stored Snapshots of the Integration page (`/integrations/<id>`).
3. Read the log lines `snapshot_processing_interrupted` (`integration`, `error`): the processing worker stopped
   because the database failed or the connection was lost, and tries again after a growing pause.
4. Check the database pool of each replica: `muster_db_pool_connections` by `state` against
   `muster_db_pool_max_connections`, and the rate of `muster_db_pool_acquire_wait_seconds_total`. A pool that is always
   fully acquired, with the wait time growing, means that the replicas need more connections.
5. Check whether failures add to the load: `muster_ingest_failed_snapshots_total` by `integration` and the log line
   `snapshot_failed` (`integration`, `stored_snapshot`, `error`). A failed Snapshot is not retried by itself; the
   Snapshots behind it are processed.
6. Check the database itself: CPU, I/O, locks and slow queries.

## Fix

- Restore the database or the connection to it; the worker resumes on its own.
- Give the replicas more connections (`pool_max_conns` of `MUSTER_DATABASE_URL`, by default the larger of 10 and the
  number of CPUs), or run more replicas. Each Integration processes `processing.parallel_groups` (2) Alertmanager
  groups at the same time.
- Reduce the input: an Alertmanager that sends very often or very large groups makes more work. Check its
  `group_interval` and `repeat_interval`.
- After fixing the cause of `snapshot_failed`, process the failed Snapshots again with
  `muster ingest replay --since <duration> [--integration <name>] --actor <name>`, for example
  `muster ingest replay --since 2h --integration prod-eu --actor "Jane Doe"`. Processing is idempotent, so Snapshots
  that were processed change nothing.
