# C-19. Self-observation

[L1 index](../L1.md) · Stage: Operations · UI: yes · Depends on: C-03, C-06, C-08, C-11, C-12, C-15

**Goal.** Muster reports its own health — chart alert rules, a dashboard, the complete set of Internal alerts, an
outgoing heartbeat and a read-only System status page — so that its failure never goes unnoticed. The registries for
log events and metrics are C-02; this capability completes the L1 catalogue and the tooling around it.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The platform operator enables the chart's alert rules and dashboard; critical rules go through an Alertmanager route
   that bypasses Muster.
2. The Admin sets the outgoing heartbeat URL to a healthchecks.io check; when the whole cluster goes down, the external
   check alerts.
3. The Routes page suggests a Route sending `alertname=~"Muster.*"` to an admin Destination (C-08.FR-11); template
   errors and Broken Destinations arrive there.
4. An alert links to its runbook page for the installed version.
5. The Admin opens the System status page and sees which replica leads, which Destinations are Broken and which
   Integrations have lost their Heartbeat.
6. The OIDC client secret expires in 12 days; `MusterOIDCSecretExpiring` reaches the admin Destination.

## Functional requirements

- **C-19.FR-2** Every metric of the [L1 catalogue](reference.md#metrics-catalogue) exists and the generated reference
  page lists it; CI checks the catalogue, the registry and the page against each other.
- **C-19.FR-3** The chart ships the alert rules of [reference.md](reference.md#chart-rules), rendered only when the
  Prometheus or VictoriaMetrics CRDs are enabled, each with a `description`, a `runbook_url` and its default expression;
  thresholds and the job selector are chart values. The documentation tells users to send the critical rules through an
  Alertmanager route that bypasses Muster.
- **C-19.FR-4** The chart ships one "Muster" dashboard with a fixed uid, a data source variable and the variables
  `namespace`/`job`, `integration`, `route` and `destination` (names joined from `*_info`), and four rows: Overview
  (open Alert Groups by status, new Alert Groups, time to acknowledge and to resolve, delivery p95 against 5 seconds,
  Storms), Ingestion (requests by Integration and outcome, processing delay, backlog, resolutions by reason,
  Heartbeats), Delivery (attempts by Destination and class, queues, Broken, messenger API latency) and Platform (Leader,
  API, sign-in, templates, clock, database pool, memory and goroutines). It is delivered as a ConfigMap for the Grafana
  sidecar and as a `GrafanaDashboard` resource that references the same ConfigMap, each behind its own flag.
- **C-19.FR-5** The L1 Internal alerts are the [registry in reference.md](reference.md#internal-alerts); each is raised
  by its capability through the mechanism of C-06.FR-14. This capability adds `MusterOIDCSecretExpiring`: the Leader
  raises it while the OIDC client secret expiry date (C-03.FR-5) is less than `oidc.secret_expiry_lead` away, and
  resolves it when the date is moved further out or removed.
- **C-19.FR-7** The outgoing heartbeat is an Organization setting: a URL (encrypted, because such URLs carry tokens) and
  a proxy (C-03.FR-19). Only the Leader sends it, once per `outgoing_heartbeat.interval`, with the method
  `outgoing_heartbeat.method` and the timeout `outgoing_heartbeat.timeout`; a failure is counted and logged and the next
  tick is the retry.
  Without a URL nothing is sent and the System status page suggests setting one up.
- **C-19.FR-9** Every chart rule and Internal alert has a `runbook_url` built from a base URL, the `major.minor`
  version (`latest` for development builds) and `operations/runbooks/<alertname>`: chart rules take the chart value
  `alerting.runbookBaseURL` and the chart's version; Internal alerts take `MUSTER_RUNBOOK_BASE_URL`, which the chart
  sets from the same value, and the binary's version. CI checks that each has a page (C-21).
- **C-19.FR-10** The System status page is for Admins and only reads; it offers no actions, only links to the entities
  it lists. It shows the Leader and every live replica with the key ids it holds; the recovery state after downtime;
  delivery queues per Destination; Broken Destinations with their reason; Integrations with Heartbeat lost or truncated
  Snapshots; Routes and Destinations with template errors; active Storms; and the last result of the outgoing heartbeat.

## UI

System status page; Organization → Outgoing heartbeat (URL, proxy form, last result).

## API surface

`system-status` (read, Admins); outgoing heartbeat fields on `organization`; `/metrics` on the internal listener.

## Acceptance

- **C-19.AC-1** With no Leader for 2 minutes in a test cluster, `MusterNoLeader` fires and the outgoing heartbeat stops.
- **C-19.AC-2** A Broken Destination raises `MusterDestinationBroken`, which reaches another Destination on the
  suggested Route.
- **C-19.AC-3** No metric series carries an `alertname`, user or Alert Group number label.
- **C-19.AC-4** Every rule rendered by `helm template` with rules enabled has a `runbook_url` whose page exists in the
  documentation source.
- **C-19.AC-5** With an OIDC client secret expiry date 10 days ahead, Muster raises `MusterOIDCSecretExpiring` with
  severity warning; moving the date 30 days ahead resolves it.
- **C-19.AC-6** The System status page shows a Broken Destination and a Heartbeat-lost Integration and offers no
  actions; a Responder gets `403` from `system-status`.
- **C-19.AC-7** Only the Leader sends the outgoing heartbeat to the fake dead man's switch, once per interval; without a
  URL nothing is sent.
- **C-19.AC-8** A rule unit test (for example `promtool test rules`) shows each chart rule firing on its documented
  condition and staying silent without it.

## Related ADRs

ADR-0007, ADR-0011, ADR-0014, ADR-0015.

## Depends on

C-03 — OIDC settings and the proxy form; C-06 — Internal alert mechanism; C-08 — Routes for Internal alerts; C-11 —
delivery metrics and Broken Destinations; C-12 and C-15 — template errors of Routes and outgoing webhooks, shown on the
System status page and in the dashboard.

## Suggested story split

- **BE** — chart rules and dashboard, `MusterOIDCSecretExpiring`, outgoing heartbeat, `system-status`, catalogue
  checks.
- **FE** — System status page, outgoing heartbeat settings.
