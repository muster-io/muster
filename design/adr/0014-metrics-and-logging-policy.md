# 0014. Observability: metric labels, histograms, Internal alerts, an outgoing heartbeat and a closed log catalogue

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — Internal alerts carry entities by id with a separate name label; closed label sets listed in
  the catalogue; dropped `resolved` notifications and template rendering time in the catalogue; Snapshots that failed
  processing

## Context

Muster is part of the alerting path; when it fails silently, nobody is paged. Its metrics, logs and alert rules ship to
users with the Helm chart and must work with both Prometheus and VictoriaMetrics. The two share the scrape format but not
histograms: VictoriaMetrics' own `vmrange` histograms are not understood by Prometheus' `histogram_quantile`.

Metric labels decide cardinality. In a self-hosted installation the number of Integrations, Routes and Destinations is
set by configuration — tens — while alert labels, users and Alert Group numbers grow with traffic. At the same time,
operators need to see in their own monitoring which Integration went quiet or which Destination is broken.

Some problems Muster can report through its own pipeline, to the people who already watch it; others — Muster down, or
the Destination that would carry the report broken — need a path that bypasses Muster. And when the whole cluster that
runs Muster and its monitoring goes down, nothing inside it can report anything.

Logs during a Storm can explode if every Alert produces a line, and a log vocabulary that grows with whoever writes the
code turns into noise nobody reads.

## Decision

**Metric labels.**

- Allowed: configuration entities — `integration`, `route`, `destination` — always by **immutable id**, never by name, so
  renaming breaks no series. Names come from info metrics (`muster_route_info{route, name}`, `muster_integration_info`,
  `muster_destination_info`) joined in PromQL.
- Never: alert labels (`alertname`, `severity`, …), Alert Group numbers, users, `org_id`. Analysis by alert content
  belongs in the Alert Group list in the UI.
- A future hosted mode that serves several Organizations turns the entity labels off with a switch.
- All other labels take values from closed sets defined in code (`outcome`, `reason`, `command`, `transport`, `kind`,
  …); the catalogue in the product requirements lists each set, and adding a value is a reviewed change like adding a
  metric. The `transport` label names how a command arrived — `ui`, `api`, `mattermost`, `telegram` — or `system` for
  every transition Muster starts itself: on a timer, or because of ingestion, such as a Reopen, a resolve by the system
  or a rise to Urgent.

**Histograms.** Metrics use the VictoriaMetrics/metrics library with **Prometheus-compatible `le` histograms only**,
with buckets chosen for each kind of latency (ingestion, delivery, messenger APIs, template rendering). An architecture
lint forbids the library's `vmrange` histogram constructor (ADR-0016). There is no switch between formats, so one set
of chart rules and one dashboard work everywhere.

**Pull only.** Metrics are scraped from `/metrics` on the internal listener, next to the health endpoints and not
exposed through the ingress. Muster does not push; installations that need push run an agent next to Muster.

**Database gauges only from the Leader.** Gauges computed from the database (open Alert Groups by status, queue depths,
Broken Destinations) are exported only by the Leader (ADR-0007), which `muster_leader` identifies. Other replicas omit
them, so sums are not multiplied by the number of replicas.

**Catalogue.** The first-release catalogue covers:

- ingestion — requests by Integration and outcome, processing delay, backlog, truncated Snapshots by Integration
  (`muster_ingest_truncated_snapshots_total`), Alerts resolved by reason (`resolved`, `gone`, `stale`,
  `integration_deleted`), `resolved` notifications dropped because their fingerprint fires nowhere
  (`muster_ingest_resolved_dropped_total`), Stored Snapshots that failed processing
  (`muster_ingest_failed_snapshots_total`), Heartbeat lost;
- Alert Groups and commands — open Alert Groups by status; created, reopened and resolved (by whom); commands by
  transport and outcome; time to acknowledge and time to resolve as histograms from 1 minute to 24 hours;
- delivery — attempts by Destination, kind (Publication, update, Thread reply, Storm summary, final edit, outgoing
  webhook event) and outcome (delivered or the error class); `muster_delivery_latency_seconds`, the delivery
  service-level indicator with an objective of p95 ≤ 5 s; queue length; Broken; Storm active by Route;
- platform — outbound HTTP clients (ADR-0015), template errors and template rendering time by kind of template
  (`muster_template_render_duration_seconds`), API requests by route pattern, sign-in failures, clock skew,
  `muster_build_info`, database pool, process and Go runtime.

The reference page of all metrics is generated from the registry in code, and CI checks that it is current.

**Chart rules and dashboard.** The chart ships alert rules, rendered only when the Prometheus or VictoriaMetrics CRDs
are present. Critical: `MusterDown`, `MusterNoLeader` (the Leader's tasks — Telegram polling, the Heartbeat lost and
Stale checks, partition maintenance, the outgoing heartbeat — have stopped), `MusterDeliveryFailing`,
`MusterDestinationBroken`, `MusterHeartbeatLost`. Warning: `MusterDeliverySlow`, `MusterDeliveryQueueGrowing`,
`MusterIngestBacklog`, `MusterIngestRejected` (an oversized ingestion request, or more unauthorized ones than a
threshold, so that a single scanner request does not fire it), `MusterTemplateError`, `MusterClockSkew`,
`MusterLoginFailures`. Every rule has a `description` and a `runbook_url` into the versioned documentation; thresholds
are chart values; the documentation tells users to send critical rules through an Alertmanager route that bypasses
Muster. One "Muster" dashboard with Overview, Ingestion, Delivery and Platform rows ships as a
ConfigMap for the Grafana sidecar and as a `GrafanaDashboard` resource, each behind its own flag.

**Internal alerts.** Problems Muster can still report through its own pipeline are raised as Internal alerts of the
built-in "Muster" Integration and routed by ordinary Routes; the UI suggests a Route for them to an admin Destination.
The set is **closed**: like log events, every Internal alert is declared in a registry in code with its name, labels,
condition and runbook page; the reference page is generated from the registry, and adding an Internal alert is a
reviewed change to it. The first-release registry starts with `MusterHeartbeatLost`, `MusterDestinationBroken`,
`MusterTemplateError`, `MusterSnapshotTruncated` and `MusterOIDCSecretExpiring`, a warning raised 14 days before the
expiry date that an Admin may enter for the OIDC client secret. Internal alerts are deduplicated by fingerprint,
resolved explicitly when their condition clears, and never Gone or Stale. Like metrics, they name configuration entities
by immutable id (`integration`, `route`, `destination`), with the current name in a separate label (`integration_name`,
…) so that Routes can match either; their fingerprint is computed from `alertname` and the id labels only, so renaming
an entity updates the open Internal alert instead of resolving it and raising a new one. **One condition has one name:**
an Internal alert and a chart rule for the same condition share the name and the runbook page. A Broken Destination
cannot report about itself, which is why the chart rules and the warnings in the UI remain.

**Outgoing heartbeat.** Muster sends its own heartbeat once a minute to an external dead man's switch — any URL that
alerts when the signal stops, such as a healthchecks.io check or an Uptime Kuma push monitor. The URL is an Organization
setting, stored encrypted (ADR-0011) because such URLs usually carry a token; without one nothing is sent, and the UI
suggests setting it up. Only the Leader sends it (ADR-0007), so the signal stops when Muster is down, when no replica
leads (a Leader that loses the database stops leading) and when the whole cluster is gone: the cases that the chart
rules, running in the same place, cannot report. The request goes through the outbound HTTP package with a client class
of its own (ADR-0015); a failed send is counted and logged, never queued: the next minute's send is the retry.

**Logs.** Structured lines on stdout, written only through the domain logger (enforced by an architecture lint,
ADR-0016). The set of log events is **closed**: every event is declared in a registry in code with its level and
fields, the logger accepts only registered events, a reference page is generated from the registry, and CI fails if
the page is out of date or an unregistered event is used. Adding an event is a reviewed change to the registry, not an
ad-hoc log line. The first-release registry starts with these groups:

- ingestion — accepted and stored, rejected, processed, failed, replayed;
- routing — one line per processed Snapshot with the Routes and Alert Groups its Alerts went to, never a line per Alert;
  Fallback template used;
- Alert Groups — every status change with its reason, Takeover, `startsAt` continuation;
- commands — who, what, through which Transport and with what result, refusals included;
- delivery — an attempt and its outcome by error class, Destination Broken and recovered, possible duplicate
  Publication;
- background work — leadership acquired and lost, timers fired (collapsed ones included), outgoing heartbeat failed;
- modes — Storm, recovery after downtime, clock skew;
- security — sign-in, refusal, token issued and revoked (a copy of the Audit log entry);
- lifecycle — start with version, migrations, shutdown.

Levels are chosen by the reader: **ERROR** — someone must look within the hour; **WARN** — needed for an investigation;
**INFO** — an investigation is impossible without it. SIGTERM is logged at WARN. Every line is self-contained
(`group=#412`, `route=…`, `integration=…`). Secrets never reach logs (ADR-0011).

## Consequences

- Users see in their own monitoring which Integration is quiet or which Destination is broken, without exposing alert
  content or people.
- Cardinality is bounded by the size of the configuration.
- Joining names through `*_info` makes PromQL a little longer; the shipped dashboard does it for users.
- Native VictoriaMetrics histograms are not used, so `le` buckets have to be chosen with care for each metric.
- One name per condition means one runbook page, whether the warning came from Muster itself or from the chart rule.
- A new log event costs a registry entry and a review, and the generated page always shows the full list. That keeps
  logs readable during Storms, at the price of slower ad-hoc logging.
- Without a Leader the database gauges disappear — which is exactly the situation `MusterNoLeader` alerts on.
- If Muster, Prometheus and Alertmanager go down together, the external dead man's switch still notices — provided the
  user has set one up.

## Alternatives considered

- **Entity names as label values.** Readable, but renaming breaks series continuity.
- **Alert labels such as `alertname` or `severity` in metrics.** Cardinality driven by traffic; that analysis belongs
  in the UI.
- **`vmrange` histograms, or a switch between formats.** Prometheus users would lose quantiles, or every rule and
  dashboard would have to exist twice.
- **Pushing metrics.** The library can push, but an agent next to Muster does the same with no extra code in Muster.
- **Every replica exporting database gauges, with rules taking `max`.** Easy to get wrong in sums; Leader-only is
  simpler.
- **Different names for an Internal alert and the chart rule about the same condition.** Two runbook pages and two
  words for one problem.
- **An open log vocabulary controlled only by code review.** Drifts into noise, and Storms amplify it.
- **Chart rules alone for Muster's own health.** They run in the same cluster and go down with it; only a signal
  checked outside can notice that everything stopped.
- **Keeping the list of log events in this ADR.** Every new event would change an accepted decision; the rule belongs
  here, the list belongs next to the code that enforces it.
