# Outgoing webhook: template mode

An outgoing webhook Destination in the **template mode** keeps one message per Alert Group in any chat that has a REST
API, the way Muster keeps a message in Mattermost or Telegram: a **create** request posts it, an **update** request
edits it to the current state, and optional **open thread** and **reply in thread** requests carry the follow-ups.
You write each request as Go templates; Muster sends them, signed, and reads the values it needs from the answers.

For an ordered stream of every lifecycle event instead, use the [events mode](events.md); the mode `both` sends both.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org` and the chat's API is
`https://chat.example.org/api`.

## Create the Destination

Create a Destination of type `webhook` with the mode `template` (or `both`, with the `events` request too):

```json
{
  "type": "webhook",
  "name": "team chat",
  "mode": "template",
  "template": {
    "create": {
      "method": "POST",
      "url": "https://chat.example.org/api/channels/ops/messages",
      "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }],
      "body": "{\"text\":{{ printf \"#%d %s (%s)\" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}}",
      "extract": [{ "name": "id", "path": "$.data.id" }]
    },
    "update": {
      "method": "PUT",
      "url": "https://chat.example.org/api/channels/ops/messages/{{ .Response.id }}",
      "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }],
      "body": "{\"text\":{{ printf \"#%d %s (%s)\" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}}"
    }
  },
  "proxy": { "enabled": false },
  "mentions": { "...": "Mention settings, as for any Destination" },
  "limiter": { "limit": 5, "per_seconds": 1 }
}
```

Each request has:

- a **method**: `GET`, `POST`, `PUT`, `PATCH` or `DELETE`;
- a **URL**, **headers** and an optional **body**, each a Go template in the same sandbox as message templates;
- for `create` and `open_thread` only, **extraction rules**: a name and a JSONPath (RFC 9535) into the JSON answer.

`create` and `update` are required; `open_thread` and `reply_in_thread` are optional. When you save, every template
is parsed and run on a dry run with a built-in example Alert Group, its Secrets and the values it may read filled with
`example-<name>`; an error answers `422` at `/template/<request>/<field>` with its line and column. The URL must then be
an absolute `http` or `https` URL.

Muster sets `webhook-id`, `webhook-timestamp` and `webhook-signature` on every request, signed with the Destination's
Signing secret as in the events mode, and `Content-Type: application/json` when a request has a body and no
`Content-Type` header of its own. The requests go through Muster's outbound address policy and the Destination's
proxy; redirects are never followed. The answers map to outcomes as in the [events mode](events.md#how-the-answer-counts).

## What the templates read

| Field | Value |
|---|---|
| `.AlertGroup` | `Number`, `Title`, `Summary`, `Status`, `Route`, `SeverityLevel`, `Urgent`, `StartedAt`, `URL`, `ReopenCount`, `Owner`; absent (nil) for a Storm summary |
| `.Alerts` | the Alerts, as in Alertmanager: `Status`, `Labels`, `Annotations`, `StartsAt`, `EndsAt`, `GeneratorURL`, `Fingerprint`; with `.Alerts.Firing` and `.Alerts.Resolved` |
| `.Status`, `.GroupLabels`, `.CommonLabels`, `.CommonAnnotations`, `.ExternalURL` | as in Alertmanager templates |
| `.Storm` | for a Storm summary only: `Route`, `AlertGroupCount`, `UrgentCount`, `Final` (the Storm ended) and `URL` (the Route's Alert Groups in Muster) |
| `.Response` | the values the extraction rules found, by name |
| `.Event` | the lifecycle event a Thread reply carries, such as `alerts_added`; empty otherwise |
| `.Notify` | `true` when the message is Loud |
| `.Mentions` | the Mention targets of a Loud message (see below) |
| `.Secrets` | the Destination's Secrets, by name |
| `.Final` | in the final edit only: "No longer updated here; current state in Muster: {link}" |

Alert data reach the templates **as received**, not escaped for any markup, because Muster does not know the chat's
format. Write JSON bodies with `toJson` (or `js` inside a string), so that a label with a quote or a line break cannot
break the body.

Read a Secret as `{{ .Secrets.<name> }}` and an extracted value as `{{ .Response.<name> }}` — or with `$.` in front, or
as `{{ index .Secrets "<name>" }}` with a quoted name. Any other use, such as `{{ with .Secrets }}`, `{{ $s := .Secrets }}`
or `{{ toJson .Response }}`, is refused when you save: Muster checks before each request that every Secret and value a
template reads exists, which it can only do for names written out. Printed or encoded whole, as `{{ toJson . }}` would,
the Secrets show as `[redacted]`.

## Extraction rules and `.Response`

A rule picks the first node its JSONPath selects in the JSON answer of `create` or `open_thread` — a string, a number
as written, or a boolean — and stores it as a string under its name, per Alert Group and Destination. The first rule
of `create` is also the message id Muster shows. `create` reads no values (it runs first), `open_thread` and `update`
read those of `create` (an `update` can come before any thread exists), and `reply_in_thread` reads those of both.

When a rule finds nothing — the answer is not JSON, the path selects nothing, `null`, an object or an array, or a value
longer than 1024 bytes — the `create` still counts as delivered, the Alert Group's Timeline records
`template_value_missing` with the rule's name, and every later request that reads the value fails as a template error.

## Reconciliation

The template mode follows the Desired state like a messenger. The Desired state is the rendered `update` request
(with Secrets and extracted values left out): when a change leaves it as it was, no request is made, and changes made
while a request is pending collapse into the next `update`. Lifecycle events whose form is a Thread reply in the
lifecycle event tables — new Alerts, a Takeover, a Reopen, an ack timeout notice, a Reminder… — go through
`reply_in_thread`, batched like Thread replies in a messenger; without `reply_in_thread` they are not sent.

After the Destination was Broken, the template mode sends only the current state: one `update` per published Alert
Group, no reply for the events of the period, and no `create` for an Alert Group that started and resolved meanwhile.

Deleting the Destination sends the final edit through `update`, with `.Final` set, and wipes its secrets once the
final edits are done; in the mode `both`, the queued events end as Not delivered at once.

## Recipe: a chat with one-step threads

A reply is posted under the message:

```json
"reply_in_thread": {
  "method": "POST",
  "url": "https://chat.example.org/api/channels/ops/messages/{{ .Response.id }}/replies",
  "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }],
  "body": "{\"text\":{{ printf \"%s on #%d\" .Event .AlertGroup.Number | toJson }},\"notify\":{{ .Notify }}}"
}
```

## Recipe: a chat with two-step threads

The chat needs a thread created first; `open_thread` runs once, before the first reply of each message, and adds its
values:

```json
"open_thread": {
  "method": "POST",
  "url": "https://chat.example.org/api/threads",
  "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }],
  "body": "{\"root\":\"{{ .Response.id }}\"}",
  "extract": [{ "name": "thread", "path": "$.thread.id" }]
},
"reply_in_thread": {
  "method": "POST",
  "url": "https://chat.example.org/api/threads/{{ .Response.thread }}/messages",
  "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }],
  "body": "{\"text\":{{ printf \"%s on #%d\" .Event .AlertGroup.Number | toJson }}}"
}
```

If `open_thread` fails, no reply is sent and the reply is retried by the usual rules; once it succeeded it does not
run again for that message, whatever the reply after it came to.

## Recipe: Storm summaries

During a Storm, the Destination gets one Storm summary through `create` — Loud, first — and its later counts and final
state through `update`, instead of the Alert Groups the Storm holds. Their templates see `.Storm` and no Alert Group, so
templates that should handle both branch on it:

```json
"body": "{\"text\":{{ if .Storm }}{{ printf \"Storm on %s: %d new Alert Groups, %d Urgent — %s\" .Storm.Route .Storm.AlertGroupCount .Storm.UrgentCount .Storm.URL | toJson }}{{ else }}{{ printf \"#%d %s (%s)\" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}{{ end }},\"over\":{{ if .Storm }}{{ .Storm.Final }}{{ else }}false{{ end }}}"
```

A template that reads `.AlertGroup` without the branch fails for a summary as a template error.

## Mentions

Muster does not know the chat's syntax for a Mention, so a Loud message carries its targets as data in `.Mentions`:
each has `Type` (`everyone`, `group` or `user`) and, by type, `Everyone` (`all`, `channel` or `here`), `Name`, `ID` and
`Login`. The trusted function `mention` renders a target as plain text — `@all` (or `@channel`, `@here` as the Mention
settings chose), the group's name, or `@` and the login — for the template to wrap in the chat's syntax:

```
{{ range .Mentions }}<{{ mention . }}> {{ end }}
```

## Template errors

A request whose template fails is not sent: that delivery or Thread reply ends as Not delivered with no fallback body,
since the chat expects its own format. The Destination shows `template_error` with `fallback` `not_sent`,
`muster_template_errors_total{template="webhook_request"}` grows, the log line `webhook_template_failed` names the
request, and the Internal alert `MusterTemplateError` fires with the labels `destination`, `destination_name` and
`template`. The next request of the Destination that renders clears the error and resolves the alert.

Preview a request template before you save it with `POST /api/v1/template-previews` and the kind `webhook_request`: it
renders against a sample, with Secrets shown as `[redacted]` and extracted values as `example-<name>`.
