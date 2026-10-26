# 0019. Opt-in telemetry

- Status: Accepted
- Date: 2026-10-26

## Context

Muster has sent nothing about its installations so far: L1 has no telemetry at all, and the rules for contributors
forbid adding telemetry, analytics or email (NFR-7). The project learns about adoption only from image pulls and GitHub,
which say nothing about which features are used, at what scale, or how well delivery works, so the maintainers guess
which messengers, Integrations and features deserve their time.

Telemetry that is on by default, or that has to be turned off, is reason enough for many self-hosted installations —
those in closed networks in particular — not to run a product at all (journal D120). Whatever Muster sends must also fit
what earlier decisions fixed: configuration lives in the database and changes only through the API (ADR-0010); outbound
HTTP goes through one package, with a policy per client class and a proxy per client (ADR-0015); singleton work belongs
to the closed list of Leader tasks (ADR-0007). And the end-to-end suites and the documentation screenshots start from a
fresh installation, where any dialog at the first sign-in gets in the way.

## Decision

**Off until an Admin opts in, category by category.** Telemetry is off by default. At the first sign-in of an Admin,
Muster asks whether to send anonymous telemetry, with a switch for each category and nothing on until the Admin turns it
on; an Admin can change the choice at any time in the Organization's settings. The consent is an Organization setting in
the database, changed only through the API and recorded in the Audit log; no environment variable turns telemetry on or
off (ADR-0010; journal D497, D502).

**Categories** (journal D498), each with its own switch:

- **Basic** — a random installation id, the version, the operating system and architecture, the way Muster was
  installed, the major version of PostgreSQL and the number of replicas.
- **Scale** — orders of magnitude, never exact numbers, of Users, Teams, Routes, and Alerts and Alert Groups per day;
  the types of Integrations and Destinations in use.
- **Features** — whether OIDC sign-in, Schedules, Escalation chains, Maintenance windows and Notification gateways are
  used.
- **Quality** — percentiles of delivery latency, the share of failed deliveries by type, and which Internal alerts
  fired.

Every report carries the random installation id, whichever categories are on, so that the collector can tell reports
apart; the id identifies nothing outside the collector. A report never contains labels, names, URLs, addresses, texts or
anything else taken from Alerts, messages or configuration, and the collector keeps no IP addresses. The UI says so next
to the switches, and a public page of the documentation lists every field and how it is collected (journal D497, D498).

**A preview, not a confirmation.** The settings page shows the next report exactly as it will be sent. A daily send asks
for no confirmation: a question every day would be answered out of habit or not at all (journal D502).

**One fixed collector.** Reports go to a collector that the project runs, at a fixed address built into Muster; the L2
PRD names it, and no setting changes it, so configuration cannot redirect a report to anyone else (journal D497, D502).
Requests go through the outbound package in a client class of their own, `telemetry`, with a proxy setting of its own
and under the outbound address policy like every other client (ADR-0015). The class makes one attempt per daily run with
a short timeout; a failure is counted and logged, and the next day's run is the retry.

**A daily Leader task.** Sending the report is a Leader task (ADR-0007; journal D502). It is safe to run twice: each
report carries the installation id and the day it covers, and the collector keeps one report per installation and day.

**Never email.** Telemetry is the only thing Muster sends about itself, and only with consent; Muster never sends email.
NFR-7 and the rule for contributors say so from L2 on (journal D502).

**Development mode and tests.** `muster dev` and the end-to-end suites never show the consent dialog. In development
mode, reports go to a fake collector among the fake servers, so the path is tested without reaching the project's
collector (journal D502).

## Consequences

- The project learns how Muster is used only from installations that agree, which skews the picture towards those
  willing to share; nothing is collected from the rest.
- An Admin who agrees to the basic category but not to quality sends only that; categories cost a little more UI than
  one switch.
- The project has to run and secure the collector and keep the list of fields public; a change of the fields changes the
  documentation page and the preview together.
- An installation without outbound access simply never sends, and nothing breaks.
- A failed send loses that day's report; the data is statistical and needs no retries.
- NFR-7 and the rule for contributors change from "nothing" to "nothing without consent, and never email".

## Alternatives considered

- **Telemetry on unless turned off.** A red flag for closed networks and against what Muster promises; never (journal
  D120).
- **Sending only from a CLI command** that the operator schedules, such as a daily cron job. Leaves NFR-7 almost
  unchanged, but almost nobody would set it up, so the data would say nothing (journal D502).
- **One switch for everything.** An Admin willing to share the version and the scale but not delivery quality would
  share nothing; categories, which some open-source projects ask about at onboarding, let them choose (journal D498).
- **Confirming each report before it is sent.** A daily question that nobody answers; the preview shows the same content
  once (journal D502).
- **Consent through an environment variable or the Helm chart.** A product setting outside the database, against
  ADR-0010, and invisible in the UI.
- **A configurable collector address, or a third-party analytics service.** A changed address could send installation
  data anywhere, and a third-party service would see every installation's address; the project's own collector at a
  fixed address avoids both (journal D497).
- **The consent dialog in development mode and tests too.** Would break every Playwright suite and the documentation
  screenshots on a fresh installation (journal D502).
