# C-07. Heartbeat

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-05, C-06

**Goal.** Prove, per Integration, that the path from the cluster to Muster works; call people when it stops; and let
staleness run only while it works.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The Admin turns on the Heartbeat of "prod-eu", copies the Heartbeat URL and the snippet for an always-firing
   `Watchdog` alert into Alertmanager, and watches the state change from "waiting for the first signal" to "live".
2. The cluster's network fails. After the Heartbeat timeout the Integration is Heartbeat lost; `MusterHeartbeatLost`
   with `severity=critical` is raised (and, once Routes and Destinations exist, reaches the ops channel); staleness
   stops; the Integration shows "No contact with Alertmanager since 14:03".
3. The network returns; Muster resolves the Internal alert.
4. An Integration without a Heartbeat shows "Muster will not notice when Alertmanager goes quiet, and Stale resolution
   is off".

## Functional requirements

- **C-07.FR-1** Each Integration has a Heartbeat URL on the ingest listener that accepts any GET or POST carrying one of
  the Integration's tokens, as a Bearer header or in the path; the body is ignored. While the Integration's Heartbeat is
  off, a signal is answered `204` and nothing is recorded, so turning the Heartbeat on later starts from "waiting".
- **C-07.FR-2** The Heartbeat is off by default (`integration.heartbeat`); its timeout is an Integration setting
  (`integration.heartbeat_timeout`).
- **C-07.FR-3** The Heartbeat state is one of: not configured; waiting for the first signal; live; lost. The first
  signal moves "waiting" to "live" without raising anything.
- **C-07.FR-4** When no signal arrives within the timeout, the Integration becomes Heartbeat lost and Muster raises the
  Internal alert `MusterHeartbeatLost` (C-06.FR-14) with the labels `alertname`, `severity=critical`,
  `integration=<id>`, `integration_name=<name>` and the Integration's Static labels. When the signal returns, Muster
  resolves it. After Muster itself was down (C-02.FR-12), the timeout is measured from the later of the last signal and
  the moment the Leader started leading again, so that Muster's own downtime does not make every Integration Heartbeat
  lost.
- **C-07.FR-5** While the Heartbeat is lost, staleness is paused as in C-06.FR-9, and the Integration shows since when
  Muster has had no contact.
- **C-07.FR-6** The snippet contains an always-firing rule (`Watchdog` from kube-prometheus-stack, or a `vector(1)`
  rule), a dedicated Alertmanager route with `repeat_interval` set to `snippet.heartbeat_repeat_interval`, and an
  Alertmanager receiver pointing at the Heartbeat URL. The documentation says that a cron job also works but proves
  less.
- **C-07.FR-8** The Leader runs the Heartbeat checks. `muster_heartbeat_lost{integration}` exposes the state; the chart
  rule `MusterHeartbeatLost` (C-19) is the backup when Muster cannot deliver.

## UI

Heartbeat section of the Integration form (on/off, timeout, URL, snippet); state badge in the Integrations list and on
the Integration page; the Heartbeat banners of [reference.md](reference.md#banners-warnings-and-notices).

## API surface

Heartbeat settings and state as part of `integrations`; the Heartbeat endpoint on the ingest listener.

## Acceptance

Checked with the fake Alertmanager and a virtual clock.

- **C-07.AC-1** After the first signal the state is live; `integration.heartbeat_timeout` without a signal raises
  `MusterHeartbeatLost` with the Integration's Static labels, visible as a firing Alert of the built-in Integration; the
  next signal resolves it.
- **C-07.AC-2** Before the first signal nothing is raised, and Alerts of the Integration never go Stale.
- **C-07.AC-3** The Heartbeat URL accepts `GET` and `POST` with a valid token and refuses a wrong token with `401`.
- **C-07.AC-4** With a live Heartbeat and a learned 5-minute interval, an Alert whose Alertmanager group last arrived at
  T resolves as Stale at T+15 min. If, in addition, no Heartbeat signal arrives from T to T+10 min — the Integration is
  Heartbeat lost from T+5 min until the signal returns at T+10 min — it resolves as Stale at T+25 min. Without a
  Heartbeat it never resolves as Stale.
- **C-07.AC-5** While the Heartbeat is lost, the Integration page shows "No contact with Alertmanager since HH:MM", and
  deleting the Integration resolves its `MusterHeartbeatLost`.

## Related ADRs

ADR-0002, ADR-0007, ADR-0014.

## Depends on

C-05 — Integrations and tokens; C-06 — staleness bookkeeping and the Internal alert mechanism.

## Suggested story split

- **BE** — Heartbeat endpoint, state machine, Leader checks, Internal alert, metric, staleness pause.
- **FE** — Heartbeat section, badges, banners.
