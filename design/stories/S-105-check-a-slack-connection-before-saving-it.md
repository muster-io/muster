---
id: S-105
title: "Check a Slack Connection before saving it, with the dry probe without a token (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-101]
covers: [C-24.FR-2, C-24.AC-2, C-13.FR-14]
files_touched:
  - api/openapi.yaml
  - internal/connections/draft.go
  - internal/connections/draft_test.go
  - internal/slack/check.go
  - internal/slack/check_test.go
  - internal/api/connections_test.go
  - internal/archlint/secretleak.go
  - test/e2e/connection_drafts_test.go
  - web/src/components/connection-form.tsx
acceptance:
  - "[C-24.FR-2, C-24.AC-2, C-13.FR-14] `checkConnectionDraft` takes a Slack draft — the event mode, the bot token and the app-level token typed in the form, the proxy, and for a saved Connection its id — and runs the steps of the saved check with the typed tokens; it stores nothing — no Connection, no bot, workspace or app — and writes no Audit log entry: after a passing draft check of a new Slack Connection, `listConnections` lists what it listed before (`draft_test.go`, `test/e2e/connection_drafts_test.go`)."
  - "[C-24.FR-2, C-24.AC-2, C-13.FR-14] A draft of a saved Slack Connection with only the proxy changed and no token typed makes only the dry probe `POST https://slack.com/api/auth.test` without `Authorization`, through the draft's proxy, which passes on a `200` JSON answer `ok: false` with `not_authed` and fails with `not_slack` for any other answer and `unexpected_status` for another status; no request carrying a stored token reaches the fake server or the new proxy; with the token typed again the steps run with the typed token through the new proxy (`check_test.go`, the end-to-end test against the fake Slack server and the fake proxy)."
  - "[C-24.FR-2] The records of a draft check are kept in the Request log with the kind `draft_check` and its run, the typed tokens masked (`secretleak.go` probe `slack_connections`, the draft check added)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: 314
---

# S-105. Check a Slack Connection before saving it, with the dry probe without a token (BE)

## Scope

**IN**

- `checkConnectionDraft` for a Slack Connection: the rule of C-13.FR-14 — typed tokens with the draft's proxy, a
  stored token only with the saved proxy, otherwise the dry probe of C-24.FR-2 — and its Request log.

**OUT**

- The form's "Check connection" panel for Slack, which uses this operation (S-103).

## Contracts

- **Spec** (`api/openapi.yaml`): `SlackConnectionDraft` (`type`, `event_mode`, `bot_token`, `app_token`, write-only,
  `proxy`, and `connection_id` for a saved Connection, whose omitted `proxy` is its saved proxy), joined to
  `ConnectionDraft`; the description of `checkConnectionDraft` names Slack; `ConnectionCheckStep.name` describes
  `dry_probe` for Slack, and `NullableConnectionCheckCode` the code `not_slack` that S-097 added.
- **Operation**: `checkConnectionDraft` (`POST /connections/checks`, `connections:write`, interactive) for the type;
  the `422` at `/type` of S-097 for a Slack draft is gone.
- **Draft check** (`internal/connections/draft.go`): a Slack draft's route — the address is Slack's, so only the proxy
  can differ from the saved Connection; a token typed in the draft goes with the draft's proxy; a stored token only
  when the draft's proxy equals the saved one; otherwise only the dry probe. The steps are those of `checkConnection`
  (S-097, S-101) and the kind `draft_check` in the Request log, as S-087 built for the other types.
- **Dry probe** (`internal/slack/check.go`): `auth.test` without `Authorization` through the given proxy, in the
  interactive class; `not_slack` for an answer that is not Slack's `{"ok":false,"error":"not_authed"}` JSON,
  `unexpected_status` for a JSON answer with another status, and the network codes of the other steps.
- **SPA type check** (`web/src/components/connection-form.tsx`): the new member of `ConnectionDraft` gets a `slack`
  branch in the switches the type check names, building no draft until S-103 adds the Slack form.
- **Fake Slack server**: `auth.test` without a token already answers `not_authed` (S-097).
- **Secrets** (lint 5): the probe `slack_connections` gains a draft check whose request fails.

## Steps

1. Add the draft schema and route, and the dry probe. Check: `draft_test.go`, `check_test.go`,
   `go test ./internal/api/...`.
2. Extend the end-to-end test of drafts and the secret probe. Check:
   `go test -tags e2e -run TestConnectionDrafts ./test/e2e/`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; the Connection "sl" (C) as in S-097, in `socket` mode, no proxy
API=localhost:8080/api/v1; FS=127.0.0.1:18095/_fake; FP=127.0.0.1:18091/_fake

# a new Connection: the typed tokens, nothing stored
N0=$(curl -s -b jar $API/connections | jq '.items | length')
curl -s "${H[@]}" $API/connections/checks -d '{"type":"slack","event_mode":"socket","bot_token":"xoxb-muster-dev-slack-token-b",
  "app_token":"xapp-muster-dev-slack-token-b","proxy":{"enabled":false}}' | jq -c '{ok, s: [.steps[].name]}'
# {"ok":true,"s":["token","scopes","app_token"]}
echo $(( $(curl -s -b jar $API/connections | jq '.items | length') - N0 ))        # 0

# a saved Connection with only the proxy changed: the dry probe alone, without a token
curl -s -X DELETE $FS/requests; curl -s -X DELETE $FP/requests
curl -s "${H[@]}" $API/connections/checks -d "{\"type\":\"slack\",\"event_mode\":\"socket\",\"connection_id\":\"$C\",
  \"proxy\":{\"enabled\":true,\"type\":\"http\",\"address\":\"127.0.0.1:18091\"}}" | jq -c '{ok, s: [.steps[] | {name, via}]}'
# {"ok":true,"s":[{"name":"dry_probe","via":"proxy"}]}
curl -s $FS/requests | jq '[.[] | select((.headers.Authorization // []) | length > 0)] | length'   # 0
# the same with the token typed again: the steps run through the new proxy
curl -s "${H[@]}" $API/connections/checks -d "{\"type\":\"slack\",\"event_mode\":\"socket\",\"connection_id\":\"$C\",
  \"bot_token\":\"xoxb-muster-dev-slack-token-b\",\"app_token\":\"xapp-muster-dev-slack-token-b\",
  \"proxy\":{\"enabled\":true,\"type\":\"http\",\"address\":\"127.0.0.1:18091\"}}" | jq -c '[.steps[] | "\(.name):\(.via)"]'
# ["token:proxy","scopes:proxy","app_token:proxy"]
curl -s -b jar "$API/request-log?run=$(curl -s "${H[@]}" $API/connections/checks -d '{"type":"slack","event_mode":"socket",
  "bot_token":"xoxb-muster-dev-slack-token-b","app_token":"xapp-muster-dev-slack-token-b","proxy":{"enabled":false}}' | jq -r .request_log_run)" |
  jq -r '[.items[].check] | unique | join(",")'                                    # draft_check
grep -c 'xoxb-muster-dev-slack-token-b\|xapp-muster-dev-slack-token-b' dev.log      # 0
```

## Open questions

None.

## Notes

- Suggested commit: `feat(connections): check a slack connection before saving it`.
- The check before saving was planned with the Slack pages at first, as S-087 did for the other types; it became a
  backend story of its own so that the frontend story stays one (the review of the Slack contracts). It follows S-101,
  whose `apps.connections.open` joins the step `app_token`, and comes before S-103, whose form uses it.
- Slack's address cannot change, so the only unsaved change that keeps a stored token from going out is the proxy;
  the dry probe proves that Slack's API answers through the new proxy without any token.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-2 | partial | the check before saving and its dry probe; the form is S-103 |
| C-24.AC-2 | partial | the API; "Check connection" in the forms is S-103 |
| C-13.FR-14 | partial | the check before saving of a Slack Connection |
