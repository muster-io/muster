# 0015. Outbound HTTP and SSRF policy

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — the Broken probe, its Destination check included, runs in the delivery class, not the
  interactive one

## Context

Muster calls many HTTP endpoints: the Mattermost and Telegram APIs, outgoing webhooks to any address an admin enters,
the OIDC provider (discovery, code exchange, key sets), and later LLM providers, MCP servers and the Alertmanager API in
the `pull` Connection mode. These calls differ in what a retry costs and in who is waiting for the result, but they
share the same risks:

- **Server-side request forgery.** An admin — or anyone who obtains admin rights — can point an outgoing webhook at any
  address, and an outgoing webhook can extract values from a response and show them in messages (ADR-0005). Pointed at
  a cloud metadata endpoint, that copies the host's cloud credentials into a chat. Checking the host name is not
  enough: DNS can answer one thing when the name is checked and another when the connection is made.
- **Redirects** forward credentials — authorization headers, tokens in URLs — to wherever a server points, and bypass
  any address check made before the first request.
- **Retries** written separately in every client drift apart: some retry unknown errors, some retry forever, some wait
  longer than anyone budgeted.
- **Proxies.** Some networks can reach a messenger only through a proxy — for example where the messenger is blocked —
  while other clients of the same installation must not use that proxy; in some closed networks only the proxy can
  resolve outside names. One process-wide proxy from the environment cannot express that.
- **Secrets in addresses.** The Telegram Bot API carries the bot token in the request path, and errors of Go's HTTP
  client (`url.Error`) repeat the full URL; a log line or an error shown in the UI can leak the token. Where Telegram is
  blocked, its API may be reached through a reverse proxy under the installation's own domain or through a self-hosted
  Bot API server — and a bot that runs on a self-hosted server silently moves back to Telegram's cloud on any request
  with its token to `api.telegram.org`, after which presses and comments start to go missing.
- Outbound calls must be observable in one way: which client fails, and how slow it is.

## Decision

**One outbound client package.** Every outbound HTTP request is made through one package that builds clients from a
per-client configuration; no other code creates an HTTP client or transport, which an architecture lint enforces
(ADR-0016). Every client has a connect timeout and an overall request timeout. Requests are counted in
`muster_client_requests_total` and `muster_client_request_duration_seconds`, labelled by client class and outcome.

**No redirects.** Clients never follow redirects. A redirect response fails the request, and the error names the
target so that an admin can correct the configured URL.

**Retries by client class.** The package makes attempts; what follows a failure is a policy registered once per class:

| Class | Who waits | Policy |
|---|---|---|
| Delivery — messenger send and edit, outgoing webhook requests, the Broken probe with the Destination check it runs when no delivery waits | the Desired state, not a person | one attempt per call; the result is classified as `RetryAfter`, `Transient`, `Fatal` or unknown, and the delivery row schedules the next attempt within its attempt and time budgets (ADR-0005); a failed probe is retried by the next one |
| Interactive — callback answers, ephemeral replies, account-link messages, test messages, connection and Destination checks started from the UI, the OIDC code exchange | a person | one attempt within a time budget of a few seconds; messenger calls take their limiter tokens ahead of deliveries and never queue (ADR-0005); a failure is shown to that person |
| Background — Telegram `getUpdates`, OIDC discovery and key sets | a loop | exponential backoff with jitter and a cap between attempts; polling keeps going, and a `409 Conflict` from a second poller only makes it back off (ADR-0007); a key-set refresh keeps the last good keys |
| Heartbeat — Muster's outgoing heartbeat (ADR-0014) | an external dead man's switch | one attempt per tick with a short timeout; a failure is counted and logged, and the next tick is the retry |
| Later — LLM providers, MCP servers, Alertmanager polling | an investigation or a poller | budgets set when those features are designed, under the same rules |

The class follows who waits, not which call is made: the same Destination check is interactive when a person presses
"Check" and delivery work when the Broken probe runs it, so a probe never takes limiter tokens ahead of deliveries.

Rules for every class: retries use exponential backoff with jitter within an attempt budget and a total time budget;
`Retry-After` and Telegram's `retry_after` are honoured exactly; an unknown status code is not retried; a refusal by
Muster's own rate limiter is not a failure and is not retried in place: delivery and background work waits for its turn,
while an interactive call, which takes its token ahead of them, fails fast when none is free within its budget; an error
reported in the body of a successful response, such as Telegram's `ok: false`, is parsed inside the attempt and
classified like a status code; the provider's error text is kept as a separate, untrusted field, escaped wherever it is
shown.

**SSRF policy.** An Organization setting chooses the outbound address policy:

- **standard** — the default for self-hosted installations: private addresses are allowed, because Mattermost servers,
  webhook receiving endpoints and identity providers often live in the same private network as Muster; loopback
  addresses are blocked unless the Organization allows them as a network, because loopback reaches Muster's own
  internal listener and any sidecar next to it;
- **strict** — only public addresses, plus networks the Organization explicitly allows.

In both modes, link-local addresses (`169.254.0.0/16`, `fe80::/10`) and known cloud metadata addresses (such as
`fd00:ec2::254`) are always blocked, and no allowed network can open them; unspecified and multicast addresses are
always blocked as well. The Organization can also list allowed and denied networks, and host names or domains for
targets behind a proxy (see below); a denied entry always wins. A future hosted mode always uses strict. The check runs
**on the IP address after name resolution**, inside the dialer: every address a name resolves to is checked, and the
connection goes only to an address that passed, so a DNS answer that changes between check and connection cannot bypass
it. A blocked request fails without retry, as a configuration error that names the rule that blocked it.

**Proxies per client.** Each client that may need one — each Mattermost and Telegram Connection, each outgoing webhook
Destination, the OIDC provider, Muster's outgoing heartbeat, and later each LLM provider and MCP server — has its own
proxy setting in the UI: use a proxy or not; its type (HTTP, HTTPS or SOCKS5); its address; and, when the proxy needs
authentication, a username and a password, stored encrypted and write-only in the API (ADR-0011). There is no
environment-wide proxy: `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are ignored. The UI's connection check takes the same
path as real traffic, proxy included. Through a proxy, the proxy resolves the target's name; Muster resolves it too and
applies the SSRF policy to every address it gets, or to the address itself if the target is an IP address. Where only
the proxy can resolve names, as in some closed networks, Muster checks the name instead: a name matching a denied entry
is blocked, in strict mode only a name matching an allowed entry passes, and a request that passes is logged as sent
without an address check. The proxy's own address is always checked by the same policy, like any other target, so a
proxy setting cannot reach a metadata, link-local or denied address, and a proxy on loopback needs an allowed network.

**Base addresses and secrets in URLs.** A Telegram Connection has a Bot API base URL, `https://api.telegram.org` by
default, that may point at a reverse proxy or at a self-hosted Bot API server, with a path prefix if needed; it is an
ordinary target under the outbound address policy and the Connection's proxy. Muster sends the real token only to the
saved base URL. A connection check of an address first makes a dry probe without the token — `GET <base>/bot0:x/getMe`,
which any Bot API answers with `401` — and only against the saved address calls `getMe` and `getWebhookInfo` with the
token. Muster offers no server kind and no wizard for moving a bot between servers; the documentation explains the
trade-offs and recommends a proxy or a reverse proxy over a self-hosted server. The outbound package redacts secrets
that travel in URLs from everything it logs and from every error it returns, including the URL inside Go's `url.Error`,
and logs only the scheme and host of a base URL, whose path may be a secret prefix; the architecture lint that pushes
known secrets through the code (ADR-0016) covers these errors.

**Scope.** Clients added later — LLM providers, MCP servers, Alertmanager polling — use the same package, the same
policy and the same proxy settings.

## Consequences

- One place to audit for SSRF, redirects, proxies, timeouts and retries; a new client cannot opt out by accident.
- Self-hosted installations work out of the box with internal messengers, webhook receiving endpoints and identity
  providers, while the path that would leak cloud credentials stays closed; strict mode is there for installations that
  want more. Loopback is closed by default, so a messenger, receiving endpoint or proxy on Muster's own host needs an
  allowed network.
- Through a proxy, the final name resolution is the proxy's; a proxy could still connect elsewhere, which is accepted
  because an admin chose it. Where only the proxy resolves names, the check covers the name alone, and the log says so.
- Interactive calls never wait behind a Storm: they take the next free token or fail within seconds.
- Per-client proxies take more configuration than one environment variable, but traffic that must go through a proxy
  and traffic that must not can share one installation, and the choice is visible in the UI.
- An admin who enters a redirecting URL sees an error instead of a silently followed redirect.
- Retry behaviour is uniform and visible in the `muster_client_*` metrics.
- A Telegram bot can be reached through a reverse proxy or a self-hosted Bot API server by changing one address, and no
  check sends its token anywhere else; moving a bot to a self-hosted server remains a manual, documented step.

## Alternatives considered

- **Each feature with its own HTTP client.** Every client would re-implement — or forget — SSRF checks, redirect
  handling, timeouts and retries.
- **Follow redirects and check every hop.** Still forwards credentials to hosts the admin never entered, and the APIs
  Muster talks to do not need redirects.
- **Strict policy by default.** Most self-hosted messengers and webhook receiving endpoints live in private networks;
  the first setup would fail for most users.
- **Allow loopback in the standard policy.** Loopback reaches Muster's own internal listener and any sidecar next to
  it; the rare setup that needs it opens it explicitly.
- **Trust the proxy address as configuration.** A proxy pointed at a metadata or link-local address would reach exactly
  what the policy blocks.
- **Refuse requests whose target Muster cannot resolve itself.** Closed networks where only the proxy resolves outside
  names could not reach their messengers at all.
- **No SSRF policy for self-hosted installations.** Leaves cloud metadata readable through webhook response values.
- **Checking the host name before connecting.** DNS rebinding answers the check with a harmless address and the
  connection with a forbidden one.
- **A process-wide proxy from the environment.** One proxy for everything, or `NO_PROXY` lists to maintain; it cannot
  send messenger traffic through a proxy while keeping the identity provider on a direct path, and it is invisible in
  the UI.
- **Retrying unknown status codes.** Hides misconfiguration and wastes budgets (ADR-0005).
- **Checking a new Telegram base URL with `getMe` and the real token.** Against a self-hosted server, or with the old
  address still configured elsewhere, the check itself splits the bot between two servers; a dry probe without the
  token proves that a Bot API answers there without that risk.
- **A server kind (cloud or self-hosted) and a wizard that moves the bot between servers.** Needed only for
  self-hosted Bot API servers, which a proxy or a reverse proxy makes unnecessary for blocked networks; it can come
  later if asked for.
