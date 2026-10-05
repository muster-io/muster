# Muster

Muster is a self-hosted alert grouping and on-call service that sits after Alertmanager. It turns Alertmanager's
notifications into Alert Groups — one problem with a number, a status and an Owner — and shows each Alert Group as one
editable message in Mattermost, in Telegram or through an outgoing webhook. People acknowledge, resolve and snooze an
Alert Group from the chat or the web UI, and Muster keeps its message current, sends a Loud update only when someone
has to react and records everything in a Timeline and an Audit log.

Muster is an open alternative to Grafana IRM for teams that run Prometheus-style monitoring and want their alerting
path to keep working inside their own network. One binary and one PostgreSQL database are a complete installation.

## Status

Muster is designed, and its implementation is starting. **It is not usable yet**: there is no release, and the binary
only prints its version. The product requirements, architecture decisions, database schema, API specification and the
story contracts that the implementation follows are in [`design/`](design/) and [`api/`](api/).

## Roadmap

Muster is built in four layers. Each layer is usable on its own and is the base for the next.

- **L1 — Alert Groups in messengers (the first release).** Alertmanager webhooks read as Snapshots, with a Heartbeat
  per Integration; Routes and Group keys built in the UI; the Alert Group lifecycle — Firing, Acknowledged, Resolved,
  Snoozed — with Reopen, visible reasons and a Timeline; a web UI that also works on a phone; commands from the web UI,
  the API, Mattermost and Telegram; one reconciled Root message per Destination, with Threads and Storm summaries;
  outgoing webhooks; ack timeouts and Reminders; local and OIDC sign-in, TOTP, Personal access tokens, Service accounts
  and an Audit log; Muster's own metrics, alert rules and Internal alerts. See [L1](design/prd/L1.md).
- **L2 — On-call parity.** Schedules, rotations, escalations and paging the person on call; asking Alertmanager
  directly; a generic inbound webhook; backup Destinations; Alertmanager silence sync; a Terraform provider and an MCP
  server. See [L2](design/prd/L2.md).
- **L3 — Incidents.** A coordinated response to a problem that may span several Alert Groups, with its own lifecycle
  and record. See [L3](design/prd/L3.md).
- **L4 — AI-assisted investigation.** An optional module, off unless configured, that brings your own model: it
  summarizes the context of an Alert Group and later investigates it with read-only tools, adding the result to the
  message that has already been sent. See [L4](design/prd/L4.md).

## Design

| Document | Content |
|---|---|
| [`CONTEXT.md`](CONTEXT.md) | The glossary; its terms are used everywhere, code included |
| [Product requirements](design/prd/README.md) | What Muster does, layer by layer, and how to check it |
| [Architecture decisions](design/adr/README.md) | Every technical decision with its reasons and the alternatives |
| [Architecture](design/architecture.md) | The system, its flows and state machines |
| [Database schema](design/db/schema.md) | Tables, invariants, indexes and retention |
| [API](api/README.md) | Conventions of the HTTP API and its [OpenAPI specification](api/openapi.yaml) |
| [Stories](design/stories/README.md) | The contracts the implementation is built from, one pull request each |

## Building

You need Go 1.27, GNU make and git. With the default `GOTOOLCHAIN=auto`, the `go` command fetches the toolchain that
`go.mod` asks for.

```sh
make help                          # list the targets
make build && ./bin/muster version # muster 0.0.0-dev (commit <commit>)
make ci                            # the checks a pull request runs
```

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) and [`AGENTS.md`](AGENTS.md), the guide for everyone who writes code for
Muster, people and coding agents alike. Contributions are accepted under the [Contributor License Agreement](CLA.md).

## Licence

Muster is licensed under the [GNU Affero General Public License, version 3 only](LICENSE) (AGPL-3.0-only).
Third-party code is listed in [`NOTICE`](NOTICE).
