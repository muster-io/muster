# 0008. Spec-first OpenAPI and API conventions

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — a label filter is only the repeated `label=<matcher>` (the `label.<name>` shorthand is gone);
  on an update an omitted field keeps its value and `null` clears it; ingestion is excluded from request validation;
  the stable problem codes are catalogued in `x-problem-codes`; naming rules for actions, enum values and units;
  operations on the caller's own account accept the web session only; a `public_id` has a fixed format — a type prefix
  of one or two letters and 12 random Crockford base32 characters — and is case-insensitive on input

## Context

Muster is API-first: the web UI, a future Terraform provider, a CLI and an MCP server are all clients of one HTTP API.
That API must be stable from 1.0, reviewable in pull requests, and implementable by contributors and coding agents
without drift between documentation, server and clients. Conventions — pagination, errors, filters, concurrent edits —
must be fixed before the first endpoint, because changing them later breaks every client.

The tooling, as checked in October 2026: oapi-codegen v2.8 generates a typed "strict" server for the standard library's
`ServeMux` (Go 1.22+ patterns), so no router dependency is needed; its OpenAPI 3.1 support is marked "initial" (nullable
through `type: [T, "null"]`; unions of several types become `any`). kin-openapi validates requests against 3.1
documents. oasdiff detects breaking changes in 3.0 and 3.1 documents. OpenAPI 3.2 exists, but generators barely support
it.

## Decision

**Spec-first.** `api/openapi.yaml` is the source of truth and is reviewed like code; the piece of spec a story adds is
part of that story's contract. Generated code is checked in, and CI verifies it is current.

- **OpenAPI 3.1.2, restricted subset:** nullable only as `type: [T, "null"]`; no unions of several types; no
  `$dynamicRef`, `unevaluated*` or `if`/`then`/`else`. Before implementation starts, a spike runs nullable fields and
  `oneOf` end to end through the generators; if it fails, the spec is written in OpenAPI 3.0.4 instead.
- **Server:** oapi-codegen v2.8 (`std-http-server` + `strict-server`) on the standard library `ServeMux`; middleware are
  plain `func(http.Handler) http.Handler`. A handler returning a response of the wrong shape does not compile.
- **Validation:** requests are validated against the spec by `oapi-codegen/nethttp-middleware` (kin-openapi); responses
  are validated in tests. Domain rules live in the domain packages and the command layer (ADR-0016), not in the spec.
  `go-playground/validator` is used only for bootstrap configuration. The ingestion endpoints are the exception: they
  accept any body up to the size limit and store it as received (ADR-0002), so they are excluded from request
  validation and their handlers read the raw body instead of a generated type; the Alertmanager payload schema only
  documents what processing expects. The generator runs with `nullable-type`, so a request can tell an omitted field
  from an explicit `null`. The hand-written health and metrics handlers are excluded from the generated server.
- **Clients:** the Go client comes from the same generator; the UI client from orval (ADR-0009).
- **Tooling:** Redocly CLI lints the spec and builds the reference. oasdiff runs on every pull request — as a report
  before 1.0, as a blocking check from 1.0. The binary serves its spec at `/api/v1/openapi.yaml`; the human-readable
  reference is published on the documentation site.

**Conventions.**

- **Versioning.** Path prefix `/api/v1`. During 0.x breaking changes are allowed; from 1.0, `/api/v1` is stable, and a
  removal is announced one minor release in advance.
- **Resources and identifiers.** Resources are plural kebab-case nouns (`/alert-groups`). URLs use the opaque
  `public_id`, never internal ids or `#N`. A `public_id` is a type prefix of one or two letters, distinct per type,
  followed by 12 random characters of Crockford base32 (`0`–`9` and `A`–`Z` without `I`, `L`, `O` and `U`; 60 bits),
  such as `AGK7M3QX9P2RTA`; the API returns it in upper case and accepts any case, reading `O` as `0` and `I` or `L` as
  `1`. The prefix tells a person and a log reader what an id names, and the alphabet survives being read aloud or
  retyped. Actions on an Alert Group are command endpoints that match the commands of
  ADR-0004: `POST /alert-groups/{id}/acknowledge`, `…/unacknowledge`, `…/resolve`, `…/unresolve`, `…/snooze`,
  `…/unsnooze`. Other actions follow one rule: a state change of an existing resource is a verb sub-path
  (`/users/{id}/disable`, `/route-suggestions/{id}/accept`), a result that the server computes or creates is a plural
  noun (`/destinations/{id}/checks`, `/tests`, `/previews`, `…/confirmation`).
- **Pagination.** Cursor-based: `cursor` and `limit` (default 50, maximum 500). A test fails if a list query uses
  `OFFSET`.
- **Filtering and sorting.** Query parameters following one scheme: `status=…`, `route=…`, `label=<matcher>` (an
  Alertmanager-syntax Matcher, repeated and combined with AND; there is no per-label `label.<name>=…` shorthand, which
  OpenAPI cannot declare), `from` and `to`, `q`; sorting by allowed fields, for example `sort=-created_at`. Queries over past Alert Groups always carry a
  time range, by default the last 7 days.
- **Errors.** RFC 9457 `application/problem+json` with a stable `type`, plus an `errors[]` extension in which each item
  has a JSON `pointer` to the offending field and a stable `code`. Validation errors from the middleware and from the
  strict server are mapped to `Problem` by one function; the UI maps `errors[]` onto form fields. Every stable `code`
  of a problem type, and every `errors[].code`, is catalogued in the spec (`x-problem-codes` on `Problem`) and is part
  of the contract like a field name.
- **Concurrent edits.** Configuration resources return an `ETag`; updates require `If-Match` and fail with `412` on a
  mismatch, so two admins — or an admin and Terraform — do not overwrite each other. Bodies carry the same value as
  `etag`, so a client can update an item of a list without reading it again. On an update an omitted optional field
  keeps its stored value and an explicit `null` clears a nullable one (a Secret included); a choice that must be
  deliberate, such as a Snooze with no end, is a field of its own rather than a missing one.
- **Idempotency.** No `Idempotency-Key` header. Natural uniqueness, such as a Route's name within the Organization, turns
  a repeated create into `409`. Commands are idempotent where the domain says so (a repeated Acknowledge by the Owner).
- **Time and units.** UTC in RFC 3339 format. Durations are integer seconds in fields named `*_seconds`; the two
  exceptions are retention periods, whole days in `*_days`, and measured latencies, milliseconds in `*_ms`. Enum values
  are snake_case; kebab-case stays in URL segments, problem types and the `type` of live-update hints, which name
  resources.
- **Secrets.** Secret fields are write-only; reads return whether the field is set and when it last changed (ADR-0011).
- **Authentication.** Browser sessions use a cookie (`HttpOnly; Secure; SameSite=Lax`) and need a CSRF token or a
  mandatory header on every mutating request. Automation uses Personal access tokens or Service account tokens as bearer
  tokens. Integration tokens are accepted only by the ingestion endpoints. Operations that change the caller's own
  account — profile, password, TOTP, Personal access tokens, Account links, sessions — accept the web session only, so
  a leaked token can neither mint another token nor remove a second factor; Service account tokens cannot read `/me`.
- **Rate limits.** Per token, answered with `429` and `Retry-After`. Ingestion is not rate-limited (ADR-0002).

## Consequences

- Every API change is visible as a spec diff, and breaking changes are caught automatically.
- The 3.1 subset constrains schema design: polymorphic payloads need care, and the spike decides whether `oneOf` is
  usable at all.
- There is no router dependency; middleware composition is plain Go.
- Cursor pagination rules out "jump to page N" in the UI; lists rely on filters and time ranges instead.
- Clients must handle `412` by re-reading; the Terraform provider gets optimistic locking for free.
- If the spike fails, the fallback to 3.0.4 means migrating the spec to 3.1 later.

## Alternatives considered

- **Code-first** (generate the spec from Go code, for example with huma). The spec would stop being a reviewable
  contract.
- **chi as the router.** Was the initial choice; unnecessary once the generator targets the standard library.
- **ogen.** Stricter types and generated validation, but it brings its own router and OpenTelemetry instrumentation that
  does not match Muster's metrics stack, and its 3.1 support is partial.
- **OpenAPI 3.0.4.** The best supported version, but a new spec would have to migrate later; kept as the fallback.
- **OpenAPI 3.2.** Generators do not support it yet.
- **Offset pagination.** Unstable under concurrent inserts and slow on large tables.
- **`Idempotency-Key` on creates.** No L1 client needs it; natural uniqueness covers repeated creates.
- **libopenapi-validator** (validates responses too and reads 3.2). It would add a second OpenAPI parser next to
  kin-openapi, which the generator, the validation middleware and oasdiff already share.
