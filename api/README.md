# Muster API

`openapi.yaml` is the source of truth for the HTTP API (OpenAPI 3.1.2). It is written before the code it describes, reviewed
like code, and served by the binary at `/api/v1/openapi.yaml`. Generated code (`internal/api/gen/`, `pkg/apiclient/`,
`web/src/api/gen/`) is checked in, `make generate` regenerates it, and CI's `generate` job fails on stale output.

## How the spec is organised

- **One file.** `paths` are grouped by tag, one tag per capability area: Sessions, Profile, Users, OIDC, Audit log, API
  tokens, Organization, Integrations, Ingest, Messenger callbacks, Routes, Alert groups, Live updates, Connections,
  Destinations, Links, Templates, Account links, System, Health and metrics, Outgoing webhooks.
- **Servers and listeners.** The default server is the app listener, `{public_url}/api/v1`. Operations on the other
  listeners carry their own `servers` entry: ingestion, Heartbeat and messenger callbacks use `{ingest_url}/api/v1`; health
  and metrics use the internal listener. Every operation also names its listener in `x-listener`.
- **Components.** `schemas` hold resources, `*Input` request bodies and `*List` pages; `parameters` hold pagination,
  `If-Match`, the Alert Group filters and path ids; `responses` hold the shared problem answers (`400`, `401`, `403`, `404`,
  `409`, `410`, `412`, `413`, `422`, `428`, `429`, `500`, `503`) with examples.
- **Outgoing webhooks.** The events-mode request that Muster sends is described under the top-level `webhooks` object
  (`OutgoingWebhookEvent`, `version: 1`, with the `webhook-id`, `webhook-timestamp` and `webhook-signature` headers); the
  request body has one example per event name.
- **Live updates.** `GET /live-updates` is a server-sent events stream. OpenAPI 3.1 cannot type event streams, so the
  event data is the schema `HintEvent`: one object `{type, id}` whose `type` enum lists every hint and whose `id` is a
  `public_id` or `null`. The stream starts with `retry: 3000`; when the session ends the server closes it and the
  reconnect gets `401`, which stops `EventSource`.
- **Ingestion is not typed.** The two ingestion operations take any bytes (`*/*`, binary) and the handler stores the raw
  body, so they are excluded from request validation and never answer `400`. `AlertmanagerWebhook` documents the payload
  that processing expects; it is referenced by nothing, which is the one lint warning about a component.

## Conventions (ADR-0008)

| Topic | Rule |
|---|---|
| Identifiers | Plural kebab-case nouns; URLs use the opaque `public_id`, never internal ids or `#N`. |
| Public ids | A type prefix of one or two letters and 12 random characters of Crockford base32 (60 bits), such as `AGK7M3QX9P2RTA`; see [`public_id` format](#public_id-format). Responses use the canonical upper case; input is case-insensitive and normalized. |
| Actions | A state change of an existing resource is a verb sub-path (`/acknowledge`, `/disable`, `/accept`, `/reset-totp`); a result the server computes or creates is a plural noun (`/checks`, `/tests`, `/previews`, `/confirmation`). |
| Pagination | `cursor` and `limit` (default 50, maximum 500); responses carry `next_cursor`. No offsets. |
| Filters and sorting | Query parameters: `status`, `route`, `from`, `to`, `q`, `number`, `label`, `sort` with an allowed list (`-` sorts descending). `label` is a whole Alertmanager-syntax Matcher, repeated and combined with AND: `label=namespace%3D%22payments%22&label=pod%3D~%22api-.*%22`. There is no per-label shorthand. Alert Group queries default to the last 7 days; `number` ignores the range. |
| Errors | RFC 9457 `application/problem+json` (`Problem`) with a stable `type` URI, a stable `code` where a refusal needs one, and `errors[]` items with a JSON `pointer` and a stable `code`. `x-problem-types` lists the types; `x-problem-codes` lists every code per type. An operation that this build does not implement yet answers `501` (`not-implemented`). |
| Concurrency | Configuration resources return `ETag` and the same value as `etag` in the body; updates are `PUT` with `If-Match`; `412` on a mismatch, `428` without the header. The Routes list ETag covers the order for `PUT /route-order`. Deletes and `setDestinationSecret` take an optional `If-Match`. |
| Omitted and null | On an update an omitted optional field keeps its stored value and an explicit `null` clears a nullable one (an optional Secret such as a proxy password or the outgoing heartbeat URL included). A deliberate choice is a field of its own: a Snooze is `{until}` or `{no_end: true}`, never a missing value. |
| Conflicts | `409` for natural-key conflicts, resources in use and refused Commands (`command-refused` with a `code`). |
| Rate limits | `429` with `Retry-After`, per token; ingestion is not rate-limited. Calls on the interactive path (checks, channel lists, link codes) answer `503` with `Retry-After` when no limiter token is free in time. |
| Time and units | RFC 3339 UTC; durations are integer seconds in `*_seconds`. Exceptions: retention periods are whole days in `*_days`, measured latencies are milliseconds in `*_ms`. |
| Enums | snake_case values. Kebab-case stays in URL segments, problem types and the `type` of live-update hints. |
| Secrets | Write-only in `*Input` schemas (`writeOnly: true`); reads show `<field>_status` with `set` and `updated_at`. Free text that may echo a remote answer (`error`, `reason`, `message`, `response_body`) is masked. |
| Nullability | Only `type: [T, "null"]`. A required nullable field (such as a hint's `id`) is always present, `null` meaning "none". |
| Actors | An Owner is always a User (`UserRef`). A Service account cannot become an Owner; the resolver of an Alert Group and the author of a Note are an `ActorRef` (a User or a Service account). |

### `public_id` format

Entities keep two identifiers: the `#N` of an Alert Group, for people, and the opaque `public_id` of every resource, for
URLs, the API and signed buttons. A `public_id` is a type prefix followed by 12 random characters of Crockford base32 —
`0`–`9` and `A`–`Z` without `I`, `L`, `O` and `U` — generated in Go (60 bits). The prefixes use the same letters, so a
whole id can be normalized; each type has its own, and all current prefixes have two letters. Responses always carry the
canonical upper-case form. Input is case-insensitive and normalized before lookup: `O` reads as `0`, `I` and `L` as `1`.
The `PublicId` schema carries the pattern, and the database pins each table's prefix and alphabet with a `CHECK`.

| Prefix | Entity | Table |
|---|---|---|
| `AE` | Audit log entry | `audit_log` |
| `AG` | Alert Group | `alert_groups` |
| `AK` | Account link | `account_links` |
| `AR` | Account link request | `account_link_requests` |
| `CN` | Connection | `connections` |
| `DE` | Delivery event (a Timeline entry of the kind `delivery`) | `delivery_events` |
| `DS` | Destination | `destinations` |
| `KR` | Link rule | `link_rules` |
| `NE` | Note (also the id of its Timeline entry) | `notes` |
| `NK` | Integration token | `integration_tokens` |
| `NT` | Integration | `integrations` |
| `PT` | Personal access token | `api_tokens` |
| `RG` | Organization | `organizations` |
| `RT` | Route | `routes` |
| `SA` | Service account | `service_accounts` |
| `SN` | Session | `sessions` |
| `SR` | User | `users` |
| `SS` | Stored Snapshot | `stored_snapshots` |
| `ST` | Service account token | `api_tokens` |
| `TB` | Lookup table | `lookup_tables` |
| `TE` | Timeline entry (lifecycle and system entries) | `timeline_entries` |

## Security

- `sessionCookie` plus `csrf` (`X-CSRF-Token`, value from the session) for the UI; both are needed on mutating requests.
  A session can be limited (`totp_required`, `totp_enrolment_required`): only reading it, signing out and the second
  factor or enrolment work, and everything else answers `403` with the same code.
- Operations that change the caller's own account (profile, password, TOTP, Personal access tokens, Account links,
  the OIDC identity, ending sessions) list `sessionCookie` + `csrf` only; a token gets `403` (`session_required`). Reads under `/me` accept a
  User's token and give `403` (`service_account_not_allowed`) to a Service account's.
- `apiToken`: bearer Personal access token (`mstr_pat_`) or Service account token (`mstr_sat_`).
- `integrationToken`: bearer Integration token (`mstr_int_`), accepted only by ingestion and Heartbeat; the path-token
  variants set `security: []` and carry the token in the path.
- `telegramSecretToken`: the webhook secret header of the optional Telegram webhook mode. The Mattermost callback has no
  scheme: authenticity rests on the signed action id in the body.

## Extensions

| Extension | Where | Meaning |
|---|---|---|
| `x-permission` | operation | The Permission needed (`<resource>:<verb>`, see the `Permission` schema for the Role matrix), a list of alternatives, or `authenticated` (any signed-in identity), `integration-token` or `none`. |
| `x-permission-check` | operation | `dispatcher` on the Command operations: the middleware checks only that the caller is authenticated, and the dispatcher of `internal/groups` checks the Permission of `x-permission` as its first step, logs `command_refused` and answers the same `403` `forbidden` (ADR-0016). Without it the middleware checks `x-permission`. |
| `x-listener` | operation, server | `app`, `ingest` or `internal`. |
| `x-problem-types` | `Problem` schema | Stable problem types with their statuses. |
| `x-problem-codes` | `Problem` schema | The stable `code` values of each problem type, and whether they appear in `code` or in `errors[].code`. |
| `x-event-kinds` | `LifecycleEvent` schema | The Timeline kind of each lifecycle event. |

## Tooling

- **make lint** runs Redocly with `.redocly.yaml`, which applies the recommended ruleset; accepted warnings are listed
  in `.redocly.lint-ignore.yaml` and explained here under "Spike result".
- **make generate** runs oapi-codegen with `api/codegen-server.yaml` (std-http-server + strict-server + models) and
  `api/codegen-client.yaml` (client + models), and orval for the TypeScript client. It regenerates the route tree and
  the Go and TypeScript clients.
- **oasdiff** compares the spec with the base branch on every pull request. The report goes to the job summary and, on a
  pull request from this repository, into a comment; it does not block before 1.0 (ADR-0008).

Configuration that worked in the spike, for both oapi-codegen files:

```yaml
output-options:
  skip-prune: true       # schemas reachable only from `webhooks` would be dropped otherwise
  nullable-type: true    # tells an omitted optional field from an explicit null
  exclude-tags:          # hand-written handlers; Outgoing webhooks are requests Muster sends
    - Outgoing webhooks
    - Health and metrics
```

`nullable-type` adds a dependency on `github.com/oapi-codegen/nullable`.

## Spike result (OpenAPI 3.1 subset through the generators)

Run against this file, in a scratch module that is not part of the repository.

- **Redocly CLI, recommended ruleset:** valid, no errors, 9 warnings: `operation-2xx-response` on the three OIDC
  redirect operations (they answer `302` only), `operation-4xx-response` on `/health/live`, `/health/ready`, `/metrics`
  and `/openapi.yaml`, which have no error answers, and on `mattermostAction`, which answers `200` to every request,
  and `no-unused-components` for `AlertmanagerWebhook` (documentation of the raw ingestion body).
- **oapi-codegen v2.8.0:** `std-http-server` + `strict-server` + `models` (about 33,000 lines) and, separately, `client` +
  `models` (about 47,000 lines) both generate, `go build ./...` and `go vet ./...` pass. kin-openapi v0.149.0 loads and
  validates the document as 3.1.2.
- **What works as is:** `type: [T, "null"]`, nullable enums, `oneOf` with `discriminator` (Timeline entries, Connections,
  Destinations: union types with `AsX` and `Discriminator()`), `allOf` merging of a base with variants,
  `additionalProperties: true`, `writeOnly`/`readOnly`, top-level `webhooks`, `text/event-stream` responses, and a
  `*/*` binary request body (the generated request object carries `ContentType` and an `io.Reader`).
- **What had to change or be configured:**
  - `skip-prune: true` is required: otherwise schemas reachable only from `webhooks` are dropped and the build fails.
  - `nullable-type: true` turns an optional nullable field into `nullable.Nullable[T]`, so a handler can tell an absent
    field, `null` and a value apart (checked with a unit test on `ProxyConfigInput.password`). A required nullable field
    is `Nullable` without `omitempty`.
  - The generator also emits a `WebhookReceiverInterface` for the `webhooks` entry. Muster sends those requests, so the
    interface is ignored; `exclude-tags` does not suppress it. `exclude-tags` does remove the operations of the excluded
    tags, which is how health and metrics stay out of the strict server.
  - A schema named like an operation's response wrapper collides in the client (`mattermostAction` and a schema
    `MattermostActionResponse`); the schema is `MattermostActionAnswer`. Keep schema names off `<OperationId>Response`.
  - `allOf` with a single member was replaced by the schema itself; `PersonalAccessToken` is one flat object; `HintEvent` is
    one object with an enum `type`, because its former members shared no single `type` value.
  - The raw body of the ingestion endpoint cannot be preserved by a typed handler, so it is declared as binary and the
    request-validation middleware skips the two ingestion operations.
  - Operation-level `servers` mark the other listeners; the request-validation middleware needs one view of the document
    per listener. This was not exercised.
- **Verdict:** OpenAPI 3.1.2 works for this subset; no fallback to 3.0.4 is needed.

## Decisions the requirements left open

These choices were made in the spec and the requirements now carry them (see the capabilities named); confirm or amend
them when a story touches the operation.

- Operations that the first API surfaces did not list are now in them: sign-in options, OIDC start and callback, the
  second-factor step, password setup (C-03); `enable` of a Service account (C-04); Route profiles, Route suggestions
  (`accept` and `dismiss`) and `route-order` (C-08); `live-updates` (C-09); the user directory (C-10); the Mattermost
  callback and the Telegram webhook (C-13, C-14).
- The Permission list is final and in `design/prd/l1/reference.md`: per-command Permissions for Alert Groups,
  `alerts:read`, `templates:preview`, `destinations:test`, `organization:read` (the Admin-only reads of the outbound
  policy, the Keyring and severity values; the basic read of `organization` needs no Permission); answering "Still on it"
  uses `alert-groups:acknowledge`; listing a Connection's channels uses `connections:write` because it calls out with the
  bot token. The allocation to Roles is confirmed (P-42).
- The outgoing heartbeat stays on `organization`, readable by every signed-in identity with the URL masked.
- Integration tokens have an optional `name`; OIDC settings have a `display_name` for "Sign in with {provider}" (default:
  the host of the issuer); an Integration carries `open_alert_group_count` for the delete dialog.
- A Lookup table has named columns and rows of a key and one value per column; a template reads a cell with
  `lookup "<table>" <key> "<column>"`. The built-in "Explore" rule uses the columns `address` and `datasource_uid`.
- Timeline entries without a lifecycle event (downtime, Fallback template use, a missing extracted value) use the kind
  `system`; the names of delivery events are listed in `DeliveryEventKind`, and a delivery entry carries `loudness` and
  `mentions` like the Loud and Quiet table.
- Replay is only the CLI command `muster ingest replay`; there is no API. The metrics are only the pull endpoint
  `/metrics`; the catalogue is not served as JSON.
- Problem `type` URIs live under `https://muster-io.github.io/muster/problems/`.
- A Destination can always be deleted (`deleteDestination` has no `409`): a soft delete that wipes its secrets after
  the final edits. A Connection used by Destinations that are not deleted cannot be deleted (`409` `in_use`).
