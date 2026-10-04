# C-06. Snapshot processing

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-05

**Goal.** Every Stored Snapshot becomes Alerts with the right state — firing, resolved explicitly, Gone or Stale — with
no false resolutions from HA duplicates, restarts or truncation, and no loud surprises from restarted rule evaluators.
This capability also provides the Internal alert mechanism and shows what Muster learned about each Integration.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#alertmanager).

## Scenarios

1. An alert disappears from two consecutive Snapshots of its `groupKey` and stays away for
   `processing.gone_min_absence`; Muster resolves it as Gone with a reason.
2. An Alertmanager HA pair sends two copies of a Snapshot 15 seconds apart, one of them incomplete; nothing resolves.
3. A whole Alertmanager group is silenced in Alertmanager, so nothing more arrives; with a live Heartbeat (C-07), its
   Alerts go Stale after three learned repeat intervals.
4. A rule evaluator restarts for longer than the alerts' `endsAt` window; Alertmanager sends the same alerts with a new
   `startsAt` and no `resolved` in between. Muster records a Continuation, quietly.
5. `max_alerts` truncates a Snapshot; Muster raises `MusterSnapshotTruncated` and keeps the unlisted Alerts alive while
   Snapshots of that Alertmanager group keep arriving.
6. An informational Alertmanager route repeats daily; until its interval is learned, Muster waits
   `processing.stale_after_unlearned` before treating its Alerts as Stale.
7. The Admin opens the Integration page and sees the Alertmanager routes Muster has learned, with their repeat
   intervals, and the Alerts it currently tracks.
8. After a processing bug is fixed, an operator runs `muster ingest replay --since 2h --actor ops-alice`.
9. A single Alertmanager without persistent storage restarts. Its first Snapshots of a group, a minute apart, list 3
   and then 9 of 10 firing alerts, and the next one all 10; the alert missing from the first two stays firing.
10. Both instances of an Alertmanager HA pair restart while 20 alerts of a group resolve. The one `all alerts resolved`
    Snapshot lists 6 of them, and Alertmanager never sends the others; Muster resolves all 20.

## Functional requirements

- **C-06.FR-1** A worker woken by `LISTEN/NOTIFY` processes Stored Snapshots in arrival order per Integration, at most
  one per Integration at a time.
- **C-06.FR-2** Each Snapshot is split into Alerts by fingerprint. Processing is idempotent per Alert on
  `fingerprint + status + startsAt`; a duplicate Snapshot, such as the identical copy from the other instance of an HA
  pair (F-037), changes nothing.
- **C-06.FR-3** Static labels are added to every Alert first. If an Alert already has the label, its own value wins and
  the Alert carries a warning naming the label (shown in its Alert Group's Timeline from C-09). Processing then emits
  Alert changes — a new firing, a resolution with its reason, a Continuation, an annotation change — which C-08 routes
  and C-09 groups; until those capabilities are merged, the changes are recorded on the Alerts only.
- **C-06.FR-4** An Alert sent as `resolved` is resolved at once — from C-09 on, in the latest Alert Group in which that
  fingerprint still fires. A `resolved` for an Alert that is already resolved, or for an earlier firing of it (an older
  `startsAt` than its current one), changes nothing: it is not counted as dropped and never reopens or starts anything.
  Prometheus re-sends a resolved alert for about 15 minutes, so the same `resolved` arrives several times, also inside
  later firing Snapshots (F-046). Any other `resolved` for a fingerprint that fires nowhere is dropped and counted in
  `muster_ingest_resolved_dropped_total{integration}`. Resolved Alerts never create Alerts or Alert Groups.
- **C-06.FR-21** A Snapshot with `status: resolved` — the one Alertmanager sends with `notification_reason`
  `all alerts resolved` — resolves at once every Alert still firing in its Alertmanager group, including the Alerts it
  does not list, as if each were listed as `resolved` (FR-4). After a restart of both instances of an HA pair, that
  Snapshot can list only some of the resolved alerts, and Alertmanager never sends the others (F-044); a truncated one
  leaves out its tail (F-048). The `status` of any other Snapshot is that of the whole Alertmanager group, so a `firing`
  Snapshot can list resolved Alerts; each Alert's own `status` decides for it.
- **C-06.FR-5** An Alert is Gone in an Alertmanager group once it has been missing from consecutive Snapshots of its
  `groupKey` — at least two — for at least `processing.gone_min_absence`, counted from the first Snapshot that missed it
  to the latest one; a later Snapshot that misses it decides again, and one that lists it ends the count. Snapshots of
  one `groupKey` arriving within the Integration's duplicate window count as one, and an Alert listed in either of them
  counts as listed. The minimum absence covers a restarted Alertmanager, whose first Snapshots can miss a firing alert
  twice, a minute apart, because Prometheus re-sends firing alerts only every 2 minutes (F-042, F-043); a restarted
  instance of an HA pair is covered by the duplicate window, because the other instance sends the full Snapshot 15
  seconds later (F-041).
- **C-06.FR-6** A Snapshot with `truncatedAlerts > 0` never counts as evidence of absence; Alertmanager keeps the first
  alerts in its sort order and counts resolved ones in `truncatedAlerts` too (F-048). A `groupKey` is truncated from
  such a Snapshot until its first untruncated Snapshot, or until no Snapshot of it has arrived for longer than
  `stale_after`. While it is truncated, its unlisted Alerts stay alive as long as Snapshots of that Alertmanager group
  arrive. While any `groupKey` of an Integration is truncated, the Internal alert `MusterSnapshotTruncated` is raised
  for that Integration; it is resolved when none is left. Every truncated Snapshot is counted in
  `muster_ingest_truncated_snapshots_total{integration}`.
- **C-06.FR-7** An Alert that appears in several Alertmanager groups is resolved by absence only when it is Gone or
  Stale in every one of them. Sibling Alertmanager routes with identical matchers and `continue: true` share one
  `groupKey`, so their Snapshots are one Alertmanager group to Muster (F-049). A reload of Alertmanager that changes a
  route's matchers starts a new `groupKey` and silences the old one for good, so the Alerts it listed go Stale there
  while the new `groupKey` keeps them alive (F-036).
- **C-06.FR-8** Each Alert and `groupKey` pair has a `last_seen_at`. `stale_after` is `processing.stale_after_factor`
  times the repeat interval learned for the Alertmanager route of that `groupKey`, learned from the gaps between
  Snapshots whose `notification_reason` is `repeat interval elapsed` (F-050) or, for Alertmanager versions without that
  field, between identical Snapshots; near-duplicates within the duplicate window are ignored. The learned interval is
  the median of the recent gaps: single gaps range from just above the route's `repeat_interval` in an HA pair whose
  instances tick out of phase to `repeat_interval + 2 × group_interval` after a reload of Alertmanager, around a steady
  `repeat_interval + group_interval` (F-033 to F-035), and three learned intervals cover the longest of them. Until an
  interval is learned, `stale_after` is `processing.stale_after_unlearned`. Stale is what resolves the Alerts of an
  Alertmanager group that sends nothing at all — silenced, inhibited or muted as a whole (F-045); an alert that resolves
  during such a mute is still reported if the mute ends within about 14 minutes, and otherwise only goes Stale (F-047).
- **C-06.FR-9** Stale applies only while the Integration's Heartbeat is live (C-07). Time without a live Heartbeat —
  from the last signal before a loss until the signal returns — and time during which Muster itself was down do not
  count towards `stale_after`. Muster's own downtime is recognised the same way, as a gap between Heartbeat signals
  longer than the Heartbeat timeout (`integration.heartbeat_timeout`), so a downtime shorter than the timeout does count;
  this is accepted, because the timeout is small against `stale_after`, at least three learned repeat intervals. An
  Integration without a Heartbeat, or still waiting for its first signal, never resolves Alerts as Stale; until C-07 is
  merged, no Integration does.
- **C-06.FR-10** An Alert resolved as Gone or Stale carries the reason "Alertmanager no longer reports this alert — it
  resolved without notice, was silenced or inhibited in Alertmanager, or the Alertmanager routing changed".
- **C-06.FR-11** The same fingerprint with a new `startsAt` and no `resolved` in between, while the Alert is open, is a
  Continuation: `startsAt` is updated and the change is recorded; nothing is Loud and nothing reopens. A restart of
  Prometheus whose first evaluation falls exactly on the alerts' `endsAt` produces one; vmalert, which aligns `startsAt`
  to the minute, practically does not (F-051 to F-053). After the Alert has been resolved (explicitly, Gone or Stale),
  the next firing of that fingerprint is a new firing.
- **C-06.FR-12** A change in annotations is an Alert change of its own: it updates what the Alert shows and is Quiet
  (from C-11, a Root message update without a Thread reply).
- **C-06.FR-13** Windows and timers use Muster's clock at the moment of receipt; `startsAt` and `endsAt` are only shown
  and used to order events of the same source.
- **C-06.FR-14** Internal alerts form a closed registry in code — name, labels, condition, severity and runbook page
  for each — with a generated reference page that CI keeps current (ADR-0014). They enter processing as Alerts of the
  built-in "Muster" Integration: each raise and each resolve is a synthetic Stored Snapshot of that Integration, marked
  internal, which the same worker processes in order with the Integration's other Snapshots, so Internal alerts also
  appear in its Stored Snapshot list and replay like any Snapshot. Muster raises and resolves them explicitly,
  deduplicates them by fingerprint, routes them by ordinary Routes once C-08 exists, and never resolves them as Gone or
  Stale: a synthetic Snapshot is never evidence of absence. A configuration entity appears in
  an Internal alert's labels by its immutable id (`integration`, `route`, `destination`), with its current name in a
  separate label (`integration_name`, …); the fingerprint is computed from `alertname` and the id labels only, so
  renaming the entity updates the name label of the open Internal alert in place, Quietly like an annotation change,
  instead of resolving it and raising a new one. The built-in Integration is listed with the others, marked built-in; it
  has no tokens and no Heartbeat and cannot be edited or deleted. The L1 registry is in
  [reference.md](reference.md#internal-alerts); each Internal alert is added by the capability that raises it.
- **C-06.FR-15** Processing writes one log line per Snapshot — never one per Alert — and exports the processing delay,
  the backlog, resolutions by reason (`resolved`, `gone`, `stale`, `integration_deleted`) and dropped `resolved`
  notifications, as listed for C-06 in the metrics catalogue, and the Snapshots that failed (FR-20).
- **C-06.FR-16** When an Integration is deleted (C-05.FR-8), its open Alerts are resolved with the reason "Integration
  {name} deleted", and its Internal alerts (`MusterSnapshotTruncated`, later `MusterHeartbeatLost`) are resolved.
- **C-06.FR-17** `muster ingest replay --since <duration> [--integration <name>] --actor <name>` reprocesses Stored
  Snapshots; because processing is idempotent per Alert, replaying already processed Snapshots changes nothing. Each
  replay is recorded in the Audit log.
- **C-06.FR-18** The Integration page shows a table of the Alertmanager routes seen (taken from `groupKey`), with the
  learned repeat interval and the resulting time to resolve by absence; when an interval exceeds
  `processing.long_repeat_warning`, a warning with the recommended route snippet; and, while any `groupKey` is
  truncated, the truncation warning. The Integrations list shows both warnings.
- **C-06.FR-19** The Integration page has an Alerts view: the Alerts Muster currently tracks for the Integration —
  labels, state (firing, or resolved with its reason and time), `startsAt`, time last seen, the Alertmanager groups
  listing it and any Static label warning — filtered by state and by labels; from C-08 and C-09 on, each also shows its
  Route, Severity level and Alert Group. Resolved Alerts stay in the view for `retention.alert_details`.
- **C-06.FR-20** A Stored Snapshot that cannot be processed — the body is not JSON, lacks the fields of an Alertmanager
  payload, or processing fails — is marked `failed` with the error, logged as the registered ingestion event "failed"
  and counted in `muster_ingest_failed_snapshots_total{integration}`; it is never retried by itself and never blocks the
  Snapshots behind it, and `muster ingest replay` reprocesses it after a fix (FR-17). The Stored Snapshot keeps the body
  as received.

## UI

Integration page: learned Alertmanager routes table, truncation and long-interval warnings, Alerts view; warnings in the
Integrations list; the built-in "Muster" Integration in the list.

## API surface

`integrations/{id}/alertmanager-routes` (list learned routes); `integrations/{id}/alerts` (list with filters); the CLI
subcommand `muster ingest replay`.

## Acceptance

Each statement is checked with the fake Alertmanager, through the Integration's Alerts view or its API.

- **C-06.AC-1** An Alert missing from one Snapshot stays firing; missing from the next one too, received at least
  `processing.gone_min_absence` after the first, it resolves as Gone with the reason of FR-10.
- **C-06.AC-2** Snapshot S1 lists an Alert and S2, 20 seconds later, does not: they count as one Snapshot in which the
  Alert is listed. After S3 without it, outside the duplicate window, the Alert still fires; after S4 without it,
  `processing.gone_min_absence` after S3, it is Gone.
- **C-06.AC-3** An Integration without a Heartbeat never resolves an Alert as Stale, however long its Alertmanager group
  stays silent; for a route whose Snapshots repeat every 5 minutes, the Integration page shows a learned interval of 5
  minutes.
- **C-06.AC-4** A new `startsAt` without `resolved` on an open Alert is recorded as a Continuation: the Alert stays
  firing with the new `startsAt`, and no new firing is recorded.
- **C-06.AC-5** A truncated Snapshot raises `MusterSnapshotTruncated` as a firing Alert of the built-in Integration; the
  next untruncated Snapshot of the same `groupKey` resolves it.
- **C-06.AC-6** Replaying the last hour twice changes no Alert and records no Alert change the second time.
- **C-06.AC-7** Deleting an Integration resolves its open Alerts with the deletion reason and resolves its
  `MusterSnapshotTruncated`.
- **C-06.AC-8** An Alert carrying `cluster="a"` on an Integration with the Static label `cluster=b` keeps `a` and shows
  a warning naming `cluster`.
- **C-06.AC-9** One webhook with 200 Alerts produces exactly one processing log line.
- **C-06.AC-10** A `resolved` for a fingerprint that fires nowhere changes no Alert and increases
  `muster_ingest_resolved_dropped_total` for its Integration by one; deleting an Integration with two open Alerts
  increases `muster_alerts_resolved_total{reason="integration_deleted"}` by two.
- **C-06.AC-11** Renaming an Integration whose `MusterSnapshotTruncated` is firing changes that Alert's
  `integration_name` label and keeps the same fingerprint; no second Internal alert is raised.
- **C-06.AC-12** A Stored Snapshot whose body is `not json` is marked `failed` with an error, increases
  `muster_ingest_failed_snapshots_total` for its Integration by one, writes one "failed" log line, and the next valid
  Snapshot of the Integration is processed normally.
- **C-06.AC-13** With 20 Alerts firing in one `groupKey` and one more in another, a Snapshot of the first with
  `status: resolved` and `notification_reason` `all alerts resolved` that lists 6 of the 20 resolves all 20 and
  increases `muster_alerts_resolved_total{reason="resolved"}` by 20; the Alert of the other `groupKey` still fires.
- **C-06.AC-14** After an Alert is resolved, the same `resolved` arriving four more times within 15 minutes — alone and
  inside firing Snapshots of its `groupKey` — changes no Alert, records no Alert change and leaves
  `muster_ingest_resolved_dropped_total` unchanged. When the fingerprint then fires with a new `startsAt`, a `resolved`
  with the old `startsAt` leaves it firing.
- **C-06.AC-15** The restart of scenario 9: with `processing.gone_min_absence` at its default, Snapshots of a `groupKey`
  at T, T+60 s and T+120 s that list 3, 9 and 10 of its 10 firing Alerts, Alert A missing from the first two, leave
  every Alert firing. If A is instead missing from Snapshots at T, T+60 s and T+6 min, it stays firing until T+6 min
  and is Gone then.

## Related ADRs

ADR-0002, ADR-0003, ADR-0006, ADR-0014.

## Depends on

C-05 — Integrations, Stored Snapshots and the ingestion path.

## Suggested story split

- **BE** — processing (split, idempotency, repeated resolves, resolved Alertmanager groups, Gone with its minimum
  absence, truncation, Continuation, staleness bookkeeping, learned intervals), the Internal alert mechanism and
  built-in Integration, replay, the two read APIs.
- **FE** — Integration page sections (learned routes, warnings, Alerts view) and list warnings.
