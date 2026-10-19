---
id: S-096
title: Snapshots directly from Grafana Alerting's webhook contact point (BE)
capability: C-06
kind: be
layer: L1
depends_on: [S-020, S-028, S-060, S-091]
covers: [C-06.FR-4, C-06.FR-8, C-06.FR-21, C-06.AC-16, C-06.AC-17, C-06.AC-18, C-05.FR-11]
files_touched:
  - CONTEXT.md
  - design/prd/L1.md
  - design/prd/l1/C-05-integrations.md
  - design/prd/l1/C-06-snapshot-processing.md
  - design/prd/l1/defaults.md
  - design/adr/0002-webhook-only-ingestion-with-snapshot-semantics.md
  - design/facts.md
  - design/architecture.md
  - design/stories/S-020-snapshot-processing-and-alerts-view.md
  - design/stories/README.md
  - design/stories/coverage.md
  - api/openapi.yaml
  - internal/ingest/payload.go
  - internal/ingest/payload_test.go
  - internal/ingest/process.go
  - internal/ingest/process_test.go
  - internal/ingest/grafana_integration_test.go
  - internal/ingest/testdata/grafana/ha-main.jsonl
  - internal/ingest/testdata/grafana/trunc.jsonl
  - internal/fakes/fakealertmanager/scenarios.go
  - internal/links/render_test.go
  - docs/integrations/grafana.md
  - docs/concepts/snapshot-semantics.md
  - docs/operations/runbooks/MusterSnapshotTruncated.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
acceptance:
  - "[C-06.FR-4, C-06.AC-16] A firing Alert whose current `startsAt` is 07:27:10, listed as `resolved` with the `startsAt` 07:27:00 and the `endsAt` 07:42:04 — the resolve that the other replica of a Grafana HA pair sends (F-111) — resolves with the reason `resolved`, alone and inside a Snapshot with `status: resolved`, and its next firing is a new firing with `episode` 2, not a Continuation (`process_test.go`)."
  - "[C-06.FR-4, C-06.AC-16] A `resolved` with an older `startsAt` than the Alert's current one and an `endsAt` before the current `startsAt`, equal to it, or none changes nothing and is not counted as dropped, alone and inside a Snapshot with `status: resolved`; every other row of the Alerts table of S-020 keeps its result, the row \"resolved, firing with a newer startsAt\" included (`TestAlertRules` with the new rows)."
  - "[C-06.FR-4, C-06.AC-16] C-06.AC-13 and C-06.AC-14 still hold: `TestResolvedGroup` and `TestRepeatedResolves` pass with their resolves in Alertmanager's shape — `endsAt` the time of the resolve, before the next firing — and `TestIntegrationFacts` passes every scenario of the fake Alertmanager, F-033 to F-053, on PostgreSQL 14 and 17, every expectation unchanged, with the late resolve of the F-046 scenario carrying the `endsAt` of the firing it ended."
  - "[C-06.FR-21, C-06.AC-17] With 10 Alerts firing in one `groupKey`, a Snapshot with a top-level `orgId`, `status: resolved` and `truncatedAlerts: 5` that lists 5 of them as resolved resolves those 5, leaves the other 5 firing and marks the `groupKey` truncated; the same Snapshot without `orgId`, or with `orgId` `null`, resolves all 10, and so does an untruncated one with `orgId` (`process_test.go`; `grafana_integration_test.go` with the captured truncated body of 07:53:10 and five more Alerts of its `groupKey`)."
  - "[C-06.FR-8, C-06.FR-21] Every body of `testdata/grafana/ha-main.jsonl` (84) and `trunc.jsonl` (6) parses with `Payload.Grafana` true and `HasReason` false, every alert's fingerprint taken from the body; Grafana's other fields — `version` `\"1\"`, top-level `title`, `message`, `state` and `appVersion`, and per alert `values` (an object or null), `valueString`, `silenceURL`, `dashboardURL`, `panelURL`, `ruleUID`, `orgId` and `imageURL` — are accepted and not read; a body of the fake Alertmanager, with `version` `\"4\"` and no `orgId`, parses with `Grafana` false (`payload_test.go`)."
  - "[C-06.FR-4, C-06.FR-8, C-06.AC-18] The 84 bodies the Grafana HA pair sent, stored for a new Integration at their `recv` times on a manual clock and processed in order, on PostgreSQL 14 and 17, fail no Snapshot and leave every Alert in the state of the latest body that lists it: `SpikeDisk` on host `x` (the paused rule) is resolved, `SpikeNoData` on host `n` is resolved by the body of 07:39:27 and fires again at 07:57 as a new firing with `episode` 2, not a Continuation, and the route `{}/{}` has a learned repeat interval between 120 and 155 s (F-108). The test fails on `master` before the change. The 6 bodies of the contact point with Max alerts 5 fail no Snapshot either, and `MusterSnapshotTruncated` fires for their Integration (`grafana_integration_test.go`)."
  - "An Alert Group of Alerts from Grafana links to the rule's page in Grafana, `<root_url>/alerting/grafana/<rule uid>/view?orgId=1`, as `source`, and the built-in Explore rule, with a row of the Lookup table `grafana` for the Alert Group's cluster, writes no link, counts no template error and logs nothing, because the URL has no `g0.expr` (`render_test.go`; live)."
  - "[C-06.FR-21] The spec's `AlertmanagerWebhook` describes the payload of Grafana's webhook contact point beside Alertmanager's and documents the optional `orgId` as the mark of a Snapshot from Grafana; `make generate-check` passes with the regenerated server, clients and API reference."
  - "[C-05.FR-11] The documentation page Connect Grafana describes connecting Grafana directly — the webhook contact point with its settings and a provisioning example, the notification policy, one Integration per Grafana cluster and organization, and the limits of this section's Contracts — and through an external Alertmanager; the Alertmanager section of the site becomes Integrations, Snapshot semantics and the `MusterSnapshotTruncated` runbook name Grafana where they apply, `llms.txt` has the scenario Connect Grafana, and `make docs-check` passes."
  - "[C-06.FR-4, C-06.FR-8, C-06.FR-21] Live, a single Grafana OSS 13.2.4 sends to `muster dev` through a webhook contact point with an Integration token: after a new group, an added alert, one resolved alert, all resolved and a paused rule, the Integration's firing Alerts (`listIntegrationAlerts`) equal the active alerts of Grafana's own Alertmanager each time, no Stored Snapshot failed, and the route learns a repeat interval; Grafana's truncated `status: resolved` body sent with and without `orgId` leaves 5 and 0 of 10 Alerts firing."
verify: "make ci test-integration e2e docs-check"
operator_attention: false
issue: 304
---

# S-096. Snapshots directly from Grafana Alerting's webhook contact point (BE)

## Scope

**IN**

- Grafana Alerting's webhook contact point as a source of Snapshots (D383): Grafana's built-in Alertmanager sends
  Alertmanager's webhook format with additions (F-105), and the three properties Snapshot processing rests on hold —
  `groupKey` names the route and the group (F-106), every Snapshot lists the whole Alertmanager group at Max alerts 0,
  and repeats come at the `repeat_interval` (F-108). The parser reads it as it is; this story makes that explicit and
  marks a Snapshot from Grafana.
- The resolve of an overlapping firing (C-06.FR-4): the replicas of a Grafana HA pair evaluate the rules each on its
  own, so their copies of one alert can carry different `startsAt` (F-111), and today the resolve of the replica whose
  copy Muster did not keep is taken for a resolve of an earlier firing — the Alert stays firing for good, and its next
  firing is a quiet Continuation.
- The truncated Snapshot from Grafana (C-06.FR-21): Grafana computes `status` from the alerts it lists (F-110), so a
  truncated `status: resolved` must not resolve the other Alerts of its Alertmanager group.
- The facts F-105 to F-112 and the documents that state Alertmanager as the only source: the glossary, L1, C-05,
  C-06, ADR-0002, the architecture and the processing table of S-020 (changed with this contract).
- The captured bodies of Grafana 13.2.4 as fixtures, and an integration test that processes them.
- The spec's description of the payload, and the documentation: Connect Grafana with both ways (C-05.FR-11), Snapshot
  semantics, the runbook of `MusterSnapshotTruncated`, the site's navigation and `llms.txt`.

**OUT**

- A Grafana provisioning snippet in the token dialog, and a text of its own instead of the Alertmanager route snippet
  for routes whose path has `__grafana_` labels (C-06.FR-18): L2, with a later change of C-05 and C-06. The Integration
  page, its banners and the reason of Gone and Stale keep speaking of Alertmanager, which Grafana's built-in one is.
- Keeping the annotations of a resolve on the Alert, which would show `grafana_state_reason` (`Paused`, `RuleDeleted`,
  `MissingSeries`, F-109) in the Alerts view: the model takes annotations from firing listings only, and the reason
  stays visible in the Stored Snapshot. Proposed to the maintainer in the pull request (Notes).
- `ha_single_node_evaluation`, retries of the webhook contact point, Grafana's silences and mute timings, several
  organizations on one Integration, a custom payload, HMAC signing and the Heartbeat through Grafana: not verified
  (facts, Pending); the documentation says so where it mentions them.
- A generic inbound webhook for other senders: L2 (ADR-0002).
- A fake Grafana server: the captured bodies stand for Grafana in tests, and a live check posts them to the ingest
  listener.

## Contracts

- **Operations, tables, settings, metrics, log events, Internal alerts**: none new; no migration. Ingestion is
  unchanged: it stores any body as received and answers `202` (C-05.FR-3); Grafana's bodies were accepted and
  processed without a failure before this story (D383).
- **Payload** (`internal/ingest/payload.go`): `Payload` gains `Grafana bool`, true when the body has a top-level
  `orgId` that is present and not `null`. Grafana's webhook contact point always sends it and Alertmanager never does
  (F-105); `orgId` is read as raw JSON, so its type never fails a body. The parser stays tolerant, and its comment says
  so: `version` is not read (`"4"` from Alertmanager, `"1"` from Grafana); unknown fields are ignored, Grafana's
  top-level `title`, `message`, `state` and `appVersion` and per-alert `values` (an object or `null`), `valueString`,
  `silenceURL`, `dashboardURL`, `panelURL`, `ruleUID`, `orgId` and `imageURL` included; `notification_reason` and
  `routeLabels` are absent, so `HasReason` is false and repeats are learned from identical Snapshots (C-06.FR-8,
  F-108). `fingerprint` is taken from the body; Grafana's covers the hidden `__alert_rule_uid__` (F-107), so
  `Fingerprint(labels)`, used only when a body has none, never stands in for it. `generatorURL` is kept as it is: the
  "Source" link shows Grafana's rule page.
- **The resolve of an overlapping firing** (`process.go`, `apply`, C-06.FR-4): a `resolved` listing of a firing Alert
  whose current `startsAt` is later than the listed one resolves it, reason `resolved`, when the listed `endsAt` is
  strictly later than the current `startsAt`; without an `endsAt`, or with one not later, it is a resolve of an earlier
  firing and changes nothing, not counted, as today. The rows of the Alerts table of S-020 become:

  | Listed as | Row state | Result |
  |---|---|---|
  | resolved | firing, the listed `startsAt` not older | resolved, reason `resolved` |
  | resolved | firing, the listed `startsAt` older and the listed `endsAt` later than the current `startsAt` | resolved, reason `resolved` |
  | resolved | firing, the listed `startsAt` older and no `endsAt`, or one not later than the current `startsAt` | nothing, not counted |
  | resolved | resolved | nothing, not counted |

  The rule holds for late Snapshots and replays too, which apply only the `resolved` rows. `resolveGroup` keeps skipping
  an Alert listed as resolved — its own row decides it, and now resolves it when the firings overlap — so only its
  comment changes. `applyInternal` (Internal alerts) is unchanged. With one rule evaluator Alertmanager is unaffected:
  it merges overlapping firings of one fingerprint into one alert with the earliest `startsAt` (F-053), and Prometheus
  and vmalert resolve an alert before it fires again, so a resolve Alertmanager sends of an earlier firing, re-sent
  ones included (F-046), ends before the next firing starts. With an HA pair of rule evaluators, Alertmanager replaces
  rather than merges an alert whose incoming time range contains the stored one, so it can send a resolve with an older
  `startsAt` whose `endsAt` is later than the next firing's `startsAt`; Muster then resolves the Alert, as it does when
  it kept Alertmanager's merged `startsAt`, where before this change the Alert stayed firing for good.
- **The truncated Snapshot from Grafana** (`process.go`, `run`, C-06.FR-21): a Snapshot with `status: resolved` resolves
  its whole Alertmanager group (`resolveGroup`) unless `Payload.Grafana` and `TruncatedAlerts > 0`; then only the Alerts
  it lists as resolved resolve, by their own rows, and the unlisted ones stay alive as C-06.FR-6 says. Truncation is
  tracked as before: the `groupKey` is truncated, `MusterSnapshotTruncated` fires, and
  `muster_ingest_truncated_snapshots_total` counts it. Alertmanager's truncated `status: resolved` keeps resolving the
  whole Alertmanager group (F-048), and so does an untruncated one from Grafana.
- **Fixtures** (`internal/ingest/testdata/grafana/`): JSON lines
  `{"recv": "<RFC 3339 time of arrival>", "path": "<the receiver's path>", "body": <the body as received>}`, verbatim
  from the log of the stand's HTTP receiver in the test of F-105 to F-112 on the development stand (Grafana OSS 13.2.4,
  2026-10-19), headers dropped. `ha-main.jsonl` holds the 84 bodies of the HA pair's catch-all contact point,
  `trunc.jsonl` the 6 bodies of the contact point with `maxAlerts: 5`. They carry no secret: that receiver never logged
  the `Authorization` header, and the bodies hold only test labels and the stand's example hostnames. Fixtures need no
  licence header (AGENTS.md).
- **Integration test** (`grafana_integration_test.go`, build tag `integration`, `dbtest.ForEach`): for each fixture, a
  new Integration with the default duplicate window; for each line, the business clock is set to `recv`, the body is
  stored (`Snapshots.Store`) and the processor drained, as `runScenario` does in `facts_test.go`. Expected states are
  computed from the bodies: for each fingerprint, the `status` of the latest body that lists it. A third case sends,
  under the `groupKey` of `trunc.jsonl`, an untruncated firing body with the five alerts of its body of 07:53:10 and
  five more made from them (other `host`, other fingerprint), then that captured body, once as captured and once
  without `orgId`.
- **Fake Alertmanager** (`scenarios.go`): the F-046 scenario's resolve with the old `startsAt`, sent after the new
  firing, gets `EndsAt` at the time it first resolved (120 s), as Prometheus re-sends a resolve with its original
  `endsAt`; the fake would otherwise stamp it with the time of the step, after the new firing's `startsAt`, which
  Alertmanager does not send with one rule evaluator. Its expectations are unchanged.
- **Links** (`render_test.go`): a firing Alert whose `generatorURL` is
  `http://grafana.example.org/alerting/grafana/spike-cpu/view?orgId=1`, with the Lookup table `grafana` holding the
  Alert Group's cluster, gets the `source` link with that URL and no Explore link, and the evaluation counts no
  `muster_template_errors_total` and logs no `link_rule_failed`. The built-in template is unchanged (C-12.FR-9).
- **Spec** (`api/openapi.yaml`, then `make generate`): `AlertmanagerWebhook`'s description names the payload of
  Grafana Alerting's webhook contact point (`version` `"1"`) beside Alertmanager's (version 4); `version` says it is
  not read; a new optional property `orgId` (integer) says that only Grafana sends it, that it marks a Snapshot from
  Grafana, and that a truncated Snapshot from Grafana with `status: resolved` resolves only the Alerts it lists
  (C-06.FR-21); `truncatedAlerts` names Max alerts in Grafana and `notification_reason` says that Grafana does not
  send it. The request body of `ingestSnapshot` and `ingestSnapshotWithPathToken` says that Grafana's webhook contact
  point sends the same JSON. The generated server, the Go and TypeScript clients and the API reference change through
  `make generate` and are not listed.
- **Documentation** (C-05.FR-11, D383):
  - `docs/integrations/grafana.md` becomes **Connect Grafana**, with both ways. **Directly**: an Integration for each
    Grafana cluster and organization — an HA pair is one, and two organizations with the same policies give the same
    `groupKey`s (F-106) — and its token; a webhook contact point to the Integration's ingest URL,
    `MUSTER_INGEST_URL` followed by `/api/v1/ingest`, method POST, the `Authorization` header with the scheme `Bearer`
    and the Integration token as its credentials, **Max alerts 0** (the default), "Disable resolved message" off and no
    custom payload, with a provisioning example (`type: webhook`, `httpMethod: POST`, `authorization_scheme: Bearer`,
    `authorization_credentials`, `disableResolveMessage: false`); a notification policy that sends to it with a
    `repeat_interval` of 5–15 minutes. Do not merge rules into one Grafana notification group: keep Grafana's default
    `group_by`, `[grafana_folder, alertname]`, and let Muster group, because after a restart of Grafana a group of
    rules with different evaluation intervals sends Snapshots without the slower rules' alerts until their first
    evaluation, which Muster can take for Gone and then a loud new firing (F-112). A contact point chosen in the rule
    (simplified routing) puts `__grafana_route_settings_hash__` into the route path of the `groupKey`, so editing the
    rule's notification settings starts a new `groupKey` and the old one falls silent (C-06.FR-7, F-106). NoData and
    Error are alerts of their own, `DatasourceNoData` and `DatasourceError`, with the label `rulename`; a paused or
    deleted rule and a missing series resolve explicitly, and `grafana_state_reason` shows in the Stored Snapshot
    (F-109). The "Source" link opens the rule in Grafana; the built-in Explore link is not built for Grafana's alerts.
    An HA pair needs nothing more: Muster takes the resolve of either replica (F-111); `ha_single_node_evaluation` is
    named as untested. The **Heartbeat** page's setup is for Alertmanager; the same idea — an always-firing rule with
    a notification policy of its own, `repeat_interval` about one minute, to a webhook contact point at the
    Integration's Heartbeat URL — has not been verified with Grafana, and without a Heartbeat Muster never resolves an
    Alert as Stale (C-06.FR-9). **Through an external Alertmanager**: the present content, and "Why through an
    Alertmanager" goes. The section Labels stays.
  - `docs/concepts/snapshot-semantics.md`: Snapshots come from Alertmanager or Grafana's built-in one; under Resolved,
    the resolve of an overlapping firing with the HA pair of Grafana; under Truncation, the truncated Snapshot from
    Grafana and Max alerts 0.
  - `docs/operations/runbooks/MusterSnapshotTruncated.md`: the diagnosis and the fix name Max alerts 0 on Grafana's
    webhook contact point beside `max_alerts: 0`.
  - `mkdocs.yml`: the section "Alertmanager" becomes "Integrations", with Connect Alertmanager, Connect Grafana and
    Heartbeat; `docs/index.md` (line 38) describes the section with both sources.
  - `docs/llms.txt.tmpl`: a scenario "Connect Grafana" after "Connect Alertmanager" — Connect Grafana, Heartbeat,
    Snapshot semantics, finished when the Integration page shows the time of the last Snapshot — whose quotes match
    the pages word for word, or the generator of S-091 fails the build.

## Steps

1. **Fixtures and the parser.** Copy the two files from the stand's log, add `Payload.Grafana` and the parser's
   comment. Check: `payload_test.go` reads every line of both fixtures and the cases of the acceptance;
   `grep -ciE 'authorization|bearer|mstr_' internal/ingest/testdata/grafana/*.jsonl` prints 0 for each file.
2. **The HA test, red.** Write `grafana_integration_test.go` and run it before changing the processing. Check:
   `go test -tags integration -run TestIntegrationGrafana ./internal/ingest/` fails, naming `SpikeDisk`/`x` and
   `SpikeNoData`/`n` as firing; the output goes into the pull request.
3. **The resolve of an overlapping firing.** Change `apply` and the comment of `resolveGroup`; add the rows of the
   table to `TestAlertRules` and a test of the HA sequence. `TestRepeatedResolves` and the last part of
   `TestResolvedGroup` give a resolve the `endsAt` one hour after its `startsAt` (the helper `resolved`), after the
   next firing's `startsAt` — a shape Alertmanager does not send with one rule evaluator, whose firings would have
   merged (F-053) — so they take Alertmanager's real shape, `endsAt` the time of the resolve, before the next firing;
   fix the F-046 scenario of the fake Alertmanager the same way. Check:
   `go test ./internal/ingest/ ./internal/fakes/fakealertmanager/`; `make test-integration` (with
   `TestIntegrationFacts`) and the test of step 2 pass.
4. **The truncated Snapshot from Grafana.** Change `run`. Check: the unit test of C-06.AC-17 and the third case of
   `grafana_integration_test.go`.
5. **Links.** Add the case to `render_test.go`. Check: `go test ./internal/links/`.
6. **Spec.** Change `api/openapi.yaml` and run `make generate`. Check: `make generate-check`.
7. **Documentation.** Write the pages, the navigation and the scenario of `llms.txt`. Check: `make docs-check`.
8. **Live check.** As below, on the development stand. Check: the Verification.

## Verification

On the development stand: `muster dev` from this branch; Prometheus v3.15.0 scraping a small exporter of the series
`muster_test_value{rule,host}`, whose values the stand's helper `SET <rule> <host> <value>` sets; Grafana OSS 13.2.4
as one instance on `127.0.0.1:3000` with `GF_SERVER_ROOT_URL=http://grafana.example.test:3000/`, its admin password in
`GF_ADMIN_PASSWORD`, and two Grafana-managed rules in the folder `Checks`, `SpikeCPU` and `SpikeDisk`, evaluated
every 10 s with `for: 0s`, firing while `max without(job, instance) (muster_test_value{rule="cpu"})` (`"disk"`) is
above 0, with the labels `severity` and `team`. Grafana evaluates and notifies on its own time (10 s, `group_wait`
10 s, `group_interval` 30 s); `WAIT` waits for its next Snapshot, nothing waits for a timer of Muster.

```sh
make dev > dev.log 2>&1 &
# the Admin's Personal access token in A, as in S-092
API=localhost:8080/api/v1; GF=127.0.0.1:3000; GFA=(-u "admin:$GF_ADMIN_PASSWORD")
INT=$(curl -s "${A[@]}" $API/integrations -d '{"name":"grafana","connection_mode":"webhook_only","static_labels":{"cluster":"lab"}}' | jq -r .id)
export MUSTER_TOKEN=$(curl -s "${A[@]}" $API/integrations/$INT/tokens -d '{"name":"grafana"}' | jq -r .value)
curl -s "${A[@]}" $API/lookup-tables -d '{"name":"grafana","columns":["address","datasource_uid"],"entries":[
  {"key":"lab","values":{"address":"http://grafana.example.test:3000","datasource_uid":"prom"}}]}' > /dev/null
cat > provisioning/alerting/muster.yaml <<'YAML'
apiVersion: 1
contactPoints:
  - orgId: 1
    name: muster
    receivers:
      - uid: muster
        type: webhook
        disableResolveMessage: false
        settings:
          url: http://127.0.0.1:8081/api/v1/ingest
          httpMethod: POST
          authorization_scheme: Bearer
          authorization_credentials: ${MUSTER_TOKEN}
policies:
  - orgId: 1
    receiver: muster
    group_by: [grafana_folder, alertname]
    group_wait: 10s
    group_interval: 30s
    repeat_interval: 2m   # short for the check; the documentation recommends 5–15 minutes
YAML
# (re)start Grafana with MUSTER_TOKEN in its environment: it reads the provisioning, ${MUSTER_TOKEN} included, at start
N() { curl -s "${A[@]}" $API/integrations/$INT | jq .snapshot_count; }
WAIT() { local n=$(N); until [ "$(N)" -gt "$n" ]; do sleep 2; done; }
GRAFANA() { curl -s "${GFA[@]}" "$GF/api/alertmanager/grafana/api/v2/alerts?active=true" | jq -c '[.[] | "\(.labels.alertname)/\(.labels.host)"] | sort'; }
MUSTER() { curl -s "${A[@]}" "$API/integrations/$INT/alerts?state=firing" | jq -c '[.items[] | "\(.labels.alertname)/\(.labels.host)"] | sort'; }

SET cpu a 1; WAIT; GRAFANA; MUSTER                       # both: ["SpikeCPU/a"]                 a new group
SET cpu b 1; WAIT; GRAFANA; MUSTER                       # both: ["SpikeCPU/a","SpikeCPU/b"]    an alert added
SET cpu a 0; WAIT; GRAFANA; MUSTER                       # both: ["SpikeCPU/b"]                 one resolved
curl -s "${A[@]}" "$API/integrations/$INT/alerts?state=resolved&label=host%3D%22a%22" | jq -r '.items[0].resolve_reason'
# resolved
G=$(curl -s "${A[@]}" "$API/integrations/$INT/alerts?label=host%3D%22b%22" | jq -r '.items[0].alert_group.id')
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.links[] | {kind, url}]'
# [{"kind":"source","url":"http://grafana.example.test:3000/alerting/grafana/spike-cpu/view?orgId=1"}]   no Explore link
grep -c '"event":"link_rule_failed"' dev.log             # 0
SET cpu b 0; WAIT; GRAFANA; MUSTER                       # both: []                             all resolved
curl -s "${A[@]}" $API/alert-groups/$G | jq -r .status   # resolved

# a paused rule resolves explicitly (F-109)
SET disk x 1; WAIT; MUSTER                               # ["SpikeDisk/x"]
sed -i 's/isPaused: false/isPaused: true/' provisioning/alerting/rules-disk.yaml   # the file of SpikeDisk
curl -s "${GFA[@]}" -X POST $GF/api/admin/provisioning/alerting/reload > /dev/null; WAIT
GRAFANA; MUSTER                                          # both: []
curl -s "${A[@]}" "$API/stored-snapshots?integration=$INT&limit=1" | jq -r '.items[0].id' |
  xargs -I{} curl -s "${A[@]}" $API/stored-snapshots/{} | jq -r '.body | fromjson | .alerts[0].annotations.grafana_state_reason'
# Paused

# repeats are learned without notification_reason (F-108); no Snapshot failed
SET cpu c 1; WAIT; WAIT; WAIT                            # the first notification and two repeats
curl -s "${A[@]}" $API/integrations/$INT/alertmanager-routes | jq -c '[.items[] | {route_path, learned_repeat_interval_seconds}]'
# [{"route_path":"{}","learned_repeat_interval_seconds":<between 120 and 150>}]     a policy without child policies
curl -s "${A[@]}" "$API/stored-snapshots?integration=$INT&state=failed" | jq '.items | length'   # 0

# C-06.AC-17 with the captured truncated body of 07:53:10: five Alerts it lists, five more of its groupKey
TR=internal/ingest/testdata/grafana/trunc.jsonl
RES=$(jq -c 'select(.body.status == "resolved") | .body' $TR | head -1)
FIRE=$(jq -c '.status = "firing" | .truncatedAlerts = 0 | .alerts |= map(.status = "firing" | .endsAt = "0001-01-01T00:00:00Z")
  | .alerts += [.alerts[] | .labels.host |= "x" + . | .fingerprint |= "x" + .]' <<<"$RES")
for mark in with without; do
  I=$(curl -s "${A[@]}" $API/integrations -d "{\"name\":\"trunc-$mark\",\"connection_mode\":\"webhook_only\"}" | jq -r .id)
  T=$(curl -s "${A[@]}" $API/integrations/$I/tokens -d '{"name":"t"}' | jq -r .value)
  for b in "$FIRE" "$RES"; do
    [ $mark = without ] && b=$(jq -c 'del(.orgId)' <<<"$b")
    curl -s -o /dev/null -w '%{http_code} ' -X POST localhost:8081/api/v1/ingest -H "Authorization: Bearer $T" -d "$b"
  done; sleep 1
  curl -s "${A[@]}" "$API/integrations/$I/alerts?state=firing" | jq '.items | length'
done
# 202 202 5      with orgId: the five Alerts it does not list keep firing
# 202 202 0      without orgId: all ten resolve

# C-06.AC-18 and the fixtures, with TestIntegrationFacts, on PostgreSQL 14 and 17
make test-integration
```

## Open questions

None.

## Notes

- Suggested commit: `feat(ingest): take Snapshots from Grafana Alerting's webhook contact point`.
- Decided by the maintainer after a live test of Grafana OSS 13.2.4 on the development stand (D383): its built-in
  Alertmanager keeps the three properties of a Snapshot, and Muster already accepted and processed every body; the
  single instance ended in the same state as Grafana. The HA pair left two Alerts firing for good — a paused rule and a
  missing series, whose resolves came from the replica with the older `startsAt` — and recorded the missing series'
  return as a quiet Continuation. The truncated `status: resolved` was not sent to Muster on the stand; its effect
  follows from `resolveGroup`, and the third case of the integration test and the live check above send it.
- **Strictly later, not "not earlier".** Alertmanager merges two firings of one fingerprint only when their ranges
  overlap strictly, so a firing that starts exactly when the previous one ended is a firing of its own; the existing
  row "resolved, firing with a newer startsAt", whose `endsAt` equals the new `startsAt`, keeps its result.
- **A copy of an HA pair can still record a quiet Continuation.** When the first copy Muster keeps is the one with the
  older `startsAt`, the other replica's copy with the later one is a new `startsAt` without a resolve, which C-06.FR-11
  records as a Continuation, once per Alert, Quiet; later copies with the older `startsAt` are old copies. The live
  test kept the later one first. This story does not change the firing rows; the documentation of Connect Grafana
  names `ha_single_node_evaluation` as untested.
- **A late resolve of a Grafana HA pair can end a later firing.** With a Grafana HA pair whose alert flaps, a late
  resolve with the `startsAt` S1 and the `endsAt` E1 — in a replay, for example — resolves a later firing whose
  `startsAt` falls between S1 and E1, because the two overlap. Alertmanager cannot produce that with one rule
  evaluator, because it merges such firings.
- **`grafana_state_reason`.** Proposed to the maintainer in the pull request: keep the annotations of a resolve on the
  Alert, so that the Alerts view and the Alert Group page show why Grafana resolved it (`Paused`, `RuleDeleted`,
  `MissingSeries`). It changes the processing model — annotations come from firing listings only — and is not part of
  this story.
- **Facts.** F-070 to F-104 are reserved for the facts of the Slack test, so those of Grafana start at F-105.
- **Changes to documents made with this contract**: C-06 gains scenarios 11 and 12 and C-06.AC-16 to AC-18, its
  FR-4, FR-8 and FR-21 change, and FR-2, FR-6 and FR-7 cite the facts of Grafana; C-05's goal, scenario 6 and FR-11;
  L1 §1.1 Input and NFR-12; ADR-0002's Position, its Snapshot semantics and an Amended line; the row
  `integration.duplicate_window` of `defaults.md` cites F-111; the glossary's Snapshot and Truncated Snapshot;
  `facts.md` (the section Grafana and its Pending); `architecture.md` (the system context and the processing flow); the
  Alerts table of S-020. They are proposed to the maintainer in this pull request.
- The live check uses a `repeat_interval` of 2 minutes so that two repeats come within minutes; the documentation
  recommends 5–15 minutes, as for Alertmanager.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-06.FR-4 | partial | the resolve of an overlapping firing; the rest is S-020 and S-028 |
| C-06.FR-8 | partial | Snapshots from Grafana, without `notification_reason`, learned from identical Snapshots on the captured bodies and live; the rest is S-020 and S-023 |
| C-06.FR-21 | partial | the truncated Snapshot from Grafana; the resolved Alertmanager group is S-020 |
| C-06.AC-16 | full | |
| C-06.AC-17 | full | |
| C-06.AC-18 | full | |
| C-05.FR-11 | partial | connecting Grafana, directly and through an external Alertmanager; the Alertmanager part is S-018 |
