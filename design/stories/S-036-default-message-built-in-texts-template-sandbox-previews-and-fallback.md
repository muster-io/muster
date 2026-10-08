---
id: S-036
title: Default message, built-in texts, template sandbox, previews and the Fallback template (BE)
capability: C-12
kind: be
layer: L1
depends_on: [S-035]
covers: [C-12.FR-1, C-12.FR-2, C-12.FR-3, C-12.FR-4, C-12.FR-5, C-12.FR-6, C-12.FR-7, C-12.FR-10, C-12.FR-11, C-12.FR-13, C-12.AC-1, C-12.AC-2, C-12.AC-3, C-12.AC-4, C-12.AC-5, C-09.FR-10, C-08.FR-1]
files_touched:
  - internal/templates/sandbox.go
  - internal/templates/alertmanager.go
  - internal/templates/data.go
  - internal/templates/sandbox_test.go
  - internal/messages/message.go
  - internal/messages/default.go
  - internal/messages/replies.go
  - internal/messages/safe.go
  - internal/messages/shorten.go
  - internal/messages/fallback.go
  - internal/messages/render.go
  - internal/messages/preview.go
  - internal/messages/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/messages/default_test.go
  - internal/messages/safe_test.go
  - internal/messages/shorten_test.go
  - internal/messages/render_test.go
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/keyring/keyring.go
  - internal/keyring/keyring_test.go
  - internal/delivery/render.go
  - internal/delivery/delivery.go
  - internal/delivery/enqueue.go
  - internal/delivery/membership.go
  - internal/delivery/publication.go
  - internal/delivery/storm.go
  - internal/delivery/threads.go
  - internal/delivery/worker.go
  - internal/delivery/query.sql
  - internal/delivery/render_test.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/membership_test.go
  - internal/delivery/publication_test.go
  - internal/delivery/storm_test.go
  - internal/delivery/threads_test.go
  - internal/delivery/worker_test.go
  - internal/delivery/deliverytest/recorder_test.go
  - internal/delivery/live_test.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/dispatcher_test.go
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/internalalerts_test.go
  - internal/api/templates.go
  - internal/api/templates_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/api/problem.go
  - internal/api/server.go
  - api/openapi.yaml
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - test/e2e/routing_test.go
  - sqlc.yaml
  - go.mod
  - NOTICE
acceptance:
  - "[C-12.FR-1, C-12.FR-3] The default Root message has, in order, the status colour, `#N` and the title linked to the Alert Group page, the environment and the absolute start time in `organization.time_zone`, the group labels, the common labels and common annotations without the excluded ones, the `summary` in italics, the Alerts, the links, the notices that apply, the footer and the buttons of the status; every built-in text exists in English and Russian, and CI fails when a key is missing in one of them."
  - "[C-12.FR-1, C-12.AC-4] An Alert Group with 25 Alerts differing in `pod` shows above its list the distinct values \"pod: p1, p2, … +15 more\" and \"Full list in Muster\"; previewed with `length_limit` 4096 it is shortened — the Alert list first, then the label sections — and keeps the title, status, footer, buttons and the link to Muster."
  - "[C-12.FR-7, C-12.AC-1] A label value `@channel` is rendered with a zero-width space after `@`, a label value longer than `message.value_cap` is cut with \"…\", and alert data reach templates already escaped for the target markup."
  - "[C-12.FR-4, C-12.FR-5, C-12.AC-2] `previewTemplate` of a template calling `env` returns `valid: false` with `unknown_function` and its line and column; saving it on a Route answers 422 at `/policy/templates/root_message` with the same code and position; a template that renders against the Route's recent Stored Snapshots is saved and its preview shown."
  - "[C-12.FR-4] Templates use Alertmanager's function names (`toUpper`, `join`, `reReplaceAll`, `safeHtml` …) and the registered sprout functions only; `now` follows Muster's clock; `until`, `seq` and `repeat` are capped and output stops at `template.output_cap`."
  - "[C-12.FR-2, C-08.FR-1] In `updateRoute`, a template key absent from `policy.templates` keeps the stored template and `null` resets it to the built-in one; `getRoute` returns `null` for a built-in template."
  - "[C-12.FR-2] A Route's `root_message` template replaces only the body — the heading, links, notices, footer and buttons stay Muster's; a Route's line template replaces the default line of each Alert; the rest of the message is unchanged."
  - "[C-12.FR-2, C-12.FR-5] `previewTemplate` with an empty `template` returns the built-in template's `source` for the kind in the requested language, and `format` `html` lays the output out as Telegram HTML with alert data escaped for HTML."
  - "[C-12.FR-6, C-12.AC-3] A Route template that passed its dry run and fails on the next Alert Group produces the Fallback template — every label and the buttons — for that message, increases `muster_template_errors_total{route,destination,template=\"root_message\"}`, sets the Route's `template_error`, records a `fallback_template_used` Timeline entry and raises `MusterTemplateError`; the next successful render clears the error and resolves the Internal alert."
  - "[C-12.FR-3, C-12.AC-5] With `route.language` `ru` the default message and the Thread replies are in Russian, for example the footer \"Закрыта автоматически: {reason}\"; times in messages are absolute, never relative."
  - "[C-12.FR-10] A Storm summary reads \"⛈ Storm on {Route}: K Alert Groups, C urgent — open in Muster\", and in its final state \"Storm over: M Alert Groups still open\"."
  - "[C-12.FR-1] Buttons carry an opaque action id that names the Alert Group by its `public_id`, signed with the button-signature sub-key of the active key and its key id, at most 64 bytes in its compact form; verification accepts any key of the Keyring and refuses a changed byte."
  - "[C-12.FR-13] `muster_template_render_duration_seconds{template}` is observed for every rendered template."
verify: "make ci test-integration"
operator_attention: true
issue: 36
---

# S-036. Default message, built-in texts, template sandbox, previews and the Fallback template (BE)

## Scope

**IN**

- The template sandbox of ADR-0012: sprout with explicit registries, Alertmanager's functions under their names, the
  injected clock, the caps.
- The markup-neutral Root message of C-12.FR-1 and its default content, the Thread reply texts, the Storm summary and
  the notices, in English and Russian per `route.language`, in `organization.time_zone`.
- Route templates (Root message, line, ack timeout notice) with parsing and a dry run on save, `previewTemplate`, the
  Fallback template and the template error state with `MusterTemplateError`.
- Safe output, shortening to an adapter's length limit, signed button action ids.
- Replacing the minimal renderer of S-034 and S-035.

**OUT**

- Links, Link rules, Lookup tables and Mention settings (S-037); the markup of each messenger and its layout (S-061,
  S-042); the outgoing webhook request templates (S-045); rendering the ack timeout notice at runtime (S-049).
- The editors (S-038).

## Contracts

- **Operations implemented**: `previewTemplate` (`templates:preview`) for the kinds `root_message`, `line` and
  `ack_timeout_notice`, added to the implemented-operations map of `internal/api/server.go` with the renderer and the
  sandbox wired in `runtime.go`; `createRoute` and `updateRoute` accept `policy.templates` and `policy.language` (the
  `unsupported` answer of S-025 is removed — the tests that assert it change with it: the table of
  `internal/routing/routes_test.go` that expects `unsupported` for each template, and `test/e2e/routing_test.go`,
  "the templates until S-036") and dry-run templates; `Route.template_error` is filled. Schemas:
  `TemplatePreviewRequest`, `TemplatePreviewResult`, `TemplateKind`, `TemplateErrorState`, `RouteTemplates`, `Language`,
  `ProblemError` (`line`, `column`).
- **Sandbox** (C-12.FR-4, ADR-0012; `internal/templates`): Go `text/template` with sprout (MIT) and only these
  registries: strings, lists, maps, conversions, regular expressions (RE2); Alertmanager's template functions under
  their Alertmanager names (`toUpper`, `toLower`, `title`, `join`, `match`, `reReplaceAll`, `safeHtml`, `stringSlice`,
  `date`, `tz`, `humanizeDuration` …, re-implemented, never copied); `now` from `internal/clock`; `until`, `seq` and
  `repeat` capped at 1,000 items; output written through a writer that stops at `template.output_cap` characters with an
  error. Anything else is `unknown_function`. One sandbox serves messages, Link rules (S-037) and outgoing webhooks
  (S-045).
- **Template data** (`data.go`): Alertmanager's template data — `Status`, `Alerts` (each with `Status`, `Labels`,
  `Annotations`, `StartsAt`, `EndsAt`, `GeneratorURL`, `Fingerprint`), `GroupLabels`, `CommonLabels`,
  `CommonAnnotations`, `ExternalURL` — plus `.AlertGroup` (`Number`, `Title`, `Summary`, `Status`, `Route`,
  `SeverityLevel`, `Urgent`, `StartedAt`, `URL`, `ReopenCount`, `Owner`). The line template sees one Alert and
  `.AlertGroup`. Label and annotation values arrive escaped for the target markup, with `@` neutralized and cut to
  `message.value_cap`, so `safeHtml` returns its argument unchanged.
- **Message** (C-12.FR-1; `message.go`, `default.go`): a markup-neutral structure — `Colour` (status), `Heading` (`#N`
  and title, linked to `MUSTER_PUBLIC_URL/alert-groups/<public_id>`), `Environment` (`cluster` and `environment` labels
  and the start time), `GroupLabels` (the Group key labels except `alertname`), `CommonLabels` (without `severity`,
  `environment`, `prometheus`, `alertname` and the group labels), `CommonAnnotations` (without `summary`, `description`
  and runbook links), `Summary`, `Alerts` (sections 8), `Links` (S-037), `Notices` (still firing after a manual resolve,
  Unclaimed from S-049, Reopen count, delivered late, previous message deleted, no longer updated here), `Footer`
  ("Acknowledged by {user}", "Resolved by {user}", "Resolved automatically: {reason}", "Snoozed by {user} until {time}"),
  `Buttons`. The Alerts section follows `message.alerts_listed`: up to the line limit one line per Alert with the
  labels that differ between Alerts, firing and newest first, resolved ones struck through; above the limit the
  distinct values of each differing label up to the per-label limit, then "+K more", and "Full list in Muster". Adapters
  lay the structure out (S-061, S-042) and declare `Markup()` (`markdown`, `html` or `plain`) and `LengthLimit()`.
- **Route templates** (C-12.FR-2, FR-4; `routes.template_root_message`, `template_line`,
  `template_ack_timeout_notice`): a `root_message` template renders the body of the Root message — the sections from
  `Environment` to `Alerts` — while the colour, heading, links, notices, footer and buttons always come from Muster
  (C-12.FR-2); a `line` template replaces the default line of each Alert; an `ack_timeout_notice` template is stored
  and dry-run here and rendered from S-049. A `null` `root_message` or `line` does not execute a template: Muster lays
  the default content out itself, section by section, so that shortening can trim the Alert lines and the label
  sections in turn, and the default line of an Alert shows the labels that differ between the Alerts. A `null`
  `ack_timeout_notice` renders the built-in template. Every kind has a built-in template, a sandbox template per
  language embedded in `default.go`, whose source an editor starts from: the `root_message` one renders the same
  sections as the default content, and the `line` one, which sees a single Alert, the Alert's labels without the
  excluded ones.
- **Absent and `null` templates** (`api/openapi.yaml` `RouteTemplates`, `internal/api/routes.go`,
  `internal/routing/routes.go`): today `templateOf` in `internal/api/routes.go` maps an absent key and `null` alike to
  nil, and `Update` writes NULL for nil, so an edit that leaves a template out silently resets it to the built-in. From
  this story a key absent from `policy.templates` keeps the stored template, and an explicit `null` resets it to the
  built-in one: the routing input carries each template as "keep", "built-in" or a text, `Update` writes only the
  changed columns, `createRoute` treats absent as built-in, and the description of `RouteTemplates` in the spec says
  so. A dry run and the error state of the Fallback template concern only a template that is set.
- **Built-in texts** (C-12.FR-3, FR-10; `texts/en.json`, `texts/ru.json`, embedded): every text of the default Root
  message and the Thread replies — new Alerts with "…and K more — open in Muster", Replacement, Reopen, Takeover, Snooze
  ended, rise to Urgent, resolution by the system, the release of a disabled or deleted Owner ("The owner was disabled
  — this Alert Group has no owner now." and its `deleted` variant), ack timeout notices, Unclaimed, Reminders,
  auto-unacknowledge — the
  Storm summary ("⛈ Storm on {Route}: K Alert Groups, C urgent — open in Muster" / "Storm over: M Alert Groups still
  open"), the notes of S-035 and the button labels. A test fails when a key exists in one language only. Times are
  absolute in `organization.time_zone`: `2026-10-04 14:05 UTC` for the start, `HH:MM` inside notes.
- **Dry run on save** (C-12.FR-5): a template is parsed, then rendered against up to `template.dry_run_sample` most
  recent Stored Snapshots whose Alerts the Route took, or a built-in example when there are none; any failure answers
  `422` at `/policy/templates/<name>` with `template_syntax` or `unknown_function`, `line`, `column` and a `detail`.
- **Previews** (`previewTemplate`): the kind's template rendered against `stored_snapshot_id`, `alert_group_id` or, by
  default, the Route's recent Stored Snapshots or the example, in `language` (default: the Route's), shortened to
  `length_limit` when given (`truncated`); errors return `200` with `valid: false` and `errors[]` carrying `line` and
  `column`, never as a Problem. An empty `template` previews what a `null` template of the kind shows (above). `source`
  returns the request's `template` — for an empty one the source of the built-in template in the preview's language,
  for an editor to start from; null for `link_rule` and `webhook_request`. The output is laid out in the requested `format` — Mattermost
  Markdown (the default) or Telegram HTML, with alert data escaped for it — with the buttons as a last line of
  bracketed labels, and `format` is echoed in the result.
- **Fallback template** (C-12.FR-6, ADR-0012; `fallback.go`): built in, always valid (a test renders it against every
  example and fuzzed label sets): the heading, every label of the Alert Group and of each Alert, the footer and the
  buttons. When a Route template fails while rendering a Root message or a Thread reply, the Fallback template is used
  for that message and, in the dispatcher's transaction: `muster_template_errors_total{route,destination="",template}`
  + 1; `routes.template_error_since`, `template_error`, `template_error_template` set if empty; a `system` Timeline entry
  `fallback_template_used` with the template and the error, recorded by `groups` from the result `delivery.Enqueue`
  returns; `MusterTemplateError` raised with `route`, `route_name` and `template` (registered here: warning). The next
  successful render of that template for that Route clears the state and resolves the Internal alert. Saving a new
  template clears the state as well.
- **Safe output** (C-12.FR-7; `safe.go`): `@` in alert data is followed by U+200B; links are kept only with the scheme
  `http` or `https`; label and annotation values longer than `message.value_cap` are cut at a character boundary with
  "…"; escapers for Mattermost Markdown and Telegram HTML live here and are applied by the adapters (C-13.FR-8,
  C-14.FR-13).
- **Shortening** (C-12.FR-11; `shorten.go`): a rendered message longer than the adapter's `LengthLimit()` loses Alert
  lines first (down to the distinct values), then the common annotations, common labels and group labels, never the
  heading, status, footer, buttons or the link to Muster.
- **Buttons** (C-12.FR-1, ADR-0011; `internal/buttons`): the buttons of
  [reference.md](../prd/l1/reference.md#buttons-and-links-by-status) per status, Snooze as one button per duration of
  `route.snooze_durations` ("Snooze 1 h"); each carries an action id: version, subject kind (`root`, `reply`, `test`),
  the subject's `public_id` (an Alert Group's, or a Destination's for a test), the command and its argument (a Snooze
  duration index), the first bytes of the key id and an HMAC-SHA256 with the button-signature sub-key of the active key,
  truncated to 128 bits — at most 64 bytes in its compact base64url form, so Telegram's `callback_data` holds it, and a
  separate full key id for Mattermost's `context.key_id`. `deliveries.desired_button_key_id` records the key.
  Verification finds the key in the Keyring by its id and compares the HMAC in constant time.
- **Renderer** (`internal/delivery/render.go`, `internal/messages/render.go`): `messages.Renderer` replaces the minimal
  renderer of S-034: Root messages at `Enqueue`, Thread replies (`replies.go`) when the worker sends them, the Storm
  summary from the `storms` row.
- **Metrics** (C-12.FR-13): `muster_template_errors_total{route,destination,template}` and
  `muster_template_render_duration_seconds{template}` (`le` buckets for template rendering); `template` takes the
  closed set `root_message`, `line`, `ack_timeout_notice`, `link_rule`, `webhook_request`.
- **Log events**: `fallback_template_used` (WARN: `route`, `group`, `template`, `error`), `template_recovered` (INFO:
  `route`, `template`).
- **Dependencies**: sprout in `go.mod`, listed in NOTICE under the shipped licence list.
- **Defaults**: `message.alerts_listed`, `template.dry_run_sample`, `template.output_cap`,
  `message.value_cap`, `route.language`, `organization.time_zone`.

## Steps

1. Write the sandbox and the template data. Check: `sandbox_test.go` refuses `env`, `readFile` and an unknown name,
   stops a loop at the cap and an output at `template.output_cap`, and runs a set of Alertmanager templates unchanged.
2. Write the message structure, the default content, the texts and the replies. Check: `default_test.go` checks the
   section order, the exclusions and both languages.
3. Write safe output, shortening and the buttons. Check: unit tests cover C-12.AC-1, AC-4 and the action id size and
   tampering.
4. Write dry runs, previews, the Route fields and the Fallback template with the error state. Check: `render_test.go`
   and `templates_test.go`.
5. Replace the minimal renderer and extend `live_test.go`. Check: Verification below.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011; an Integration "lab" with its fake receiver and the helpers NOTIFY and
# AG as in S-020 and S-028; the On-call policy in P as in S-025
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
PREVIEW() { curl -s "${H[@]}" $API/template-previews -d "$1"; }

# C-12.AC-2: an unregistered function, in a preview and on save
PREVIEW '{"kind":"root_message","template":"{{ env \"HOME\" }}"}' | jq -c '{valid, e: [.errors[] | {code, line, column}]}'
# {"valid":false,"e":[{"code":"unknown_function","line":1,"column":4}]}
curl -s "${H[@]}" $API/routes -d "{\"name\":\"T\",\"matchers\":[],\"urgent\":false,\"group_key\":[\"alertname\"],\"destination_ids\":[],
  \"policy\":$(jq -c '.templates.root_message = "{{ env \"HOME\" }}"' <<<"$P")}" | jq -c '{status, e: .errors[0] | {pointer, code, line}}'
# {"status":422,"e":{"pointer":"/policy/templates/root_message","code":"unknown_function","line":1}}

# C-12.AC-1, AC-4: 25 Alerts, all carrying @channel and a long common annotation, so that 4,096 characters need
# shortening
curl -s -X PUT $FAM/groups/m1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"PodDown"}}' > /dev/null
LONG=$(printf 'd%.0s' $(seq 1 4000))
for i in $(seq 1 25); do curl -s -X PUT $FAM/groups/m1/alerts/p$i -d "{\"labels\":{\"pod\":\"p$i\",\"cluster\":\"prod\",\"owner\":\"@channel\"},\"annotations\":{\"details\":\"$LONG\"}}" > /dev/null; done
NOTIFY m1 '{"reason":"first notification"}'; G=$(AG 'alertname%3D%22PodDown%22')
OUT=$(PREVIEW "{\"kind\":\"root_message\",\"template\":\"\",\"alert_group_id\":\"$G\",\"length_limit\":4096}")
jq -c '{valid, truncated, len: (.output | length <= 4096)}' <<<"$OUT"        # {"valid":true,"truncated":true,"len":true}
jq -r .output <<<"$OUT" | grep -o -e '^🔴 \[#[0-9]* PodDown\]' -e 'pod: p1, p2, .* +15 more' -e 'Full list in Muster' -e 'Open in Muster' -e '\[Ack\] \[Resolve\] \[Snooze 1 h\]'
# 🔴 [#N PodDown]
# pod: p1, p2, … +15 more
# Full list in Muster
# Open in Muster
# [Ack] [Resolve] [Snooze 1 h]
jq -r .output <<<"$OUT" | grep -c $'owner: @\u200bchannel'                    # 1

# C-12.AC-5: the default message of a Route in Russian
for i in $(seq 1 25); do curl -s -X PUT $FAM/groups/m1/alerts/p$i -d "{\"labels\":{\"pod\":\"p$i\",\"cluster\":\"prod\"},\"status\":\"resolved\"}" > /dev/null; done
NOTIFY m1 '{"reason":"all alerts resolved"}'
PREVIEW "{\"kind\":\"root_message\",\"template\":\"\",\"alert_group_id\":\"$G\",\"language\":\"ru\"}" | jq -r .output | grep -c '^Закрыта автоматически: '
# 1

# C-12.FR-5: a valid template is saved and previewed against the Route's recent Snapshots — the Route takes the next
# Snapshot of PodDown once it exists
R=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"pods\",\"matchers\":[{\"label\":\"alertname\",\"op\":\"=\",\"value\":\"PodDown\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[],\"policy\":$(jq -c '.templates.line = "{{ .Labels.pod }} on {{ .Labels.cluster }}"' <<<"$P")}")
jq -c '{t: .policy.templates.line, e: .template_error}' <<<"$R"               # {"t":"{{ .Labels.pod }} on {{ .Labels.cluster }}","e":null}
for i in 1 2; do curl -s -X PUT $FAM/groups/m1/alerts/q$i -d "{\"labels\":{\"pod\":\"q$i\",\"cluster\":\"prod\"}}" > /dev/null; done
NOTIFY m1 '{"reason":"new alerts added"}'
PREVIEW "{\"kind\":\"line\",\"template\":\"{{ .Labels.pod }} on {{ .Labels.cluster }}\",\"route_id\":$(jq .id <<<"$R")}" | jq -c '{valid, sample}'
# {"valid":true,"sample":"stored_snapshot"}
```

C-12.AC-3 and the Russian Thread replies need a Destination, which C-12 does not have; they are checked through the
recording test adapter, and S-061 repeats them against the fake Mattermost server:

```sh
make dev-db
go test -tags integration -run 'TestLive/(fallback_template|russian_replies|storm_texts)' -v ./internal/delivery/...
# === RUN   TestLive/fallback_template                  (C-12.AC-3)
#     template passed its dry run (2 Alerts); Alert Group with 1 Alert: fallback used; muster_template_errors_total{template="root_message"} +1;
#     route template_error set; timeline system_event=fallback_template_used; MusterTemplateError firing; next good render: cleared and resolved
# === RUN   TestLive/russian_replies                    (C-12.AC-5)
#     thread reply: "Новые алерты (2): …"
# === RUN   TestLive/storm_texts                        (C-12.FR-10)
#     "⛈ Storm on db: 21 Alert Groups, 1 urgent — open in Muster" … "Storm over: 4 Alert Groups still open"
# --- PASS: TestLive (…)
```

## Open questions

None.

## Notes

- Suggested commit: `feat(messages): add the template sandbox, the default message, built-in texts and previews`.
- `operator_attention: true` — the Russian texts of `texts/ru.json` are reviewed by the operator in the pull request.
- The Alertmanager functions are re-implemented from their documented behaviour; no code is copied from Alertmanager's
  repository beyond what its licence allows, and none from copyleft projects.
- The default Root message is laid out from its sections, not by executing the built-in template, so that shortening
  can trim the Alert lines and the label sections in turn; the built-in templates, whose sources `previewTemplate`
  returns for an empty `template`, render the same sections for an editor to start from.
- Route templates render the Root message; Thread replies use the built-in texts, so the Fallback template and the
  template error state concern Root messages.
- `updateRoute` resolves a template key left out to the stored template before it writes the row, which leaves the
  other columns as they were; the dry run runs before the Route is locked, and again under the lock only for a
  template that changed meanwhile.
- The sandbox leaves out sprout's `shuffle` (not deterministic) and `set`, `unset`, `merge` and `mergeOverwrite`
  (they change a dict in place, which can make it contain itself); besides the caps of the Contracts, every
  function result is bounded in total size, and the calls that multiply their input (`repeat`, `indent`, `replace`,
  `join`, `wrap`, the regular expression replacements, `print`) are refused before they build too much.
- When a Destination joins a Route, its Root messages are rendered and the template error state is settled in the
  Route's transaction; the counter, the log line and the Timeline entry of a failure there come with the next render
  through the dispatcher.
- Delivery reads what a message shows through the dispatcher's transaction (`internal/messages/query.sql`), signs the
  buttons with the Keyring (`keyring.VerifyPrefix` checks a signature cut to 128 bits), stores the key in
  `deliveries.desired_button_key_id`, and reports a failed Route template to the dispatcher, which records
  `fallback_template_used`; the Message type of delivery is `messages.Message`, which is why its tests change.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-12.FR-1 | partial | the structure, the default content and the buttons; links are S-037, the layouts S-061 and S-042 |
| C-12.FR-2 | partial | the line template; its editor is S-038 |
| C-12.FR-3 | partial | the texts and `route.language`; the language field on the page is S-038 |
| C-12.FR-4 | partial | the sandbox for message templates; Link rules are S-037, outgoing webhook requests S-045 |
| C-12.FR-5 | partial | the dry run and the preview API; the editor is S-038 |
| C-12.FR-6 | partial | message templates; a failing Link rule is S-037 |
| C-12.FR-7 | partial | neutralized `@`, value cap, `http(s)` links and the escapers; the adapters apply them (S-061, S-042) |
| C-12.FR-10 | full | |
| C-12.FR-11 | partial | shortening; each adapter declares its limit (S-061, S-042) |
| C-12.FR-13 | partial | the metrics for message templates; `link_rule` and `webhook_request` are observed from S-037 and S-045 |
| C-12.AC-1 | partial | the zero-width space; the `javascript:` link is S-037 |
| C-12.AC-2 | partial | the API; the editor shows the position in S-038 |
| C-12.AC-3 | full | through the recording test adapter; repeated against the fake Mattermost server in S-061 |
| C-12.AC-4 | full | |
| C-12.AC-5 | full | repeated against the fake Mattermost server in S-061 |
| C-09.FR-10 | partial | the resolution reason in messages |
| C-08.FR-1 | partial | the template and language policy fields |
