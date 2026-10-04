# C-15. Outgoing webhook

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-12, C-13

**Goal.** Deliver Alert Groups to any HTTP endpoint — as an ordered stream of versioned events, or as templated requests
that create, update and thread a message in any chat with a REST API — signed, with credentials kept secret, and within
the outbound address policy.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The Admin sends Alert Group events to an automation endpoint that verifies signatures and records every step.
2. The Admin connects a chat that has a REST API: "create" posts a message and extracts `$.data.id` from the response,
   "update" edits `…/messages/{{ .Response.id }}`, and "reply in thread" posts follow-ups; for a chat that needs a
   thread created first, "open thread" runs once before the first reply.
3. The chat's API needs a bearer token; the Admin stores it as the Secret `token` and writes the header
   `Authorization: Bearer {{ .Secrets.token }}`. A Viewer opening the Destination sees the reference, never the token.
4. The Admin regenerates the Signing secret; requests carry both signatures until the previous secret is retired.
5. A template fails; that delivery is Not delivered, the Destination shows a template error, `MusterTemplateError` is
   raised.
6. A URL pointing at a cloud metadata address is refused with an error naming the rule.
7. The receiving endpoint is down for an hour. The Destination becomes Broken; afterwards every event of the hour
   arrives in order.
8. A Storm hits a Route. The messengers get one Storm summary, while the automation endpoint still records a `created`
   event for each of the 300 Alert Groups.

## Functional requirements

- **C-15.FR-1** An outgoing webhook Destination has a name, a mode — events, template or both — its requests, its
  Secrets (FR-10), a proxy (C-03.FR-19), Mention settings (FR-11) and a limiter (`destination.webhook.limiter`). It has
  no Connection and no Destination check.
- **C-15.FR-2** Events mode sends one POST per lifecycle event, one to one with the rows of the lifecycle event tables
  (C-09.FR-22, C-10.FR-15, C-17.FR-11): every event of every Alert Group on the Destination's Routes, Quiet events and
  events with nothing to show in a messenger included; the delivery events of the Loud/Quiet table are not sent. Events
  are sent in order per Alert Group, at least once and never collapsed: the next event of an Alert Group waits until the
  previous one is delivered or ends as Not delivered; events of different Alert Groups do not wait for each other. Each
  event has its own `webhook-id`, kept across retries, so that the receiver can drop duplicates. The body is versioned
  JSON: `version` (`1`); `event` (the event name of its row); `notify` (`true` exactly when the row is Loud); `mentions`
  (FR-11); `sequence` (the Alert Group's lifecycle event number, kept across retries: it grows with every lifecycle
  event of the Alert Group, may have gaps, and for a Destination added to the Route later does not start at 1, so
  receivers rely only on "greater means newer"; 0 on `test`); `occurred_at`; `actor` (the user or Service account and
  the Transport, or `system` with the reason); `alert_group` (number, `id`, title, `summary`, status, Owner, Route,
  Urgent, Severity level, times, URL); and `alerts[]` (one element per Alert, by fingerprint). Receivers ignore event
  names and fields they do not know. Every event carries the current state of the Alert Group. The event `test` is sent
  only by a Destination test (C-16.FR-1): it carries an example or recent Alert Group with `test: true` and belongs to
  no Alert Group's order. Events mode is not subject to Storms: during a Storm every event is sent, `created` of the
  Alert Groups the Storm summary stands for included. Events that came due while the Destination was Broken are sent in
  order after it recovers, none dropped and with no age limit. New event names and new fields are added
  within `version` 1; removing a field or changing its meaning needs a new `version`. The URL and headers are templates
  that may use Secrets. The schema is part of the API specification.
- **C-15.FR-3** Template mode defines a "create" request (method, URL, headers, body) with extraction rules that pick
  values from the response (for example `$.data.id`); the values are stored per Alert Group and Destination. "Update"
  requests use them and run through reconciliation, so only the latest state is sent. Optional "open thread" runs once,
  before the first Thread reply, and adds its extracted values; optional "reply in thread" carries the events whose last
  column in the lifecycle event tables is a Thread reply. Templates see the Alert Group, its Alerts, the extracted values
  (`.Response`), the event, `.Notify`, `.Mentions` (FR-11), `.Secrets` and the trusted `mention` function. Template mode
  follows Storms like a messenger (C-11.FR-6): the Storm summary is sent through the "create" and "update" requests,
  whose templates then see `.Storm` — the Route, the counts and the link to Muster — and no Alert Group. After a Broken
  period, template mode sends only the current state, like a messenger (C-11.FR-19).
- **C-15.FR-4** If an extraction rule finds nothing, the "create" counts as delivered, the Timeline records the missing
  value, and later requests that use it fail as template errors.
- **C-15.FR-5** Each Destination has its own random Signing secret, generated when the Destination is created and shown
  once in the response that creates it; an Admin can regenerate it later, and it is shown once again. Every request
  carries `webhook-id`, `webhook-timestamp` and `webhook-signature` headers in the style of the Standard Webhooks
  specification, with HMAC-SHA256 over the id, the timestamp and the body. After regeneration, requests carry signatures
  with both the new and the previous secret until the Admin retires the previous one; meanwhile the Destination shows
  "The previous secret still signs, since {date}". Master key rotation does not change Signing secrets.
- **C-15.FR-6** Response mapping: `2xx` → delivered; `429` or `503` with `Retry-After` → RetryAfter; `408`, other `5xx`,
  timeouts and network errors → Transient; `401`, `403`, `404`, `410` → Fatal; `400`, `413`, `422`, redirects and every
  other response → unknown. Redirects are never followed; the error names the target. A request blocked by the outbound
  address policy (FR-8) → Fatal, so the Destination becomes Broken with the rule that blocked it as the reason: every
  later request would be blocked too.
- **C-15.FR-7** A request whose template fails is not sent; that delivery ends as Not delivered with no fallback body,
  the Destination shows a template error and `MusterTemplateError` is raised.
- **C-15.FR-8** Requests go through the outbound HTTP package under the outbound address policy and the Destination's
  proxy (ADR-0015).
- **C-15.FR-9** The documentation includes the event schema with every event name, the versioning rule of FR-2, how to
  drop duplicates by `webhook-id`, signature verification examples and template recipes for chats with one-step and
  two-step threads, Storm summaries included.
- **C-15.FR-10** Secrets: a Destination holds named secret values, encrypted and write-only (C-03.FR-21), referenced as
  `{{ .Secrets.<name> }}` in the URL, headers and body of both modes. Secret values — and the Signing secret — are
  masked in test results, previews, Timeline errors and logs. Everyone who can read Destinations sees the URL and header
  templates with the references only. The form warns when a header named `Authorization`, or a URL's query or user
  information, holds a literal value instead of a Secret reference.
- **C-15.FR-11** Mention settings of an outgoing webhook Destination have the kinds and choices of C-12.FR-8 — nobody,
  everyone, chosen Muster users or named groups — and resolve the symbolic Mentions of each lifecycle event the same
  way. Because Muster does not know the receiver's markup, the result is data: the event body's `mentions[]` and the
  templates' `.Mentions` list each target as `{"type": "everyone"}`, `{"type": "group", "name": …}` or
  `{"type": "user", "id": …, "name": …, "login": …}`, and in templates `mention` renders a target as plain text —
  `@all`, the group name, or `@` and the login — for the template to wrap in the receiver's syntax.
- **C-15.FR-12** Deleting an outgoing webhook Destination (C-11.FR-14): in events mode its queued events are abandoned
  and end as Not delivered, no final event is sent, and its secrets — the Signing secrets, the Secrets and the proxy
  password — are wiped at once; in template mode the final edit goes out through the "update" request first and the
  secrets are wiped after it; in mode both, the events are abandoned at once and the secrets wiped after the final
  edits.

## UI

Destination form with mode tabs, request builders for create, update, open thread and reply in thread, extraction rules,
header editor, Secrets (add, replace, remove; values never shown), Signing secret actions (generate, regenerate, retire
previous), the proxy form and previews (C-16); the shared Destination pages of C-13.

## API surface

`destinations` of type webhook (create, update); `destinations/{id}/secrets` (list names with "set" and last change,
set, delete); `destinations/{id}/signing-secret` (read status, regenerate, retire previous); creating an outgoing webhook Destination
returns its first Signing secret once.

## Acceptance

Checked against a fake receiving endpoint.

- **C-15.AC-1** The receiving endpoint verifies every request with the secret shown at generation.
- **C-15.AC-2** After regeneration, requests carry two signatures until the previous secret is retired, then one.
- **C-15.AC-3** In template mode, Acknowledge leads to one "update" request that uses the id extracted from the "create"
  response.
- **C-15.AC-4** A URL resolving to `169.254.169.254` is refused under both policies.
- **C-15.AC-5** A `302` response ends that delivery as Not delivered, and the error names the redirect target.
- **C-15.AC-6** In events mode, `created`, `acknowledged` and `resolved` of one Alert Group arrive in this order, each
  with its own `webhook-id`; when the endpoint answers `acknowledged` once with `503` and `Retry-After`, it is retried
  with the same `webhook-id`, and `resolved` is not sent before it succeeds.
- **C-15.AC-7** `400`, `413` and `422` end that delivery as Not delivered with the Destination healthy; `404` makes the
  Destination Broken.
- **C-15.AC-8** A Secret used in a header reaches the endpoint, and its value appears neither in the Destination as read
  by a Viewer, nor in the Timeline error of a failed delivery, nor in any log line.
- **C-15.AC-9** After a Broken period, the events mode delivers every event of the period in order; the template mode
  sends only the current state.
- **C-15.AC-10** During a Storm of 30 new Alert Groups on a Route with threshold 20, an events-mode Destination of that
  Route receives 30 `created` events, and a template-mode Destination receives one Storm summary through its "create"
  request instead of the non-urgent Alert Groups after the 20th.
- **C-15.AC-11** Every row of the lifecycle event tables produces exactly one events-mode request whose `event` is the
  row's name and whose `notify` is its loudness, Quiet rows and rows with nothing to show in a messenger included, with
  `version` `1` (a table-driven test).
- **C-15.AC-12** With Mention settings that mention Alice for new Alert Groups, the `created` event carries
  `mentions` with Alice's id, name and login, and an `alerts_added` event of an acknowledged Alert Group carries none.
- **C-15.AC-13** Deleting an events-mode Destination while its endpoint is down with three events queued sends nothing
  more to the endpoint, ends the three events as Not delivered, and leaves the Destination with no Signing secret and no
  Secrets.

## Related ADRs

ADR-0005, ADR-0011, ADR-0012, ADR-0015.

## Depends on

C-11 — delivery; C-12 — templates; C-13 — the shared Destination pages.

## Suggested story split

- **BE** — events mode mapped to the lifecycle event tables, template mode with extraction, threads and Storm summaries,
  Mentions as data, Secrets, Signing secret, error mapping.
- **FE** — the outgoing webhook Destination form with request builders, Secrets and Signing secret actions.
