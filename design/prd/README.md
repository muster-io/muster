# Muster — Product Requirements

- Status: Draft
- Date: 2026-10-02

This directory holds the product requirements for Muster, a self-hosted alert grouping and on-call service that sits
after Alertmanager. It says **what** Muster does and how to check it; the **why** behind each architectural choice lives
in the [Architecture Decision Records](../adr/README.md), and every domain term follows the glossary in
[`CONTEXT.md`](../../CONTEXT.md).

## Why Muster exists

Alertmanager is very good at deciding which alerts to send and when: it deduplicates, inhibits, applies Alertmanager
silences and batches notifications. What it sends, though, is a stream of notifications, not something a team can
work with. Every repeat is a new message, nothing records who is handling a problem, and a person in a chat cannot say
"I've got this", "it's fixed" or "not until morning" in a way the next notification respects.

Teams therefore put an on-call tool between Alertmanager and their messengers. For many self-hosted installations that
tool was Grafana OnCall. Its open-source edition was archived in March 2026; its successor, Grafana IRM, is not
available as open-source software that a team can run inside its own network. Muster is an **open, self-hosted
alternative to Grafana IRM**, licensed AGPL-3.0-only (ADR-0001), built for teams that run Prometheus-style monitoring
and talk in Mattermost or Telegram — and want their alerting path to keep working inside a closed network.

Muster turns Alertmanager's notifications into **Alert Groups**: one problem with a number, a status and an Owner,
shown as **one editable message** per messenger channel. People acknowledge, resolve and snooze it from the chat or the
web UI; Muster keeps the message current, rings phones only when someone needs to react, reminds people about what they
took, and records everything in a Timeline and an Audit log.

## Principles

1. **After Alertmanager, not instead of it.** Inhibition, deduplication of redundant rule evaluators and Alertmanager
   silences stay in Alertmanager. Muster starts where Alertmanager stops (ADR-0002).
2. **Simple mechanisms first.** Flapping detection, delayed publication, maximum ages and similar machinery are added
   only when real operation shows they are needed. Until then Muster prefers a small set of rules people can predict.
3. **API-first.** Every feature is an API operation first; the web UI is one client of the API, and the Terraform
   provider and MCP server of later layers will be others. Configuration lives only in the database and changes only
   through the API (ADR-0008, ADR-0010).
4. **One editable message per Alert Group.** Each Destination shows an Alert Group as a single Root message that is
   kept in its current state. Follow-ups go to its Thread. Only a new message can ring a phone, so Muster sends new
   messages only when someone has to react (ADR-0005).
5. **Nothing is lost, everything is explained.** Webhooks are stored before they are processed; a duplicate message is
   preferred to a lost one; every automatic change carries a visible reason.
6. **Muster watches itself.** It reports its own failures through its own pipeline where it can, and through paths
   that bypass it where it cannot (ADR-0014).
7. **Small to run.** One binary and one PostgreSQL database are a complete installation (ADR-0006, ADR-0009).

## Layers

Muster is built in four layers. Each layer is usable on its own and is the base for the next.

**L1 — Alert Groups in messengers.** Alertmanager webhooks with Snapshot semantics and a Heartbeat per Integration;
user-built Routes and Group keys; the four-status Alert Group lifecycle with Reopen, Grace period, Snooze and visible
reasons; a web UI to manage current and past Alert Groups; commands from the web UI, the API, Mattermost and Telegram;
delivery as one reconciled Root message per Destination with Threads, Storm summaries and a fixed Loud/Quiet table,
with Broken Destinations that recover to the current state; outgoing webhooks; ack timeouts and Reminders; local and
OIDC sign-in, TOTP, Personal access tokens, Service accounts and an Audit log; Muster's own metrics, alert rules,
Internal alerts and outgoing heartbeat. L1 is the first release. See [L1.md](L1.md).

**L2 — On-call parity.** Schedules, rotations, escalations beyond the ack timeout and paging the person on call; the
`pull` and `agent` Connection modes that ask Alertmanager directly; a generic inbound webhook for sources other than
Alertmanager; backup Destinations, Alertmanager silence sync, Telegram forum topics and group chats, a Terraform
provider and an MCP server; a maintenance mode and a configurable Loud/Quiet matrix. See [L2.md](L2.md).

**L3 — Incidents.** A coordinated response to a problem that may span several Alert Groups, with its own lifecycle and
record. See [L3.md](L3.md).

**L4 — AI-assisted investigation.** An optional module, off unless configured, that brings your own model: deterministic
enrichers collect context, a language model summarizes it — later chooses what to look at, and finally investigates
with read-only tools — and the result is added to the message that has already been sent. See [L4.md](L4.md).

## Personas

**Admin.** Installs and runs Muster, connects Alertmanager clusters as Integrations, creates Connections and
Destinations, builds Routes, manages Users, sign-in and Organization settings, and watches Muster's own health. Holds
the Admin Role. Often also an on-call responder.

**On-call responder.** Works in Mattermost or Telegram. Acknowledges, resolves and snoozes Alert Groups from the chat,
gets ack timeout notices and Reminders, opens the web UI for details, Notes and Unresolve. Holds the Responder Role.

**Viewer.** Follows what is happening without acting on it — a team lead, a neighbouring team, a support engineer.
Reads Alert Groups and their Timelines in the UI and sees messages in chats, but every command is refused. Holds the
Viewer Role.

**Automation.** Scripts, CI jobs, Terraform and assistants that call the API. A **Personal access token** acts as the
User who created it, narrowed to the permissions the token was given ("alex via token laptop-scripts"). A **Service
account** is a non-person identity with its own Role and tokens, for long-lived automation such as configuration as
code. Neither signs in to the UI or links messenger accounts.

## How to read these documents

| Document | Content |
|---|---|
| [L1.md](L1.md) | The first release: scope, rollout stages, the capability map, conventions, non-functional requirements, what is out of scope and what is still open |
| [l1/C-NN-….md](l1/) | One file per L1 capability `C-01`…`C-21`, with scenarios, functional requirements (`C-NN.FR-n`), UI, API surface, acceptance statements (`C-NN.AC-n`) and a suggested story split |
| [l1/reference.md](l1/reference.md) | Tables shared by several capabilities: Loud and Quiet, buttons, banners, Roles and Permissions, Internal alerts, metrics, chart rules |
| [l1/defaults.md](l1/defaults.md) | Every configurable value and built-in limit of L1, with its status: decided or provisional |
| [L2.md](L2.md) | Scope of the on-call parity layer and the designs already kept for it, plus features waiting for operating experience |
| [L3.md](L3.md) | Scope of the Incidents layer |
| [L4.md](L4.md) | Scope of the AI layer: tiers, data sources, protocols, safety and cost controls |
| [ADR index](../adr/README.md) | Architectural decisions with their context, consequences and rejected alternatives |
| [Verified facts](../facts.md) | Behaviour of Telegram, Mattermost and Alertmanager checked on the real systems, each fact `F-NNN` with its date and method; requirements cite them |
| [`CONTEXT.md`](../../CONTEXT.md) | The glossary; capitalized terms in these documents (Alert Group, Route, Destination, …) have exactly that meaning |

Conventions:

- **Capabilities** (`C-NN`) are the units of delivery, each with its own file and issue. Its stories are contract files
  `design/stories/S-NNN-<slug>.md`: one backend story, or a backend story and then a frontend story when it has a UI.
  A capability lists the capabilities it depends on, and each of its acceptance statements can be checked when it
  merges ([L1 conventions](L1.md#2-conventions)).
- **Requirements** are written as statements about Muster's observable behaviour; each one is meant to be checked by a
  test or by hand. Values and behaviours marked _provisional_ are starting points that a story or a test-environment
  measurement may change before release; each has an item in the open questions of [L1.md](L1.md), and the
  [defaults table](l1/defaults.md) lists every configurable value in one place.
- **ADR references** (ADR-00NN) point to the reason behind a requirement. The PRD does not repeat that reasoning.
- The PRD covers product behaviour. Engineering standards — code layout, lints, test strategy — are in the ADRs; the
  API itself is specified in `api/openapi.yaml`.
