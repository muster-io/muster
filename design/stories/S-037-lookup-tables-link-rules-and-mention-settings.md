---
id: S-037
title: Lookup tables, Link rules and Mention settings (BE)
capability: C-12
kind: be
layer: L1
depends_on: [S-036]
covers: [C-12.FR-1, C-12.FR-4, C-12.FR-6, C-12.FR-7, C-12.FR-8, C-12.FR-9, C-12.FR-12, C-12.FR-13, C-12.AC-1, C-12.AC-6, C-09.FR-14, C-01.FR-13]
files_touched:
  - internal/links/lookup.go
  - internal/links/rules.go
  - internal/links/render.go
  - internal/links/explore.go
  - internal/links/query.sql
  - internal/links/lookup_test.go
  - internal/links/rules_test.go
  - internal/links/render_test.go
  - internal/templates/sandbox.go
  - internal/templates/sandbox_test.go
  - internal/mentions/mentions.go
  - internal/mentions/query.sql
  - internal/mentions/mentions_test.go
  - internal/messages/default.go
  - internal/messages/render.go
  - internal/messages/preview.go
  - internal/messages/message.go
  - internal/messages/safe.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/messages/default_test.go
  - internal/messages/render_test.go
  - internal/delivery/delivery.go
  - internal/delivery/worker.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/deliverytest/recorder.go
  - internal/delivery/live_test.go
  - internal/groups/read.go
  - internal/groups/grouping.go
  - internal/groups/dispatcher_test.go
  - internal/fakes/fakealertmanager/groups.go
  - internal/api/links.go
  - internal/api/links_test.go
  - internal/api/templates.go
  - internal/api/alertgroups.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/runtime/bootstrap.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - sqlc.yaml
  - internal/logging/events.go
  - test/e2e/links_test.go
acceptance:
  - "[C-12.FR-9] Lookup tables are created, read, listed, updated and deleted with named columns and rows of a key and one value per column; a row whose values do not match the columns answers 422 `column_mismatch`; deleting a table that a Link rule's template reads answers 409 `in_use`."
  - "[C-12.FR-9, C-12.AC-6] A Link rule \"Dashboard\" whose URL template calls `lookup \"grafana\" .Labels.cluster \"address\"` renders its link in `previewTemplate` against a Stored Snapshot with `cluster=prod`; a Link rule whose template fails at runtime is left out of the links and increases `muster_template_errors_total{template=\"link_rule\"}`."
  - "[C-12.FR-9] The built-in \"Explore\" rule exists from the first start, cannot be deleted (409 `builtin_immutable`), and turns `generatorURL` into a Grafana Explore link through the Lookup table `grafana` (columns `address`, `datasource_uid`) keyed by `environment`, else `cluster`; without the table or the key it yields no link and no error."
  - "[C-12.FR-9, C-12.FR-1, C-09.FR-14] `getAlertGroup` returns `links`: the matching Link rules (scope Alert Group, or one link per value of a label), `runbook_url` as \"Runbook\", `dashboard_url` as \"Dashboard\" and `generatorURL` as \"Source\"; the Root message shows the same links as one line after \"Open in Muster\"."
  - "[C-12.FR-7, C-12.AC-1] A `runbook_url` annotation `javascript:alert(1)` yields no link anywhere."
  - "[C-12.FR-8] Mention settings are validated per Destination type — `everyone` `channel`, `all` or `here` where the messenger offers it, `none` only for Telegram; `user_ids` naming existing Users; groups only where the messenger has them — and resolve a lifecycle event's symbolic Mentions into targets: the Destination's setting for the kind, the Owner for `owner`, the previous Owner for `previous_owner`, and only the Owner for a Reopen into acknowledged, whatever the setting."
  - "[C-12.FR-8] In a template, `{{ mention \"all\" }}` produces a trusted token that the adapter renders for its messenger, while a literal `@all` in the template's text is neutralized like alert data."
  - "[C-12.FR-12] A user target is rendered with the user's messenger username when the user has an Account link in that Destination's identity space, and with the Muster display name otherwise, which mentions nobody; the footer names users the same way."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 37
---

# S-037. Lookup tables, Link rules and Mention settings (BE)

## Scope

**IN**

- Lookup tables and Link rules with their API, the built-in "Explore" rule, the `lookup` template function.
- The links of an Alert Group — Link rules, `runbook_url`, `dashboard_url` and `generatorURL` — in the Root message and
  on `getAlertGroup`.
- Mention settings: validation per Destination type, resolution of symbolic Mentions into targets, the trusted
  `mention` template function, user names through Account links.

**OUT**

- Storing Mention settings on a Destination, which arrives with each type's create and update (S-039, S-042, S-044),
  and rendering targets in each messenger's syntax (S-061, S-042); Mentions as data for outgoing webhooks (S-044,
  S-045).
- Creating Account links (S-051); the pages (S-038, S-064).

## Contracts

- **Operations implemented**: `listLookupTables`, `createLookupTable`, `getLookupTable`, `updateLookupTable`,
  `deleteLookupTable` (`lookup-tables:read` / `:write`); `listLinkRules`, `createLinkRule`, `getLinkRule`,
  `updateLinkRule`, `deleteLinkRule` (`link-rules:read` / `:write`); `previewTemplate` gains the kind `link_rule`;
  `AlertGroup.links` is filled. Schemas: `LookupTable(Base, List)`, `LookupEntry`, `LinkRule(Base, List)`,
  `LinkRuleScope`, `Matcher`, `AlertGroupLink`, `MentionSettings`, `MentionSetting`.
- **Lookup tables** (C-12.FR-9; `lookup_tables`, `lookup_table_entries`): a name unique in the Organization (`409
  name_taken`), a description, ordered column names (unique, at least one) and rows replaced as a whole on update, each
  with a key and exactly one value per column (`422 column_mismatch` at `/entries/<n>/values`, `422 duplicate` at
  `/entries/<n>/key` for a key given twice). A table has at most 50 columns and 10,000 rows; a name or a column name
  has at most 200 characters, the description 2,000, a key or a value 4,096 (`422 too_long`). Configuration
  conventions of ADR-0008: `ETag`, `If-Match`, Audit log entries `lookup_table.created`, `.updated`, `.deleted` with
  diffs. Deleting a table answers `409 in_use` while the URL template of a Link rule calls `lookup` with its name (found
  by parsing the templates); the Problem names those rules in `link_rules`, `{id, name}` in the order they were
  created. The hint `organization` is not used; Link rules and tables change no message by themselves.
- **`lookup` function** (`internal/templates/sandbox.go`): `lookup "<table>" <key> "<column>"` reads one cell through a
  per-render cache; a missing table, key or column yields the empty string, never an error. One render reads at most
  100 rows; the 101st is a template error, as a loop past its cap is. A table name or key that cannot exist — not
  valid UTF-8, with a NUL, or longer than any key — reads nothing and is never sent to the database, so a lookup cannot
  abort the transaction of a render. A page of `listLookupTables` holds at most 50 tables, since each carries its rows;
  the list continues with its cursor.
- **Link rules** (C-12.FR-9; `link_rules`, `link_rule_matchers`): a name unique in the Organization, Matchers like a
  Route's (Alertmanager syntax, RE2 anchored, a missing label as an empty value), a scope — `alert_group`, or
  `label_value` with its label — and a URL template. The template is parsed and dry-run on save against the most recent
  Stored Snapshots of the Organization (`template.dry_run_sample`) whose common labels its Matchers match, or the
  built-in example when none does, so that a rule is not refused for data it never renders, each sample a render
  with its own lookups; a failure answers `422` at
  `/url_template` with `template_syntax` or `unknown_function`, `line` and `column`. Audit log entries
  `link_rule.created`, `.updated`, `.deleted`.
- **Evaluation** (`render.go`, C-12.FR-9): for an Alert Group, each rule whose Matchers all match the Alert Group's
  common labels yields one link (scope `alert_group`, named by the rule) or one link per distinct value of its label
  among the Alert Group's Alerts (scope `label_value`, named "<rule>: <value>"), in the order the rules were created. The
  template sees the data of S-036 plus `.Labels` — the common labels for scope
  `alert_group`, the labels of the first Alert carrying the value for `label_value` — and `.Value`. A rendered URL is
  kept only when it parses as an absolute `http` or `https` URL; an empty result is no link. A template that fails is
  left out and counted in `muster_template_errors_total{route,destination,template="link_rule"}` with the Alert Group's
  Route; it raises no Internal alert and records no Timeline entry. `muster_template_render_duration_seconds{template=
  "link_rule"}` is observed. A rule's failures are counted only for an Alert Group, not for the samples of a dry run or
  a preview. After the rules come the annotations `runbook_url` ("Runbook") and `dashboard_url` ("Dashboard") of the
  first Alert whose value is an `http(s)` URL, and the `generatorURL` of the first firing Alert that has one
  ("Source"); the Alerts are read firing first and newest first, so the first Alert with a value need not be the one
  that has a usable link. A URL is kept only without user information, spaces, control or format characters, or the
  characters of Mention tokens. `previewTemplate` for `link_rule` returns the link the rule yields, empty when its
  output is not such a URL.
- **Built-in "Explore" rule** (C-12.FR-9; `link_rules.builtin`): a start-up ensure step (`bootstrap.go`, S-007) creates
  it once per Organization: name "Explore", no Matchers, scope `alert_group`, a URL template that takes `address` and
  `datasource_uid` from the Lookup table `grafana` by the `environment` label, else the `cluster` label, and the PromQL
  expression from the `g0.expr` parameter of the first firing Alert's `generatorURL`, and builds Grafana's documented
  Explore URL `<address>/explore?schemaVersion=1&orgId=1&panes=<JSON>` with that data source and expression and the
  range `now-1h` to `now`; it yields nothing when the table, the key, a cell or the expression is missing. An Alert
  Group with neither label is the common case: the template checks for the key before it calls `lookup` and yields no
  link, so no error is counted or logged. Its URL template and Matchers can be edited; it cannot be deleted, renamed or
  given another scope (`409 builtin_immutable`), and `builtin` is read-only. When the rule exists and its URL template
  is still one that an earlier release created, the ensure step replaces it with the current built-in template, as a
  new version of the rule; an edited template is kept, and a second run changes nothing. A table the built-in rule reads — `grafana` — is therefore `in_use` while the rule is
  unchanged.
- **Links in messages and on the page** (C-12.FR-1 item 9, C-09.FR-14): `messages` places the links after "Open in
  Muster" in one line (`Message.Links`), a rule's link by its name made neutral like alert data and the others by the
  built-in texts `link.runbook`, `link.dashboard` and `link.source` in the Route's language; the links of a Root
  message are computed when it is rendered, through the transaction of the change, and a preview of a Root message
  shows those of its sample. `getAlertGroup` returns them as `links` (`kind`, `name`, `url`, and `link_rule_id` for a
  rule's link), computed on read through `groups.SetLinks` with the English names; `kind` is `link_rule`, `runbook`,
  `dashboard` or `source`, so a client names the built-in links in its own language and tells them from a rule of the
  same name. In Mattermost Markdown a link's URL has `(`, `)`, `<`, `>` and `\`
  percent-encoded; in Telegram HTML it is escaped.
- **Mention settings** (C-12.FR-8; `internal/mentions`): `Validate(type, MentionSettings)` — every kind of
  `MentionSettings` present; `everyone` one of `none`, `channel`, `all`, `here` for Mattermost and `none` only for
  Telegram (`422 unsupported` at `/mentions/<kind>/everyone`); `user_ids` naming Users that exist and are not deleted
  (`422 unknown_id`); `groups` as Mattermost group names, and empty for Telegram (`422 unsupported`, C-12.FR-8);
  outgoing webhooks accept every choice (they receive Mentions as data). `Resolve(event mentions,
  Destination, AlertGroup) → []Target` turns `new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`, `snooze_ended` and
  `rise_to_urgent` into the Destination's setting for that kind, `owner` into the Owner as the event recorded it — the
  Owner after the Timeline entry, or the one it released for a rise to Urgent or an auto-unacknowledge —
  `previous_owner` into the previous Owner of a `takeover`, and a Reopen into acknowledged into the Owner only, as the
  lifecycle event table gives it only `owner`. A user listed by a lower-case `public_id` is normalized; deleted users,
  duplicates and the choices the Destination's type does not offer are left out. A target
  is `everyone` (with the chosen word), `group` (name) or `user` (`public_id`, display name, login, and the messenger
  username of the user's Account link in the Destination's identity space — all of Telegram, or `mattermost:<connection>`
  — read from `account_links`). Delivery passes the targets of a Loud message to the adapter in `Call.Targets`,
  resolved by its `Mentioner` (`*mentions.Service`, wired by the runtime) in the transaction that prepares the call —
  a Publication without a lifecycle event, a Thread reply with the first event it carries; a Quiet message gets none,
  and a resolution that fails leaves the call for its next attempt. The recording adapter records `Targets`.
- **`mention` function** (C-12.FR-8, ADR-0012): `{{ mention "all" }}`, `{{ mention "channel" }}`, `{{ mention "here" }}`,
  `{{ mention "owner" }}` and `{{ mention "group" "<name>" }}` emit a trusted token that survives escaping and that the
  adapter replaces with its own syntax (S-061, S-042) or with data (S-045); every other `@` in template output is
  neutralized like alert data. The token is a name between characters of the Unicode private use area; `messages`
  strips those characters from alert data — values, label and annotation names, fingerprints — and the layout shows a
  token only in template output, so only a template writes a token, and the preview and the plain text
  show a token as `@all`, `@channel`, `@here`, `@owner` or `@<group>`. Any other argument is a template error.
- **User names** (C-12.FR-12): the footer and user targets use the messenger username when the user has an Account link
  in the Destination's identity space, otherwise the display name as plain text, which notifies nobody. A Root message
  carries the footer naming the user by display name in `Footer` and, per identity space where the user has an
  Account link with a username, the footer naming them by that username in `Footers`; `Message.FooterIn(space)` is the
  one an adapter shows. The username is written without `@`, so a footer never notifies.
- **Fake Alertmanager** (C-01.FR-13): `PUT /_fake/groups/{group}/alerts/{alert}` accepts `generator_url`, sent as the
  Alert's `generatorURL`.
- **Log events**: `link_rule_failed` (WARN: `link_rule`, `route`, `group`, `error`) at most once per rule and Alert
  Group.
- **Wiring** (`internal/api/server.go`, `internal/api/problem.go`, `internal/runtime/runtime.go`, `sqlc.yaml`): the ten
  operations join the implemented-operations map and the API `Config` gains `links`; `problem.go` maps the new domain
  errors (`in_use`, `builtin_immutable`, `column_mismatch`, the name conflicts); the runtime hands `links` and
  `mentions` to the renderer; `sqlc.yaml` gains the entries for `internal/links/query.sql` and
  `internal/mentions/query.sql`.

## Steps

1. Write Lookup tables with `lookup` and their API. Check: `lookup_test.go` covers the column check, replacing rows,
   `in_use` and missing cells.
2. Write Link rules with their API, the dry run and the built-in rule. Check: `rules_test.go` covers the scopes, the
   `builtin` refusals and the ensure step run twice.
3. Write link evaluation, the annotations and the Explore link, and wire them into messages and `getAlertGroup`. Check:
   `render_test.go` covers C-12.AC-1, AC-6 and the absent table.
4. Write Mention settings validation, resolution and the `mention` function. Check: `mentions_test.go` covers each
   kind, the fixed rules and both name forms.
5. Extend `live_test.go` with Mentions through the recording adapter and add the end-to-end test. Check: Verification
   below.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011; an Integration "lab" without Static labels, its id in INT, with its
# fake receiver and the helpers NOTIFY and AG as in S-020 and S-028; PREVIEW as in S-036
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake

# C-12.FR-9: a Lookup table and its column check
curl -s "${H[@]}" $API/lookup-tables -d '{"name":"grafana","columns":["address","datasource_uid"],"entries":[
  {"key":"prod","values":{"address":"https://grafana.example.org","datasource_uid":"PROM1"}}]}' | jq -c '{name, n: (.entries | length)}'
# {"name":"grafana","n":1}
curl -s "${H[@]}" $API/lookup-tables -d '{"name":"bad","columns":["a"],"entries":[{"key":"k","values":{"b":"x"}}]}' | jq -c '[.status, .errors[0].code]'
# [422,"column_mismatch"]

# C-12.AC-6: a Link rule using the table, and one that fails only on the Alert Group below (it passes its dry run)
LR='{{ lookup \"grafana\" .Labels.cluster \"address\" }}/d/latency?var-ns={{ .Labels.namespace }}'
curl -s "${H[@]}" $API/link-rules -d "{\"name\":\"Dashboard\",\"matchers\":[{\"label\":\"cluster\",\"op\":\"=~\",\"value\":\".+\"}],
  \"scope\":{\"type\":\"alert_group\"},\"url_template\":\"$LR\"}" | jq -r .name                       # Dashboard
curl -s "${H[@]}" $API/link-rules -d '{"name":"Broken","matchers":[],"scope":{"type":"alert_group"},
  "url_template":"https://x.example.org/{{ if eq .Labels.namespace \"api\" }}{{ (index .Alerts 5).Labels.pod }}{{ end }}"}' | jq -r .name   # Broken

# Alerts with a runbook, a javascript: link and a generatorURL
curl -s -X PUT $FAM/groups/l1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"HighLatency"}}' > /dev/null
curl -s -X PUT $FAM/groups/l1/alerts/a -d '{"labels":{"cluster":"prod","namespace":"api"},"annotations":{"runbook_url":"https://wiki.example.org/latency"},"generator_url":"http://prometheus:9090/graph?g0.expr=up%3D%3D0"}' > /dev/null
curl -s -X PUT $FAM/groups/l1/alerts/b -d '{"labels":{"cluster":"prod","namespace":"api","pod":"b"},"annotations":{"runbook_url":"javascript:alert(1)"}}' > /dev/null
NOTIFY l1 '{"reason":"first notification"}'; G=$(AG 'alertname%3D%22HighLatency%22')
SS=$(curl -s -b jar "$API/stored-snapshots?integration=$INT&limit=1" | jq -r '.items[0].id')
PREVIEW "{\"kind\":\"link_rule\",\"template\":\"$LR\",\"stored_snapshot_id\":\"$SS\"}" | jq -c '{valid, output}'
# {"valid":true,"output":"https://grafana.example.org/d/latency?var-ns=api"}
ERR() { curl -s localhost:8082/metrics | grep '^muster_template_errors_total{.*template="link_rule"' | awk '{s+=$2} END {print s+0}'; }
E0=$(ERR)
curl -s -b jar "$API/alert-groups/$G" | jq -c '[.links[] | {kind, name, url: .url[0:60]}]'
# [{"kind":"link_rule","name":"Explore","url":"https://grafana.example.org/explore?schemaVersion=1&orgId=1&"},
#  {"kind":"link_rule","name":"Dashboard","url":"https://grafana.example.org/d/latency?var-ns=api"},
#  {"kind":"runbook","name":"Runbook","url":"https://wiki.example.org/latency"},
#  {"kind":"source","name":"Source","url":"http://prometheus:9090/graph?g0.expr=up%3D%3D0"}]  (no "Broken", no javascript:)
curl -s -b jar "$API/alert-groups/$G" | jq -r '[.links[] | .link_rule_id // "-"] | join(" ")'  # KR… KR… - -
echo $(( $(ERR) - E0 ))                                                                       # 1

# C-12.FR-9: the built-in rule
EX=$(curl -s -b jar $API/link-rules | jq -r '.items[] | select(.builtin) | .id')
curl -s "${H[@]}" -X DELETE $API/link-rules/$EX | jq -c '[.status, .code]'   # [409,"builtin_immutable"]
curl -s "${H[@]}" -X DELETE $API/lookup-tables/$(curl -s -b jar $API/lookup-tables | jq -r '.items[0].id') | jq -c '[.status, .code, [.link_rules[].name]]'
# [409,"in_use",["Explore","Dashboard"]]

# C-12.FR-8: trusted and literal mentions in a template
PREVIEW '{"kind":"root_message","template":"{{ mention \"all\" }} and @all"}' | jq -r .output | sed -n 2p | od -c | head -2
# 0000000    @   a   l   l       a   n   d       @ 342 200 213   a   l   l
# 0000020   \n
```

Mention resolution per Destination needs a Destination: `live_test.go`
covers it through the recording test adapter (`TestLive/mentions`: a Destination with `new_alert_group` → `channel`
and `new_alerts` → Alice; a firing `created` carries `everyone:channel`, an `alerts_added` on an acknowledged Alert
Group carries none, a Reopen into acknowledged carries only the Owner; with Alice's Account link in the Destination's
identity space the Loud `alerts_added` names her by her username, and so does the footer once she acknowledged), and
S-061 and S-042 show it in the messengers.

## Open questions

None.

## Notes

- Suggested commit: `feat(links): add lookup tables, link rules and mention settings`.
- `lookup` never fails, so a Link rule that reads a missing row yields no link instead of a template error; a real
  template error (a failing function, an index out of range) is counted.
- The Explore link follows Grafana's documented Explore URL format; the end-to-end test checks it against a fixed
  expected URL.
- Changes in the pull request of the story: the files that carry the targets through delivery (`delivery.go`,
  `worker.go`, `threads.go`, the recording adapter), the footers and tokens of a message (`message.go`, `safe.go`,
  the texts of the link names), the Linker of `getAlertGroup` (`grouping.go`, `alertgroups.go`), and the tests that
  cover them joined `files_touched`; the Verification reads the Stored Snapshots of the Integration (`integration` is
  required), counts only the error counter (the duration histogram carries the same label), expects the Explore link
  — the Lookup table row has a data source and the first Alert a `g0.expr` — and prints the second line of the
  preview, the first being the heading.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-12.FR-1 | partial | item 9, the links line |
| C-12.FR-4 | partial | Link rule URL templates and `lookup` |
| C-12.FR-6 | partial | a failing Link rule is left out and counted |
| C-12.FR-7 | partial | `http(s)`-only links |
| C-12.FR-8 | partial | validation, resolution and `mention`; storing the settings is S-039 and S-042, the messenger syntax S-061 and S-042, the form S-064, data for webhooks S-044 and S-045 |
| C-12.FR-9 | partial | the API, the rules and the links; the pages and the links block are S-038 |
| C-12.FR-12 | full | checked with real Account links in C-18.AC-7 (S-051) |
| C-12.FR-13 | partial | `link_rule` rendering time and errors |
| C-12.AC-1 | partial | the `javascript:` link; with S-036 complete |
| C-12.AC-6 | partial | the API; the page preview is S-038 |
| C-09.FR-14 | partial | `links` on the Alert Group |
| C-01.FR-13 | partial | `generatorURL` in the fake Alertmanager |
