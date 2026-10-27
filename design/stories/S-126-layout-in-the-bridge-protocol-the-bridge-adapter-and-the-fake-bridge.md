---
id: S-126
title: The structured layout in the Bridge protocol, the Bridge adapter, muster bridge check and the fake bridge (BE)
capability: C-29
kind: be
layer: L2
depends_on: [S-125]
covers: [C-29.FR-1, C-29.FR-4, C-29.FR-6, C-29.FR-8, C-29.AC-6, C-12.FR-1, C-22.FR-4, C-22.FR-21]
files_touched:
  - api/bridge.yaml
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/bridge/testmsg.go
  - internal/bridge/testmsg_test.go
  - internal/bridge/conformance.go
  - internal/bridge/conformance_test.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - test/e2e/bridge_layout_test.go
  - docs/messengers/bridge.md
  - AGENTS.md
acceptance:
  - "[C-29.FR-8, C-22.FR-4] `api/bridge.yaml` gives `MessageContent` the optional `layout` (`Layout`), additively in protocol version 1: `number` (the Alert Group's number alone), `title` (without the number), `status` {`key` — `firing`, `acknowledged`, `snoozed`, `resolved`; `word`, in the Route's language}, `fields` ({`name`, `value`}[]), `summary`, `description`, `labels` ({`key`, `value`}[]), `alerts` {`label`, `values` ({`value`, `resolved`}[]), `total`}, `notices` (string[]), `assignees` and `owner` (each person {`name`, `ref`, `team`} with `ref` the `ref` of their entry in the envelope's `mentions`, or null, and the optional `team` {`name`} that S-168 fills, C-32.FR-12), and `links` ({`label`, `url`}[]); every value is plain text, never CommonMark; `make lint` passes Redocly and oasdiff reports no breaking change."
  - "[C-29.FR-1, C-29.FR-4, C-29.AC-6, C-12.FR-1] For a firing Root message the fake bridge receives `body` and `layout`, whose `number` is the number alone, whose `title` has no `#`, whose values carry no CommonMark escapes and whose `alerts.total` is the number of Alerts, also when fewer values are listed; a test message carries `layout` too, Storm summaries and Thread replies do not (`envelope_test.go`, `testmsg_test.go`, `bridge_layout_test.go`)."
  - "[C-29.AC-6] After an Acknowledge by a User linked through the Bridge, a new Root message to a bridge with `native_mentions` names the Owner in `layout.owner` with the `ref` of their Mention entry, and an edit, which carries no Mentions, names them with the `ref` null; through a bridge without `native_mentions` the Owner has the `ref` null (`envelope_test.go`)."
  - "[C-29.FR-6] When the body must be cut below `max_body_length`, `layout` keeps the title, the status, the fields, the people and the links, and its description, Alerts, summary and labels shrink in that order with the body's, so that the two never disagree; the buttons stay in the envelope's `buttons` (`delivery_test.go`)."
  - "[C-29.FR-8, C-22.FR-21] `muster bridge check` sends `layout` in its message steps and passes against a bridge that ignores it and one that renders it; the fake bridge records `layout` with every message and edit in `/_fake/messages` (`conformance_test.go`, `fakebridge_test.go`)."
  - "[C-29.FR-8] The Bridge page of the documentation says that Root messages carry the same content as `layout` beside `body`, and the generated Bridge protocol reference shows the schema; `make docs-check` passes."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: 415
---

# S-126. The structured layout in the Bridge protocol, the Bridge adapter, muster bridge check and the fake bridge (BE)

## Scope

**IN**

- `layout` next to `body` in the envelope of a Root message and of a test message, with plain-text values, the number
  apart from the title, the Alerts with their total, labels as key and value, and people as references to the
  envelope's Mentions (C-29.FR-8, ADR-0017 as amended) — additively in protocol version 1.
- The Bridge adapter that fills it from the compact layout of S-125, shortened together with the body (C-29.FR-4,
  C-29.FR-6, C-22.FR-4 revised).
- `muster bridge check` sending it, and the fake bridge recording it.

**OUT**

- The types for `layout` in the Go and Python bridge libraries: S-127, S-128 (in their repositories).
- The guide for bridge authors with an example of rendering `layout` natively: S-130.
- The Assignees, which appear from S-168; until then `assignees` is empty and `owner` is set while the Alert Group is
  acknowledged.

## Contracts

- **Protocol** (`api/bridge.yaml`, MIT): new schemas `Layout`, `LayoutStatus`, `LayoutField`, `LayoutLabel`,
  `LayoutAlerts`, `LayoutAlertValue`, `LayoutPerson` (`name`, `ref`, and the optional `team` {`name`} that stays absent
  until S-168 names the Teams of the Assignees and the Owner), `LayoutLink`; `MessageContent` gains the optional
  `layout`, so that `postMessage` and `editMessage` carry it; the description of `MessageContent` says that a bridge
  renders either `body` or `layout` and that a bridge that ignores `layout` is unaffected. The shape is the
  `MessageLayout` of `openapi.yaml` (S-125). `protocol_version` stays 1.
- **Messages** (`internal/messages/layout.go`): `LayoutOf(Message)` — the structured content of a laid-out message,
  plain text, from the same fields the adapters use, with the references of its people left for the Bridge adapter.
- **Bridge adapter** (`internal/bridge/envelope.go`, `delivery.go`, `testmsg.go`): `content` adds `layout` to Root
  messages and test messages; the Owner, and the Assignees once they exist, get the `ref` of their entry in `mentions`
  where the message carries their Mention — a new message to a bridge with `native_mentions` — and `null` otherwise, an
  edit included (C-29.AC-6); when the body is cut, `layout` is cut in the same order (C-29.FR-6). The Desired state's
  hash covers `layout`, so a change of layout alone edits the message.
- **Check** (`internal/bridge/conformance.go`): the steps that post and edit send `layout`; a bridge's answer is judged
  as before.
- **Fake bridge** (`internal/fakes/fakebridge/messages.go`): each recorded message and edit keeps its `layout`, shown in
  `/_fake/messages`; the fake renders nothing from it.
- **AGENTS.md**: the paragraph on development mode names that the fake bridge records `layout` with every message and
  edit.
- **Settings**: `message.compact_alerts_listed`; the bridge's `max_body_length` and `native_mentions` (C-22).
- **Log events, metrics**: none new.
- **Documentation**: Connect a messenger through a bridge — "What Muster posts" names `layout`; the reference page of
  the protocol is generated from `api/bridge.yaml`.

## Steps

1. Add `Layout` to `api/bridge.yaml`. Check: `make generate-check` (the models of `internal/bridge/gen/`); `make lint`;
   the two comparisons of the CI step "oasdiff breaking, api/bridge.yaml" (`.github/workflows/ci.yml`) run locally —
   `oasdiff breaking --fail-on ERR --flatten-allof` of `origin/master:api/bridge.yaml` against the new file, and the
   reverse with the step's `--severity-levels` — report no ERR: run on a draft of this change with oasdiff v1.33.0 in
   the plan review, the reverse direction reports `request-property-removed` for `layout` on `POST /v1/messages` and
   `PATCH /v1/messages/{message_id}` as WARN only, which the step lets pass. A bridge ignores request fields it does
   not know (`api/bridge.yaml`, "Version"), so the warning is no breakage.
2. Write `LayoutOf` and fill `layout` in the adapter, with the references and the cut. Check: `layout_test.go`,
   `envelope_test.go`, `delivery_test.go`, `testmsg_test.go`.
3. Send it in the check and record it in the fake bridge. Check: `conformance_test.go`, `fakebridge_test.go`.
4. Write `test/e2e/bridge_layout_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-076: A, the demo Bridge Connection "Example Bridge" with its Destination (channel ch-alerts of the fake
# bridge, native_mentions on), bob linked through it; the Integration "sev" with NOTIFY, PUTA and AG; a Route "br" to
# that Destination with Group key alertname.
FB=127.0.0.1:18094/_fake
curl -s -X PUT $FAM/groups/b1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"QueueFull"}}' > /dev/null
for p in api-1 api-2 api-3 api-4 api-5 api-6 api-7; do PUTA b1 $p "{\"labels\":{\"svc\":\"br\",\"pod\":\"$p\",\"severity\":\"warning\"},
  \"annotations\":{\"summary\":\"Queue *full* on #1\"}}"; done
NOTIFY b1 '{"reason":"first notification"}'; G=$(AG 'svc%3D%22br%22'); sleep 1
curl -s $FB/messages | jq -c '[.[] | select(.kind == "root")] | last | {n: (.layout.number | type), t: .layout.title,
  s: .layout.status.key, sum: .layout.summary, a: {label: .layout.alerts.label, listed: (.layout.alerts.values | length), total: .layout.alerts.total}}'
# {"n":"number","t":"QueueFull","s":"firing","sum":"Queue *full* on #1","a":{"label":"pod","listed":5,"total":7}}
curl -s "${B[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
curl -s $FB/messages | jq -c '[.[] | select(.kind == "root")] | last | .edits | last | .layout.owner'       # {"name":"bob","ref":null}
printf %s muster-dev-bridge-token-b > tok
./bin/muster bridge check http://127.0.0.1:18094 --token-file tok --channel ch-alerts --no-interactive; echo "exit $?"
# every step passed or skipped; exit 0
curl -s $FB/messages | jq -c '[.[] | select(.kind == "test" or .kind == "root")] | last | has("layout")'      # true
```

`test/e2e/bridge_layout_test.go` repeats these checks and adds a new Root message after the acknowledgement, through a
Route to the same Destination, whose `layout.owner.ref` is the `ref` of bob's entry in its `mentions`, and a bridge
whose `max_body_length` forces a cut.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): send the structured message layout to bridges`.
- The summary in the check above keeps `*full*` and `#1` as they are: `layout` is plain text, and the bridge escapes it
  for its own markup (C-29.FR-8).
- The libraries' copies of `api/bridge.yaml` fail their `make spec-check` once this merges, until S-127 and S-128 take
  the new copy.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-29.FR-1 | partial | the layout through a Bridge; the Assignees are S-168 |
| C-29.FR-4 | partial | the Bridge envelope |
| C-29.FR-6 | partial | the cut of `layout` with the body |
| C-29.FR-8 | partial | the protocol, the adapter and the check; the libraries are S-127 and S-128 |
| C-29.AC-6 | full | |
| C-12.FR-1 | partial | revised for L2: the layout through a Bridge |
| C-22.FR-4 | full | revised for L2 |
| C-22.FR-21 | partial | revised for L2: the check accepts `layout`; the role `gateway` with `sendNotification` is S-145 |
