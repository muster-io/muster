---
id: S-055
title: Organization settings, values seen and outbound policy editing (BE)
capability: C-20
kind: be
layer: L1
depends_on: [S-047, S-051, S-053]
covers: [C-20.FR-1, C-20.FR-2, C-20.FR-3, C-20.FR-8, C-20.AC-1, C-20.AC-3, C-20.AC-4, C-20.AC-6, C-02.FR-21, C-04.FR-8]
files_touched:
  - api/openapi.yaml
  - internal/organization/organization.go
  - internal/organization/severityvalues.go
  - internal/organization/outbound.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/organization/outbound_test.go
  - internal/organization/outbound_integration_test.go
  - internal/organization/severityvalues_integration_test.go
  - internal/organization/heartbeat_integration_test.go
  - internal/outbound/client.go
  - internal/outbound/client_test.go
  - internal/outbound/proxy_test.go
  - internal/users/users.go
  - internal/live/live_integration_test.go
  - internal/archlint/secretleak.go
  - internal/routing/routing_integration_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/api/problem.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - test/e2e/organization_test.go
acceptance:
  - "[C-20.FR-1, C-20.FR-8, C-04.FR-8] `updateOrganization` with `If-Match` changes the name, time zone, Severity levels with their emoji and colours, \"critical is Urgent\", Instance labels, retention periods, the TOTP policy, the token grace and the outgoing heartbeat; every change is recorded as `organization.updated` with a before/after diff, a stale `If-Match` answers `412`, and nothing answers `unsupported` any more."
  - "[C-20.AC-6] An update that sets `retention.alert_group_summaries_days` below `retention.alert_details_days` answers `422` with `retention_order` at `/retention/alert_group_summaries_days` and changes nothing; equal periods are accepted."
  - "[C-20.FR-2, C-20.AC-3] After mapping `P1` to critical, a new Alert with `severity=\"P1\"` starts an Urgent Alert Group with the level critical, while an open Alert Group whose Alerts carry `P1` keeps its status, level and urgency."
  - "[C-20.FR-1, C-20.AC-4] After Alerts with `severity=\"P5\"` arrive, `listSeverityValues` lists `P5` with its count, the level warning and `mapped: false`, over `organization.severity_values_window`."
  - "[C-20.FR-1] After the time zone becomes `Europe/Berlin`, the times in new messages are shown in that zone."
  - "[C-20.FR-3, C-20.AC-1, C-02.FR-21] `updateOutboundPolicy` with `If-Match` replaces the policy and its lists, effective at once on every replica — idle pooled connections opened under the old policy included — and sends the `organization` hint; under the standard policy a test of an outgoing webhook to 127.0.0.1 fails with `blocked` until `127.0.0.0/8` is allowed, also right after a successful test warmed a connection, and one to 169.254.169.254 stays `blocked` even with `169.254.0.0/16` allowed; `getOutboundPolicy` lists the rules that always apply."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 55
---

# S-055. Organization settings, values seen and outbound policy editing (BE)

## Scope

**IN**

- Editing every Organization setting that exists with its defaults since S-007, with the retention rule and the values
  seen of the severity label.
- Editing the outbound address policy, effective at once on every replica, idle pooled connections included.
- A request whose client went away is no longer logged as an `api_request_failed` error.

**OUT**

- The Keyring status, `muster secrets rotate-key` and the re-render after a rotation (S-071, split from this story).
- The Organization settings pages (S-056); the TOTP policy (S-012) and the outgoing heartbeat (S-053), whose editing
  exists already.
- The key rotation and backup sections of the documentation (S-060).

## Contracts

- **Operations implemented**: `updateOrganization` for every field (the remaining `unsupported` answers of S-012 are
  removed), `getOutboundPolicy` and `updateOutboundPolicy` (`organization:read` / `organization:write`) and
  `listSeverityValues` (`organization:read`). Schemas: `OrganizationBase`, `OrganizationInput`, `RetentionSettings`,
  `SeverityMappingEntry`, `SeverityLevelStyle`, `OutboundPolicy`, `SeverityValue`, `SeverityValueList`; hint
  `organization`. `getKeyring` stays `501` until S-071.
- **Organization** (C-20.FR-1, FR-8, C-04.FR-8; `Service.Update` and `unsupported()` in `organization/organization.go`,
  `organizations`): validation — `name` not empty; `time_zone` an IANA zone (`invalid_format`), checked by the rule User
  profiles already use, moved from `validTimeZone` in `internal/users/users.go` to `organization.ValidTimeZone`, which
  `users` now calls, so both refuse the same names (`organization` cannot import `users`: `users` → `auth` →
  `organization`); `severity_label` a label name; `severity_mapping` values unique (`duplicate` at
  `/severity_mapping/<i>/value`); `severity_styles` exactly one entry per level with a non-empty emoji and a `#rrggbb`
  colour; `instance_labels` label names without duplicates; retention periods from 1 to 36500 days, with
  `alert_group_summaries_days` ≥ `alert_details_days` (`422` `retention_order` at
  `/retention/alert_group_summaries_days`, checked before the database `CHECK`); `oidc_token_grace_seconds` from 1 to
  3153600000 (36500 days). The upper bounds keep the periods far from the overflow of a Go duration, which would move a
  retention cutoff into the future; they are `maximum` in `api/openapi.yaml`, so request validation refuses a larger
  number with `400` before it reaches the domain, whose `422` `out_of_range` is the second, defensive check. Every
  field error carries the full JSON pointer of the field (`/retention/alert_details_days`,
  `/severity_styles/<i>/color`), never only its first segment, because the shared form of S-056 shows each error at its
  field. A valid update writes the row, increments `version` (the ETag), records `organization.updated` with the diff
  (Secrets as `secret_changed`) and sends the `organization` hint in its transaction, as S-012 does.
- **Later evaluations only** (C-20.FR-2): routing already reads the severity label and mapping in each evaluation's
  transaction (`GetRoutingStamp` in `routing/evaluate.go`), and the dispatcher reads "critical is Urgent" and the
  Instance labels the same way (`groups/query.sql`), so a change applies from the next evaluation on every replica
  without a cache and without a notification of its own; stored Severity levels and statuses are never recomputed by a
  settings change, and urgency follows the stored level and the current "critical is Urgent" at read time (C-08.FR-6).
  Retention tasks and message rendering read the current values at their next run.
- **Values seen** (C-20.FR-1, AC-4; `organization/severityvalues.go`): the distinct values of the label
  `severity_label` among Alerts whose `last_seen_at` is within `organization.severity_values_window`, each with the
  number of Alerts, the level of the current mapping (an unmapped value counts as warning) and `mapped`;
  `window_days` in the answer.
- **Outbound policy** (C-20.FR-3, C-02.FR-21; `organization/outbound.go`, `outbound_policies`): `policy` `standard` or
  `strict`; `allowed` and `denied` entries must parse with the policy's own parser of S-009 (`invalid_format` at
  `/allowed/<i>` or `/denied/<i>`, `duplicate` for repeats); `always_blocked` is `outbound.AlwaysBlocked()` (already
  exported by `internal/outbound/policy.go`: link-local, cloud metadata, unspecified, multicast) and is read-only. An
  update records `outbound_policy.updated` with the diff, sends `NOTIFY muster_outbound_policy`
  (`organization.OutboundPolicyChannel`) and then the `organization` hint in its transaction — a replica handles its
  notifications in order, so a replica whose streams have the hint already applies the new policy. On that
  notification, and when its LISTEN is back after a loss, every replica drops its per-replica cache of S-009 at once
  (one cache per replica, shared by every outbound client; its 10-second age stays as a fallback), and the replica that
  made the change does so as soon as it commits, answering S-009's open question 1.
- **Idle connections** (C-20.AC-1, FR-3; `outbound/client.go`): the policy is checked when a connection is dialled, so
  an idle pooled connection opened under the old policy would carry the next request past a stricter one.
  `outbound.CloseIdleConnections()` moves a policy generation; each client builds its transport for one generation,
  and its next request after the generation moved goes through a fresh transport, which dials and checks the address
  against the new policy, while the old transport's idle connections are closed. Requests in flight finish on the old
  transport, which no new request reaches — neither a connection returned to its pool afterwards nor an HTTP/2
  connection with an open stream carries a later request — and its remaining connections close once idle. Nothing
  keeps the clients, so rebuilt ones do not accumulate. Each replica calls it on `NOTIFY muster_outbound_policy`, and
  also when its cache re-reads a policy that differs from the cached one (a missed notification). `client_test.go`
  covers it: a warm connection to an allowed loopback server, then a stricter policy, and the next request fails with
  `blocked` without reaching the server; and a request in flight across the change, over HTTP/1.1 and over HTTP/2,
  after which the requests made during it and the next two are all `blocked` and never reach the server.
- **Canceled requests** (`api/problem.go`): an error that is `context.Canceled` while the request's own context is done
  — the client went away — is not logged as `api_request_failed` at ERROR; nothing reaches the client, and the request
  metric still counts it. Any other error keeps the ERROR line. A unit test in `server_test.go` covers both.
- **Operations without a story**: `getOutboundPolicy`, `updateOutboundPolicy` and `listSeverityValues` join the
  `implemented` map of `internal/api/server.go`; `getKeyring` stays the `501` example of `TestNotFoundAndNotImplemented`
  (`server_test.go`), whose comment now names S-071.
- **Log events**: `organization_updated` (INFO: `fields`), `outbound_policy_updated` (INFO).
- **Defaults**: `organization.*`, `retention.*`, `auth.oidc_token_grace`, `organization.outbound_policy`,
  `organization.outbound_allowed`, `organization.outbound_denied`.

## Steps

1. Write the Organization update with its validation. Check: `organization_test.go` covers every field, the full
   pointers, C-20.AC-6 and a stale `If-Match`; `routing_integration_test.go` shows a mapping change affecting only
   later Alerts.
2. Write the values seen. Check: `severityvalues_integration_test.go` covers the window, the counts and unmapped values.
3. Write the outbound policy editing, its notification and the closing of idle connections. Check: `outbound_test.go`
   and `client_test.go` cover C-20.AC-1 against the dialer of S-009 and the warm connection;
   `outbound_integration_test.go` covers the immediate effect on a second replica.
4. Stop logging canceled requests as errors. Check: the unit test in `server_test.go`.
5. Write the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-053: the Admin's session (`jar`, `H`), the Mattermost Destination "alerts" on the Route "t", the Integration
# "lab" with FIRE, PUTA, NOTIFY and AG of S-049, the Mention settings M (none for every event) of S-044, and the
# outgoing webhook "auto" (WD) of S-044 pointing at http://127.0.0.1:18093/hook/auto:
# WD=$(curl -s "${H[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"auto\",\"mode\":\"events\",\"events\":{\"url\":\"http://127.0.0.1:18093/hook/auto\",\"headers\":[]},\"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}" | jq -r .destination.id)
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
ORG() { O=$(curl -s -b jar $API/organization); curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c "del(.id, .etag) | $1" <<<"$O")"; }
POL() { Q=$(curl -s -b jar $API/organization/outbound-policy); curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$Q")" $API/organization/outbound-policy -d "$(jq -c "del(.etag, .always_blocked) | $1" <<<"$Q")" > /dev/null; }
TEST() { curl -s "${H[@]}" -X POST $API/destinations/$1/tests -d '{"source":{"kind":"example"}}' | jq -c '{c: ([.steps[] | .error_class] | map(select(. != "none")) | first)}'; }

# C-20.AC-6, FR-8: the retention order; equal periods; a stale If-Match
ORG '.retention.alert_group_summaries_days = 30 | .retention.alert_details_days = 90' | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'
# {"status":422,"p":"/retention/alert_group_summaries_days","c":"retention_order"}
ORG '.retention.alert_group_summaries_days = 90 | .retention.alert_details_days = 90' | jq -c .retention   # {…,"alert_details_days":90,"alert_group_summaries_days":90,…}
curl -s "${H[@]}" -X PUT -H 'If-Match: "1"' $API/organization -d "$(curl -s -b jar $API/organization | jq -c 'del(.id, .etag)')" | jq -r .status   # 412
curl -s -b jar "$API/audit-log?action=organization.updated&limit=1" | jq -c '[.items[0].diff[].pointer]'   # ["/retention/alert_group_summaries_days"]
ORG '.time_zone = "Mars/Olympus"' | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'   # {"status":422,"p":"/time_zone","c":"invalid_format"}

# C-20.FR-1, AC-4: values seen; C-20.AC-3, FR-2: mapping P1 changes later evaluations only
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"Sev\"}}" > /dev/null
  PUTA $1 a "{\"labels\":{\"team\":\"t\",\"host\":\"$1\",\"severity\":\"$2\"}}"; NOTIFY $1 '{"reason":"first notification"}'; AG "host%3D%22$1%22"; }
GOLD=$(FIRE v1 P1); FIRE v2 P5 > /dev/null
curl -s -b jar $API/organization/severity-values | jq -c '[.items[] | select(.value == "P5" or .value == "P1") | {value, count, level, mapped}]'
# [{"value":"P1","count":1,"level":"warning","mapped":false},{"value":"P5","count":1,"level":"warning","mapped":false}]
ORG '.severity_mapping += [{"value":"P1","level":"critical"}]' > /dev/null
curl -s -b jar $API/alert-groups/$GOLD | jq -c '{status, severity_level, urgent}'   # {"status":"firing","severity_level":"warning","urgent":false}
GNEW=$(FIRE v3 P1); curl -s -b jar $API/alert-groups/$GNEW | jq -c '{severity_level, urgent}'   # {"severity_level":"critical","urgent":true}

# C-20.FR-1: the time zone of messages
ORG '.time_zone = "Europe/Berlin"' > /dev/null; FIRE v4 warning > /dev/null; sleep 1
curl -s $FMM/posts | jq -r '[.[] | select(.root_id == "")] | last | .props.attachments[0].text' | grep -cE ' CES?T'   # 1

# C-20.AC-1, FR-3: the outbound address policy, a warm connection and the hint
curl -s -b jar $API/organization/outbound-policy | jq -c '{policy, allowed, a: (.always_blocked | length > 0)}'   # {"policy":"standard","allowed":["127.0.0.0/8"],"a":true}
TEST $WD                                                     # {"c":null}   (leaves an idle connection to 127.0.0.1:18093)
(curl -m 5 -sN -b jar $API/live-updates > sse.txt &); sleep 1
POL '.allowed = []'; TEST $WD                                # {"c":"blocked"}
sleep 1; grep -c '"type":"organization"' sse.txt             # 1
POL '.allowed = ["127.0.0.0/8"]'; TEST $WD                   # {"c":null}
MD=$(curl -s "${H[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"meta\",\"mode\":\"events\",\"events\":{\"url\":\"http://169.254.169.254/latest\",\"headers\":[]},\"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}" | jq -r .destination.id)
POL '.allowed = ["127.0.0.0/8", "169.254.0.0/16"]'; TEST $MD  # {"c":"blocked"}
grep -c '"event":"outbound_blocked".*169.254.169.254' dev.log   # 1
grep -c '"event":"api_request_failed"' dev.log                 # 0
```

`test/e2e/organization_test.go` repeats these steps in the harness mode of S-004 with the fakes in the test process,
with a second replica started as `muster dev --replica`: the outbound policy change is seen by the second replica at
once, a warm connection on the second replica is closed by the notification, and it also covers Instance labels, the
token grace and a strict policy.

## Open questions

None.

## Notes

- Suggested commit: `feat(organization): edit organization settings and the outbound policy`.
- Tests that need PostgreSQL are `*_integration_test.go` files with the `integration` build tag, which
  `make test-integration` runs; the other test files stay unit tests.
- Split: the Keyring status, the rotation and the re-render after it moved to S-071 after the plan review of this
  phase, because with the files that review added this story touched about 40 files.
- Re-checking the policy when a pooled connection is reused, instead of closing idle connections, is an acceptable
  alternative if the transport makes it simple; the warm-connection test stays the same.
- The per-replica severity cache and the `muster_organization` channel of the earlier contract are gone: routing
  reads the settings in its own transaction, and live updates use the `organization` hint on `muster_hints`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-20.FR-1 | partial | the API; the pages are S-056 |
| C-20.FR-2 | full | |
| C-20.FR-3 | partial | the API; the editor is S-056 |
| C-20.FR-8 | full | |
| C-20.AC-1 | full | |
| C-20.AC-3 | full | |
| C-20.AC-4 | partial | the API; the Severity levels page is S-056 |
| C-20.AC-6 | full | |
| C-02.FR-21 | partial | editing the policy, effective at once |
| C-04.FR-8 | partial | editing the token grace in the API; the Security page is S-056 |
