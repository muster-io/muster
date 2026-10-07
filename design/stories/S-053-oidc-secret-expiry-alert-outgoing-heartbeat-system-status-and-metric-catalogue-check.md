---
id: S-053
title: OIDC secret expiry alert, outgoing heartbeat, System status, runbook URLs and the metric catalogue check (BE)
capability: C-19
kind: be
layer: L1
depends_on: [S-045, S-049]
covers: [C-19.FR-2, C-19.FR-5, C-19.FR-7, C-19.FR-9, C-19.FR-10, C-19.AC-1, C-19.AC-2, C-19.AC-3, C-19.AC-5, C-19.AC-6, C-19.AC-7, C-02.FR-10]
files_touched:
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics_test.go
  - internal/config/config.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/internalalerts_test.go
  - internal/oidc/expiry.go
  - internal/oidc/expiry_test.go
  - internal/outgoingheartbeat/sender.go
  - internal/outgoingheartbeat/sender_test.go
  - internal/organization/settings.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/api/system.go
  - internal/api/system_test.go
  - internal/api/organization_test.go
  - internal/systemstatus/status.go
  - internal/systemstatus/query.sql
  - internal/systemstatus/status_test.go
  - internal/leader/tasks.go
  - internal/logging/events.go
  - internal/fakes/fakewebhook/fakewebhook.go
  - internal/archlint/secretleak.go
  - test/e2e/self_observation_test.go
acceptance:
  - "[C-19.FR-2] `make generate-check` fails when a metric of the catalogue in reference.md is missing from the registry or from docs/reference/metrics.md, when one has another type or labels, or when the registry has a metric the catalogue lacks."
  - "[C-19.AC-3] No series exported on `/metrics` carries a label other than the catalogue's — never `alertname`, `severity`, a user or an Alert Group number — checked by a test over the registry and live by a scrape."
  - "[C-19.FR-5, C-19.AC-5, C-02.FR-10] With the OIDC client secret expiry date 10 days ahead the Leader raises `MusterOIDCSecretExpiring` with severity warning; moving the date 30 days ahead, or removing it, resolves it."
  - "[C-19.FR-9] Every Internal alert carries the annotation `runbook_url` = `MUSTER_RUNBOOK_BASE_URL`/<major.minor or `latest`>/operations/runbooks/<alertname>/."
  - "[C-19.FR-7, C-19.AC-7] With `outgoing_heartbeat.url` set through `updateOrganization`, only the Leader sends `outgoing_heartbeat.method` to it once per `outgoing_heartbeat.interval` through the `heartbeat` client class and its proxy; a failure is counted under `client=\"heartbeat\"` and logged, and the next tick is the retry; without a URL nothing is sent."
  - "[C-19.AC-1] With no replica leading, `muster_leader` is 0 everywhere and no outgoing heartbeat is sent; once a replica leads again the heartbeat resumes."
  - "[C-19.FR-10, C-19.AC-6] `getSystemStatus` returns the replicas with the Leader and their key ids, the recovery state, delivery queues, Broken Destinations with their reason, Integrations with Heartbeat lost or truncated Snapshots, template errors, active Storms and the last outgoing heartbeat, and offers no operation that changes anything; a Responder gets `403`."
  - "[C-19.AC-2] A Broken Mattermost Destination raises `MusterDestinationBroken`, whose Alert Group reaches another Destination through the Route of the `internal_alerts` suggestion."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 53
---

# S-053. OIDC secret expiry alert, outgoing heartbeat, System status, runbook URLs and the metric catalogue check (BE)

## Scope

**IN**

- The check of the metric registry against the catalogue of reference.md and the generated page; the label policy.
- `MusterOIDCSecretExpiring`, the last Internal alert of L1, and the `runbook_url` of every Internal alert.
- The outgoing heartbeat: its Organization setting and the Leader's sender.
- The System status read.

**OUT**

- The chart's alert rules, their unit tests and the dashboard (S-059, split from this story); the runbook pages and the
  check that each rule and Internal alert has one (S-058, S-059).
- The System status page and the outgoing heartbeat settings page (S-054).
- The monitoring and outgoing heartbeat sections of the documentation (S-060).

## Contracts

- **Operations implemented**: `getSystemStatus` (`system-status:read`); `updateOrganization` accepts
  `outgoing_heartbeat.url` (write-only, `null` clears it) and `outgoing_heartbeat.proxy` — the `unsupported` answer of
  S-012 is removed for these two fields only; `getOrganization` shows them as `url_status` and `proxy`. Schemas:
  `SystemStatus`, `ReplicaStatus`, `QueueDepth`, `BrokenDestination`, `IntegrationAttention`, `TemplateErrorEntry`,
  `ActiveStorm`, `OutgoingHeartbeatResult`; hint `system-status`.
- **Metric catalogue check** (C-19.FR-2; `internal/tools/refgen`): `make generate` and `make generate-check` read the
  table of the [metrics catalogue](../prd/l1/reference.md#metrics-catalogue) and compare it with the metric registry
  and `docs/reference/metrics.md`: the same names, types, label names and, where the table lists them, label values.
  Any difference fails with the metric and the field. `metrics_test.go` also checks that every metric of the catalogue
  is constructed when the server starts.
- **Label policy** (C-19.AC-3, ADR-0014): label names come only from the catalogue — entity ids (`integration`,
  `route`, `destination`), the closed sets (`client`, `outcome`, `state`, `route_pattern`, `method`, `code`, `kind`,
  `reason`, `status`, `by`, `command`, `transport`, `template`), `name` of the `*_info` metrics, `version` and `commit`,
  and `le`. A test over the registry fails on any other name.
- **Internal alerts** (C-19.FR-5, FR-9; `internal/internalalerts`, `internal/oidc/expiry.go`): `MusterOIDCSecretExpiring`
  (severity `warning`, no labels besides `alertname` and `severity`) is registered and the reference page regenerated.
  A Leader task (`internal/leader/tasks.go`) runs every 5 minutes and when the OIDC settings change: it raises the
  alert while `client_secret_expires_on` is less than `oidc.secret_expiry_lead` away, and resolves it when the date
  moves further out or is removed, raising and resolving only on transitions (C-06.FR-14). Every Internal alert's
  `runbook_url` annotation is `MUSTER_RUNBOOK_BASE_URL` + `/` + the binary's `major.minor` (`latest` for the
  development version `0.0.0-dev`) + `/operations/runbooks/<alertname>/`. `MUSTER_RUNBOOK_BASE_URL` is a bootstrap
  setting of `internal/config` with the default of defaults.md, `https://muster-io.github.io/muster`; the chart sets it
  from `alerting.runbookBaseURL` (S-059).
- **Outgoing heartbeat** (C-19.FR-7, ADR-0014; `internal/outgoingheartbeat`, `organizations.outgoing_heartbeat_*`): a
  Leader task; once per `outgoing_heartbeat.interval` of Muster's clock, when a URL is set, it sends
  `outgoing_heartbeat.method` with an empty body through the outbound package in the `heartbeat` client class, with the
  Organization's heartbeat proxy, no retries and a timeout of `outgoing_heartbeat.timeout`. It stores `outgoing_heartbeat_last_attempt_at`, `_last_ok` and the masked `_last_error`; a failure is
  counted by `muster_client_requests_total{client="heartbeat"}` and logged `outgoing_heartbeat_failed` (WARN: `error`),
  never queued. The URL is a Secret: registered for redaction, shown as `SecretStatus`, `secret_changed` in the Audit
  log diff, pushed through the sender by a lint-5 probe. A change of the result between ok and failed sends a
  `system-status` hint. Without a Leader nothing is sent (ADR-0007).
- **Per Organization**: both Leader tasks of this story, the OIDC secret expiry check and the outgoing heartbeat, run
  per Organization: each iterates over the Organizations (one in L1) and passes `org_id` to every query (lint 1).
- **System status** (C-19.FR-10; `internal/systemstatus`, read-only, any replica): `version` and `commit`; `replicas`
  — live rows of `replicas` with their key ids and `refreshed_at`, `leader` for `runtime_state.leader_replica_id`;
  `recovery` — `active` while `runtime_state.recovery_until` is ahead, with the last `downtime_periods` row;
  `delivery_queues` — Destinations with pending deliveries or due Thread replies (the count of
  `muster_delivery_queue`); `broken_destinations` with `since` and the masked reason; `integrations_attention` —
  `heartbeat_lost` and `snapshot_truncated` with `since`; `template_errors` of Routes and Destinations;
  `active_storms`; `outgoing_heartbeat` (`configured` false without a URL). Each entity is an `EntityRef` that the page
  links; the resource has no operation besides the read.
- **Fake dead man's switch** (C-01.FR-13): the fake receiving endpoint of S-044 (`127.0.0.1:18093`) answers `200` to
  `GET` and `POST` under `/ping/` and records them in its `requests`.
- **Log events**: `outgoing_heartbeat_failed` (WARN), `outgoing_heartbeat_recovered` (INFO), `oidc_secret_expiring`
  (WARN: `expires_on`) on the raise.
- **Defaults**: `outgoing_heartbeat.url`, `outgoing_heartbeat.interval`, `outgoing_heartbeat.method`,
  `outgoing_heartbeat.timeout`, `oidc.secret_expiry_lead`, `MUSTER_RUNBOOK_BASE_URL`, `replica.live_expiry`,
  `leader.absence_notice`.

## Steps

1. Write the catalogue check and the label policy test. Check: removing a metric from the registry, changing a label in
   reference.md or editing the generated page each fail `make generate-check`.
2. Register `MusterOIDCSecretExpiring`, write the expiry task and the `runbook_url` annotation. Check:
   `expiry_test.go` with a manual clock covers C-19.AC-5 and the removed date.
3. Write the outgoing heartbeat setting and sender. Check: `sender_test.go` covers one send per interval, the proxy,
   the failure without retry and nothing without a URL or without leadership.
4. Write the System status read. Check: `status_test.go` fills every section from fixtures.
5. Write the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-039: the Admin's session (`jar`, `H`), the Responder "bob" (jar `bob`), the Mattermost Connection "mm" (C)
# with MKD, the Destination "alerts" (D, ch-alerts), the Integration "lab" with FIRE, ADV and AG of S-049, the Route "t"
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FWH=127.0.0.1:18093/_fake; CLK=localhost:8082/_dev/clock
PINGS() { curl -s $FWH/requests | jq '[.[] | select(.path == "/ping/dev")] | length'; }
DAYS() { curl -s $CLK | jq -r --argjson d "$1" '.now | .[0:10] + "T00:00:00Z" | fromdate + $d * 86400 | strftime("%Y-%m-%d")'; }
FIRING() { curl -s -b jar "$API/integrations/$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" | jq -c '[.items[].labels.alertname]'; }

# C-19.FR-7, AC-1, first and on real time, before the development clock is advanced: the outgoing heartbeat is sent by
# the Leader only; with no replica leading nothing is sent, and it resumes with a Leader
curl -s -b jar $API/system-status | jq -c .outgoing_heartbeat   # {"configured":false,"last_attempt_at":null,"last_ok":null,"last_error":null}
O=$(curl -s -b jar $API/organization)
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization \
  -d "$(jq -c 'del(.id, .etag) | .outgoing_heartbeat = {url: "http://127.0.0.1:18093/ping/dev", proxy: {enabled: false}}' <<<"$O")" \
  | jq -c .outgoing_heartbeat.url_status.set                # true
sleep 65; PINGS                                             # 1   (or 2)
curl -s $FWH/requests | jq -r '[.[] | select(.path == "/ping/dev")][-1].method'   # GET
curl -s -b jar "$API/audit-log?action=organization.updated&limit=1" | jq -c '.items[0].diff[] | select(.field == "outgoing_heartbeat.url")'
# {"field":"outgoing_heartbeat.url","secret_changed":true}
KEY=$(psql "$MUSTER_DATABASE_URL" -Atc "SELECT (classid::bigint << 32) | objid::bigint FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 AND granted")
kill %1; wait; psql "$MUSTER_DATABASE_URL" -c "SELECT pg_advisory_lock($KEY); SELECT pg_sleep(900)" > /dev/null & LOCK=$!
make dev > dev.log 2>&1 & sleep 10
curl -s localhost:8082/metrics | grep '^muster_leader '      # muster_leader 0
P0=$(PINGS); sleep 130; echo $(( $(PINGS) - P0 ))           # 0
curl -s -b jar $API/system-notices | jq -r '.items[].kind'   # no_replica_leading
kill $LOCK; sleep 10; curl -s localhost:8082/metrics | grep '^muster_leader '   # muster_leader 1
P0=$(PINGS); sleep 65; echo $(( $(PINGS) - P0 )) | grep -cE '^[12]$'           # 1

# C-19.AC-7: once per interval of Muster's clock; a failure is counted, logged and retried by the next tick
P0=$(PINGS); ADV 60; ADV 60; ADV 60; sleep 1; echo $(( $(PINGS) - P0 ))   # 3   (4 if a real minute also passed)
curl -s -b jar $API/system-status | jq -c '.outgoing_heartbeat | {configured, last_ok}'   # {"configured":true,"last_ok":true}
curl -s -X POST $FWH/faults -d '{"path":"/ping/dev","status":503}' > /dev/null; ADV 60; sleep 1
curl -s -b jar $API/system-status | jq -c '.outgoing_heartbeat | {last_ok, e: (.last_error | test("503"))}'   # {"last_ok":false,"e":true}
grep -c '"event":"outgoing_heartbeat_failed"' dev.log       # 1
curl -s localhost:8082/metrics | grep -c 'muster_client_requests_total{client="heartbeat",outcome="transient"}'   # 1
curl -s -X DELETE $FWH/faults > /dev/null; ADV 60; sleep 1
curl -s -b jar $API/system-status | jq -r .outgoing_heartbeat.last_ok   # true

# C-19.FR-2, AC-3: the catalogue check and the label policy
make generate-check && echo ok                              # ok
grep -o '`muster_[a-z_]*`' design/prd/l1/reference.md | tr -d '`' | sort -u | while read m; do grep -q "| \`$m\` |" docs/reference/metrics.md || echo "missing $m"; done
#                                                            (no output)
curl -s localhost:8082/metrics | grep '^muster_' | grep -o '{[^}]*}' | tr ',' '\n' | sed 's/=.*//; s/[{}]//g' | sort -u | tr '\n' ' '
# by client code command commit destination integration kind le method name outcome reason route route_pattern state status template transport version
curl -s localhost:8082/metrics | grep '^muster_' | grep -cE '(alertname|severity|user|number|org_id)='   # 0

# C-19.FR-5, AC-5, FR-9: the OIDC client secret expires in 10 days, then in 30
OIDC() { S=$(curl -s -b jar $API/oidc-settings); curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$S")" $API/oidc-settings \
  -d "$(jq -c --arg d "$1" 'del(.etag, .client_secret_status, .warnings, .updated_at) | .client_secret_expires_on = (if $d == "" then null else $d end)' <<<"$S")" > /dev/null; }
OIDC "$(DAYS 10)"; ADV 300; FIRING                          # ["MusterOIDCSecretExpiring"]
curl -s -b jar "$API/integrations/$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" \
  | jq -r '.items[0] | "\(.labels.severity) \(.annotations.runbook_url)"'
# warning https://muster-io.github.io/muster/latest/operations/runbooks/MusterOIDCSecretExpiring/
OIDC "$(DAYS 30)"; ADV 300; FIRING                          # []
grep -c '"event":"oidc_secret_expiring"' dev.log            # 1

# C-19.AC-2, FR-10, AC-6: a Broken Destination reaches the admin Destination; System status shows it; a Responder gets 403
OPS=$(MKD ops ch-alerts-prod | jq -r .destination.id)
curl -s "${H[@]}" -X POST $API/route-suggestions/internal_alerts/accept -d "{\"destination_ids\":[\"$OPS\"]}" > /dev/null
curl -s -X DELETE $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
G=$(FIRE s1 Broken t); sleep 2
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-alerts-prod" and .root_id == "")] | last | .props.attachments[0].title'   # #… MusterDestinationBroken
curl -s -b jar $API/system-status | jq -c '{b: [.broken_destinations[] | {n: .destination.name, r: (.reason | test("403"))}], l: [.replicas[] | select(.leader) | (.key_ids | length > 0)]}'
# {"b":[{"n":"alerts","r":true}],"l":[true]}
curl -s -o /dev/null -w '%{http_code}\n' -b bob $API/system-status   # 403
```

`test/e2e/self_observation_test.go` repeats these steps and adds: two replicas, where only the Leader sends the
outgoing heartbeat and a handover keeps one send per interval (C-19.AC-7); an Integration with the Heartbeat on whose
signals stop, listed under `integrations_attention` as `heartbeat_lost`; a truncated Snapshot listed as
`snapshot_truncated`; a failing Route template under `template_errors`; a Storm under `active_storms`; the recovery
state after a simulated downtime; and the heartbeat through the fake HTTP proxy.

## Open questions

None.

## Notes

- Suggested commit: `feat(observability): add the outgoing heartbeat, system status and the oidc secret expiry alert`.
- Split: the chart rules, their unit tests and the dashboard moved to S-059, which follows the runbook pages of S-058,
  so that this story stays a backend slice with a live check.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-19.FR-2 | full | |
| C-19.FR-5 | full | |
| C-19.FR-7 | partial | the setting in the API and the sender; the settings page is S-054 |
| C-19.FR-9 | partial | Internal alerts; the chart rules are S-059, the pages S-058 |
| C-19.FR-10 | partial | the read; the page is S-054 |
| C-19.AC-1 | partial | no outgoing heartbeat without a Leader; `MusterNoLeader` is S-059 |
| C-19.AC-2 | full | |
| C-19.AC-3 | full | |
| C-19.AC-5 | full | |
| C-19.AC-6 | partial | the read and the `403`; the page is S-054 |
| C-19.AC-7 | full | |
| C-02.FR-10 | partial | the outgoing heartbeat and the OIDC secret expiry check join the Leader tasks |
