---
id: S-101
title: "Slack Socket Mode held by the Leader: the WebSocket through the outbound package, confirmed envelopes, refresh and back-off, and the warning about a shared app (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-100]
covers: [C-24.FR-2, C-24.FR-9, C-24.FR-14, C-24.FR-17, C-24.AC-3, C-24.AC-10, C-24.AC-17, C-24.AC-19, C-02.FR-10, C-02.FR-20, C-23.FR-1, C-23.FR-3]
files_touched:
  - go.mod
  - api/openapi.yaml
  - internal/outbound/websocket.go
  - internal/outbound/websocket_test.go
  - internal/outbound/proxy.go
  - internal/archlint/callrules.go
  - internal/archlint/testdata/rule4/**
  - internal/archlint/secretleak.go
  - internal/slack/socket.go
  - internal/slack/socket_test.go
  - internal/slack/check.go
  - internal/slack/check_test.go
  - internal/slack/client.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/query.sql
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/api/connections.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/db/migrations/0019_request_log_socket_envelopes.up.sql
  - internal/db/migrations/0019_request_log_socket_envelopes.down.sql
  - internal/db/migrate_test.go
  - internal/db/db_test.go
  - design/db/schema.md
  - internal/requestlog/inbound.go
  - internal/requestlog/inbound_test.go
  - internal/requestlog/mask.go
  - internal/requestlog/mask_test.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeslack/socket.go
  - internal/fakes/fakeslack/presses.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - design/architecture.md
  - design/stories/S-020-snapshot-processing-and-alerts-view.md
  - design/stories/S-103-slack-connection-and-destination-pages-check-before-saving-and-profile-links.md
  - design/stories/coverage.md
  - deploy/helm/muster/files/rules.yaml
  - deploy/helm/muster/tests/rules/MusterNoLeader.yaml
  - docs/operations/runbooks/MusterNoLeader.md
  - docs/operations/high-availability.md
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/src/components/app-shell.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/connection-check.test.tsx
  - test/e2e/slack_test.go
acceptance:
  - "[C-24.FR-9, C-24.AC-10, C-02.FR-10] The Leader's task `slack_socket_mode` holds one connection for each Slack Connection in `socket` mode that is not deleted, opened after a new `apps.connections.open` each time; a `hello` that has not come within `slack.hello_timeout` closes the connection and opens the next; an `interactive` envelope is confirmed with `{\"envelope_id\": …}` on its socket before its press is handled by the path of S-100, and an envelope of another type is confirmed and ignored; losing leadership or stopping the replica closes every connection at once; a Connection switched to `http` or deleted loses its connection within a second (`socket_test.go`, with a manual clock and the fake server's socket)."
  - "[C-24.FR-9, C-24.AC-10] After `disconnect` with the reason `warning` or `refresh_requested`, the next connection is open, with its `hello`, before the old one closes; `link_disabled`, `too_many_websockets` and a failed dial back off by `slack.socket_backoff`, log `slack_socket_failed` with the reason, count against nothing and make no Destination Broken; with two replicas only the Leader holds a connection, and after it stops the new Leader opens one (`test/e2e/slack_test.go`, `E2E_REPLICAS=2`)."
  - "[C-24.FR-2, C-24.FR-9, C-24.AC-10] Each `hello` logs `slack_socket_connected` with `num_connections`, and stores on the Connection its time and the connections it counts beyond those the Leader holds; when there are more, the Leader logs `slack_socket_shared`, the Connection's read shows `socket.other_connections`, the check passes with the warning `another_client_connected`, and `muster doctor` prints a WARN line; the next `hello` without others clears them."
  - "[C-24.FR-2] In `socket` mode the step `app_token` of the check also calls `apps.connections.open` with the app-level token and passes on `ok`, without opening the socket; `not_allowed_token_type` there fails it with `wrong_token_type`."
  - "[C-02.FR-20, C-24.AC-3] The outbound package opens a WebSocket with the client's dialer and proxy — by CONNECT through an HTTP or HTTPS proxy, also for a `ws` target, and through a SOCKS5 proxy — with the outbound address policy, the connect timeout and the user agent, no redirect, and the handshake counted in `muster_client_requests_total` (`websocket_test.go`); outside `muster dev` a socket address that is not `wss` under `slack.com` is refused; with an HTTP proxy on the Connection the socket reaches the fake Slack server only through the fake proxy, by CONNECT; lint 4 refuses `websocket.Dial` outside `internal/outbound` (a fixture under `testdata/rule4`)."
  - "[C-24.FR-14, C-24.AC-17, C-23.FR-1, C-23.FR-3] Each envelope that carries an `envelope_id` is recorded as `inbound` with the operation of its `type` (`interactive`), the socket's host without its query as its URL, the envelope as its request and the confirmation as its answer, without a status; `hello`, `disconnect` and pings are not; the `url` that `apps.connections.open` answers has its `ticket` replaced by `[redacted]` in the record, the logs and every error; the app-level token and the `ticket` reach no log line (`secretleak.go` probe `slack_socket`)."
  - "[C-24.FR-17] The log events `slack_socket_connected`, `slack_socket_shared` and `slack_socket_failed` are in the registry and its reference page; the banner of Admins without a Leader, the chart rule `MusterNoLeader`'s description and its runbook name Slack's Socket Mode among the paused tasks, in English and Russian, and `make rules-test` passes."
  - "[C-24.AC-19] On the test workspace, with the Connection in `socket` mode, the Leader's `hello` counts one connection, a new Alert Group appears in `#alerts`, and a person's press of Ack, from a Slack account linked to a Responder, acknowledges it as that User with the Transport `slack`."
verify: "make ci test-integration e2e-go rules-test docs-check"
operator_attention: true
issue: 310
---

# S-101. Slack Socket Mode held by the Leader: the WebSocket through the outbound package, confirmed envelopes, refresh and back-off, and the warning about a shared app (BE)

## Scope

**IN**

- WebSocket connections in the outbound package, and the lint that keeps them there.
- The Leader task `slack_socket_mode`: one Socket Mode connection per Slack Connection in `socket` mode, its `hello`,
  the planned refresh, back-off, and the envelopes confirmed at once and handed to the presses of S-100.
- The count of other connections of the app, on the Connection, in its check and in `muster doctor`;
  `apps.connections.open` in the check.
- The Request log of envelopes and the masking of the socket's `ticket`; Slack's Socket Mode among the tasks a missing
  Leader pauses.
- The socket of the fake Slack server, and presses sent through it.

**OUT**

- The press path itself and the answers (S-100); the Connection page that shows the warning (S-103).

## Contracts

- **Dependency**: `github.com/coder/websocket` (ISC) — the client in Muster, the server in the fake. The pull request
  states its licence, its latest release and its maintenance (ADR-0001); `make licenses` passes.
- **Outbound** (`internal/outbound/websocket.go`, `proxy.go`): `DialWebSocket(ctx, client, url, header)` opens the
  connection with the client's transport — the dialer that checks the address policy after name resolution, the
  client's proxy by CONNECT (also for `ws`, so that a plain proxy request never carries the upgrade) or SOCKS5 — its
  connect timeout, its user agent and no redirect; the handshake is one request in `muster_client_requests_total`, and
  the URL's query is redacted from errors and logs. Reading, writing and pinging the open connection are the caller's.
- **Lint 4** (`internal/archlint/callrules.go`): `websocket.Dial` of `github.com/coder/websocket` outside
  `internal/outbound` fails like an HTTP client would; `websocket.Accept` in the fakes and tests is allowed.
- **Socket** (`internal/slack/socket.go`): per Connection, `apps.connections.open` in the background class, the
  address accepted only as `wss` under `slack.com` (or the fake's under `muster dev`) and registered as a secret,
  `DialWebSocket` through the Connection's proxy, `hello` within `slack.hello_timeout`, a ping every 30 s with a
  10-s deadline, the frames read in a loop: `hello` (recorded), `disconnect` (`warning` and `refresh_requested` open the
  next connection first; `link_disabled` and `too_many_websockets` back off), and every envelope with an
  `envelope_id` confirmed at once — `interactive` handed to `slack.Presses.Go` with the process's context (the drain
  at shutdown waits for it, as for the callback's presses), the others ignored. Failures back off by
  `slack.socket_backoff`, reset by a `hello`.
- **Leader task** (`internal/leader/tasks.go`): `slack_socket_mode` starts and stops one socket per Slack Connection
  in `socket` mode, reading the list again on the Connections' change hint and every minute; it stops all sockets when
  leadership ends (ADR-0007).
- **Connections** (`internal/connections/slack.go`, `connections.go`, `query.sql`, and `internal/api/connections.go`
  for the read): `ListSocketConnections`; `RecordSocketHello` writes `slack_socket_hello_at` and
  `slack_socket_other_connections` for the version the socket read, not a change of the configuration; the read of a
  Slack Connection returns `socket` (`hello_at`, `other_connections`), null in `http` mode and before the first
  `hello`; the check's warning and the doctor's WARN line read it. A save of the Connection forgets
  `slack_socket_hello_at` and `slack_socket_other_connections`: its socket starts again and counts at its next `hello`.
- **Spec** (`api/openapi.yaml`): `SlackConnection.socket` (`hello_at`, `other_connections`), read-only;
  `ConnectionCheckResult.warnings` gains `another_client_connected`, described, and `connection-check.tsx` shows its
  text from reference.md in English and Russian, since its switch over the warnings must name every value.
- **Check** (`internal/slack/check.go`): `apps.connections.open` is the first call of the step `app_token`, before
  `auth.test` with the app-level token, so that a bot token typed as the app-level token gets Slack's
  `not_allowed_token_type` (F-070) and fails the step with `wrong_token_type`; its answer's `url` is dropped unread.
- **Request log** (`internal/requestlog`): envelopes recorded as C-24.FR-14 says, through the inbound recorder, in one
  place with the callbacks; an envelope's record has the method `WEBSOCKET`, since a frame has no HTTP method and the
  column requires one; masking by shape of `ticket=` in a socket address, so the answer of `apps.connections.open` is
  masked in the outbound record too.
- **Schema** (`internal/db/migrations/0019_request_log_socket_envelopes`, `design/db/schema.md`):
  `request_log_answer_check` of 0013 refuses an answer's body without a status, which an envelope's record needs; an
  expand-only migration loosens it for `inbound` records, and the other directions keep the rule.
- **Log events**: `slack_socket_connected` (info: the Connection, `num_connections`, the connections the Leader holds,
  and `error` when the count could not be stored), `slack_socket_shared` (warn: the Connection, the connections counted
  beyond the Leader's — another program is connected to the same Slack app and takes some of its presses) and
  `slack_socket_failed` (warn: the Connection, the reason — `dial`, `hello_timeout`, `link_disabled`,
  `too_many_websockets`, `closed` — the back-off, and `error`, what failed, masked, empty for `hello_timeout`,
  `link_disabled` and `too_many_websockets`). The reason `dial` covers a failed `apps.connections.open`, an address it
  answered that is refused, a failed handshake and a client of the Connection that could not be built.
- **No Leader** (`web/src/locales`, `app-shell.test.tsx`, `deploy/helm/muster/files/rules.yaml`, its rule test and
  `docs/operations/runbooks/MusterNoLeader.md`): the texts of reference.md name Slack's Socket Mode among the paused
  tasks; `docs/operations/high-availability.md` names it in its table of Leader tasks and in the Leader's handover.
- **Fake Slack server** (`internal/fakes/fakeslack/socket.go`, `presses.go`): `/link/` accepts a WebSocket for a
  ticket that `apps.connections.open` issued, once, sends `hello` with `num_connections` — the app's open sockets plus
  `extra_connections` from `/_fake/config` — and `/_fake/socket` scripts `disconnect` with a reason, a missing `hello`
  and `too_many_websockets`, ends the newest socket without `disconnect` (`close`) and sends an envelope of a given
  type (`send`); its `GET` shows each app's open and opened sockets, its `hello`s and whether the last planned refresh
  overlapped, and every envelope with its confirmation; `/_fake/press` sends the payload as an `interactive` envelope
  through a socket of the app when it has one and no `request_url`, or as `transport` (`http` or `socket`) names, and
  records the confirmation and the time between them.
- **Architecture** (`design/architecture.md`): the Socket Mode flow and the Leader task; the clock table of S-020
  gains the socket's waits (real clock) and `slack_socket_hello_at` (business clock).
- **Defaults**: `slack.hello_timeout`, `slack.socket_backoff`, `connection.slack.event_mode`.

## Steps

1. Add the WebSocket dial to the outbound package and the lint. Check: `websocket_test.go` through the fake HTTP and
   SOCKS5 proxies, `make lint-arch` with the new fixture.
2. Write the socket loop with the fake's socket and the Leader task. Check: `socket_test.go`, `leader_test.go`.
3. Record the `hello`s, the warning in the check and `muster doctor`, and `apps.connections.open` in the check. Check:
   `check_test.go`, `doctor_test.go`, `slack_test.go`.
4. Record envelopes and mask the `ticket`; the no-Leader texts. Check: `mask_test.go`, `inbound_test.go`,
   `make rules-test`, `make e2e-go` with `E2E_REPLICAS=2`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-100: the session, FAM, NOTIFY, ADV, AG, FIRE, LINK, PRESS and the Route "sl" with the Destination "alerts" (D)
# of the Connection "sl" (C), switched back to `socket` mode; Alice linked as U0FAKEALICE
API=localhost:8080/api/v1; FS=127.0.0.1:18097/_fake
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s -X PUT $FS/config -d '{"request_url":{"b":null}}' > /dev/null
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"socket",
  "proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null; sleep 2

# C-24.FR-9, AC-10: the Leader's socket, a press through it, confirmed first
grep '"event":"slack_socket_connected"' dev.log | tail -1 | jq -c '{num_connections}'      # {"num_connections":1}
FIRE m1 DiskFull s1; G=$(AG 'pod%3D%22s1%22'); TS=$(curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -r '[.[] | select(.text | contains("DiskFull"))][-1].ts')
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"ack\",\"user\":\"U0FAKEALICE\"}" > /dev/null; sleep 1
curl -s $FS/socket | jq -c '.envelopes[-1] | {confirmed, ms_lt_100: (.confirm_ms < 100)}'    # {"confirmed":true,"ms_lt_100":true}
curl -s -b jar $API/alert-groups/$G | jq -c '{status, owner: .owner.login}'                 # {"status":"acknowledged","owner":"alice"}

# the planned refresh: the next socket before the old one closes
curl -s -X POST $FS/socket -d '{"app":"b","disconnect":"refresh_requested"}' > /dev/null; sleep 2
curl -s $FS/socket | jq -c '.apps.b | {open, overlapped}'                             # {"open":1,"overlapped":true}

# C-24.FR-2, AC-10: another program on the same app
curl -s -X PUT $FS/config -d '{"extra_connections":{"b":1}}' > /dev/null
curl -s -X POST $FS/socket -d '{"app":"b","disconnect":"refresh_requested"}' > /dev/null; sleep 2
grep -c '"event":"slack_socket_shared"' dev.log                                     # 1 or more
curl -s -b jar $API/connections/$C | jq -c '.socket.other_connections'                # 1
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '{ok, warnings}'        # {"ok":true,"warnings":["another_client_connected"]}
./bin/muster dev doctor | grep -c '^WARN.*sl.*another'                               # 1
curl -s -X PUT $FS/config -d '{"extra_connections":{"b":0}}' > /dev/null
curl -s -X POST $FS/socket -d '{"app":"b","disconnect":"refresh_requested"}' > /dev/null; sleep 2
curl -s -b jar $API/connections/$C | jq -c '.socket.other_connections'                # 0

# back-off: link_disabled, then a missing hello
curl -s -X POST $FS/socket -d '{"app":"b","disconnect":"link_disabled"}' > /dev/null; sleep 2
grep '"event":"slack_socket_failed"' dev.log | tail -1 | jq -c '{reason}'               # {"reason":"link_disabled"}
curl -s -b jar $API/destinations/$D | jq -r .health.state                             # healthy

# C-24.FR-14, AC-17: envelopes in the Request log; no ticket anywhere
curl -s -b jar "$API/request-log?connection=$C&operation=interactive&limit=1" | jq -c '.items[0] | {direction, status, u: (.url | contains("?"))}'
# {"direction":"inbound","status":null,"u":false}
curl -s -b jar "$API/request-log?connection=$C&operation=apps.connections.open&limit=1" | jq -r '.items[0].id' | xargs -I{} curl -s -b jar "$API/request-log/{}" | grep -c 'ticket=[^[]'   # 0
grep -c 'ticket=[^[]\|xapp-muster-dev-slack-token-b' dev.log                       # 0

# C-24.AC-3: the socket through the fake HTTP proxy, by CONNECT
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"socket",
  "proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null; sleep 3
curl -s 127.0.0.1:18091/_fake/requests | jq -r '[.[].target] | unique | join(",")'   # 127.0.0.1:18097: every connection, the socket's included

# C-24.FR-17: the banner without a Leader names Slack (the web test), and the chart rule
make rules-test > /dev/null && echo ok                                              # ok
```

Live, on the maintainer's test workspace, with `operator_attention` — Socket Mode on in the app (as the spike left it),
and the Connection "test workspace" (L) back in `socket` mode with its app-level token, Alice linked to the operator's
Slack account as in S-100:

```sh
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/connections/$L | jq -r .etag)" $API/connections/$L -d "$(jq -n \
  --rawfile a ~/.config/muster-spike/slack-app-token '{type:"slack",name:"test workspace",event_mode:"socket",
  app_token:($a|rtrimstr("\n")),proxy:{enabled:false},limiter:{limit:50,per_seconds:60}}')" > /dev/null; sleep 3
grep '"event":"slack_socket_connected"' dev.log | grep -c "$L"                        # 1
curl -s "${H[@]}" -X POST $API/connections/$L/checks | jq -c '{ok, warnings}'         # {"ok":true,"warnings":[]}
FIRE live3 LiveSocket t1; LG=$(AG 'pod%3D%22t1%22')
# the operator presses Ack on the message in #alerts
until curl -s -b jar $API/alert-groups/$LG | jq -e '.status == "acknowledged"' > /dev/null; do sleep 2; done
curl -s -b jar "$API/alert-groups/$LG/timeline" | jq -r '[.items[] | select(.kind == "acknowledged")][0] | "\(.transport) \(.actor.login)"'   # slack alice
```

At the end the Alert Group is resolved, its messages are deleted with the bot token, the link row is removed and the
Connection is deleted, which closes the socket; the app stays in Socket Mode.

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): receive slack button presses over socket mode on the leader`.
- `operator_attention` is true: only a person's press shows the real envelope; the operator also confirms that the
  spike's own test clients are closed, so that `num_connections` counts Muster's alone.
- The socket is held by the Leader only (D385); the maintainer weighed a socket on every replica, which loses no press
  during a Leader change, against Slack's limit of ten connections per app and a clear warning about a second
  installation (ADR-0007). An installation that cannot lose a press uses HTTP.
- CONNECT is used for a `ws` target too, so that the fake server and a real `wss` address take the same path through
  a proxy, as F-103 saw for Slack.
- `slack_socket_other_connections` counts at each `hello`, which Slack sends at least every few hours (F-100); the
  warning may lag the second installation by that much, and the check says when the count was taken. Right after a
  Leader crashed, Slack may still count the old Leader's socket until it notices that it is gone (a Pending fact), so a
  warning then is cleared by a later `hello`; the live check with the operator looks for it after a stopped replica.
- The Request log page hides the answer of a record without a status and calls it "No answer"; showing an envelope's
  confirmation there is left to S-103, which owns the Slack pages, so that this story stays a backend story. Here the
  record is checked through the API.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-2 | partial | `apps.connections.open` and the warning; the page S-103 |
| C-24.FR-9 | partial | Socket Mode; HTTP is S-100 |
| C-24.FR-14 | partial | envelopes and the `ticket`; the rest S-097, S-099, S-100 |
| C-24.FR-17 | partial | the socket's log events and the no-Leader texts |
| C-24.AC-3 | partial | the socket through the proxy; the Web API requests S-097, S-098 |
| C-24.AC-10 | full | |
| C-24.AC-17 | partial | envelopes; the callback S-100, the page showing an envelope's confirmation S-103 |
| C-24.AC-19 | partial | Socket Mode on the test workspace; HTTP S-100 |
| C-02.FR-10 | partial | the Leader task `slack_socket_mode` |
| C-02.FR-20 | partial | WebSocket connections through the outbound package |
| C-23.FR-1 | partial | Socket Mode envelopes |
| C-23.FR-3 | partial | the `ticket` |
