# 0016. Flat domain packages, a command layer and architecture lints

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — delivery events live in a table of their own, outside the Alert Group tables; the move to the
  Default route is a system transition
- Amended: 2026-10-14, corrected 2026-10-16 — the conformance check of `muster bridge check` sends outside delivery,
  calling the Bridge protocol client directly against a bridge outside any installation
  ([ADR-0017](0017-bridge-protocol-for-messengers-without-native-support.md)); lint 3 checks calls of the delivery
  adapter's methods, not the messenger clients beneath them, so it needs no entry for the command
- Amended: 2026-10-26 — for L2 (journal D491, D508, D513, D518, D519, D532, D547): the commands Attach and Detach, and
  new system transitions — the steps of Escalation chains, a change of Assignees, a Team that joins an Alert Group, the
  start and end of a Maintenance window, an attached Alert Group following the main one; the Transport `gateway`, whose
  Acknowledge applies only while nobody owns the Alert Group, and the Transport `mcp` of an MCP server in the binary;
  lint 3 also allows calls through a Notification gateway from their sender

## Context

Muster is a single Go service, written by contributors and by coding agents working from story files with compiler and
test feedback. Two forces pull in different directions:

- Most of the code is configuration CRUD — Integrations, Routes, Connections, Destinations, Users — with little domain
  logic. Layering it through use cases, ports and adapters, as full Clean Architecture prescribes, multiplies files and
  indirection without protecting anything.
- The Alert Group lifecycle is different. The same commands arrive through four Transports — the web UI, the API,
  Mattermost and Telegram — and Muster itself causes transitions too, on timers and because of ingestion; an MCP server
  and the CLI will come later. Every path must check the same permission and precondition, change the state the same way
  and leave the same Audit log and Timeline entries.

Some invariants span the whole code base and break in one careless line: one Organization's data must never appear in
another's query; Alert Groups must change only through their state machine; messenger messages must change only
through reconciliation; outbound HTTP must pass the SSRF policy; secrets must not reach logs. Review alone does not hold
such rules across hundreds of changes; coding agents in particular follow what the build enforces.

## Decision

**Flat domain packages.** Code lives in `internal/<domain>` packages, one per domain — for example `groups`, `routing`,
`ingest`, `delivery`, `integrations`, `destinations`, `users`, `audit` — next to a few shared infrastructure packages:
database access, the outbound HTTP client (ADR-0015), the domain logger and metrics. There is no project-wide split into
domain, application, infrastructure and interface layers. **Configuration CRUD is flat**: an API handler calls its
domain package, which validates, writes through the generated queries (ADR-0006) and records the Audit log with a
before/after diff, with nothing in between. An interface is declared by its consumer, and only where a second
implementation exists or is planned (ADR-0001).

**A command layer for the Alert Group lifecycle.** The `groups` package exposes the commands of ADR-0004 — Acknowledge,
Unacknowledge, Resolve, Unresolve, Snooze, Unsnooze and, from L2, Attach and Detach — and the system transitions:
resolution by the system, Reopen, the end of a Snooze, a rise to Urgent, ack timeout notices, Reminders,
auto-unacknowledge after unanswered Reminders (ADR-0004) and the move of an open Alert Group to the Default route when
its Route is deleted (`moved_to_default_route`), and from L2 the steps of Escalation chains with the Pages, skipped
steps and undelivered Pages they record, a change of Assignees, a Team that joins an Alert Group or a value of the Team
label that names no Team, the start and end of a Maintenance window for an Alert Group, and an attached Alert Group
following the status of the one it is attached to (ADR-0004, ADR-0018). All of them pass through **one dispatcher** with
fixed steps: **permission → precondition → transition → Audit log (people and automation) → Timeline → re-render**, the
last step updating the Desired state for reconciliation (ADR-0005). Transports are thin adapters: they authenticate,
turn a request into a command and turn the result into a response. Recording the Audit log is a step of the dispatcher,
not a rule to remember, so no command from a person or automation can skip it. System transitions — every transition
Muster starts itself, on a timer, because of ingestion or because of a configuration change such as deleting a Route —
use the same dispatcher with Muster as the actor and the Transport `system` (ADR-0014); they need no permission and are
recorded in the Timeline, while the Audit log records what people and automation did.

**Transports of L2.** The MCP server is an adapter like the others: it runs in the binary on the app listener,
authenticates with a Personal access token and acts with its User's Permissions, derives its tools from the operations
of the API specification (ADR-0008), reads Alert Groups, Alerts, Timelines, Schedules and who is on call, and runs
Acknowledge, Resolve and Snooze and adds Notes through the dispatcher with the Transport `mcp`; configuration changes
through the API and the Terraform provider, never through it (journal D491, D519). A Notification gateway is a Transport
too: the acknowledgement it reports runs Acknowledge with the Transport `gateway` as the person the Page was for, under
a precondition of its own — it applies to a firing or snoozed Alert Group and changes nothing once someone owns it
(journal D518).

**Architecture lints.** From the first story, CI runs a separate architecture step (`make lint-arch`, with its code
behind the `lint` build tag) that fails the build when:

1. a query on a table with `org_id` does not filter by it — checked by parsing the SQL queries;
2. a query that writes Alert Group tables is called from outside the `groups` package; delivery records its delivery
   events in a table of its own, outside the Alert Group tables, and the Timeline view merges them (ADR-0005), so this
   lint has no exception for delivery;
3. a messenger send or edit through the delivery adapter interface is called from outside the delivery worker and the
   named interactive path (ADR-0005); the messenger clients beneath the adapters are not checked, so the conformance
   check of `muster bridge check`, which calls the Bridge protocol client directly (ADR-0017), needs no exception; from
   L2, a call through a Notification gateway is allowed only from the gateway sender and the interactive path (ADR-0005;
   journal D532);
4. an HTTP client or transport is created outside the outbound HTTP package (ADR-0015);
5. a known secret value reaches a log line — a test pushes known secrets through the code and inspects the output;
6. `context.Background()` is used outside `main`, the wiring code and tests;
7. anything logs other than through the domain logger;
8. a histogram other than a Prometheus-compatible `le` histogram is constructed (ADR-0014).

Standard linter rules (for example forbidden-identifier rules) are used where they suffice, and small analyzers over the
Go syntax tree and the SQL files where they do not.

## Consequences

- Code is found by domain, and CRUD stays short.
- A new Transport — an MCP server, the CLI — is an adapter; the rules for Alert Groups already live in one place.
- Breaking an invariant fails the build instead of waiting for a reviewer to notice; the lints need maintenance as
  packages move, and a false positive is fixed in the lint rather than worked around in the code.
- Domain code calls the generated queries directly; replacing the database is not a goal (ADR-0006).
- The architecture step costs CI time and its own code, which is reviewed like any other.

## Alternatives considered

- **Full Clean Architecture** — domain, application, infrastructure and interface layers with ports everywhere. Pays
  off in complex domains; for CRUD it adds files and indirection, and the one complex area, the Alert Group lifecycle,
  is covered by the command layer.
- **Flat packages without a command layer.** Each Transport would re-implement permissions, preconditions and Audit log
  records for every command, and they would drift apart.
- **One generic "set status" operation.** See ADR-0004.
- **Code review instead of lints.** Rules that must hold for every line are exactly what reviews miss.
- **Row-level security alone for Organization isolation.** Possible later as a second line of defence; the query check
  catches the mistake at build time.
