---
id: S-059
title: Chart alert rules with rule unit tests and the Grafana dashboard
capability: C-19
kind: infra
layer: L1
depends_on: [S-053, S-058]
covers: [C-19.FR-3, C-19.FR-4, C-19.FR-9, C-19.AC-1, C-19.AC-4, C-19.AC-8, C-21.FR-3, C-21.AC-1]
files_touched:
  - deploy/helm/muster/.helmignore
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
  - internal/tools/runbookcheck/runbookcheck_test.go
  - go.mod
  - Makefile
  - .github/workflows/ci.yml
  - AGENTS.md
  - CONTRIBUTING.md
acceptance:
  - "[C-19.FR-3] With `alerting.prometheusRule.enabled` the chart renders a `PrometheusRule`, with `alerting.vmRule.enabled` a `VMRule`, both holding the twelve rules of reference.md with their severity, `description`, `runbook_url` and default expression; thresholds, windows, `for` and the job selector are chart values; without either flag no rule is rendered."
  - "[C-19.AC-8, C-19.AC-1] `make rules-test` runs `promtool test rules` over the rendered rules: each rule fires on its documented condition — `MusterNoLeader` after 2 minutes of `max(muster_leader) == 0` — and stays silent without it."
  - "[C-19.FR-9, C-19.AC-4, C-21.FR-3, C-21.AC-1] Every rendered rule's `runbook_url` is `alerting.runbookBaseURL`/<the major.minor of the chart's `appVersion`, or `latest` for development builds>/operations/runbooks/<alertname>/, and `make docs-check` fails unless rendered rules, Internal alerts and runbook pages match one to one; the chart passes `alerting.runbookBaseURL` to Muster as `MUSTER_RUNBOOK_BASE_URL`."
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
  `runbook_url`. The job selector is part of every series selector of every rule — `up{job="muster"}`,
  `muster_delivery_queue{job="muster"}`, `muster_delivery_attempts_total{job="muster",outcome="delivered"}` — as the
  updated table of reference.md shows, so that another job's metrics of the same names never fire Muster's rules.
  Values: `alerting.jobSelector` (default `job="<muster.name>"`, the scrape job's name, which the ServiceMonitor takes
  from the `app.kubernetes.io/name` label: `muster` unless `nameOverride` changes it), and per rule under
  `alerting.rules.<name>`:
  `enabled`, `for`, and its thresholds and windows (`MusterDeliveryFailing.window`, `MusterDeliverySlow.seconds` and
  `.window`, `MusterDeliveryQueueGrowing.size` and `.window`, `MusterIngestBacklog.backlog`, `.delaySeconds` and
  `.window`, `MusterIngestRejected.unauthorized` and `.window`, `MusterTemplateError.window`, `MusterClockSkew.seconds`,
  `MusterLoginFailures.failures` and `.window`), with the defaults of reference.md.
- **Rule unit tests** (C-19.AC-8; `tests/rules/**`, `make rules-test`): `helm template` with the rules enabled, the
  group extracted to a file, `promtool check rules` and `promtool test rules` with one test file per rule: the series
  that make it fire, the time it starts firing (its window and `for`) with the labels and annotations of the fired
  alert, its `runbook_url` included — for a rule whose condition holds from its first samples or that fires at once,
  the window is pinned by the time it stops firing — and a series set that keeps it silent, including
  `MusterNoLeader` after 2 minutes of `muster_leader` 0 on every target (C-19.AC-1) and `MusterIngestRejected` with one
  oversized request but not with ten unauthorized ones.
- **`runbook_url`** (C-19.FR-9; `_helpers.tpl`): `{{ alerting.runbookBaseURL }}/{{ major.minor of appVersion, or
  "latest" for 0.0.0-dev }}/operations/runbooks/<alertname>/`; the Deployment sets `MUSTER_RUNBOOK_BASE_URL` from the same
  value, so Internal alerts of S-053 point into the same site. The version is the chart's `appVersion` — the Muster
  version it deploys, which the Internal alerts take from the binary — never the chart's own `version`, so both point
  at the documentation of the same release (C-19.FR-9 as amended).
- **Dashboard** (C-19.FR-4; `files/dashboard.json`, `templates/dashboard.yaml`): uid `muster`, title "Muster", the
  variables `datasource` (Prometheus type), `namespace`, `job`, `integration`, `route`, `destination` — entity variables
  from `muster_integration_info`, `muster_route_info` and `muster_destination_info` showing names and filtering by id —
  and four rows: **Overview** (open Alert Groups by status, new Alert Groups, time to acknowledge and to resolve, delivery
  p95 against 5 seconds, active Storms), **Ingestion** (requests by Integration and outcome, processing delay, backlog,
  resolutions by reason, Heartbeat lost), **Delivery** (attempts by Destination and outcome, queues, Broken Destinations,
  messenger API latency by client class), **Platform** (Leader, API requests and latency, sign-in failures, template
  errors and rendering time, clock skew, database pool, memory and goroutines). Rendered as a ConfigMap with the sidecar
  label from `dashboard.configMap.labels` (default `grafana_dashboard: "1"`) and annotations from
  `dashboard.configMap.annotations`, such as the sidecar's folder annotation, when `dashboard.configMap.enabled`, and as a
  `GrafanaDashboard` (`grafana.integreatly.org/v1beta1`) with `configMapRef` to that ConfigMap — `name` and `key`
  (`muster.json`), both required by the CRD — `instanceSelector`, which the CRD requires too
  (`dashboard.grafanaDashboard.instanceSelector`, default `matchLabels: {dashboards: grafana}`), `folder` from
  values and, with `dashboard.grafanaDashboard.allowCrossNamespaceImport`, `allowCrossNamespaceImport: true` for a
  Grafana in another namespace, when `dashboard.grafanaDashboard.enabled`, which fails to render without the ConfigMap.
- **Checks** (`internal/tools/chartcheck`, in `make helm-check`): the rendered rule names equal the chart rules table of
  reference.md; each rule has the severity, the `for` and, whitespace aside, the default expression of that table, a
  `summary`, a `description` and a `runbook_url` of the form above, computed with the binary's own version function;
  the `PrometheusRule` and the `VMRule` have the same spec; the dashboard has the uid, the variables and the four rows,
  every panel queries the data source variable, the regex of each entity variable takes the id as the value and the
  name as the text from its `query_result` series as Grafana prints them, labels in the sorted order Prometheus returns,
  and every metric name in its queries and in the rule expressions is in the metric registry (S-053).
  `make helm-check` runs it on the all-options rendering and on the same values in the chart packaged with a release
  `appVersion` (`v1.4.0-rc.1`, whose runbooks are under `/1.4/`), and fails when the default values render a rule
  resource or the dashboard, or when the `GrafanaDashboard` renders without its ConfigMap. Before the lookup a name
  loses the `_bucket`, `_sum` and `_count` suffixes of a histogram, and the series every Prometheus scrape or Go process
  exports without being in the catalogue — `up`, `process_*` and `go_*` — are allowed. `make helm-check` validates
  the rendered CRD resources with kubeconform and their published schemas through the CRDs catalog location already
  pinned in the Makefile (`KUBECONFORM_FLAGS`, which ServiceMonitor uses); the story checks that the catalog has
  `PrometheusRule`, `VMRule` and `GrafanaDashboard` at that commit, moves the pin forward if one is missing, and never
  skips missing schemas.
- **Packaging**: `.helmignore` gains `/tests/`, anchored at the chart root, so that the rule unit tests are not part
  of the packaged chart while a `templates/tests/` of `helm test` hooks still would be.
- **Value descriptions**: every new value under `alerting.*` and `dashboard.*` has a `description` in
  `values.schema.json`, which the chart values reference of S-057 renders and checks.
- **Runbook check** (C-21.FR-3, C-19.AC-4; `internal/tools/runbookcheck`): the rule names and the paths of the
  `runbook_url`s rendered with the all-options values join the one-to-one check of S-058. `make docs` renders the chart
  with the all-options values with the Makefile's pinned helm and passes the manifest to it (`-manifest`), so
  `make docs-check`, which runs on every pull request (S-057), fails a pull request that adds a chart rule without its
  page.
- **Defaults**: `alerting.runbookBaseURL`, "Chart rule thresholds, windows and job selector".

## Steps

1. Write the rule group with its values and the two resources. Check: `helm template` renders twelve rules in each and
   none without the flags; `chartcheck` passes.
2. Write one unit test per rule. Check: `make rules-test` passes;
   `make rules-test RULES_TEST_FLAGS='--set alerting.rules.MusterNoLeader.for=3m'` fails in the test of
   `MusterNoLeader` only, `RULES_TEST_FLAGS='--set alerting.rules.MusterClockSkew.seconds=5'` in the test of
   `MusterClockSkew` only, and a window changed by one minute, such as
   `RULES_TEST_FLAGS='--set alerting.rules.MusterDeliveryFailing.window=11m'`, in the test of its rule only.
3. Write the dashboard and its two deliveries. Check: `chartcheck` passes; kubeconform validates both.
4. Pass the base URL to Muster and extend the runbook check. Check: Verification below.

## Verification

```sh
helm template m deploy/helm/muster --set existingSecret=s > /tmp/def.yaml
yq 'select(.kind == "PrometheusRule" or .kind == "VMRule") | .kind' /tmp/def.yaml | wc -l   # 0
helm template m deploy/helm/muster --set existingSecret=s -f deploy/helm/muster/ci/all-options-values.yaml > /tmp/all.yaml
yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[].alert' /tmp/all.yaml | wc -l   # 12
yq 'select(.kind == "VMRule") | .spec.groups[].rules[].alert' /tmp/all.yaml | wc -l          # 12
yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "MusterNoLeader") | [.expr, .for, .labels.severity, .annotations.runbook_url] | join(" | ")' /tmp/all.yaml | grep MusterNoLeader
# max(muster_leader{job="muster"}) == 0 | 2m | critical | https://muster-io.github.io/muster/latest/operations/runbooks/MusterNoLeader/
#   (appVersion 0.0.0-dev; a release chart has its major.minor)
yq 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "MusterDeliveryFailing") | .expr' /tmp/all.yaml | grep -o 'job="muster"' | wc -l   # 2
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
and the Grafana operator present and both flags on; see the twelve rules loaded and the dashboard imported with its
variables filled from a running Muster. Then make the replicas run without a Leader: in a `psql` session to Muster's
database run `SELECT pg_advisory_lock(x'6d75737465720003'::bigint)` (`leader.LockKey`), which waits until the current
Leader lets go, and restart the Deployment; the replicas come up and keep being scraped with `muster_leader` 0, and
`MusterNoLeader` fires two minutes later; ending the `psql` session lets a replica lead again. Scaling Muster to zero
would not do: without targets there is neither a `muster_leader` nor an `up` series, so no rule fires.

## Open questions

None.

## Notes

- Suggested commit: `feat(chart): add alert rules with unit tests and the grafana dashboard`.
- The expressions use only metrics of the catalogue, so a renamed metric fails `chartcheck` before it reaches a
  cluster.
- `operator_attention`: the optional check in a test cluster.
- `files_touched` corrections: `internal/tools/runbookcheck/runbookcheck_test.go` tests the rendered rules that join
  the runbook check; `go.mod` gains `github.com/VictoriaMetrics/metricsql`, the PromQL parser `chartcheck` reads the
  metric names with, which only the tool imports, so it is not part of the binary; `AGENTS.md` and `CONTRIBUTING.md`
  list the Make targets and the pinned tools, which gain `make rules-test` and promtool.
- Contracts corrected: `runbookcheck` reads the chart that `make docs` renders with the pinned helm (`-manifest`)
  instead of running helm itself; the dashboard values `dashboard.configMap.annotations` and
  `dashboard.grafanaDashboard.allowCrossNamespaceImport` are named; the checks compare `expr`, `for` and severity
  with reference.md and run on a release `appVersion` too, and they apply the entity variables' regexes to the
  label-sorted `query_result` series, since a regex written in another label order lists no entity; the rule unit tests
  assert the annotations of the fired alerts, and the window of a rule whose condition holds from its first samples
  (`MusterDeliverySlow`, `MusterDeliveryQueueGrowing`, the delay of `MusterIngestBacklog`) or that fires at once
  (`for: 0m`) is pinned by the time the alert ends instead of the time it starts; `.helmignore` anchors `/tests/`, as
  an unanchored `tests/` would drop a `templates/tests/` at any depth. Step 2 named a changed threshold as its
  negative check; it names the `for` of `MusterNoLeader`, the threshold of `MusterClockSkew` and a window, because the
  `for` and the windows are what the tests pin by time, and both the `for` and the threshold were run as negatives.
  The Verification line of `MusterNoLeader` filters with `grep`, as `yq` prints a line per document.

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
