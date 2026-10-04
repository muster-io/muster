---
id: S-059
title: Chart alert rules with rule unit tests and the Grafana dashboard
capability: C-19
kind: infra
layer: L1
depends_on: [S-053, S-058]
covers: [C-19.FR-3, C-19.FR-4, C-19.FR-9, C-19.AC-1, C-19.AC-4, C-19.AC-8, C-21.FR-3, C-21.AC-1]
files_touched:
  - deploy/helm/muster/values.yaml
  - deploy/helm/muster/values.schema.json
  - deploy/helm/muster/ci/all-options-values.yaml
  - deploy/helm/muster/templates/_helpers.tpl
  - deploy/helm/muster/templates/alert-rules.yaml
  - deploy/helm/muster/templates/dashboard.yaml
  - deploy/helm/muster/templates/deployment.yaml
  - deploy/helm/muster/files/rules.yaml
  - deploy/helm/muster/files/dashboard.json
  - deploy/helm/muster/tests/rules/**
  - internal/tools/chartcheck/main.go
  - internal/tools/chartcheck/chartcheck_test.go
  - internal/tools/runbookcheck/main.go
  - Makefile
  - .github/workflows/ci.yml
acceptance:
  - "[C-19.FR-3] With `alerting.prometheusRule.enabled` the chart renders a `PrometheusRule`, with `alerting.vmRule.enabled` a `VMRule`, both holding the twelve rules of reference.md with their severity, `description`, `runbook_url` and default expression; thresholds, windows, `for` and the job selector are chart values; without either flag no rule is rendered."
  - "[C-19.AC-8, C-19.AC-1] `make rules-test` runs `promtool test rules` over the rendered rules: each rule fires on its documented condition — `MusterNoLeader` after 2 minutes of `max(muster_leader) == 0` — and stays silent without it."
  - "[C-19.FR-9, C-19.AC-4, C-21.FR-3, C-21.AC-1] Every rendered rule's `runbook_url` is `alerting.runbookBaseURL`/<the chart's major.minor, or `latest` for development builds>/operations/runbooks/<alertname>/, and `make docs-check` fails unless rendered rules, Internal alerts and runbook pages match one to one; the chart passes `alerting.runbookBaseURL` to Muster as `MUSTER_RUNBOOK_BASE_URL`."
  - "[C-19.FR-4] The chart ships one \"Muster\" dashboard with the uid `muster`, a data source variable and the variables `namespace`, `job`, `integration`, `route` and `destination` with names joined from `*_info`, and the rows Overview, Ingestion, Delivery and Platform; it is rendered as a ConfigMap for the Grafana sidecar with `dashboard.configMap.enabled` and as a `GrafanaDashboard` that references that ConfigMap with `dashboard.grafanaDashboard.enabled`."
  - "[C-19.FR-4] Every query of the dashboard uses only metrics of the catalogue, checked by `make helm-check`."
verify: "make ci helm-check rules-test docs-check"
operator_attention: true
issue: 59
---

# S-059. Chart alert rules with rule unit tests and the Grafana dashboard

## Scope

**IN**

- The chart's alert rules for the Prometheus and VictoriaMetrics operators, their values and their unit tests.
- The "Muster" dashboard as a sidecar ConfigMap and as a `GrafanaDashboard`.
- `runbook_url` of the rules and the base URL passed to Muster; the one-to-one check of rendered rules against the
  runbook pages.

**OUT**

- The documentation of monitoring and the bypass route (S-060).
- Everything of C-19 that runs in the binary (S-053); the System status page (S-054).

## Contracts

- **Origin**: split from S-053 when the contracts were written; it follows S-058 so that the check of rendered rules
  finds the runbook pages.
- **Rules** (C-19.FR-3; `files/rules.yaml`, `templates/alert-rules.yaml`): one group `muster` defined once and
  rendered into a `PrometheusRule` (`monitoring.coreos.com/v1`) and/or a `VMRule` (`operator.victoriametrics.com/v1beta1`)
  with the same `spec.groups`, each behind its flag and with extra labels from values. The twelve rules of the
  [chart rules table](../prd/l1/reference.md#chart-rules) with their default expressions, `for` and severity; each has
  the labels `severity`, the annotations `summary`, `description` (what fired and the first thing to check) and
  `runbook_url`. Values: `alerting.jobSelector` (default `job="muster"`), and per rule under `alerting.rules.<name>`:
  `enabled`, `for`, and its thresholds and windows (`MusterDeliveryFailing.window`, `MusterDeliverySlow.seconds` and
  `.window`, `MusterDeliveryQueueGrowing.size` and `.window`, `MusterIngestBacklog.backlog`, `.delaySeconds` and
  `.window`, `MusterIngestRejected.unauthorized` and `.window`, `MusterTemplateError.window`, `MusterClockSkew.seconds`,
  `MusterLoginFailures.failures` and `.window`), with the defaults of reference.md.
- **Rule unit tests** (C-19.AC-8; `tests/rules/**`, `make rules-test`): `helm template` with the rules enabled, the
  group extracted to a file, `promtool check rules` and `promtool test rules` with one test file per rule: the series
  that make it fire, the time it starts firing (its window and `for`), and a series set that keeps it silent, including
  `MusterNoLeader` after 2 minutes of `muster_leader` 0 on every target (C-19.AC-1) and `MusterIngestRejected` with one
  oversized request but not with ten unauthorized ones.
- **`runbook_url`** (C-19.FR-9; `_helpers.tpl`): `{{ alerting.runbookBaseURL }}/{{ major.minor of appVersion, or
  "latest" for 0.0.0-dev }}/operations/runbooks/<alertname>/`; the Deployment sets `MUSTER_RUNBOOK_BASE_URL` from the same
  value, so Internal alerts of S-053 point into the same site.
- **Dashboard** (C-19.FR-4; `files/dashboard.json`, `templates/dashboard.yaml`): uid `muster`, title "Muster", the
  variables `datasource` (Prometheus type), `namespace`, `job`, `integration`, `route`, `destination` — entity variables
  from `muster_integration_info`, `muster_route_info` and `muster_destination_info` showing names and filtering by id —
  and four rows: **Overview** (open Alert Groups by status, new Alert Groups, time to acknowledge and to resolve, delivery
  p95 against 5 seconds, active Storms), **Ingestion** (requests by Integration and outcome, processing delay, backlog,
  resolutions by reason, Heartbeat lost), **Delivery** (attempts by Destination and outcome, queues, Broken Destinations,
  messenger API latency by client class), **Platform** (Leader, API requests and latency, sign-in failures, template
  errors and rendering time, clock skew, database pool, memory and goroutines). Rendered as a ConfigMap with the sidecar
  label from `dashboard.configMap.labels` (default `grafana_dashboard: "1"`) when `dashboard.configMap.enabled`, and as a
  `GrafanaDashboard` (`grafana.integreatly.org/v1beta1`) with `configMapRef` to that ConfigMap, `instanceSelector` and
  `folder` from values when `dashboard.grafanaDashboard.enabled`, which fails to render without the ConfigMap.
- **Checks** (`internal/tools/chartcheck`, in `make helm-check`): the rendered rule names equal the chart rules table of
  reference.md; each rule has a severity label, a `description` and a `runbook_url` of the form above; the dashboard has
  the uid, the variables and the four rows, and every metric name in its queries is in the metric registry (S-053).
  `make helm-check` validates the rendered CRD resources with kubeconform and their published schemas.
- **Runbook check** (C-21.FR-3, C-19.AC-4; `internal/tools/runbookcheck`): the rule names and the paths of the
  `runbook_url`s rendered with the all-options values join the one-to-one check of S-058.
- **Defaults**: `alerting.runbookBaseURL`, "Chart rule thresholds, windows and job selector".

## Steps

1. Write the rule group with its values and the two resources. Check: `helm template` renders twelve rules in each and
   none without the flags; `chartcheck` passes.
2. Write one unit test per rule. Check: `make rules-test` passes; changing a threshold value makes the matching test
   fail.
3. Write the dashboard and its two deliveries. Check: `chartcheck` passes; kubeconform validates both.
4. Pass the base URL to Muster and extend the runbook check. Check: Verification below.

## Verification

```sh
helm template m deploy/helm/muster --set existingSecret=s > /tmp/def.yaml
yq 'select(.kind == "PrometheusRule" or .kind == "VMRule") | .kind' /tmp/def.yaml | wc -l   # 0
helm template m deploy/helm/muster --set existingSecret=s -f deploy/helm/muster/ci/all-options-values.yaml > /tmp/all.yaml
yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[].alert' /tmp/all.yaml | wc -l   # 12
yq 'select(.kind == "VMRule") | .spec.groups[].rules[].alert' /tmp/all.yaml | wc -l          # 12
yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "MusterNoLeader") | [.expr, .for, .labels.severity, .annotations.runbook_url] | join(" | ")' /tmp/all.yaml
# max(muster_leader{job="muster"}) == 0 | 2m | critical | https://muster-io.github.io/muster/<major.minor>/operations/runbooks/MusterNoLeader/
helm template m deploy/helm/muster --set existingSecret=s -f deploy/helm/muster/ci/all-options-values.yaml \
  --set alerting.rules.MusterClockSkew.seconds=5 | yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "MusterClockSkew") | .expr'
# max by (instance) (abs(muster_clock_skew_seconds{job="muster"})) > 5
yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "MUSTER_RUNBOOK_BASE_URL") | .value' /tmp/all.yaml
# https://muster-io.github.io/muster

# C-19.AC-8, AC-1: rule unit tests
make rules-test 2>&1 | grep -c 'SUCCESS'                    # 12

# C-19.FR-4: the dashboard and its two deliveries
yq 'select(.kind == "ConfigMap" and .metadata.name == "m-muster-dashboard") | .data["muster.json"]' /tmp/all.yaml \
  | jq -c '{uid, title, v: [.templating.list[].name], rows: [.panels[] | select(.type == "row") | .title]}'
# {"uid":"muster","title":"Muster","v":["datasource","namespace","job","integration","route","destination"],"rows":["Overview","Ingestion","Delivery","Platform"]}
yq 'select(.kind == "GrafanaDashboard") | .spec.configMapRef.name' /tmp/all.yaml   # m-muster-dashboard
helm template m deploy/helm/muster --set existingSecret=s --set dashboard.grafanaDashboard.enabled=true 2>&1 | grep -c 'dashboard.configMap.enabled'   # 1
make helm-check; echo "exit=$?"                              # exit=0

# C-19.AC-4, C-21.FR-3: rendered rules, Internal alerts and pages match one to one
make docs-check; echo "exit=$?"                              # exit=0
```

**Optional manual check in a test cluster** (operator): install the chart with the Prometheus or VictoriaMetrics operator
and the Grafana operator present and both flags on; see the twelve rules loaded, the dashboard imported with its
variables filled from a running Muster, and `MusterNoLeader` firing two minutes after scaling Muster to zero.

## Open questions

None.

## Notes

- Suggested commit: `feat(chart): add alert rules with unit tests and the grafana dashboard`.
- The expressions use only metrics of the catalogue, so a renamed metric fails `chartcheck` before it reaches a
  cluster.
- `operator_attention`: the optional check in a test cluster.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-19.FR-3 | partial | the rules in the chart; the documentation of the bypass route is S-060 |
| C-19.FR-4 | full | |
| C-19.FR-9 | full | together with S-053 and S-058 |
| C-19.AC-1 | full | together with S-053: `MusterNoLeader` by the rule test |
| C-19.AC-4 | full | together with S-058 |
| C-19.AC-8 | full | |
| C-21.FR-3 | full | together with S-058: rendered rules join the one-to-one check |
| C-21.AC-1 | full | together with S-057 and S-058 |
