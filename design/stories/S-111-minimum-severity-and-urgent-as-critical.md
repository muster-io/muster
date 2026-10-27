---
id: S-111
title: "Minimum severity, Urgent as critical, and the expand step of the urgent mark and of \"critical is Urgent\" (BE)"
capability: C-26
kind: be
layer: L2
depends_on: []
covers: [C-26.FR-8, C-26.FR-9, C-26.FR-10, C-26.FR-13, C-26.AC-7, C-26.AC-8, C-26.AC-11, C-08.FR-1, C-08.FR-6, C-09.FR-1, C-09.FR-9, C-09.AC-8, C-11.FR-3, C-20.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0021_minimum_severity.up.sql
  - internal/db/migrations/0021_minimum_severity.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/routing/routes.go
  - internal/routing/profiles.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/routing/suggestions.go
  - internal/routing/suggestions_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/api/alertgroups.go
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/groups/grouping.go
  - internal/groups/grouping_test.go
  - internal/groups/transitions.go
  - internal/groups/transitions_test.go
  - internal/groups/commands.go
  - internal/groups/read.go
  - internal/groups/events.go
  - internal/groups/query.sql
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - internal/groups/groups_integration_test.go
  - internal/groups/dispatcher_test.go
  - internal/groups/list_test.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - web/src/components/timeline.tsx
  - test/e2e/routing_test.go
  - test/e2e/minimum_severity_test.go
  - test/e2e/routing_preview_test.go
  - docs/concepts/routes-and-group-keys.md
  - docs/install/versions.md
acceptance:
  - "[C-26.FR-8, C-08.FR-6, C-09.FR-1, C-08.FR-1] The Severity level of an Alert Group is the highest Severity level of its firing Alerts raised to its Route's `minimum_severity` (`none`, `warning`, `critical`), and `urgent` is true exactly when that level is critical, whatever `routes.urgent` and `organizations.critical_is_urgent` hold; a Route with the Minimum severity critical makes a warning Alert's Alert Group critical and Urgent, while `listIntegrationAlerts`, `listAlertGroupAlerts` and the `alerts[]` of its `created` webhook event show the Alert as warning (`transitions_test.go`, `minimum_severity_test.go`)."
  - "[C-26.FR-8, C-09.FR-9, C-11.FR-3] Urgency keeps deciding what it decided in L1 — the order of the delivery queue, individual publication during a Storm, the end of a Snooze or an acknowledgement on a rise, the `urgent` filter and column of `listAlertGroups`, `urgent` in outgoing webhook events and the count in Storm summaries — now from the stored Severity level alone: an acknowledged Alert Group of a Route without a Minimum severity whose Alerts become critical records the Loud `urgency_raised` mentioning `owner` and `rise_to_urgent` and becomes firing where `route.urgent_rise_removes_ack` is on, whatever `critical_is_urgent` held before the upgrade (`transitions_test.go`)."
  - "[C-26.FR-9, C-26.AC-7, C-26.AC-11, C-09.AC-8] Raising a Route's Minimum severity to critical writes one `open_group_updates` row of the kind `route_minimum_severity` in the transaction of `updateRoute`; a worker on any replica claims it with a lease and records, on each open warning Alert Group of the Route, the Quiet `urgency_raised` that removes nothing, with the `reason_code` `minimum_severity`, keeping an acknowledgement, a Snooze and its Owner; raising it to warning records the Quiet `severity_raised` with the same `reason_code` on its open info Alert Groups; lowering it records nothing and lowers their level; the open Root messages are re-rendered Quietly; a second run of the same row changes nothing (`updates_test.go`, `minimum_severity_test.go`)."
  - "[C-26.FR-10, C-26.AC-8, C-20.FR-1] Migration `0021_minimum_severity` gives every Route with `urgent` the Minimum severity critical, sets `organizations.critical_is_urgent` to true and computes the `severity_level` and `urgent` of every open Alert Group and of its pending deliveries once by the new rule, writing no Timeline entry, no delivery and no `open_group_updates` row; up, down and up again work on PostgreSQL 14 and 17 (`TestIntegrationMinimumSeverity`)."
  - "[C-26.FR-10, C-26.AC-8] After the migration a Route that was marked urgent reads `minimum_severity` `critical` and `urgent` true, and an `updateRoute` that sends `urgent: false` changes neither; `createRoute` without `urgent` is accepted, and one with `minimum_severity` `critical` stores `routes.urgent` true; `getOrganization` reads `critical_is_urgent: true` whatever `updateOrganization` sends, and an update without it is accepted; `listRouteProfiles` gives both profiles `minimum_severity` `none`."
  - "[C-26.FR-8] The suggested Route \"Muster: Heartbeat lost\" has the Minimum severity critical instead of the urgent mark, so accepting it still makes its Alert Groups critical and Urgent (`suggestions_test.go`, `routing_preview_test.go`)."
  - "[C-26.FR-13] The documentation page Routes and Group keys explains the Minimum severity under its own heading, says that an Urgent Alert Group is a critical one and that the urgent mark and \"critical is Urgent\" no longer act, and the upgrade notes of 0.2.0 in Requirements and tested versions say what C-26.FR-10 lists and open the paragraph on the rolling upgrade from 0.1; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 400
---

# S-111. Minimum severity, Urgent as critical, and the expand step of the urgent mark and of "critical is Urgent" (BE)

## Scope

**IN**

- The Route setting `route.minimum_severity` and the rule of C-26.FR-8: an Alert Group's Severity level is the highest
  of its firing Alerts raised to it, and it is Urgent exactly when that level is critical.
- The expand step of C-26.FR-10: `route.urgent` and `organization.critical_is_urgent` stop acting, stay in the API as
  read-only or ignored fields until 0.3.0, and are written so that a 0.1 replica of a rolling update computes the same
  urgency; the migration that converts urgent Routes and recomputes the open Alert Groups once.
- Applying a changed Minimum severity to the open Alert Groups of the Route (C-26.FR-9), through a durable row that any
  replica claims with a lease — the `open_group_updates` mechanism that S-113, S-163, S-121 (with S-170) and S-125
  reuse with kinds of their own.
- The parts of the documentation that this changes: the section on urgency of Routes and Group keys and the upgrade
  notes of 0.2.0.

**OUT**

- The rename of the Route profile On-call to Paging (`paging`) with every client that names it: S-162.
- The Minimum severity on the Route form and the retired urgent mark and "critical is Urgent" on the pages: S-112.
- Severity conditions of Destinations, and the Quiet publication of the Alert Groups that a raised Minimum severity
  brings into a Destination's condition, which S-121's reach makes through this story's transition (the second half of
  C-26.FR-9 and of C-26.AC-11): S-121. The publications and final edits of changed Route and Team Destinations: S-170.
- Escalation chains, which a change of the Minimum severity never starts: S-169. The page "What critical changes":
  S-124.
- 0.3.0 removes `urgent`, `critical_is_urgent` and their columns; not in 0.2.x.
- Interim state until S-112: the Route form still shows the urgent switch, whose value the server now ignores, and the
  Alert handling page still shows "critical is Urgent".

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - New schema `MinimumSeverity`: `none`, `warning`, `critical`, described as `route.minimum_severity`, "only raises
    the Severity level of the Route's Alert Groups; an Urgent Alert Group is a critical one".
  - `RouteInput` gains `minimum_severity` (optional; omitted on create is `none`, omitted on update keeps it) and loses
    `urgent` from its `required`; `urgent` becomes `deprecated: true` with the description "Read-only from 0.2.0: true
    exactly when `minimum_severity` is `critical`. Ignored in requests; removed in 0.3.0." `Route` lists
    `minimum_severity` and `urgent` in its `required`, so every read carries both.
  - `RouteProfile` gains `minimum_severity` (required, `none` for both profiles); its `urgent` stays, `false`, and is
    deprecated as on `RouteInput`.
  - `OrganizationBase` loses `critical_is_urgent` from its `required`; the field becomes `deprecated: true`, "Always
    true from 0.2.0: critical is Urgent. Ignored in requests; removed in 0.3.0."; `Organization` lists it in its
    `required`.
  - `NullableStatusChangeReason` gains `minimum_severity` ("raised by the Minimum severity of the Route");
    `TimelineAlertsEntry` gains `reason_code` (`NullableStatusChangeReason`), so that `severity_raised` carries it as
    `urgency_raised` does on `TimelineStatusEntry`; the alerts branch of the Timeline mapper in
    `internal/api/alertgroups.go` sets it.
  - The descriptions of `AgUrgent`, `AlertGroup.urgent`, `AlertGroup.severity_level` and `WebhookAlertGroup.urgent` say
    the new rule. No operation is added.
- **SPA type check** (`web/src/components/timeline.tsx`): `changeReason` switches over every
  `NullableStatusChangeReason` and ends in `unreachable`, so `make generate` would fail the TypeScript check without a
  case for `minimum_severity`; the case returns the server's `reason` text until S-112 translates it. No other
  exhaustive switch of the SPA covers a value this story adds.
- **Schema** (migration `0021_minimum_severity`, expand only; `schema.md` §4.8, §4.9 and §5):
  - `routes.minimum_severity text NOT NULL DEFAULT 'none' CHECK (minimum_severity IN ('none', 'warning',
    'critical'))`; the up migration sets it to `critical` where `urgent`.
  - `UPDATE organizations SET critical_is_urgent = true`.
  - For every Alert Group that is not resolved: `severity_level` = the highest level of the Alerts of its firing
    `alert_group_alerts` rows (`alerts.severity_level`), `info` when none fires — as `levelOf` of `transitions.go`
    gives it — raised to its Route's Minimum severity, and `urgent` =
    `severity_level = 'critical'`; the same `urgent` on its `pending` deliveries. Nothing else is written — no Timeline
    entry, no delivery, no change of a Snooze or an Owner.
  - New table `open_group_updates`: `id`, `org_id`, `kind text NOT NULL` with `open_group_updates_kind_check` (`kind IN
    ('route_minimum_severity')`; later stories replace it by `_v2`, `_v3`, … in story-map order), `route_id` (nullable,
    foreign key), `requested_at` (business clock), `after_alert_group_id bigint NOT NULL DEFAULT 0` (the cursor of a run
    that a lost lease interrupted), `lease_until` and `leased_by` (real clock); a CHECK that `route_minimum_severity`
    names a Route; the index `open_group_updates_due_idx (org_id, requested_at, id)` serves the claim. A row is deleted
    in the transaction of its last batch, so the table holds only work still to do. The schema's §5 "Queues and claims"
    describes the claim.
  - The down migration drops the table and the column and leaves `routes.urgent` and `critical_is_urgent` as the up
    migration left them.
- **Rolling update**: 0.2.0 writes `routes.urgent` = (`minimum_severity` = `critical`) on every create and update, and
  never writes `critical_is_urgent` again, so a 0.1 replica, which computes `routes.urgent OR (critical AND
  critical_is_urgent)`, finds the same urgency as 0.2.0.
- **Routing** (`internal/routing`): `Route`, `Input`, `Profile` and the view gain `MinimumSeverity`; `Input.Urgent` is
  ignored; a changed Minimum severity inserts the `open_group_updates` row in the update's transaction when the Route
  has open Alert Groups, and appears in the Audit log diff of `route.updated` (`/minimum_severity`, with `/urgent`
  changing with it). The suggestion `heartbeat_lost` of `suggestions.go` sets `MinimumSeverity: critical` instead of
  `Urgent: true`, as C-26 scenario 6 and the rule "Urgent exactly when critical" ask; `suggestions_test.go` and the
  Go end-to-end test of the suggestions (`test/e2e/routing_preview_test.go`, which asserts `Route.Urgent`) read the
  Minimum severity, with `urgent` still true.
- **Organization** (`internal/organization`, `internal/api/organization.go`): reads return `critical_is_urgent: true`;
  updates ignore the field, so it never appears in the diff of `organization.updated`.
- **Groups** (`internal/groups`): `urgentOf` becomes `level == critical`; the level of `grouping.go` and
  `transitions.go` is raised to the Route's Minimum severity (`routeInfo` gains it, the `GetRoute*` queries read it);
  the derived urgency of the list, counts and reads (`query.sql`) becomes `g.severity_level = 'critical'`. The fakes of
  the unit tests follow: the route rows of `dispatcher_test.go` gain `MinimumSeverity` and its emulation of the read's
  urgency (`urgent`) becomes the stored level; `TestUrgencyFollowsConfiguration` of `list_test.go`, which asserts the L1
  rule that an urgent Route makes its open Alert Groups Urgent, is replaced by a test of the new rule.
  `updates.go` is the worker of `open_group_updates` on every replica: woken at start, by a `NOTIFY` from the inserting
  transaction and by the development clock, it claims a row with `FOR UPDATE SKIP LOCKED` and a lease of 60 s on the
  real clock, renewed per batch, and walks the open Alert Groups of its scope by id from `after_alert_group_id` in
  batches of 50, each in its own dispatcher transaction as a system transition (`MinimumSeverityChanged`, Transport
  `system`, actor Muster): the level is computed again; a rise records the Quiet `severity_raised`, or, when it reaches
  critical, the Quiet `urgency_raised` of the row "removes nothing" of C-09.FR-22 — never the Loud one, since a change
  of configuration removes no acknowledgement and ends no Snooze (C-09.FR-9) — both with the reason
  `minimum_severity`, whose text `events.go` gives as "Minimum severity of the Route"; a fall writes the level
  silently, as a falling Severity level does; then the re-render step updates the Root messages Quietly. An Alert Group
  already at the computed level is skipped, so a second run of a row records nothing. The worker is not a Leader task,
  and delivery's tables need no new row.
- **Log events** (`internal/logging/events.go`): `open_group_update_done` (INFO; `kind`, `route`, `destination`,
  `team`, `alert_groups` — how many changed) and `open_group_update_failed` (WARN; `kind`, `route`, `destination`,
  `team`, `error`), both C-26. A row names the scope its kind has — `route` here and for S-125, `destination` for the
  button set of a Destination (S-113), `team` for S-163's and S-170's kinds, none for the Organization's button set —
  and logs only that field, so the later
  kinds need no change of `events.go`, whose strict logger refuses an undeclared field in tests. No metric.
- **Settings**: `route.minimum_severity`, `route.urgent`, `organization.critical_is_urgent` and
  `route.urgent_rise_removes_ack`, whose meaning does not change.
- **Documentation**: `docs/concepts/routes-and-group-keys.md` replaces "### Urgent" with "### Minimum severity" and
  "### Urgent" (both anchors exist afterwards, so `field.urgent` of `docs-links.json` keeps working until S-112 points
  it elsewhere); `docs/install/versions.md` gains the entry 0.2.0 of its upgrade notes for C-26.FR-10, with its
  paragraph "Rolling upgrade from 0.1": every migration of 0.2.0 applies when the first 0.2.0 replica starts, while
  0.1 replicas still run until the rollout ends; a 0.1 replica ignores the Minimum severity — it stores a lower Severity
  level on the Alert Groups it changes, while `urgent` agrees, since 0.2.0 writes `routes.urgent` — and the stories that
  add more of what 0.1 replicas ignore add one sentence each (S-115, S-117, S-121, S-140). The window lasts as long as
  the rollout, and nothing of it remains afterwards.

## Steps

1. Write the migration, its integration test and `schema.md`. Check: `TestIntegrationMinimumSeverity` migrates a
   database at 20 with an urgent Route, an open warning Alert Group of it with a pending delivery, a critical Alert
   Group of a plain Route and `critical_is_urgent` off, and finds the values of the acceptance; down and up again work.
2. Change the spec, routing and the Organization. Check: `make generate-check`; `routes_test.go` covers create and
   update with and without `urgent` and `minimum_severity`, the stored `routes.urgent` and the Audit log diff;
   `organization_test.go` covers the ignored field.
3. Change the rule in groups and the derived urgency. Check: `transitions_test.go` and `grouping_test.go` cover each
   Minimum severity against each level of the firing Alerts, a rise to critical without a Minimum severity, and the
   stored level of a created, joined and resolved Alert.
4. Write the `open_group_updates` worker. Check: `updates_test.go` runs a row on a manual clock over an acknowledged, a
   snoozed and a firing Alert Group, twice, and with two workers that each claim it once.
5. Write the end-to-end test and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-028 and S-032: the Admin's Personal access token A; an Integration "sev" with its fake receiver "sev"; ADV,
# NOTIFY, PUTA, AG, GET and LASTE.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s "${A[@]}" $API/route-profiles | jq -c '[.items[] | .minimum_severity]'                             # ["none","none"]
P=$(curl -s "${A[@]}" $API/route-profiles | jq -c '.items[0].policy')
ROUTE() { curl -s "${A[@]}" $API/routes -d "{\"name\":\"$1\",\"matchers\":[{\"label\":\"svc\",\"op\":\"=\",\"value\":\"$1\"}],
  \"minimum_severity\":\"$2\",\"group_key\":[\"alertname\"],\"destination_ids\":[],\"policy\":$P}"; }
BODY() { curl -s "${A[@]}" $API/routes/$1 | jq -c "del(.id, .etag, .position, .is_default, .destinations, .storm_active,
  .open_alert_group_count, .created_at) | $2"; }
PUTR() { curl -s "${A[@]}" -X PUT -H "If-Match: $(curl -s "${A[@]}" $API/routes/$1 | jq -r .etag)" $API/routes/$1 -d "$(BODY $1 "$2")"; }

# C-26.FR-10, C-26.AC-8: urgent is derived and ignored; a Route without urgent in its input is accepted
R1=$(ROUTE hb critical | jq -r .id); curl -s "${A[@]}" $API/routes/$R1 | jq -c '{minimum_severity, urgent}'
# {"minimum_severity":"critical","urgent":true}
psql "$MUSTER_DATABASE_URL" -Atc "SELECT urgent FROM routes WHERE public_id = '$R1'"                        # t
PUTR $R1 '.urgent = false' | jq -c '{minimum_severity, urgent}'                 # {"minimum_severity":"critical","urgent":true}
O=$(curl -s "${A[@]}" $API/organization)
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization \
  -d "$(jq -c 'del(.id, .etag) | .critical_is_urgent = false' <<<"$O")" | jq .critical_is_urgent           # true

# C-26.AC-7: a warning Alert is critical and Urgent in its Alert Group, warning on its own
curl -s -X PUT $FAM/groups/h1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"Lag"}}' > /dev/null
PUTA h1 a '{"labels":{"svc":"hb","severity":"warning"}}'; NOTIFY h1 '{"reason":"first notification"}'
G=$(AG 'svc%3D%22hb%22'); GET $G | jq -c '{severity_level, urgent}'          # {"severity_level":"critical","urgent":true}
curl -s "${A[@]}" "$API/alert-groups/$G/alerts" | jq -r '.items[0].severity_level'                         # warning

# C-26.FR-9, C-26.AC-11, C-09.AC-8: raising the Minimum severity of a Route with an acknowledged warning Alert Group
R2=$(ROUTE db none | jq -r .id)
curl -s -X PUT $FAM/groups/d1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"Slow"}}' > /dev/null
PUTA d1 b '{"labels":{"svc":"db","severity":"warning"}}'; NOTIFY d1 '{"reason":"first notification"}'
G2=$(AG 'svc%3D%22db%22'); curl -s "${A[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
PUTR $R2 '.minimum_severity = "critical"' > /dev/null; sleep 1
GET $G2 | jq -c '{status, severity_level, urgent, o: .owner.name}'   # {"status":"acknowledged","severity_level":"critical","urgent":true,"o":"Admin"}
curl -s "${A[@]}" "$API/alert-groups/$G2/timeline?limit=1" | jq -c '.items[0] | {event, loudness, reason_code}'
# {"event":"urgency_raised","loudness":"quiet","reason_code":"minimum_severity"}
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM open_group_updates"                                 # 0
grep '"event":"open_group_update_done"' dev.log | tail -1 | jq -c '{kind, alert_groups}'
# {"kind":"route_minimum_severity","alert_groups":1}
PUTR $R2 '.minimum_severity = "none"' > /dev/null; sleep 1
GET $G2 | jq -c '{severity_level, urgent}'                                    # {"severity_level":"warning","urgent":false}
curl -s "${A[@]}" "$API/alert-groups/$G2/timeline?limit=1" | jq -r '.items[0].event'                       # urgency_raised
```

The upgrade (C-26.AC-8) is checked once on the development database: in a worktree of the tag `v0.1.4`, run `make dev`,
create a Route marked urgent and one open warning Alert Group on it, and stop it; on this branch `make dev` migrates the
same database. Then `GET /routes/{id}` reads `minimum_severity` `critical` and `urgent` true, the Alert Group reads
`severity_level` `critical` and `urgent` true, its Timeline has no entry after the migration, and the fake Mattermost
server's `/_fake/requests` shows no new request about it. `test/e2e/minimum_severity_test.go` repeats the other checks,
and `TestIntegrationMinimumSeverity` the migration's.

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): replace the urgent mark with a minimum severity on routes`.
- `open_group_updates` is the one mechanism for changes of configuration that must reach the open Alert Groups after
  the save — this story's Minimum severity, the button sets (S-113), the evaluation of Teams on save (S-163), the
  Destinations of a Team (the kind of S-121, handled by S-170) and "Labels in the message" (S-125): each adds its kind
  to the CHECK and its handler to the worker. It is not a Leader task, and
  each row is safe to run twice, as ADR-0007 asks of claimed rows.
- The lowering of a Minimum severity records nothing, as C-26.FR-9 says; the level falls as a falling Severity level
  does in L1, and the Root messages show it.
- The upgrade check by hand needs a checkout of `v0.1.4`; the integration test is the automated proof.
- Migration numbers follow the story map: 0021 to 0028 for S-111, S-113, S-115, S-117, S-118, S-163, S-121 and S-125.
  A story merged out of that order renumbers its migration to the next free number in its own pull request, with its
  tests and `schema.md`, as S-092 did.
- The profile rename was split into S-162 when this contract was written: every web end-to-end spec names the profile
  id, and together they touched about 55 files.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-8 | partial | the rule and the API; the Route form and list are S-112 |
| C-26.FR-9 | partial | the open Alert Groups and their Root messages; the Destinations it brings into a condition are S-121 |
| C-26.FR-10 | partial | the migration and the API; the pages without the urgent mark and "critical is Urgent" are S-112 |
| C-26.FR-13 | partial | the Minimum severity section and the upgrade notes; the routing page and "What critical changes" are S-124 |
| C-26.AC-7 | full | |
| C-26.AC-8 | partial | the migration, `urgent` and `critical_is_urgent`; the profile ids are S-162 |
| C-26.AC-11 | partial | the Quiet `urgency_raised` without removing the acknowledgement; the Quiet publication in critical Destinations is S-121's, through this transition |
| C-08.FR-1 | partial | revised for L2: the Minimum severity replaces the urgent mark; Destination entries, Teams, chains, owner and labels follow in S-115, S-121, S-125 and later stories |
| C-08.FR-6 | full | revised for L2 |
| C-09.FR-1 | partial | revised for L2: urgency from the Severity level; the Teams are S-118, the running chains S-169 and the Assignees S-168 |
| C-09.FR-9 | partial | revised for L2: a rise to critical; chains that start over are S-169 |
| C-09.AC-8 | full | revised for L2 |
| C-11.FR-3 | full | revised for L2 |
| C-20.FR-1 | partial | revised for L2: "critical is Urgent" read-only; the page is S-112, the Team label S-118, Working hours S-115, the button set S-113 |
