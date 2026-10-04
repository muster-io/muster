# C-21. Documentation site and runbooks

[L1 index](../L1.md) · Stage: Operations · UI: the documentation site · Depends on: C-19

**Goal.** Users find versioned documentation for installing, configuring and operating Muster, and a runbook for every
alert Muster can raise.

## Scenarios

1. A new user follows "Quick start" and has Muster with docker-compose running in five minutes.
2. An operator installs with Helm, configures Alertmanager and the Heartbeat, and connects Telegram, all from the docs.
3. `MusterDeliveryFailing` fires at night; its `runbook_url` opens the page for the installed minor version.
4. A user upgrading reads the upgrade policy and the backup procedure first.
5. An operator in a network where Telegram is blocked reads which of the three ways to reach it fits, and why a
   self-hosted Bot API server is the last resort.

## Functional requirements

- **C-21.FR-1** The documentation source is `docs/`; the site is generated with Zensical (or, if it is not ready for the
  first release, Material for MkDocs with the same configuration) and published at `muster-io.github.io/muster`. Each
  minor release has a frozen build; corrections for the current minor are published from `master` as documentation-only
  releases; older minors are not changed; development builds are `latest`.
- **C-21.FR-2** The site has at least these sections: Quick start (docker-compose); Install (Helm values, existing
  Secret, the database as a URL or as fields, external PostgreSQL with a CloudNativePG `Cluster` example and notes for
  managed PostgreSQL, ingress and Gateway API, the two public addresses, the session connection for PgBouncer);
  Alertmanager (receiver and route, Heartbeat, `--dispatch.start-delay` and the other HA pair settings of C-05.FR-11,
  repeat intervals, `max_alerts: 0`, Static labels, advice against Instance labels in informational alerts); Messengers
  (Mattermost, Telegram, proxies, Telegram in restricted networks); Outgoing webhooks (event schema, ordering and
  duplicates, signature verification, Secrets, template recipes); Sign-in (OIDC with a Keycloak walkthrough, closed
  networks, TOTP); Concepts (Alert Groups, Routes and Group keys, Snapshot semantics, Loud and Quiet, statuses and
  commands, Broken Destinations and recovery);
  Operations (backup and restore with keys kept separately, upgrade policy, key rotation, HA, monitoring with the bypass
  route, outgoing heartbeat, `muster doctor` and the CLI); Runbooks; Reference (generated metrics, log events and
  Internal alerts; the API reference rendered from the OpenAPI document for that version; environment variables; chart
  values).
- **C-21.FR-3** Each chart rule and Internal alert has a page `docs/operations/runbooks/<alertname>.md` with what it
  means, its impact, how to diagnose it and how to fix it; CI checks that rules, Internal alerts and pages match one to
  one.
- **C-21.FR-4** Generated reference pages are checked for currency in CI; broken internal links fail the build.
- **C-21.FR-5** The documentation is in English; diagrams use Mermaid.
- **C-21.FR-7** "Telegram in restricted networks" recommends, in this order: a proxy on the Connection; a reverse proxy
  under the installation's own domain in front of `api.telegram.org`, with nginx and Caddy recipes (upstream SNI and
  `Host`, timeouts above the long-polling timeout, request body size, access logs off or without the request URI, a
  secret path prefix or an address allowlist); and only then a self-hosted Bot API server. For a self-hosted server it
  warns that: it needs an `api_id` and `api_hash` registered with a personal Telegram account (use a dedicated one); a
  bot lives on one server at a time; `logOut` drops unread updates and a return to the cloud is possible only after 10
  minutes; any request with the bot's token to `api.telegram.org` — even `getMe` from a script — silently moves the bot
  back to the cloud, after which presses and comments go missing; the server has no authentication of its own and must
  not be exposed; tokens are visible on its statistics port, which must stay local; `--local` serves the server's files
  and must not be used; images have no releases and must be pinned by digest; and `MusterDestinationBroken` for
  Telegram Destinations should be routed to a Destination that is not in Telegram. Moving a bot between servers is a
  documented manual procedure; Muster has no wizard for it.

## UI

The documentation site.

## API surface

None. The API reference of the site is rendered from the same OpenAPI document that the binary serves at
`/api/v1/openapi.yaml` (C-03.FR-23).

## Acceptance

- **C-21.AC-1** For a release, every `runbook_url` of the chart and every Internal alert returns a page on the published
  site.
- **C-21.AC-2** Following Quick start on a clean machine yields a running Muster.
- **C-21.AC-3** The "Telegram in restricted networks" page contains every warning of FR-7, checked against a list in
  the pull request.

## Related ADRs

ADR-0001, ADR-0008, ADR-0014, ADR-0015.

## Depends on

C-19 — the final list of chart rules and Internal alerts.

## Suggested story split

One documentation story for the site, its build and the generated references; one for the runbook pages and the
remaining sections. Pages that describe a capability are best written in that capability's own stories.
