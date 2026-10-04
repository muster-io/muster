# 0002. Sit after Alertmanager; webhook-only ingestion with Snapshot semantics

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — ingestion never answers `400`: any body within the size limit is stored as received, and a body
  that cannot be parsed is a processing error
- Amended: 2026-10-04 — after a live Alertmanager test ([verified facts](../facts.md#alertmanager)): a Snapshot with
  `status: resolved` resolves every Alert still firing in its Alertmanager group, listed or not; a repeated `resolved`
  changes nothing; Gone also needs a minimum absence, `processing.gone_min_absence`; `--dispatch.start-delay` of at
  least about 2.5 minutes; the context states the measured repeat, duplicate, restart and truncation behaviour

## Context

Prometheus-style monitoring already has a component that deduplicates alerts from redundant rule evaluators, applies
inhibition and Alertmanager silences, and batches notifications: Alertmanager. Muster's job starts where Alertmanager's
ends — turning alerts into Alert Groups people can act on in messengers. Re-implementing Alertmanager would duplicate a
mature tool and force users to move their alerting configuration.

Alertmanager talks to other systems through Alertmanager receivers. A webhook receiver sends, for one Alertmanager
group (identified by `groupKey`), a body listing **all current alerts of that Alertmanager group** — a Snapshot, not a
change. It sends again when the set of alerts changes and, while something fires, after the repeat interval of the
Alertmanager route that formed the group. Several properties of this path, checked against the source code of
Alertmanager v0.34.1, Prometheus v3.15 and vmalert v1.153 and then in a live test of those versions (the ids `F-NNN`
of [verified facts](../facts.md#alertmanager)), shape the design:

- An alert can stop appearing in Snapshots without ever being sent as resolved: it was silenced or inhibited in
  Alertmanager, the Alertmanager routing changed, it resolved while a notification failed, or its resolve was lost —
  after both instances of an HA pair restarted, the `all alerts resolved` Snapshot of a group listed 6 of its 20
  resolved alerts, and Alertmanager never sent the others (F-044). The reverse happens too: Prometheus re-sends a
  resolved alert for about 15 minutes, so the same resolve can arrive several times, also inside later firing
  Snapshots (F-046).
- When every alert of an Alertmanager group is silenced or inhibited, or a mute time interval applies, Alertmanager sends
  nothing at all — neither firing nor resolved: the notification pipeline stops on an empty set (F-045). An alert that
  resolved meanwhile is still reported if the mute ends within about 14 minutes, while Prometheus keeps re-sending its
  resolve; after a longer mute it never is (F-047). Without extra logic such an Alert Group would stay Firing forever.
- A Snapshot can be temporarily incomplete while an alert still fires: after a restart of an Alertmanager instance,
  with or without a persistent volume — alerts return from the rule evaluators over about two minutes, because
  Prometheus re-sends firing alerts only every 2 minutes at a 1-minute evaluation interval, and a restarted single
  instance listed 3 and then 9 of 10 firing alerts a minute apart (F-041 to F-043) — and whenever `max_alerts`
  truncates the list (`truncatedAlerts > 0`). Truncation keeps the first alerts in Alertmanager's sort order, so as long
  as the Alertmanager group does not change, the same tail is cut off from every Snapshot; `truncatedAlerts` counts
  resolved alerts too, and a resolved alert in the tail is never sent (F-048).
- An Alertmanager cluster, typically an HA pair, sends at least once, not exactly once. Each instance waits one peer
  timeout (15 seconds by default) per position in the cluster — the second instance 15 seconds, a third one 30 — and
  then sends its own copy if it has no record of an earlier instance's success — after a network split, a race, a slow
  or failed delivery, or because its set of alerts differs. Near-duplicate Snapshots of one `groupKey` therefore arrive
  within tens of seconds of each other: a receiver that answered after more than 15 seconds got the second copy exactly
  15 seconds after the first, a failure longer than the peer timeout was delivered by both instances under a second
  apart, and during a split both sent everything at once (F-038 to F-040). The copies are identical when the instances
  share `--web.external-url` (F-037).
- The effective repeat is not `repeat_interval`: a repeat goes out on the first tick of the group, every
  `group_interval`, after `repeat_interval` has passed — steadily 6 minutes for a `repeat_interval` of 5 minutes and a
  `group_interval` of 1 minute (F-033). In an HA pair whose instances tick out of phase, single gaps fall anywhere
  between `repeat_interval` and `repeat_interval + group_interval`, and a reload of Alertmanager can stretch one to
  `repeat_interval + 2 × group_interval` (F-034, F-035). Since v0.32 every Snapshot carries a `notification_reason`, one
  value of which, `repeat interval elapsed`, marks a repeat (F-050).
- When a rule evaluator is down longer than an alert's `endsAt` window, Alertmanager may replace the alert with a new
  one carrying a new `startsAt` without ever sending `resolved` in between.
- `groupKey` encodes the chain of Alertmanager route matchers plus the group labels, so Muster knows through which
  Alertmanager route every Snapshot came — except that sibling routes with identical matchers and `continue: true` share
  one `groupKey` and one record of notifications, and look like one Alertmanager group that repeats at the shorter
  interval (F-049). A reload that changes a route's matchers starts a new `groupKey`, and the old one falls silent for
  good (F-036). Routes may use very different repeat intervals — minutes for paging alerts, a
  day for informational ones. With `continue: true`, one alert can live in several Alertmanager groups.
- Nothing in a webhook proves that the path from the cluster to Muster still works; a quiet wire looks the same as
  "all is well".

Asking Alertmanager directly (polling its API) would give a more precise picture, including Alertmanager silences, but
needs network access from Muster to every Alertmanager, which many topologies do not allow — for example Muster outside
a closed network and Alertmanager inside it.

## Decision

**Position.** Muster sits after Alertmanager and does not replace it: inhibition, deduplication of HA rule evaluators and
Alertmanager silences stay there. In the first release (L1) the only alert source is Alertmanager's webhook, plus one
Heartbeat per Integration; the Connection mode of every Integration is `webhook-only`. A generic inbound webhook for
other sources is deferred to a later layer (L2); any script can already reach Muster through Alertmanager's
`POST /api/v2/alerts`.

**Integration.** One Integration per Alertmanager cluster — typically one per Kubernetes cluster; an HA pair is one
Integration. Each has its own Integration tokens, its own Heartbeat and optional Static labels that are added to every
Alert before routing (if the Alert already has the label, the Alert's value wins and the Timeline records a warning).
Tokens are sent as `Authorization: Bearer` (recommended) or in the path `/api/v1/ingest/<token>` for simple senders;
logs record the route pattern, never the actual path. An Integration may hold several tokens so they can be rotated. An
Integration token allows ingestion and nothing else; it is stored only as a hash and shown once, when it is created
(ADR-0011).

**Recommended configuration.** Together with a new token, the UI shows a ready Alertmanager receiver snippet that
contains it — `send_resolved: true`, `max_alerts: 0`, `http_config.authorization` — and an Alertmanager route for it
with a `repeat_interval` of 5–15 minutes. Because the token is shown only once, a new snippet means adding a new token.
`send_resolved: true` is required: a real new firing is recognised only after a `resolved`. Noise is controlled in
Muster, so long repeat intervals are not needed on the Alertmanager route to Muster. The documentation tells users to
set Alertmanager's `--dispatch.start-delay` to at least twice the longer of the rule evaluation interval and the resend
delay of the rule evaluator, plus a margin — about 2.5 minutes or more with a 1-minute evaluation interval, because
Prometheus re-sends firing alerts only every 2 minutes and 90 seconds still let a partial Snapshot through (F-043) — so
that a restarted instance does not send partial Snapshots, and to give the instances of an HA pair a persistent volume,
so that Alertmanager silences survive a restart (F-042).

**Store first, process later.** The ingestion handler checks the token and the body size, writes the body as a Stored
Snapshot into a partitioned table (ADR-0006) and answers `202` — always well within the 15-second peer timeout, so that
the second instance of an HA pair has no reason to send its own copy (an answer after 16 seconds already brought one,
F-038). The handler never answers `400` and never parses
the body against the API specification: Alertmanager does not retry a `4xx`, so refusing an odd body would lose the
notification. The body is stored as received (as text, with its content type); one that is not JSON, or does not look
like an Alertmanager payload, fails later in processing — the Stored Snapshot is marked failed, a log event is written
and `muster_ingest_failed_snapshots_total` counts it — and `muster ingest replay` reprocesses it after a fix. The only
`5xx` is a failed write, which Alertmanager retries. A worker woken by `LISTEN/NOTIFY` splits the
Snapshot into Alerts by fingerprint, routes and groups them (ADR-0003) and updates Alert Groups (ADR-0004). At most one
Stored Snapshot per Integration is processed at a time, which preserves order within an Integration. Processing is
idempotent per Alert on `fingerprint + status + startsAt`, never on a hash of the whole body, so duplicate Snapshots from
an HA pair are harmless. Bodies above about 16 MB are rejected with `413` and counted; the chart rule
`MusterIngestRejected` (ADR-0014) reports them. Ingestion has no rate limit: a `429` would make Alertmanager retry at
the worst moment. After a bug fix, `muster ingest replay --since <duration>` reprocesses Stored Snapshots.

**Snapshot semantics.** For every pair of Alert and `groupKey`, Muster remembers whether the recent Snapshots of that
`groupKey` listed the Alert.

- An Alert sent as `resolved` is resolved at once. A `resolved` for an Alert that is already resolved, or for an
  earlier firing of it, changes nothing and never reopens anything: it is a re-sent resolve (F-046).
- A Snapshot with `status: resolved` — sent with `notification_reason` `all alerts resolved` — resolves every Alert
  still firing in its Alertmanager group, also those it does not list, because after a restart of an HA pair the list
  can be partial and the rest never comes (F-044). `status` belongs to the whole Alertmanager group; in any other
  Snapshot each Alert's own status decides.
- An Alert missing from **two consecutive Snapshots** of its `groupKey`, and missing for at least
  **`processing.gone_min_absence`** (5 minutes) counted from the first Snapshot that missed it, is **Gone** in that
  Alertmanager group; until both hold, each later Snapshot that misses it decides again. A single missing Snapshot is
  not enough, and neither are two a minute apart: a restarted single instance missed a firing alert in two Snapshots 60
  seconds apart, because Prometheus re-sends firing alerts only every 2 minutes (F-042, F-043). Snapshots of the same
  `groupKey` that arrive **within the duplicate window count as one**, and an Alert listed in either of them counts as
  listed, so a near-duplicate never supplies the second miss. The window is 45 seconds by default — twice Alertmanager's
  default peer timeout plus a margin, so that the copy from the third instance of a cluster falls inside it too; an
  Integration whose Alertmanager uses a longer peer timeout sets a longer window.
- A Snapshot with `truncatedAlerts > 0` is incomplete and never counts as evidence of absence (see Truncation below).
- An Alert seen in several Alertmanager groups is resolved as Gone or Stale only when it is Gone or Stale in **every**
  one of them. The system resolves it with a reason saying that Alertmanager no longer reports it, which covers both a
  resolve without notice and an Alertmanager silence.
- The same fingerprint with a **new `startsAt` and no `resolved` in between is a continuation** as long as the Alert
  is open: Muster updates `startsAt` quietly and adds a Timeline entry; nothing is Loud and nothing reopens. Once the
  Alert itself is resolved — by a `resolved` from Alertmanager, as Gone or as Stale — the next firing of that
  fingerprint is a new firing, whatever its `startsAt` (ADR-0004).
- A change in annotations only re-renders the Root message (no Thread reply) and adds a Timeline entry.
- A resolved Alert with no open Alert Group is applied to the latest Alert Group in which that fingerprint is still
  firing, otherwise dropped and counted. Resolved Alerts never create Alert Groups.

**Staleness.** Each pair of Alert and `groupKey` has a `last_seen_at`, updated by every Snapshot of that `groupKey`
that lists the Alert. An Alert not seen for longer than `stale_after` is **Stale** in that Alertmanager group; once it is
Stale or Gone in all its Alertmanager groups, the system resolves it with a reason saying that Alertmanager stopped
reporting it (silenced or inhibited in Alertmanager, or the Alertmanager routing changed).

- `stale_after` is 3 × the repeat interval **learned per Alertmanager route** (taken from `groupKey`). The interval is
  learned from the gaps between Snapshots whose `notification_reason` marks a repeat; for Alertmanager versions without
  that field, from the gaps between identical Snapshots — the same fingerprints with the same statuses — of any
  Alertmanager group on that route. Near-duplicates within the duplicate window are ignored. The interval is the median
  of the recent gaps, so it reflects the effective repeat rather than the configured one, and single gaps that are
  shorter in an HA pair or longer after a reload do not move it; three learned intervals still cover the longest gap
  measured, `repeat_interval + 2 × group_interval` (F-033 to F-035). Until an interval is learned, a cautious 25 hours
  applies: a route that repeats daily is never staled by mistake, and a 15-minute route is learned within about 20
  minutes. The Integration page lists each Alertmanager route with its learned interval and how quickly Muster can
  resolve by absence, and warns when an interval exceeds one hour.
- **Liveness guard.** An Integration's path counts as alive only while its **Heartbeat is live**; webhooks on their own
  prove nothing about the path. Stale is applied only while the path is alive, and **time without a live Heartbeat does
  not count towards `stale_after`**: the staleness clock of every Alert of the Integration stands still from the last
  Heartbeat signal before the loss until the signal returns. After a long loss of contact the Heartbeat, repeated every
  minute, usually comes back before Alertmanager resends its other Snapshots; because the gap is not counted, the Alerts
  still firing are not resolved as Stale in that moment only to reopen loudly a few minutes later. While the Heartbeat
  is lost, nothing is resolved as Stale and the Integration shows since when Muster has had no contact. An Integration
  **without a Heartbeat** — not configured, or still waiting for its first signal — never resolves Alerts as Stale, and
  the UI says so.
- Time during which Muster itself was down does not count towards `stale_after` either.

**Truncation.** Truncation is tracked per `groupKey`. A `groupKey` becomes truncated with a Snapshot that has
`truncatedAlerts > 0`, and stops being truncated with its first Snapshot that is not truncated, or once no Snapshot of
it has arrived for longer than `stale_after`, counted like staleness. While a `groupKey` is truncated, staleness of its
Alerts that are not listed is decided by whether Snapshots of that Alertmanager group keep arriving, not by each
Alert's `last_seen_at`: as long as they arrive, its known Alerts stay alive. While at least one `groupKey` of an
Integration is truncated, Muster keeps the Internal alert `MusterSnapshotTruncated` raised for the Integration — routed
like any other Alert, telling people to set `max_alerts: 0` on the Alertmanager receiver — and shows a warning on the
Integration; the Internal alert is resolved when no truncated `groupKey` is left. Every truncated Snapshot is counted in
`muster_ingest_truncated_snapshots_total`.

**Heartbeat.** Each Integration has a Heartbeat URL that accepts any GET or POST carrying an Integration token. The
recommended sender is an always-firing alert — `Watchdog` from kube-prometheus-stack, or a `vector(1)` rule — sent by
the cluster's Alertmanager through a dedicated Alertmanager route (with a repeat interval of about one minute) and
Alertmanager receiver. Because it is sent from inside the cluster, its absence means the cluster, the network,
Prometheus or Alertmanager is unavailable. A cron job also works but proves less. The timeout is an Integration setting,
default 5 minutes. When it passes, the Integration is Heartbeat lost and Muster raises the Internal alert
`MusterHeartbeatLost` with the Integration's labels (Static labels included) and `severity=critical`, routed like any
other Alert; the UI suggests a Route for it at the top of the list. When the signal returns, that Internal alert is
resolved. Before the first signal the Integration raises nothing. Like every Internal alert, these belong to Muster's
built-in "Muster" Integration, are resolved explicitly when their condition clears, and are never Gone or Stale
(ADR-0014).

**Clock.** Windows and timers run on Muster's clock at the moment of receipt. `startsAt` and `endsAt` are only displayed
and used to order events from the same source; clocks of different sources are never compared.

**Deferred.** Two more Connection modes come later as one module: `pull` (Muster polls the Alertmanager API) and
`agent` (a small `muster agent` next to Alertmanager polls locally and pushes Snapshots over outbound HTTPS). They would
add visibility of Alertmanager silences and faster resolution. The UI shows each Integration's Connection mode and the
precision it gives. Reading an explicit repeat-interval hint from the `routeLabels` that Alertmanager 0.34 added to the
payload — present in every Snapshot, `{}` when the route sets none, and not part of `groupKey` (F-050) — is not part of
L1.

## Consequences

- Per cluster, users configure an Alertmanager receiver and route for alerts and another pair for the Heartbeat; Muster
  needs no inbound access to Alertmanager.
- Resolution by absence is honest but not instant: for Gone, two Snapshots at least `processing.gone_min_absence`
  apart, which is up to two effective repeat intervals; for Stale, three learned intervals. Explicit `resolved`
  notifications act immediately, and a resolved Alertmanager group does so for all its Alerts.
- A resolve that Alertmanager loses in a restart leaves no Alert firing: it is caught by the group's
  `all alerts resolved`, or by Gone while other Alerts of the group still fire.
- Without a Heartbeat there is no Stale: an Alert Group whose Alertmanager group was silenced as a whole stays Firing
  until a person resolves it. The Heartbeat is therefore part of the recommended setup, not an option.
- A loss of contact never resolves anything by itself: while the Heartbeat is lost, staleness stands still, and once it
  returns Alertmanager has the rest of `stale_after` to report each Alert again.
- Muster cannot tell "silenced in Alertmanager" from "resolved without notice"; the reason says so. A nightly mute
  interval in Alertmanager resolves Alert Groups, and they return as new ones in the morning.
- Learning the interval per Alertmanager route lets informational routes with long repeat intervals share an
  Integration with paging routes. Raising a route's `repeat_interval` sharply can still stale its Alerts once, until
  the new interval is learned.
- A rule evaluator restart that changes `startsAt` without a `resolved` neither pages anyone nor reopens anything.
- Stored Snapshots (14 days) make replay, template dry runs (ADR-0012) and the Group key preview (ADR-0003) possible, at
  the cost of storage (ADR-0006).
- An outage of Muster loses nothing Alertmanager resends; after a restart the truth arrives with the next Snapshots, and
  the UI shows until when the data may be incomplete.

## Alternatives considered

- **Replace Alertmanager** (receive from Prometheus or vmalert directly). Duplicates inhibition, deduplication and
  Alertmanager silences, and forces users to move their configuration.
- **Treat each webhook as a list of independent events** and resolve only on explicit `resolved`. Leaves Alert Groups
  firing forever when Alertmanager stops sending.
- **Resolve on the first absence.** Incomplete Snapshots from HA pairs and restarts would cause false resolutions
  followed by loud Reopens.
- **Gone on two consecutive Snapshots alone.** A restarted Alertmanager missed a firing alert in two Snapshots a minute
  apart; the Alert would be resolved and reopen loudly a minute later.
- **Count the minimum absence from the last Snapshot that listed the Alert.** With repeats every 10 minutes, that
  Snapshot is often older than the minimum when Alertmanager restarts, and the two partial Snapshots after the restart
  would still resolve the Alert.
- **Resolve only the Alerts that `all alerts resolved` lists.** After a restart of an HA pair the list can be partial
  and the rest never comes, so those Alerts would wait for Stale — or, without a Heartbeat, fire forever.
- **Count every Snapshot towards Gone, including near-duplicates from an HA pair.** Two equally incomplete copies sent
  seconds apart would resolve a firing Alert.
- **One expected `repeat_interval` per Integration** for staleness. An Alertmanager route with a long interval for
  informational alerts would be staled falsely and return a day later as a new, loud Alert Group.
- **Accept any recent webhook as proof that the path is alive.** An Integration that sent one webhook and then went
  silent would keep staleness running and resolve Alerts that still fire.
- **Count time without a Heartbeat towards `stale_after` and only hold resolutions while it lasts.** The Heartbeat
  returns before Alertmanager resends its other Snapshots, so after a loss of contact longer than `stale_after` every
  Alert still firing would be resolved as Stale at once and then reopened loudly.
- **Stale by `last_seen_at` for truncated Alertmanager groups.** The truncated tail never arrives, so those Alerts would
  be resolved while they fire.
- **Treat a new `startsAt` without `resolved` as a new firing.** Every long restart of a rule evaluator would page
  people about Alert Groups they already own.
- **Polling Alertmanager as a core component of L1.** More precise, but requires network access to every Alertmanager;
  deferred together with the agent.
- **A rate limit on ingestion.** Alertmanager would retry exactly during a Storm.
- **A generic inbound webhook in L1.** Needs parsing templates, preview, parse-error handling and its own automatic
  resolution (staleness does not apply to event streams); Alertmanager already offers a universal entry point.
