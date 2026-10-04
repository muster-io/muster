# C-05. Integrations

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-02, C-03

**Goal.** An Admin connects an Alertmanager cluster in minutes — an Integration with tokens, a ready Alertmanager
receiver snippet and optional Static labels — behind an ingestion endpoint that never loses a webhook it accepted.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The Admin creates the Integration "prod-eu", adds the Static label `cluster=prod-eu` and creates a token. The UI
   shows, once, an Alertmanager receiver and route snippet containing the token; the Admin pastes it into the
   Alertmanager configuration.
2. Snapshots arrive. The Integration page shows when the last one came, how many arrived and the recent Stored
   Snapshots, each viewable as received.
3. Token rotation: the Admin adds a second token (with a new snippet), switches Alertmanager and revokes the first.
4. The Admin deletes an old Integration; its tokens stop working at once.
5. A misconfigured sender posts 40 MB; it gets `413`, and the request is counted for the chart rule
   `MusterIngestRejected` (C-19).

## Functional requirements

- **C-05.FR-1** An Integration has a name unique in the Organization, a description, a Connection mode, optional Static
  labels, a duplicate window (`integration.duplicate_window`), Heartbeat settings (C-07) and one or more Integration
  tokens. In L1 the only Connection mode is `webhook-only` (`integration.connection_mode`; the API value is
  `webhook_only`); the UI shows it with the precision it gives — explicit `resolved` immediately, Gone after up to two
  repeat intervals but never sooner than `processing.gone_min_absence`, Stale after three learned intervals.
- **C-05.FR-2** An Integration token allows ingestion and Heartbeat and nothing else; it is stored as a hash and shown
  once, at creation, together with the snippet. A new snippet means a new token. An Integration may hold several
  tokens; each can be revoked.
- **C-05.FR-3** The receiving endpoint is on the ingest listener and accepts the token as `Authorization: Bearer`
  (recommended) or in the path `/api/v1/ingest/<token>`. Logs record the route pattern, never the path with the token.
  It checks the token and then the body size, stores the body as a Stored Snapshot exactly as received and answers `202`
  after the write commits, without waiting for processing (ADR-0002). It never answers `400`: the body is not parsed
  against the API specification, which excludes ingestion from request validation.
- **C-05.FR-4** A body larger than `ingest.body_limit` gets `413`; a missing or wrong token gets `401`. Every request is
  counted in `muster_ingest_requests_total{integration,outcome}` with the outcome `accepted`, `unauthorized` or
  `too_large`; a request whose token matches no Integration carries `integration="unknown"` (_provisional_, P-10).
  Neither refusal raises an Internal alert; the chart rule `MusterIngestRejected` reports them (C-19). Ingestion has no
  rate limit. A body that is not JSON, or does not look like an Alertmanager payload, is accepted and stored; it fails
  later as a processing error (C-06.FR-20), because Alertmanager does not retry a `4xx` and the notification would be
  lost. The only `5xx` is a failed write, which Alertmanager retries.
- **C-05.FR-5** The snippet contains an Alertmanager receiver with `webhook_configs` pointing at `MUSTER_INGEST_URL`,
  `send_resolved: true`, `max_alerts: 0` and `http_config.authorization`, and an Alertmanager child route for it with
  `continue: true`, to be placed first, so that it runs beside the existing receiver, and `repeat_interval` set to
  `snippet.repeat_interval`; the text explains the recommended range and that noise is controlled in Muster, not by long
  repeat intervals.
- **C-05.FR-6** Static labels are stored on the Integration; processing adds them to every Alert before routing
  (C-06.FR-3).
- **C-05.FR-7** The Integration page shows the Connection mode and its precision, the time of the last Snapshot, the
  number received, and the recent Stored Snapshots, each viewable as received (the raw text) with its processing state
  — pending, processed or failed with the error. Later capabilities add the Heartbeat
  state (C-07), the learned Alertmanager routes, truncation warnings and the Alerts view (C-06).
- **C-05.FR-8** Deleting an Integration is soft: its tokens stop working immediately, it leaves the Integrations list,
  and its Stored Snapshots stay until retention. What happens to its Alerts is C-06.FR-16; to its Alert Groups,
  C-09.FR-3.
- **C-05.FR-10** Stored Snapshots are kept for `retention.stored_snapshots`.
- **C-05.FR-11** The documentation describes one Integration per Alertmanager cluster (an HA pair is one Integration),
  the receiver and route configuration, Alertmanager's `--dispatch.start-delay` and, for HA pairs, a persistent volume
  for Alertmanager's data, so that its silences survive a restart ([F-042](../../facts.md#restarts-and-resolves)). The
  start delay must be at least twice the longer of the rule evaluation interval and the resend delay of the rule
  evaluator, plus a margin: about 2.5 minutes or more with a 1-minute evaluation interval, because Prometheus re-sends
  firing alerts only every 2 minutes and 90 seconds still let a partial Snapshot through
  ([F-043](../../facts.md#restarts-and-resolves)); a shorter delay lets a restarted Alertmanager send partial
  Snapshots.
- **C-05.FR-12** Each Integration exports `muster_integration_info{integration,name}`.

## UI

Integrations list (name, last Snapshot; later the Heartbeat state and warnings); create and edit form (name,
description, Static labels, duplicate window; later Heartbeat); token dialog showing the token and snippet once; token
list with revoke; Stored Snapshot list and viewer; delete dialog.

## API surface

`integrations` (list, create, read, update, delete); `integrations/{id}/tokens` (list, create, revoke);
`stored-snapshots` (list by Integration, time and processing state; read — the raw body, its content type, the processing
state and error); the ingestion endpoint on the ingest listener.

## Acceptance

- **C-05.AC-1** A webhook from the fake Alertmanager with a valid token gets `202` and appears as a Stored Snapshot; the
  same request without a token gets `401` and is counted.
- **C-05.AC-2** A body larger than `ingest.body_limit` gets `413` and is counted with `outcome="too_large"`.
- **C-05.AC-3** After deleting an Integration, its token gets `401` on the next request, and the Integration no longer
  appears in the list.
- **C-05.AC-5** No log line contains a token taken from a request path.
- **C-05.AC-6** The token dialog shows the token and the snippet once; the snippet contains `send_resolved: true`,
  `max_alerts: 0`, `MUSTER_INGEST_URL` and the token; listing tokens never returns a value.
- **C-05.AC-7** A request with a token that matches no Integration is counted with `integration="unknown"` and
  `outcome="unauthorized"`.
- **C-05.AC-8** A request with a valid token and the body `not json` gets `202`, the Stored Snapshot holds exactly those
  bytes, and the Snapshot ends in the state `failed` (C-06.AC-12); no `400` is ever returned by ingestion.

## Related ADRs

ADR-0002, ADR-0003, ADR-0006, ADR-0011.

## Depends on

C-02 — runtime, ingest listener, metrics; C-03 — sign-in, Permissions and the Audit log for configuration changes.

## Suggested story split

- **BE** — Integration model and tokens, ingestion endpoint, Stored Snapshots, snippet, metrics.
- **FE** — Integrations list, form, token dialog, Stored Snapshot viewer, delete dialog.
