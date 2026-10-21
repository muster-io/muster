# C-21. Documentation site and runbooks

[L1 index](../L1.md) · Stage: Operations · UI: the documentation site, and links to it from the web UI · Depends on: C-19

**Goal.** Users find versioned documentation for installing, configuring and operating Muster, and a runbook for every
alert Muster can raise; AI agents find the same documentation in a form made for them. A new Admin is led step by step,
with pictures of the screens, from an empty installation to the first Alert Group acknowledged in a messenger, and the
web UI links each page, complex field and problem to the section of the documentation of the running version that
explains it.

## Scenarios

1. A new user follows "Quick start" and has Muster with docker-compose running in five minutes.
2. An operator installs with Helm, configures Alertmanager and the Heartbeat, and connects Telegram, all from the docs.
3. `MusterDeliveryFailing` fires at night; its `runbook_url` opens the page for the installed minor version.
4. A user upgrading reads the upgrade policy and the backup procedure first.
5. An operator in a network where Telegram is blocked reads which of the three ways to reach it fits, and why a
   self-hosted Bot API server is the last resort.
6. A developer asks a coding agent to write a bridge for the team's messenger; the agent reads `llms.txt`, follows its
   rules and the pages it names instead of what it remembers, and hands the bridge over once `muster bridge check`
   passes.
7. An Admin who has just started Muster follows the tutorial "First alert in a messenger": each step shows the screen
   it describes, and at the end a test alert is an Alert Group that the Admin acknowledges from Telegram.
8. An Admin editing a Route does not know what a Group key is; "Learn more" beside the field opens the section on Group
   keys in the documentation of the installed minor version, in a new tab.
9. A Destination check fails with "Comments are not enabled for this channel"; "How to fix" beside the result opens the
   section of the Telegram page that shows, with a picture, where to enable comments.

## Functional requirements

- **C-21.FR-1** The documentation source is `docs/`; the site is generated with Zensical (or, if it is not ready for the
  first release, Material for MkDocs with the same configuration) and published at `muster-io.github.io/muster`. Each
  minor release has a frozen build; corrections for the current minor are published from `master` as documentation-only
  releases; older minors are not changed; development builds and pre-releases are `latest` (FR-11).
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
- **C-21.FR-8** The site carries documentation for AI agents, in English, generated by `make docs` from the sources of
  the pages: `llms.txt` at the root of every version and of the published site — a title, a statement that it is the
  canonical reference of Muster for agents, which do not rely on their training data about Muster, rules (the
  specifications `api/bridge.yaml` and `api/openapi.yaml` as the sources of truth, the error classes of the Bridge
  protocol, confirming a press within 3 seconds, keeping each `Idempotency-Key` per token for at least 24 hours, edit
  windows, and that a bridge is finished only when `muster bridge check` passes against it), scenarios (writing a
  bridge in Go and in Python, connecting Alertmanager, automating through the API) that name the pages to read, and a
  map of every page linked to its Markdown version; `llms-full.txt`, the text of every page in the order of the
  navigation; and a Markdown version of every page at its source path under the site, which is the page's URL with `.md`
  for an ordinary page and `index.md` under the URL for a section page or the home page. The values of the rules come
  from their sources, and `make docs-check` fails when a rule, a scenario or the map no longer matches the pages, the
  specifications or [defaults](defaults.md).
- **C-21.FR-9** Guides with screenshots. The site has one end-to-end tutorial, "First alert in a messenger", placed
  after Quick start: create an Integration and send its snippet to Alertmanager, create a Telegram Connection, add a
  Destination, add a Route that sends to it, link the Admin's Telegram account, send a test alert, and acknowledge the
  Alert Group from Telegram; each step shows the screen it describes and links to the page that explains it in full.
  The pages of the objects the web UI configures gain step-by-step sections with screenshots inside the existing pages,
  not as separate guides: the messengers (Mattermost, Telegram, Slack, Bridge), the Integrations (Alertmanager,
  Grafana, Heartbeat), Routes, Templates with Lookup tables and Link rules, Account links, Users, OIDC and TOTP, API
  tokens and Service accounts, Outgoing webhooks, the Organization settings with the System status and the Audit log,
  and the Alert Group list, page and Statistics. Every screenshot is of the English UI and has alternative text.
- **C-21.FR-10** Screenshots are made by a program, not by hand. A Playwright suite walks each guide on `muster dev`
  with demo data and the fake servers, in English, in a 1280 px wide viewport and the light colour scheme, and saves
  each screenshot under `docs/assets/screenshots/<page>/`; `make docs-screenshots` runs it by hand, like `make demo`,
  and the images are committed. No CI run commits them, because times make that flaky: the CI of pull requests
  never runs the suite, and a nightly job runs it only to show that it still passes; `make docs-check`
  fails, naming the page and the image, when a page references an image that does not exist, when an image of
  `docs/assets/screenshots/` is referenced by no page, when an image has no alternative text, or when a page loads an
  image from another site.
- **C-21.FR-11** Links from the web UI. The web UI links to the documentation of the version it runs: a release `X.Y.Z`
  links under `https://muster-io.github.io/muster/X.Y/`, any other version — a development build (`0.0.0-dev`, the
  `sha-…` and `edge` images, whose versions end in `-SNAPSHOT-<commit>`) or a pre-release — under
  `https://muster-io.github.io/muster/latest/`. The address of the site is a build constant of the web UI with no
  setting. The server derives the version segment (`X.Y` or `latest`) from the build it runs, by the same rule that
  builds the `runbook_url` of Internal alerts (C-19.FR-9), and the session carries it, since the web UI reads the
  session on every page. Links appear only in an active, full session: the sign-in and password setup pages, the TOTP
  prompt and TOTP enrolment of a limited session, and the page that says the person lacks a Permission have none. The
  links are placed as the table [Documentation links](reference.md#documentation-links) says: a link to the page's guide
  next to the title of every other page; "Learn more" next to complex fields of forms; "How to fix" next to the results
  of checks and the warnings, banners and Broken states that need the person to act. Empty states have no link. A link
  opens in a new tab, sends no `Referer` (`rel="noopener noreferrer"`), and its text is translated into English and
  Russian like any other text of the UI, while the pages it opens are in English (C-21.FR-5).
- **C-21.FR-12** Every page and anchor the web UI links to is listed once, in one file of the web UI that the links
  are built from; `make docs-check` fails, naming the entry, when the built site has no such page or no element with
  that anchor, so that renaming a heading cannot break a link of the UI unnoticed.

## UI

The documentation site, and the links to it from every page of an active, full session of the web UI (FR-11).

## API surface

The API reference of the site is rendered from the same OpenAPI document that the binary serves at
`/api/v1/openapi.yaml` (C-03.FR-23). `Session`, the answer of `getCurrentSession`, `createSession` and
`submitSessionTotp`, carries `docs_version`, the version segment of the documentation the web UI links to (FR-11).

## Acceptance

- **C-21.AC-1** For a release, every `runbook_url` of the chart and every Internal alert returns a page on the published
  site.
- **C-21.AC-2** Following Quick start on a clean machine yields a running Muster.
- **C-21.AC-3** The "Telegram in restricted networks" page contains every warning of FR-7, checked against a list in
  the pull request.
- **C-21.AC-4** On the built site, `llms.txt` links to the Markdown version of every page of the navigation and each of
  its links resolves; `llms-full.txt` holds every page; and a value of the rules that its source no longer has makes
  `make docs-check` fail, naming the file.
- **C-21.AC-5** On a fresh `muster dev`, the Playwright suite of the tutorial follows its steps in order through the
  UI, with the fake Alertmanager and the fake Telegram server, and ends with the Alert Group acknowledged by the Admin,
  its Timeline naming the Transport Telegram; every step of the tutorial has the screenshot that run took.
- **C-21.AC-6** `make docs-screenshots` writes every image the guides reference, in a 1280 px wide viewport and none
  wider, of the English UI in the light colour scheme, and changes no image when one of its specs fails; `make e2e` and
  the CI of pull requests never run it, and the nightly job runs it without committing; and `make docs-check` fails,
  naming the page and the image, for an image a page references that does not exist, for an image under
  `docs/assets/screenshots/` no page references, for an image without alternative text, and for an image loaded from
  another site.
- **C-21.AC-7** The documentation version follows the release rule: `0.1.0` and `0.1.7` link under `/muster/0.1/`,
  `1.12.3` under `/muster/1.12/`, and `0.0.0-dev`, `0.1.1-SNAPSHOT-abc1234` and `0.2.0-rc.1` under `/muster/latest/`;
  the `runbook_url` of an Internal alert raised by the same binary names the same segment.
- **C-21.AC-8** Every page of an active, full session has a documentation link next to its title, and every place of the
  table [Documentation links](reference.md#documentation-links) shows its link, which opens the named page and section
  in a new tab without a `Referer`; no empty state has one; the sign-in and password setup pages, the TOTP prompt and
  enrolment of a limited session and the page about a missing Permission have none; the texts of the links exist in
  English and Russian; and the pages with links keep no horizontal scrollbar at 360 px wide.
- **C-21.AC-9** Changing a link of the UI to an anchor the site does not have makes `make docs-check` fail, naming the
  entry of the list and the page; removing from the site a heading that the web UI links to makes it fail too, naming
  the entry or, when a page of the site also links to that heading, the linking page.

## Related ADRs

ADR-0001, ADR-0008, ADR-0014, ADR-0015, ADR-0017.

## Depends on

C-19 — the final list of chart rules and Internal alerts.

## Suggested story split

One documentation story for the site, its build and the generated references; one for the runbook pages and the
remaining sections; one, after them, for the documentation for AI agents (FR-8). Pages that describe a capability are
best written in that capability's own stories. The guides with screenshots (FR-9, FR-10) are two documentation
stories: the screenshot suite, the image check, the tutorial and the messenger pages first, then the other pages. The
links from the web UI (FR-11, FR-12) are frontend stories after the first of them: the version, the link component,
the check of anchors and the links next to page titles; then the links in forms and problems, split by the pages they
touch.
