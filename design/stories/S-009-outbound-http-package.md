---
id: S-009
title: Outbound HTTP package with the outbound address policy and proxies (BE)
capability: C-02
kind: be
layer: L1
depends_on: [S-007]
covers: [C-02.FR-1, C-02.FR-19, C-02.FR-20, C-02.FR-21, C-02.FR-22, C-02.AC-9, C-02.AC-10, C-02.AC-11, C-01.FR-13]
files_touched:
  - internal/outbound/client.go
  - internal/outbound/classes.go
  - internal/outbound/classify.go
  - internal/outbound/dialer.go
  - internal/outbound/policy.go
  - internal/outbound/proxy.go
  - internal/outbound/redact.go
  - internal/outbound/client_test.go
  - internal/outbound/dialer_test.go
  - internal/outbound/policy_test.go
  - internal/outbound/proxy_test.go
  - internal/outbound/live_test.go
  - internal/organization/outbound.go
  - internal/organization/query.sql
  - internal/fakes/fakeproxy/fakeproxy.go
  - internal/fakes/fakeproxy/fakeproxy_test.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
acceptance:
  - "[C-02.FR-21, C-02.AC-9] A request to 169.254.169.254 fails without retry with an error naming the link-local rule; a request answered with `302` fails without retry with an error naming the redirect target; under the default policy a request to a loopback address is refused naming the loopback rule, and succeeds once `127.0.0.0/8` is an allowed network."
  - "[C-02.FR-21] Under the strict policy a private address is refused unless an allowed network contains it; a denied entry wins over an allowed one; no allowed entry opens link-local, metadata, unspecified or multicast addresses; when a name resolves to a blocked and an allowed address, the connection goes only to the allowed one."
  - "[C-02.FR-20, C-02.AC-10] A request whose URL carries a registered secret and fails with a network error leaves the secret in no log line and no returned error, the `url.Error` included; the lint-5 probe for this path passes."
  - "[C-02.FR-22, C-02.AC-11, C-01.FR-13] A request through the fake SOCKS5 proxy reaches its target only through the proxy, which records it; a proxy whose address is 169.254.169.254 is refused naming the rule; a target name that only the proxy can resolve is checked against the lists, and the request is logged as `outbound_unverified_address`."
  - "[C-02.FR-20] `Retry-After` in seconds or as an HTTP date, and a retry delay reported by the caller's body classifier, are returned exactly; an unknown status code is not retried; an error inside a successful body is classified like a status code; the provider's error text is returned as a separate, untrusted field."
  - "[C-02.FR-20] The background class retries with exponential backoff and jitter up to its cap until its context ends; the delivery, interactive and heartbeat classes make one attempt."
  - "[C-02.FR-19, C-02.FR-20] Every request increments `muster_client_requests_total{client,outcome}` and observes `muster_client_request_duration_seconds{client,outcome}` with label values from closed sets."
  - "[C-02.FR-1] With `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` set, no request goes through a proxy that the client's own settings do not name."
verify: "make ci test-integration"
operator_attention: false
issue: 9
---

# S-009. Outbound HTTP package with the outbound address policy and proxies (BE)

## Scope

**IN**

- `internal/outbound`, the only code that creates HTTP clients and transports (ADR-0015, lint 4): client classes with
  their retry policies, outcome classification, timeouts, no redirects, secret redaction and metrics.
- The outbound address policy enforced inside the dialer on every resolved address, with the Organization's lists.
- Per-client proxies (HTTP, HTTPS, SOCKS5) with the checks of C-02.FR-22.
- The read side of the Organization's policy and a fake HTTP and SOCKS5 proxy for tests.

**OUT**

- The shared proxy settings object in the API and its form (S-013, S-015); editing the policy (S-055).
- Limiters and the interactive budget, which belong to delivery (S-034); the response mappings of each messenger and of
  outgoing webhooks (S-061, S-041, S-044).
- The first real consumer, the OIDC back channel (S-013), which repeats the proxy and policy checks through the API.

## Contracts

- **Client construction**: `outbound.New` takes a client configuration — class, connect timeout, overall timeout,
  optional proxy, the secrets to redact and an optional base URL — and returns a client. No other package creates an
  `http.Client` or `http.Transport`. Proxies from the environment are never consulted.
- **Classes and retry policies** (ADR-0015):

  | Class | Policy in this package |
  |---|---|
  | `delivery` | one attempt; the classified outcome goes back to the caller's delivery row |
  | `interactive` | one attempt within the budget the caller passes |
  | `background` | exponential backoff with jitter and a cap between attempts, until the context ends; a `409 Conflict` reported as such by the caller only backs off |
  | `heartbeat` | one attempt with a short timeout |

- **Outcomes** (C-02.FR-20): `ok`, `retry_after` (the exact delay from `Retry-After` in seconds or as an HTTP date, or
  from the caller's body classifier, such as Telegram's `retry_after`), `transient`, `fatal`, `unknown`, `blocked` and
  `redirect`. The default mapping: `2xx` → `ok`; `429` or `503` with `Retry-After` → `retry_after`; `408`, `500`, `502`,
  `503` without `Retry-After`, `504`, timeouts and connection errors → `transient`; `400`, `401`, `403`, `404` →
  `fatal`; anything else → `unknown`, never retried. Callers replace the mapping with their own (C-13, C-14, C-15). An
  error in the body of a successful response is classified by a caller-provided body classifier inside the attempt. The
  provider's error text is returned in its own field, marked untrusted.
- **No redirects**: a `3xx` answer fails the request with `redirect` and the error `redirect to <target> refused`, the
  target redacted like any URL.
- **Address policy** (C-02.FR-21): the dialer resolves the target, checks every address, and connects only to one that
  passed. `standard` allows private addresses and blocks loopback unless an allowed network contains it; `strict`
  allows only public addresses and allowed networks. Always blocked, whatever the lists say: link-local
  (`169.254.0.0/16`, `fe80::/10`), known cloud metadata addresses (a list in code, starting with `fd00:ec2::254` and
  `100.100.100.200`), unspecified (`0.0.0.0`, `::`) and multicast (`224.0.0.0/4`, `ff00::/8`). The Organization's
  `organization.outbound_allowed` and `organization.outbound_denied` lists hold networks, host names and domains; a
  denied entry always wins. A blocked request fails without retry with `blocked` and an error that names the rule, and
  is logged as `outbound_blocked` (WARN: client, rule, scheme and host).
- **Proxies** (C-02.FR-22): a client configuration may name a proxy — type `http`, `https` or `socks5`, address,
  optional username and password (the password a Secret decrypted with the Keyring only to build the client). The
  proxy's own address passes the same policy. Through a proxy, Muster resolves the target too and checks every address;
  when it cannot resolve the name, it checks the name against the lists — a denied name is blocked, and in strict mode
  only an allowed name passes — and logs `outbound_unverified_address` (INFO: client, scheme and host).
- **Redaction** (C-02.FR-20, ADR-0015): registered secrets are replaced with `[redacted]` in every log line and in every
  returned error, including the URL inside `*url.Error`; a base URL is logged as scheme and host only.
- **Metrics**: `muster_client_requests_total{client,outcome}` and the histogram
  `muster_client_request_duration_seconds{client,outcome}` (`le` buckets for external APIs). `client` takes the four
  class names; `outcome` takes the seven outcomes above. Both closed sets are those of the catalogue of reference.md.
- **Policy source** (`internal/organization/outbound.go`): reads `outbound_policies` of the Organization and caches it
  per replica for at most 10 seconds.
- **Fake proxies** (`internal/fakes/fakeproxy`): an HTTP `CONNECT` proxy and a SOCKS5 proxy (with optional
  authentication) that record every target they connect to.

## Steps

1. Write the policy rules and the dialer. Check: table tests cover each always-blocked range, standard and strict, the
   lists, denied over allowed, and a name resolving to a mix of addresses.
2. Write client construction, classes, classification and redirect refusal. Check: tests cover each outcome, exact
   `Retry-After` handling, body classification and the backoff of the background class with a manual clock.
3. Write proxies and the name-only check, with the fake proxies. Check: tests show traffic only through the proxy, the
   proxy address check and the unverified-address log line.
4. Write redaction and register the lint-5 probe. Check: `make lint-arch` passes and the probe sees no secret.
5. Add the metrics and the policy source. Check: tests see both metrics move and the cached policy refresh.
6. Write the live test against real sockets. Check: Verification below.

## Verification

The package has no API consumer until S-013, so its live check runs real listeners, a real resolver and the fake
proxies on loopback:

```sh
go test -tags integration -run TestLive -v ./internal/outbound/...
# === RUN   TestLive/metadata_blocked
#     blocked by the outbound address policy: 169.254.169.254 is link-local (always blocked)
# === RUN   TestLive/redirect_refused
#     redirect to http://127.0.0.1:<port>/elsewhere refused
# === RUN   TestLive/loopback_blocked_by_default
#     blocked by the outbound address policy: 127.0.0.1 is loopback (allow it with an allowed network)
# === RUN   TestLive/loopback_allowed_by_network
# === RUN   TestLive/socks5_proxy_used
#     proxy recorded target 127.0.0.1:<port>; target saw the connection from the proxy
# === RUN   TestLive/proxy_on_metadata_address_refused
#     blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)
# === RUN   TestLive/secret_in_url_redacted
#     Get "http://127.0.0.1:1/bot[redacted]/getMe": dial tcp 127.0.0.1:1: connect: connection refused
# --- PASS: TestLive (…)
HTTP_PROXY=http://127.0.0.1:9 HTTPS_PROXY=http://127.0.0.1:9 go test -tags integration -run TestLive/env_proxy_ignored -v ./internal/outbound/...
# --- PASS: TestLive/env_proxy_ignored
```

S-013 repeats the proxy and the metadata checks through the OIDC connection check of the running binary.

## Open questions

1. Editing the policy (S-055) takes effect within the 10-second cache. If that is too slow, S-055 replaces the cache
   with an invalidation by `NOTIFY`.
2. The list of cloud metadata addresses beyond link-local starts with `fd00:ec2::254` and `100.100.100.200`; it is a
   reviewed list in code and grows like the registries.

## Notes

- Suggested commit: `feat(outbound): add outbound HTTP package with address policy and proxies`.
- The check runs on the resolved address inside the dialer, so DNS answers that change between check and connection
  cannot bypass it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-02.FR-1 | partial | the proxy variables are ignored by construction; the rest is S-006 |
| C-02.FR-19 | partial | the `muster_client_*` metrics |
| C-02.FR-20 | full | adapters bring their own response mappings |
| C-02.FR-21 | full | editing the policy is S-055 |
| C-02.FR-22 | partial | the mechanism; the settings object and form are S-013 and S-015 |
| C-02.AC-9 | full | |
| C-02.AC-10 | full | |
| C-02.AC-11 | full | |
| C-01.FR-13 | partial | the fake HTTP and SOCKS5 proxies |
