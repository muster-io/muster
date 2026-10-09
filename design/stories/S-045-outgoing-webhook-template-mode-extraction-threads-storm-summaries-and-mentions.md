---
id: S-045
title: "Outgoing webhook template mode: extraction, threads, Storm summaries and Mentions as data (BE)"
capability: C-15
kind: be
layer: L1
depends_on: [S-044]
covers: [C-15.FR-1, C-15.FR-3, C-15.FR-4, C-15.FR-7, C-15.FR-9, C-15.FR-11, C-15.FR-12, C-15.AC-3, C-15.AC-9, C-15.AC-10, C-11.FR-6, C-11.FR-14, C-12.FR-4, C-12.FR-13, C-01.FR-13, C-12.FR-8]
files_touched:
  - internal/webhooks/template.go
  - internal/webhooks/extract.go
  - internal/webhooks/adapter.go
  - internal/webhooks/destination.go
  - internal/webhooks/query.sql
  - internal/webhooks/template_test.go
  - internal/webhooks/extract_test.go
  - internal/webhooks/adapter_test.go
  - internal/webhooks/secrets_test.go
  - internal/delivery/delivery.go
  - internal/delivery/render.go
  - internal/delivery/enqueue.go
  - internal/delivery/worker.go
  - internal/delivery/threads.go
  - internal/delivery/outcomes.go
  - internal/delivery/storm.go
  - internal/delivery/membership.go
  - internal/delivery/interactive.go
  - internal/delivery/webhookevents.go
  - internal/delivery/query.sql
  - internal/delivery/deliverytest/recorder.go
  - internal/delivery/live_test.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/render_test.go
  - internal/delivery/worker_test.go
  - internal/delivery/membership_test.go
  - internal/delivery/storm_test.go
  - internal/delivery/threads_test.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/broken_test.go
  - internal/delivery/publication_test.go
  - internal/delivery/presses_test.go
  - internal/delivery/limiter_test.go
  - internal/db/migrations/0006_deliveries_published_at.up.sql
  - internal/db/migrations/0006_deliveries_published_at.down.sql
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/destinations/write.go
  - internal/destinations/query.sql
  - internal/destinations/write_test.go
  - internal/groups/system.go
  - internal/messages/preview.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/default_test.go
  - internal/templates/sandbox.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/raise.go
  - internal/internalalerts/internalalerts_test.go
  - internal/api/templates.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/runtime/runtime.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakewebhook/chat.go
  - internal/fakes/fakewebhook/fakewebhook.go
  - internal/fakes/fakewebhook/fakewebhook_test.go
  - internal/logging/events.go
  - go.mod
  - NOTICE
  - design/db/schema.md
  - docs/outgoing-webhooks/templates.md
  - docs/outgoing-webhooks/events.md
  - test/e2e/webhook_template_test.go
acceptance:
  - "[C-15.FR-1, C-15.FR-3] An outgoing webhook Destination in mode `template` or `both` stores \"create\" and \"update\" requests and optional \"open thread\" and \"reply in thread\" requests — method, URL, headers, body and extraction rules — whose templates are parsed and dry-run on save."
  - "[C-15.FR-3, C-15.AC-3] A new Alert Group sends one \"create\" request and stores the values its extraction rules pick from the response (`$.data.id`); Acknowledge then leads to one \"update\" request whose URL uses that id; changes made while a request is pending collapse into the next \"update\"."
  - "[C-15.FR-3] For a Thread reply event, \"open thread\" runs once before the first \"reply in thread\" and adds its extracted values; later replies use them; without \"open thread\", \"reply in thread\" uses the values of \"create\"."
  - "[C-15.FR-4] When an extraction rule finds nothing, the \"create\" counts as delivered, the Alert Group's Timeline records `template_value_missing` naming the value, and a later request that uses it fails as a template error."
  - "[C-15.FR-7] A request whose template fails is not sent: that delivery ends as Not delivered with no fallback body, the Destination shows `template_error` with `fallback` `not_sent`, `muster_template_errors_total{template=\"webhook_request\"}` grows and `MusterTemplateError` fires with the Destination's labels; the next request that renders resolves it."
  - "[C-15.FR-11] Templates see `.Mentions` as targets and the trusted `mention` function renders a target as plain text — `@all`, the group name, or `@` and the login — for the template to wrap in the receiver's syntax."
  - "[C-15.FR-3, C-15.AC-10, C-11.FR-6] During a Storm of 30 new Alert Groups on a Route with threshold 20, a template-mode Destination receives one Storm summary through its \"create\" request — whose templates see `.Storm` with the Route, the counts and the link to Muster and no Alert Group — instead of the non-urgent Alert Groups after the 20th, and its final state through \"update\"."
  - "[C-15.FR-3, C-15.AC-9] After a Broken period the template mode sends only the current state: one \"update\" per published Alert Group, no reply for events of the period, and no \"create\" for an Alert Group that started and resolved during it."
  - "[C-15.FR-12, C-11.FR-14] Deleting a template-mode Destination sends the final edit through \"update\" first and wipes its secrets after it; in mode `both`, its queued events are abandoned at once and the secrets wiped after the final edits."
  - "[C-12.FR-4, C-12.FR-13] `previewTemplate` of the kind `webhook_request` renders a request template with Secrets shown as `[redacted]`; `muster_template_render_duration_seconds{template=\"webhook_request\"}` is observed."
  - "[C-15.FR-9] The documentation has template recipes for a chat with one-step threads and one with two-step threads, and for Storm summaries."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 45
---

# S-045. Outgoing webhook template mode: extraction, threads, Storm summaries and Mentions as data (BE)

## Scope

**IN**

- The modes `template` and `both`: the four request templates, extraction rules and the values stored per Alert Group
  and Destination, reconciliation through "create" and "update", threads through "open thread" and "reply in thread".
- Storm summaries through the requests, recovery to the current state, deletion with the final "update".
- Template errors of requests with no fallback, the missing extracted value, the template data with `.Mentions`,
  `.Storm` and `mention`.
- Previews of request templates; the template recipes; a fake chat behind the fake receiving endpoint.

**OUT**

- The events mode, the Signing secret and Secrets (S-044); the `test` event and the test of the "create" request
  (S-047); the form (S-046).

## Contracts

- **Operations implemented**: `createDestination` and `updateDestination` accept `mode` `template` and `both` (the
  `422 unsupported` of S-044 is removed); `previewTemplate` gains the kind `webhook_request`; `WebhookDestination
  .template_error` is filled. Schemas: `WebhookTemplateConfig`, `RequestTemplate`, `ExtractionRule`,
  `TemplateErrorState` (`fallback` `not_sent`).
- **Request templates** (C-15.FR-3; `template.go`): `create` and `update` are required, `open_thread` and
  `reply_in_thread` optional; each has a method, a URL, headers, an optional body and extraction rules (`name`, a
  JSONPath such as `$.data.id`). Templates are rendered in the sandbox of S-036 and see `.AlertGroup` and `.Alerts`
  (the data of S-036), `.Response` (the stored extracted values), `.Event` (the lifecycle event a reply carries, else
  empty), `.Notify` (the message is Loud), `.Mentions` (the targets of S-037 as data), `.Secrets`, and, for a Storm
  summary, `.Storm` (`Route`, `AlertGroupCount`, `UrgentCount`, `Final`, `URL`) instead of the Alert Group. Alert data
  are not escaped for a markup — the receiver's format is unknown — so templates use `toJson` or `js` for bodies.
  `mention` renders a target as plain text: `@all` (or `@channel`, `@here` as set), the group name, or `@` and the
  login. On save each template is parsed and dry-run with the samples of S-036, each extraction rule's name filled with
  `example-<name>` in `.Response`; a failure answers `422` at `/template/<request>/<field>` with `line` and `column`.
- **Reconciliation** (C-15.FR-3, ADR-0005; `adapter.go`): the template mode is an adapter like a messenger's. `Publish`
  sends "create"; `Update` sends "update" with the latest Desired state, so changes collapse; `Reply` sends "reply in
  thread" for the events whose last column in the lifecycle event tables is a Thread reply, after "open thread" has run
  once for the delivery (`deliveries.thread_opened`). The Desired state is the rendered "update" request; its hash
  decides whether a call is needed. The response mapping is S-044's.
- **Extraction** (C-15.FR-3, FR-4; `extract.go`): each rule's JSONPath is applied to a JSON response of "create" or
  "open thread"; found values are stored as strings in `deliveries.response_values` and the first rule's value as
  `message_id`. A rule that finds nothing leaves the "create" delivered, records a `system` Timeline entry
  `template_value_missing` with the rule's name — written by `groups.RecordSystemEntry` (`internal/groups/system.go`),
  which delivery calls because only `groups` writes the Timeline — and a later template that reads the missing value
  fails as a template error. The JSONPath library is listed in NOTICE.
- **Template errors** (C-15.FR-7, ADR-0012): a request whose template fails is not sent; the delivery (or Thread
  reply) ends `not_delivered` with `last_error_class` `template_error` and a `not_delivered` delivery event; there is
  no fallback body. The Destination's `template_error_since` and `template_error` are set, `muster_template_errors_total
  {route,destination,template="webhook_request"}` grows (with `route` empty), and `MusterTemplateError` is raised with
  `destination`, `destination_name` and `template`. The next request of the Destination that renders clears the state
  and resolves the Internal alert.
- **Storms** (C-11.FR-6, C-15.FR-3): Storm summaries of S-035 reach a template-mode Destination as "create" (first,
  Loud) and "update" (counts and final state) rendered with `.Storm`; held Alert Groups follow S-035's rules.
- **Recovery** (C-15.FR-3, C-11.FR-19): the template mode follows S-035's recovery like a messenger; the events of mode
  `both` follow S-044.
- **Deletion** (C-15.FR-12, C-11.FR-14; `internal/destinations/delete.go`, `internal/delivery/webhookevents.go`): mode
  `template` — the final edit goes out through "update" with `.Final` text "No longer updated here; current state in
  Muster: {link}" available to the template, and the secrets are wiped after it; mode `both` — the queued events end
  `not_delivered` at once, the final edits go out, and the secrets are wiped after them.
- **Previews** (C-12.FR-4): `previewTemplate` kind `webhook_request` with `route_id` or the Destination's sample renders
  one template string with the data above, Secrets as `[redacted]`.
- **Metrics and log events**: `muster_template_errors_total` and `muster_template_render_duration_seconds` with
  `template="webhook_request"`; `webhook_template_failed` (WARN: `destination`, `group`, `request`, `error`) and
  `webhook_value_missing` (WARN: `destination`, `group`, `rule`).
- **Fake chat** (C-01.FR-13; `internal/fakes/fakewebhook/chat.go`, behind `127.0.0.1:18093`): a small chat API —
  `POST /chat/{name}/messages` answers `{"data":{"id":"m<N>"}}`; `PUT /chat/{name}/messages/{id}` edits;
  `POST /chat/{name}/messages/{id}/replies` replies (one-step threads); `POST /chat/{name}/threads` answers
  `{"thread":{"id":"t<N>"}}` and `POST /chat/{name}/threads/{id}/messages` replies (two-step threads); `GET
  /_fake/chat/{name}` lists messages, edits and replies; faults of the harness apply.
- **Documentation** (C-15.FR-9; `docs/outgoing-webhooks/templates.md`): the template mode, the data templates see,
  extraction rules and `.Response`, recipes for a chat with one-step threads and one with two-step threads, Storm
  summaries with `.Storm`, Mentions with `.Mentions` and `mention`, template errors and the missing-value rule.

## Steps

1. Write the fake chat. Check: `fakewebhook_test.go` covers both thread styles.
2. Write the request templates with their data, the dry run and previews. Check: `template_test.go`.
3. Write extraction and the missing-value rule. Check: `extract_test.go` covers found, missing and non-JSON responses.
4. Write the adapter for reconciliation, threads, Storm summaries, template errors and deletion. Check:
   `adapter_test.go` and `live_test.go` with the recording runtime.
5. Add the documentation and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev &
# as in S-044: the session `H`, NOTIFY, ADV, AG, the On-call policy in P and the Mention settings M0
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FWH=127.0.0.1:18093
M="{\"new_alert_group\":$M0,\"new_alerts\":$M0,\"reopen\":$M0,\"ack_timeout\":$M0,\"snooze_ended\":$M0,\"rise_to_urgent\":$M0}"
CHAT() { curl -s $FWH/_fake/chat/$1 | jq -c "$2"; }
TPL='{"create":{"method":"POST","url":"http://127.0.0.1:18093/chat/ops/messages","headers":[],
  "body":"{\"text\":{{ printf \"#%d %s (%s)\" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}}","extract":[{"name":"id","path":"$.data.id"}]},
 "update":{"method":"PUT","url":"http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}","headers":[],
  "body":"{\"text\":{{ printf \"#%d %s (%s)\" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}}","extract":[]},
 "open_thread":{"method":"POST","url":"http://127.0.0.1:18093/chat/ops/threads","headers":[],"body":"{\"root\":\"{{ .Response.id }}\"}","extract":[{"name":"thread","path":"$.thread.id"}]},
 "reply_in_thread":{"method":"POST","url":"http://127.0.0.1:18093/chat/ops/threads/{{ .Response.thread }}/messages","headers":[],
  "body":"{\"text\":{{ printf \"%s %v\" .Event .Notify | toJson }}}","extract":[]}}'
D=$(curl -s "${H[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"chat\",\"mode\":\"template\",\"template\":$TPL,
  \"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}" | jq -r .destination.id)
curl -s "${H[@]}" $API/routes -d "{\"name\":\"chat\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"chat\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" > /dev/null

# C-15.AC-3: create, extract, update with the id
curl -s -X PUT $FAM/groups/c1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"QueueStuck"}}' > /dev/null
curl -s -X PUT $FAM/groups/c1/alerts/a -d '{"labels":{"team":"chat","queue":"q1"}}' > /dev/null
NOTIFY c1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'queue%3D%22q1%22')
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
CHAT ops '{messages: [.messages[] | {id, text}], edits: [.edits[] | {id, text}]}'
# {"messages":[{"id":"m1","text":"#N QueueStuck (firing)"}],"edits":[{"id":"m1","text":"#N QueueStuck (acknowledged)"}]}

# C-15.FR-3: two-step thread — open thread once, then replies
curl -s "${H[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null
curl -s -X PUT $FAM/groups/c1/alerts/b -d '{"labels":{"team":"chat","queue":"q2"}}' > /dev/null
NOTIFY c1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
curl -s -X PUT $FAM/groups/c1/alerts/c -d '{"labels":{"team":"chat","queue":"q3"}}' > /dev/null
NOTIFY c1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
CHAT ops '{threads: (.threads | length), replies: [.threads[0].messages[].text]}'
# {"threads":1,"replies":["alerts_added true","alerts_added true"]}

# C-15.FR-4: a rule that finds nothing
TPL2=$(jq -c '.create.extract = [{"name":"id","path":"$.nothing.here"}]' <<<"$TPL")
ET=$(curl -s -b jar $API/destinations/$D | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ET" $API/destinations/$D -d "{\"type\":\"webhook\",\"name\":\"chat\",\"mode\":\"template\",\"template\":$TPL2,
  \"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}" > /dev/null
curl -s -X PUT $FAM/groups/c2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Backlog"}}' > /dev/null
curl -s -X PUT $FAM/groups/c2/alerts/a -d '{"labels":{"team":"chat","topic":"x"}}' > /dev/null
NOTIFY c2 '{"reason":"first notification"}'; sleep 1; G2=$(AG 'topic%3D%22x%22')
curl -s -b jar "$API/alert-groups/$G2/timeline?kind=system" | jq -c '.items[0] | {system_event, detail}'
# {"system_event":"template_value_missing","detail":"id"}

# C-15.FR-7: the next request needs the missing value: a template error, not sent, no fallback
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null; sleep 1
curl -s -b jar $API/destinations/$D | jq -c '.template_error | {fallback, e: (.error | length > 0)}'   # {"fallback":"not_sent","e":true}
curl -s -b jar "$API/alert-groups/$G2/deliveries" | jq -r '.items[0].state'   # not_delivered
curl -s localhost:8082/metrics | grep -c 'muster_template_errors_total{.*template="webhook_request"}'   # 1
curl -s -b jar "$API/integrations/$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" \
  | jq -r '.items[] | select(.labels.alertname == "MusterTemplateError") | .labels.destination_name'   # chat

# C-12.FR-4: a request template preview with a masked Secret
curl -s "${H[@]}" $API/template-previews -d '{"kind":"webhook_request","template":"Bearer {{ .Secrets.token }} for #{{ .AlertGroup.Number }}"}' | jq -r .output
# Bearer [redacted] for #…                      (the number of the sample)
```

`test/e2e/webhook_template_test.go` repeats these steps and adds C-15.AC-10 (a Storm of 30 new Alert Groups on a Route
with threshold 20: 20 "create" requests, one Storm summary "create", the final state through "update", 30 `created`
events at an events-mode Destination of the same Route), C-15.AC-9 for the template mode (a `404` makes the Destination
Broken; after recovery one "update" per published Alert Group, no reply, no "create" for one that started and
resolved meanwhile), a one-step thread recipe, `.Mentions` and `mention` in a body, and deletion in modes `template`
and `both`.

## Open questions

None.

## Notes

- Suggested commit: `feat(webhooks): add the outgoing webhook template mode with extraction and threads`.
- The Desired state of a template-mode delivery is the rendered "update" request, so a template that does not change
  its output for a change of status makes no call, as for a messenger whose text did not change.
- The template forms `{{ with .Secrets }}…{{ end }}` and variables such as `$s := .Secrets` are not detected as Secret
  references today. A missing Secret read that way renders empty instead of failing, and in the URL it can raise a false
  `literal_credential` warning. This story either recognises these forms or refuses them on save. Done (D291): a
  template reads a Secret, or an extracted value, only as `.Secrets.<name>`, `$.Secrets.<name>` or `index .Secrets
  "<name>"` (the same for `.Response`); every other use is refused on save at its line and column, in both modes, and
  fails at runtime as a template error. Recognising the forms would need data-flow analysis of the template; refusing
  is simpler and leaves no path where a missing Secret renders empty. The URL warning reads the same three forms.
- D277: a first Publication is found by `deliveries.published_at IS NULL` instead of `message_id IS NULL` — a
  template-mode "create" whose rule finds nothing publishes with no message id. `published_at` is cleared with the
  message id when a Root message is forgotten for a republication; migration 0006 clears it on the rows already in that
  state. Every delivery query that asked "is it published" changed, which is why `files_touched` grew past the first
  estimate: the delivery queries, their in-memory fakes in the unit tests, and the schema.
- Choices of this story within the contract: extraction rules belong to "create" and "open thread" only (a rule on
  another request is refused on save, since its value is never stored); "create" reads no `.Response`, "open thread"
  and "update" read those of "create" (an "update" can come before any thread exists), "reply in thread" both; the
  Secrets printed or encoded whole (`{{ toJson . }}`) show as `[redacted]`; a request with a body and no `Content-Type` header of its own is sent as
  `application/json`, and only `Content-Length`, `Host` and the three `webhook-*` headers are reserved; each request
  has its own `webhook-id`, since reconciliation replaces a request instead of retrying it; a rule stores a string,
  number or boolean of at most 1024 bytes, anything else counts as missing; the Desired state stores the data the
  templates read and hashes the "update" request rendered with the Secrets as `[redacted]` and the values as
  `example-<name>`; `MusterTemplateError` gains the Destination as its other entity (`destination`,
  `destination_name`), and the events mode settles the template error the same way. The dry run on save uses the
  built-in example of C-12.FR-5, as an outgoing webhook belongs to no Route.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-15.FR-1 | partial | the template modes in the API; with S-044 complete, the form S-046 |
| C-15.FR-3 | full | |
| C-15.FR-4 | full | |
| C-15.FR-7 | full | |
| C-15.FR-9 | partial | the template recipes; with S-044 complete |
| C-15.FR-11 | partial | Mentions in templates; with S-044 complete |
| C-15.FR-12 | partial | the template mode and `both`; with S-044 complete |
| C-15.AC-3 | full | |
| C-15.AC-9 | partial | the template mode; with S-044 complete |
| C-15.AC-10 | partial | the template mode; with S-044 complete |
| C-11.FR-6 | partial | Storm summaries through templates |
| C-11.FR-14 | partial | the final edit through "update" |
| C-12.FR-4 | partial | outgoing webhook request templates |
| C-12.FR-13 | partial | `webhook_request` rendering time and errors |
| C-01.FR-13 | partial | the fake chat |
| C-12.FR-8 | partial | Mentions as data in template mode |
