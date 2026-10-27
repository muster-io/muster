---
id: S-149
title: Broken Notification gateways with MusterNotificationGatewayBroken, its chart rule and runbook, and the probe (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-148]
covers: [C-30.FR-8, C-30.AC-5]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0038_gateway_broken.up.sql
  - internal/db/migrations/0038_gateway_broken.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/gateways/broken.go
  - internal/gateways/broken_test.go
  - internal/gateways/broken_integration_test.go
  - internal/gateways/sender.go
  - internal/gateways/query.sql
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/internalalerts_test.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/runtime/runtime.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - deploy/helm/muster/files/rules.yaml
  - deploy/helm/muster/values.yaml
  - deploy/helm/muster/values.schema.json
  - deploy/helm/muster/tests/rules/MusterNotificationGatewayBroken.yaml
  - docs/operations/runbooks/MusterNotificationGatewayBroken.md
  - docs/operations/runbooks/index.md
  - docs/operations/monitoring.md
  - mkdocs.yml
  - design/prd/l1/reference.md
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - web/src/test/fixtures/connections.ts
  - test/e2e/gateway_broken_test.go
acceptance:
  - "[C-30.FR-8, C-30.AC-5] A gateway whose call the fake answers `401` — `forbidden`, a Fatal answer — or whose call used up `delivery.transient_budget` becomes Broken: the Bridge Connection reads `gateway` {`broken` true, `broken_since`, `reason`}, `MusterNotificationGatewayBroken` is raised with `connection` and `connection_name`, and the next run of the Leader task `gateway_broken_gauge` sets `muster_notification_gateway_broken{connection}` to 1 (`broken_test.go`, `broken_integration_test.go`)."
  - "[C-30.FR-8, C-30.AC-5] While a gateway is Broken, every call that comes due ends Not delivered with the reason `gateway_broken` and records `page_not_delivered`, and none of them is sent after it recovers; the sender probes it through `info` every `delivery.broken_probe_interval`, and a successful probe — or a successful Connection check — ends the state, resolves the alert and sets the gauge to 0 at the next run (`broken_integration_test.go`)."
  - "[C-30.FR-8, C-30.AC-5] A Bridge Connection with both roles whose gateway is Broken keeps its Destinations healthy and delivering, and a Broken Destination of it leaves the gateway as it is (`broken_integration_test.go`)."
  - "[C-30.FR-8] The chart renders the rule `MusterNotificationGatewayBroken` — `max by (connection) (muster_notification_gateway_broken{job=\"muster\"}) == 1`, `for` 5m, severity `critical` — with its threshold and `for` as chart values and its `runbook_url`; its rule unit test fires after 5 minutes at 1 and not at 0; reference.md moves the Internal alert, the chart rule and the metric into the main tables; P-88 is confirmed for both chart rules of L2; `make rules-test helm-check docs-check` pass."
  - "[C-30.FR-8] The runbook page MusterNotificationGatewayBroken says what the alert means, how to read the reason on the Connection page and in the Request log, how to fix the bridge's credentials or its provider, and that calls due meanwhile were not sent; it is in the runbook index and the monitoring page; `make docs-check` passes."
verify: "make ci test-integration e2e-go rules-test helm-check docs-check"
operator_attention: true
issue: 438
---

# S-149. Broken Notification gateways with MusterNotificationGatewayBroken, its chart rule and runbook, and the probe (BE)

## Scope

**IN**

- The Broken state of a Notification gateway — a Fatal answer to a call, or a call that used up its retry budget — kept
  apart from the state of the bridge's Destinations, with its reason (C-30.FR-8).
- The probe through `info` every `delivery.broken_probe_interval`, and the end of the state on a successful probe or
  Connection check.
- Calls due while Broken: Not delivered, never sent after recovery (P-68, decided).
- The Internal alert `MusterNotificationGatewayBroken`, the Leader's gauge `muster_notification_gateway_broken`, the
  chart rule of the same name with its rule unit test, and the runbook page.

**OUT**

- The Connection page's health and Broken banner: S-150.
- Thresholds of the chart rules beyond P-88's confirmation, which the maintainer gives with this story.

## Contracts

- **Spec** (`api/openapi.yaml`): `BridgeConnection` gains the required, read-only `gateway` (`GatewayState` {`broken`,
  `broken_since`, `reason` — a masked, untrusted text like a Destination's — and `next_probe_at`} or null when the
  Connection is not a gateway); `PageNotDeliveredReason` gains `gateway_broken`.
- **Schema** (migration `0038_gateway_broken`, expand only; `schema.md` §4.5): `connections.gateway_broken_since
  timestamptz`, `connections.gateway_broken_reason text`, `connections.gateway_next_probe_at timestamptz`,
  `connections.gateway_probe_lease_until timestamptz` and `gateway_probe_lease_owner text` (with the lease CHECK); a
  partial index `(org_id, gateway_next_probe_at) WHERE gateway_broken_since IS NOT NULL`. The down migration drops them.
- **Gateways** (`internal/gateways/broken.go`, `sender.go`): the sender marks the gateway Broken on a `forbidden` answer
  or a call whose budget ran out, records `gateway_broken` (warn; the Connection, the reason) and raises the Internal
  alert; while Broken, a call that comes due ends Not delivered with `gateway_broken` without being sent; the probe is
  claimed like a call by any replica, with a lease, every `delivery.broken_probe_interval`, and calls `getInfo`; a
  successful probe, or a successful `checkConnection` of the Connection (`internal/connections/bridge.go`), clears the
  state, records `gateway_recovered` (info) and resolves the alert. Only calls set the state; deliveries to the bridge's
  Destinations never do, and the state never touches them.
- **Internal alert** (`internal/internalalerts/registry.go`): `MusterNotificationGatewayBroken` — severity `critical`,
  entity `connection` with `connection_name` — "Notification gateway {name} is Broken", the description naming the
  reason and the runbook.
- **Leader task** (`internal/leader/tasks.go`): `gateway_broken_gauge`, appended to the closed list, every
  `delivery.broken_probe_interval`, sets `muster_notification_gateway_broken{connection}` to 1 for each Broken gateway
  and to 0 for the others, and removes the series of a deleted Connection; safe to run twice (ADR-0007). The Leader
  adds only this task; probes and calls run on every replica (NFR-2, NFR-4).
- **Metric** (`internal/metrics/catalogue.go`): `muster_notification_gateway_broken{connection}` (gauge, Leader), C-30.
- **Chart** (`deploy/helm/muster/files/rules.yaml`, `values.yaml`, `values.schema.json`): the rule of reference.md with
  its threshold and `for` as chart values; `tests/rules/MusterNotificationGatewayBroken.yaml`.
- **Shared tables**: reference.md moves `MusterNotificationGatewayBroken` from "Internal alerts added in L2" and "Chart
  rules added in L2", and `muster_notification_gateway_broken` from "Metrics added in L2", into the main tables;
  defaults.md marks the chart rules of L2 decided and L2.md §5.3 moves P-88 to "Decided since they were first listed".
- **Web test fixtures** (`web/src/test/fixtures/connections.ts`): `gateway` null in the Bridge Connection builders.
- **Log events**: `gateway_broken`, `gateway_recovered`, C-30.
- **Settings**: `delivery.broken_probe_interval`, `delivery.transient_budget`; the chart values of the rule (P-88).
- **Documentation**: the runbook page `docs/operations/runbooks/MusterNotificationGatewayBroken.md`, in the runbook
  index and the navigation; `docs/operations/monitoring.md` lists it among the Internal alerts with a chart rule.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the Broken state, the probe and the calls due while Broken; add the spec. Check: `make generate-check`;
   `broken_test.go` with a manual clock; `broken_integration_test.go` with a bridge of both roles.
3. Register the Internal alert, the Leader task and the gauge. Check: `internalalerts_test.go`, `leader_test.go`.
4. Add the chart rule, its test, the runbook and the shared rows. Check: `make rules-test helm-check docs-check`.
5. Write `test/e2e/gateway_broken_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-148: A, PETR with a confirmed contact at GW, CALL and the Alert Group G1; CLK.
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake; CLK=localhost:8082/_dev/clock
curl -s -X POST $FB/faults -d '{"path":"/v1/notifications","status":401,"times":1}' > /dev/null
# the probe through info fails until the recovery step below
curl -s -X POST $FB/faults -d '{"path":"/v1/info","status":503}' > /dev/null
CALL $G1; sleep 2
curl -s "${A[@]}" $API/connections/$GW | jq -c '.gateway | {broken, reason}'               # {"broken":true,"reason":"…401…"}
curl -s "${A[@]}" "$API/alert-groups?q=MusterNotificationGatewayBroken" | jq -r '.items[0].title'   # MusterNotificationGatewayBroken
curl -s -X POST $CLK -d '{"advance_seconds": 300}' > /dev/null; sleep 2
curl -s localhost:8082/metrics | grep "^muster_notification_gateway_broken{connection=\"$GW\"}"   # … 1
curl -s "${A[@]}" $API/connections/$GW | jq -r .gateway.broken                             # true (the probe failed)
N=$(curl -s $FB/notifications | jq length); CALL $G1; sleep 1
curl -s $FB/notifications | jq length | grep -qx "$N" && echo "not sent while Broken"      # not sent while Broken
curl -s "${A[@]}" "$API/alert-groups/$G1/timeline?kind=escalation" | jq -r '.items[0].reason'    # gateway_broken
# the probe succeeds at the next interval once the faults are gone: the state ends, nothing is sent afterwards
curl -s -X DELETE $FB/faults > /dev/null
curl -s -X POST $CLK -d '{"advance_seconds": 300}' > /dev/null; sleep 2
curl -s "${A[@]}" $API/connections/$GW | jq -r .gateway.broken                             # false
curl -s $FB/notifications | jq length | grep -qx "$N" && echo "nothing replayed"           # nothing replayed
helm template deploy/helm/muster | grep -A3 'alert: MusterNotificationGatewayBroken' | grep -c 'for: 5m'   # 1
```

`test/e2e/gateway_broken_test.go` repeats these checks, adds a call that used up its retry budget, a bridge with both
roles whose Destination keeps delivering while its gateway is Broken, and a Connection check that ends the state.

## Open questions

None.

## Notes

- Suggested commit: `feat(gateways): mark failing notification gateways broken and alert about them`.
- The maintainer confirms provisional values with this story (operator attention): P-88, the thresholds and windows of
  the chart rules of L2 — `MusterScheduleGap` (S-133) and `MusterNotificationGatewayBroken` — as reference.md has them.
- P-68 is decided (L2.md §5.3): calls due while Broken are not sent after recovery, as Pages that a Broken Destination
  could not deliver are not.
- Migration `0038` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-8 | partial | the state, the alert, the gauge, the rule and the runbook; the Connection page is S-150 |
| C-30.AC-5 | full | |
