---
id: S-023
title: Heartbeat endpoint, states, Leader checks, Internal alert and staleness pause (BE)
capability: C-07
kind: be
layer: L1
depends_on: [S-021]
covers: [C-07.FR-1, C-07.FR-2, C-07.FR-3, C-07.FR-4, C-07.FR-5, C-07.FR-6, C-07.FR-8, C-07.AC-1, C-07.AC-2, C-07.AC-3, C-07.AC-4, C-07.AC-5, C-06.FR-6, C-06.FR-7, C-06.FR-8, C-06.FR-9, C-06.FR-10, C-06.FR-15, C-06.FR-16, C-06.AC-3, C-05.FR-1, C-02.FR-10, C-01.FR-13]
files_touched:
  - internal/heartbeat/handler.go
  - internal/heartbeat/heartbeat.go
  - internal/heartbeat/check.go
  - internal/heartbeat/snippet.go
  - internal/heartbeat/query.sql
  - internal/heartbeat/heartbeat_test.go
  - internal/heartbeat/check_test.go
  - internal/ingest/stale.go
  - internal/ingest/stale_test.go
  - internal/ingest/facts_test.go
  - internal/ingest/query.sql
  - internal/internalalerts/registry.go
  - internal/integrations/integrations.go
  - internal/integrations/tokens.go
  - internal/integrations/integrations_test.go
  - internal/api/integrations.go
  - internal/api/integrations_test.go
  - internal/server/server.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakealertmanager/fakealertmanager.go
  - internal/fakes/fakealertmanager/scenarios.go
  - internal/fakes/fakealertmanager/fakealertmanager_test.go
  - internal/devmode/devmode.go
  - test/e2e/heartbeat_test.go
  - docs/integrations/heartbeat.md
acceptance:
  - "[C-07.FR-2, C-07.FR-3, C-05.FR-1] `heartbeat.enabled` is off by default; turning it on moves the state from `not_configured` to `waiting`, with `timeout_seconds` defaulting to `integration.heartbeat_timeout`; turning it off returns to `not_configured` and resolves an open `MusterHeartbeatLost`."
  - "[C-07.FR-1, C-07.AC-3] `GET` and `POST` on `/api/v1/heartbeat` with one of the Integration's tokens as `Authorization: Bearer` or in the path answer 204 whatever the body; a wrong token, a revoked one or an API token answers 401; no log line contains a token from the path."
  - "[C-07.FR-3, C-07.FR-4, C-07.AC-1] The first signal moves `waiting` to `live` without raising anything; `integration.heartbeat_timeout` without a signal makes the Integration `lost` and raises `MusterHeartbeatLost` with `severity=critical`, `integration`, `integration_name` and the Integration's Static labels as a firing Alert of the built-in Integration; the next signal makes it `live` and resolves that Alert."
  - "[C-07.AC-2] Before the first signal nothing is raised however long it takes, and Alerts of the Integration never go Stale."
  - "[C-07.FR-5, C-06.FR-8, C-06.FR-9, C-06.FR-10, C-06.FR-15, C-07.AC-4, C-06.AC-3] With a live Heartbeat and a learned 5-minute interval, an Alert whose Alertmanager group last arrived at T resolves as `stale` at T+15 min; with no signal from T to T+10 min it resolves at T+25 min; an Integration without a Heartbeat never resolves an Alert as Stale (C-06.AC-3)."
  - "[C-06.FR-6, C-06.FR-7] A truncated `groupKey` keeps its unlisted Alerts alive while its Snapshots arrive, and stops being truncated once none has arrived for `stale_after` counted like staleness; an Alert that went Stale in one `groupKey` while another still lists it keeps firing."
  - "[C-07.FR-8, C-02.FR-10] Only the Leader runs the Heartbeat check and the Stale scan, both safe to run twice; `muster_heartbeat_lost{integration}` is 1 while the Integration is lost and is exported only by the Leader."
  - "[C-07.FR-5, C-07.AC-5, C-06.FR-16] While lost, the Integration carries the warning `heartbeat_lost` with the time of the last signal; it carries `heartbeat_not_configured` without a Heartbeat and `heartbeat_waiting` before the first signal; deleting it resolves its `MusterHeartbeatLost`."
  - "[C-07.FR-6] With the Heartbeat on, `createIntegrationToken` also returns a `heartbeat_snippet` with an always-firing rule, a route with `repeat_interval` set to `snippet.heartbeat_repeat_interval` and a receiver pointing at the Heartbeat URL with the token; docs/integrations/heartbeat.md explains it and says that a cron job also works but proves less."
  - "[C-01.FR-13] The fake Alertmanager sends Heartbeat signals at an interval, on request or not at all, and reproduces F-045 and F-047, which a test runs through the Stale scan."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-023. Heartbeat endpoint, states, Leader checks, Internal alert and staleness pause (BE)

## Scope

**IN**

- Heartbeat settings on Integrations, the Heartbeat snippet and the four states.
- The Heartbeat endpoint on the ingest listener and the liveness clock it advances.
- The Leader's Heartbeat check with `MusterHeartbeatLost` and its metric.
- The Leader's Stale scan with the end of truncation by time, and the Heartbeat warnings.
- The fake Alertmanager's Heartbeat sender, the Heartbeat in `muster dev`, and the documentation page.

**OUT**

- The Heartbeat section, badges and banners in the UI (S-024).
- The Route suggestion for `MusterHeartbeatLost` (S-026).

## Contracts

- **Operations implemented**: `heartbeatGet`, `heartbeatPost`, `heartbeatGetWithPathToken`,
  `heartbeatPostWithPathToken`; `createIntegration` and `updateIntegration` accept `heartbeat.enabled: true`;
  `HeartbeatInfo` carries `state`, `last_signal_at` and `lost_since`; `IntegrationTokenCreated.heartbeat_snippet`;
  `IntegrationWarning` gains `heartbeat_not_configured`, `heartbeat_waiting` and `heartbeat_lost`.
- **Settings and states** (C-07.FR-2, FR-3; `integrations`): `integration.heartbeat` off by default;
  `integration.heartbeat_timeout` when `timeout_seconds` is omitted. Turning it on from `not_configured` gives
  `waiting`; turning it off gives `not_configured`, clears `heartbeat_lost_since` and resolves an open
  `MusterHeartbeatLost`. These runtime columns never touch `version`; every change of the state sends the live hint
  `integration` with the id. The built-in Integration stays refused (S-021).
- **Endpoint** (C-07.FR-1; ingest listener): `GET` or `POST` on `/api/v1/heartbeat` (`Authorization: Bearer`) and
  `/api/v1/heartbeat/{ingest_token}`; the token rules of S-018 (`401` otherwise); the body is discarded unread up to
  `ingest.body_limit`. Each signal runs one short transaction that locks the Integration row: `waiting` → `live`
  (nothing raised, the clock not advanced); `live` → the gap since `heartbeat_last_signal_at` is added to
  `liveness_clock_ms` when it is at most `heartbeat_timeout_seconds`; `lost` → `live`, `heartbeat_lost_since` cleared,
  `MusterHeartbeatLost` resolved, the clock not advanced; then `heartbeat_last_signal_at` = now. An Integration whose
  Heartbeat is off answers `204` and records nothing (C-07.FR-1). Lines about it name the route pattern, and the
  lint-5 probe sends a known token in the path.
- **Heartbeat check** (C-07.FR-4, FR-8, ADR-0007; Leader task every 10 seconds): a `live` Integration whose last signal
  is older than its timeout becomes `lost` with `heartbeat_lost_since` = the last signal, and raises
  `MusterHeartbeatLost` through S-021 — labels `alertname`, `severity=critical`, `integration`, `integration_name` and
  the Integration's Static labels. The timeout is measured from the later of the last signal and the moment the
  current Leader started leading after a recorded downtime (`runtime_state.leader_since`, C-07.FR-4). `waiting` never
  becomes `lost`. This check and the Stale scan below run per Organization: each iterates over the Organizations (one
  in L1) and passes `org_id` to every query (lint 1).
- **Registry**: `MusterHeartbeatLost` (severity `critical`; labels `integration`, `integration_name`, the Static labels)
  joins the registry of S-021; deletion of an Integration (S-021) and a rename resolve or relabel it like
  `MusterSnapshotTruncated`.
- **Stale scan** (C-06.FR-6, FR-7, FR-8, FR-9, FR-10; Leader task every 30 seconds, in `internal/ingest`): for each
  Integration whose Heartbeat is `live` — never `waiting`, `lost`, off, or the built-in one — it takes the Integration's
  ingestion claim of S-020, so it never runs beside its processing, and computes the effective clock E =
  `liveness_clock_ms` plus the time since the last signal when that is at most the timeout. An active presence is
  `stale` when E minus its reference exceeds `stale_after` of its Alertmanager route, the reference being
  `last_seen_clock_ms` — or, while its `groupKey` is truncated, the group's `last_snapshot_clock_ms`. An Alert whose
  presences are all `gone` or `stale` resolves with the reason `stale` and the text of C-06.FR-10 (Alert change
  `resolved`, `muster_alerts_resolved_total{reason="stale"}`). A truncated `groupKey` whose `last_snapshot_clock_ms` is
  more than `stale_after` behind E stops being truncated, which may resolve `MusterSnapshotTruncated`. Time with the
  Heartbeat lost and a Muster downtime longer than the timeout never advance the clock, so they never count
  (C-07.FR-5); a shorter downtime counts, as C-06.FR-9 accepts.
- **Snippet** (C-07.FR-6): with the Heartbeat on, `heartbeat_snippet` holds a Prometheus rule `MusterHeartbeat` with
  `expr: vector(1)` and the note that kube-prometheus-stack's `Watchdog` serves instead; an Alertmanager route matching
  that alert with `continue: true`, `group_wait: 0s`, `group_interval: 1m` and `repeat_interval` set to
  `snippet.heartbeat_repeat_interval`; and a receiver whose `webhook_configs` entry points at
  `<MUSTER_INGEST_URL>/api/v1/heartbeat` with `send_resolved: false` and the token. It is `null` while the Heartbeat is
  off.
- **Warnings** (C-07.FR-5): `heartbeat_not_configured`, `heartbeat_waiting`, and `heartbeat_lost` with `since` = the
  last signal, on every Integration except the built-in one.
- **Metrics and log events**: `muster_heartbeat_lost{integration}` (Leader gauge); `heartbeat_live` (INFO:
  `integration`, `first`), `heartbeat_lost` (WARN: `integration`, `last_signal_at`), `alerts_stale` (INFO:
  `integration`, `count`; only when a scan resolves something).
- **Fake Alertmanager** (C-01.FR-13): `POST /_fake/heartbeats` `{"name", "url", "token", "token_in", "method",
  "interval_seconds"}` starts a sender; `POST /_fake/heartbeats/{name}/send` sends one signal now; `DELETE
  /_fake/heartbeats/{name}` stops it (a cut network). `scenarios.go` adds F-045 (a muted group sends nothing, so its
  Alerts go Stale) and F-047 (a resolve during a mute arrives when the mute ends within about 14 minutes), and
  `facts_test.go` runs them through the Stale scan.
- **Development mode**: the Integration `dev-alertmanager` has its Heartbeat on, and the fake Alertmanager signals it
  every 60 seconds.
- **Documentation** (C-07.FR-6): `docs/integrations/heartbeat.md` — why the Heartbeat is part of the recommended setup,
  the rule, route and receiver of the snippet, `Watchdog` in kube-prometheus-stack, a cron job as an alternative that
  proves less, what "lost" means and that Stale resolution pauses meanwhile.

## Steps

1. Write the settings, the states and the endpoint with the liveness clock. Check: tests with a manual clock cover each
   transition and the gap rule.
2. Write the Heartbeat check, the Internal alert and the metric. Check: tests cover C-07.AC-1, AC-2 and the measure
   from the Leader's start after downtime.
3. Write the Stale scan with truncation expiry. Check: tests cover C-07.AC-4, a truncated group kept alive, an Alert
   stale in one `groupKey` but listed in another, and a second run changing nothing.
4. Write the snippet, the warnings, the fake sender, the fact scenarios and the documentation. Check: Verification
   below and `make test-integration` with F-045 and F-047.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-020: an Integration "hb" with the Static label env=prod (id INT), its token TOK registered as the fake
# receiver "hb", and the helpers ADV and NOTIFY. The clock jumps by hours below, longer than a session lives, so the
# checks use a Personal access token with all of the Admin's Permissions:
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
PAT=$(curl -s "${H[@]}" -d "{\"name\":\"verify\",\"permissions\":$(curl -s -b jar $API/me | jq -c .permissions)}" \
  $API/me/personal-access-tokens | jq -r .value)
A=(-H "Authorization: Bearer $PAT" -H 'Content-Type: application/json')
VIEW() { curl -s "${A[@]}" "$API/integrations/$INT/alerts?$1"; }
BI=$(curl -s "${A[@]}" $API/integrations | jq -r '.items[] | select(.builtin) | .id')
FIRING_BUILTIN() { curl -s "${A[@]}" "$API/integrations/$BI/alerts?state=firing"; }
HB() { curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $TOK" localhost:8081/api/v1/heartbeat; }

curl -s "${A[@]}" "$API/integrations/$INT" | jq -c '{h: .heartbeat.state, w: [.warnings[].kind]}'
# {"h":"not_configured","w":["heartbeat_not_configured"]}

# C-06.AC-3: without a Heartbeat nothing goes Stale, with the scan running
curl -s -X PUT $FAM/groups/s1 -d '{"receiver":"hb","route":"{}/{team=\"x\"}","labels":{"alertname":"Quiet"}}' > /dev/null
curl -s -X PUT $FAM/groups/s1/alerts/q -d '{"labels":{"svc":"a"}}' > /dev/null
NOTIFY s1 '{"reason":"first notification"}'
for k in 1 2 3; do ADV 300; NOTIFY s1 '{"reason":"repeat interval elapsed"}'; done        # learned: 5 min
ADV 108000; VIEW 'state=firing&label=svc%3D%22a%22' | jq '.items | length'                # 1   (30 h later)

# turn the Heartbeat on: waiting; C-07.AC-2
I=$(curl -s "${A[@]}" "$API/integrations/$INT")
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$I")" "$API/integrations/$INT" \
  -d "$(jq -c '{name, description, connection_mode, static_labels, duplicate_window_seconds, heartbeat: {enabled: true}}' <<<"$I")" \
  | jq -c '{state: .heartbeat.state, timeout: .heartbeat.timeout_seconds, w: [.warnings[].kind]}'
# {"state":"waiting","timeout":300,"w":["heartbeat_waiting"]}
ADV 3600; FIRING_BUILTIN | jq '.items | length'                                           # 0

# C-07.AC-3
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" localhost:8081/api/v1/heartbeat         # 204
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d 'anything' "localhost:8081/api/v1/heartbeat/$TOK"           # 204
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Authorization: Bearer mstr_int_wrong' localhost:8081/api/v1/heartbeat
# 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $PAT" localhost:8081/api/v1/heartbeat  # 401
grep -c "$TOK" dev.log                                                                                           # 0
curl -s "${A[@]}" "$API/integrations/$INT" | jq -r .heartbeat.state                                              # live

# C-07.AC-1: lost after the timeout, with the Static labels; the next signal resolves it
ADV 360
curl -s "${A[@]}" "$API/integrations/$INT" | jq -c '{s: .heartbeat.state, w: [.warnings[] | {kind, since}]}'
# {"s":"lost","w":[{"kind":"heartbeat_lost","since":"…"}]}
FIRING_BUILTIN | jq -c '.items[0].labels'
# {"alertname":"MusterHeartbeatLost","env":"prod","integration":"NT…","integration_name":"hb","severity":"critical"}
curl -s localhost:8082/metrics | grep "muster_heartbeat_lost{integration=\"$INT\"}"                              # … 1
HB; sleep 1; FIRING_BUILTIN | jq '.items | length'                                                             # 0

# C-07.AC-4: the group of "q" last arrives at T; one signal a minute
NOTIFY s1 '{"reason":"repeat interval elapsed"}'                                          # T
for m in $(seq 1 16); do ADV 60; HB > /dev/null; done                                    # T+16 min
VIEW 'state=resolved&label=svc%3D%22a%22' | jq -r '.items[0].resolve_reason'                                   # stale
# again with a loss: "q" fires again at T2, and no signal comes from T2 to T2+10 min
NOTIFY s1 '{"reason":"first notification"}'                                               # T2
ADV 600; HB > /dev/null                                                                   # lost from T2+5, live at T2+10
for m in $(seq 1 14); do ADV 60; HB > /dev/null; done                                    # T2+24 min
VIEW 'state=firing&label=svc%3D%22a%22' | jq '.items | length'                                                 # 1
for m in 1 2; do ADV 60; HB > /dev/null; done                                            # T2+26 min
VIEW 'state=resolved&label=svc%3D%22a%22' | jq -r '.items[0].resolve_reason'                                   # stale

# C-07.FR-6: the Heartbeat snippet
curl -s "${A[@]}" -d '{"name":"hb-2"}' "$API/integrations/$INT/tokens" | jq -r .heartbeat_snippet \
  | grep -E 'vector\(1\)|repeat_interval|/api/v1/heartbeat'
#     expr: vector(1)
#     repeat_interval: 1m
#   - url: http://localhost:8081/api/v1/heartbeat

# C-07.AC-5: deletion resolves MusterHeartbeatLost
ADV 360; FIRING_BUILTIN | jq '.items | length'                                            # 1
curl -s -o /dev/null "${A[@]}" -X DELETE "$API/integrations/$INT"; sleep 1
FIRING_BUILTIN | jq '.items | length'                                                     # 0
```

## Open questions

None.

## Notes

- Suggested commit: `feat(heartbeat): add the heartbeat endpoint, leader checks and the stale scan`.
- The Heartbeat check and the Stale scan are the Leader tasks that S-008 reserved for this story; both are idempotent,
  so two overlapping Leaders resolve nothing twice.
- The Stale scan reuses the resolution path of processing: its Alert changes reach routing and grouping like any other.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-07.FR-1 | full | |
| C-07.FR-2 | partial | the API; the section of the form is S-024 |
| C-07.FR-3 | partial | the states; the badges are S-024 |
| C-07.FR-4 | full | |
| C-07.FR-5 | partial | the pause and the warning; the banner is S-024 |
| C-07.FR-6 | partial | the snippet and the documentation; showing the snippet is S-024 |
| C-07.FR-8 | full | |
| C-07.AC-1 | full | |
| C-07.AC-2 | full | |
| C-07.AC-3 | full | |
| C-07.AC-4 | full | |
| C-07.AC-5 | partial | deletion; the page text is S-024 |
| C-06.FR-6 | full | together with S-020 and S-021 |
| C-06.FR-7 | full | together with S-020 |
| C-06.FR-8 | full | together with S-020 |
| C-06.FR-9 | full | |
| C-06.FR-10 | full | together with S-020 |
| C-06.FR-15 | full | together with S-020 and S-021 |
| C-06.FR-16 | full | together with S-021 |
| C-06.AC-3 | full | together with S-021 and S-022 |
| C-05.FR-1 | partial | the Heartbeat settings; the section of the form is S-024 |
| C-02.FR-10 | partial | the Heartbeat check and the Stale scan join the Leader tasks |
| C-01.FR-13 | partial | the fake Heartbeat sender and the Heartbeat of the demo Integration |
