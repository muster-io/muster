---
id: S-151
title: "Documentation: connecting a Notification gateway, confirming a contact and writing a gateway bridge"
capability: C-30
kind: docs
layer: L2
depends_on: [S-130, S-146, S-147, S-150]
covers: [C-30.FR-11]
files_touched:
  - docs/messengers/notification-gateways.md
  - docs/messengers/write-a-bridge.md
  - docs/messengers/bridge.md
  - docs/messengers/index.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - mkdocs.yml
  - web/e2e/screenshots/notification-gateways.spec.ts
acceptance:
  - "[C-30.FR-11] Messengers → Notification gateways explains what a Notification gateway is and that Muster ships none and never calls by itself; setting one up — a Bridge Connection for a bridge in the role `gateway`, its check showing \"Notification gateway\", a bridge with both roles; confirming a contact in the profile, the code and its limits, what Admins see; how calls combine within `gateway.combine_window`, are retried and acknowledged only while nobody owns the Alert Group, with \"Already taken by …\"; Broken gateways and their runbook; and, until the push of Mentions in Threads is verified (S-160), that at night only a Notification gateway is reliable."
  - "[C-30.FR-11] Write a bridge has the section \"A gateway bridge\": the role in `info`, `sendNotification` with its `Idempotency-Key` and action data, answering during a call through `answerEvent` in the push mode so that the answer comes while the person is still on the line, `notification_acknowledged`, the error classes of calls and what makes a gateway Broken; with the gateway examples of the Go library first and the Python library after, and the protocol without a library."
  - "[C-30.FR-11] The pages have screenshots of the profile's confirmed contact and of the Connection page of a gateway, taken by `web/e2e/screenshots/notification-gateways.spec.ts`; `make docs-screenshots GUIDES=notification-gateways` takes them, every image has its alternative text, `make docs-check` passes, and `llms.txt` lists the page in a scenario on reaching people at night."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-151. Documentation: connecting a Notification gateway, confirming a contact and writing a gateway bridge

## Scope

**IN**

- The completion of Messengers → Notification gateways from the sections S-167 wrote: setting a gateway up, contacts,
  calls and their combining, the acknowledgement from a call, Broken gateways, and the note about the night
  (C-30.FR-11).
- "A gateway bridge" in Write a bridge, for authors of gateways, with both libraries (C-30.FR-11, ADR-0017).
- Screenshots, links from the Bridge page and the Messengers overview, and the scenario in `llms.txt`.

**OUT**

- Notification rules and how their calls combine with the steps of a chain: S-161. The push facts: S-160, after which
  S-161 revisits the note about the night on every page that carries it.

## Contracts

- **Pages**: `docs/messengers/notification-gateways.md` gains "How calls are made" (one call per person and gateway,
  combining, retries, critical first, never reconciled), "Acknowledging from a call" (the Transport `gateway`, the
  precondition, the answers), "Broken gateways" (linking the runbook of `MusterNotificationGatewayBroken`) and "At
  night" (until S-160 settles the facts, only a Notification gateway is reliable); `docs/messengers/write-a-bridge.md`
  gains "A gateway bridge" with "The role in `info`", "`sendNotification`", "Answering during a call" and
  "`notification_acknowledged`", with the examples of S-146 and S-147; `docs/messengers/bridge.md` "Roles" links to it;
  `docs/messengers/index.md` lists Notification gateways; `docs/index.md` links the page from its overview.
- **Screenshots**: `web/e2e/screenshots/notification-gateways.spec.ts` on `muster dev` with the demo gateway — the
  profile's confirmed contact and the gateway's Connection page; the images are generated and not listed.
- **llms.txt** (`docs/llms.txt.tmpl`): a scenario "Reach people at night" that names the page.

## Steps

1. Write the sections and the links. Check: `make docs-check`.
2. Write the screenshot spec and take the images. Check: `make docs-screenshots GUIDES=notification-gateways`.
3. Add the scenario to `llms.txt`. Check: `make docs-check`.

## Verification

`make docs-serve`, then open Messengers → Notification gateways: the sections above in order, the two screenshots with
their alternative text, the link to the runbook and to Write a bridge → A gateway bridge, whose Go example runs
`examples/gateway` of `muster-io/muster-bridge-go`; `make docs-check` passes; `site/llms.txt` lists the page under
"Reach people at night".

## Open questions

None.

## Notes

- Suggested commit: `docs: connect a notification gateway and write a gateway bridge`.
- `covers` is C-30.FR-11 in full: every part it names is here; S-161 adds only the Notification rules and the steps of
  chains that call through a gateway.
- Examples use the contacts `+10000000001` and `+10000000002` and the people `alice` and `bob`; no real provider is
  named.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-11 | full | |
