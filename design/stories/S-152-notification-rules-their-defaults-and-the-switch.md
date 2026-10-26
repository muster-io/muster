---
id: S-152
title: Notification rules, the Organization's defaults, the four states and the switch that adds a call (BE)
capability: C-31
kind: be
layer: L2
depends_on: [S-167]
covers: [C-31.FR-1, C-31.FR-2, C-31.FR-4, C-31.FR-5, C-31.AC-3, C-31.AC-4, C-31.AC-6, C-20.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0039_notification_rules.up.sql
  - internal/db/migrations/0039_notification_rules.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/notificationrules/rules.go
  - internal/notificationrules/rules_test.go
  - internal/notificationrules/states.go
  - internal/notificationrules/states_test.go
  - internal/notificationrules/query.sql
  - internal/notificationrules/rules_integration_test.go
  - internal/api/notificationrules.go
  - internal/api/notificationrules_test.go
  - internal/api/server.go
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/organization_test.go
  - internal/users/admin.go
  - internal/users/admin_integration_test.go
  - internal/runtime/runtime.go
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - test/e2e/notification_rules_test.go
  - docs/concepts/notification-rules.md
  - mkdocs.yml
acceptance:
  - "[C-31.FR-1] A rule is an ordered list of actions — `mention`, `call` (with `gateway_id`, required when more than one Notification gateway exists) and `wait` (with `minutes`) — with at least one `mention` or `call`; more than 10 actions answers `422` `out_of_range` at the rule, a wait outside 1 to 60 minutes `422` `out_of_range` at its `minutes` (`notification_rule.limits`), and a rule without an action that Pages `422` `required` (`rules_test.go`)."
  - "[C-31.FR-2, C-20.FR-1] The Organization reads `notification_rules` — normal: a Mention; important: a Mention, a wait of 5 minutes, a Mention — and `updateOrganization` changes them under the same validation, recorded in the diff of `organization.updated`; a User without rules of their own reads the state `organization` with the Organization's current rules (`organization_test.go`, `rules_test.go`)."
  - "[C-31.FR-2, C-31.FR-5, C-31.AC-6] With the default rules, the state `call` makes the important rule exactly a call through the gateway, a Mention, a wait of 5 minutes and a Mention — the call and the first Mention at the same moment — and leaves the normal rule a Mention; `no_call` keeps the Organization's rules without any call; `call` on an Organization's important rule that already calls through that gateway adds no second call; with two gateways at which the User has confirmed contacts, `call` without `gateway_id` answers `422` `required` at `/gateway_id` (`states_test.go`)."
  - "[C-31.FR-2, C-31.AC-4] A User in the state `organization` follows a change of `organization.notification_rules` at once; the rules of a User in `call`, `no_call` or `custom` stay as they were; `resetMyNotificationRules` returns a User to `organization`, which then follows the Organization again (`rules_integration_test.go`)."
  - "[C-31.FR-4, C-31.AC-3] `me/notification-rules` takes only the web session: a Personal access token gets `403` `session_required`; saving a call action, or the state `call`, while no Notification gateway exists, or for a gateway at which the User has no confirmed contact, answers `422` `gateway_required` or `contact_required` at the action's or `/gateway_id`; `updateUserNotificationRules` (`users:write`) lets an Admin set another User's state or custom sequences, a Responder gets `403`, and every change is an Audit log entry `notification_rules.updated` with a before/after diff, with `If-Match` (`412` when stale) (`notificationrules_test.go`)."
  - "[C-31.FR-5, C-31.AC-6] After an Admin sets a sequence of three Mentions through `updateUserNotificationRules`, the User's rules read the state `custom` with that sequence; `resetMyNotificationRules` brings back `organization` (`notification_rules_test.go`)."
  - "[C-31.FR-1] An action whose gateway was deleted, or at which the User no longer has a confirmed contact, stays in the rule and reads `available` false with `unavailable_reason` `gateway_deleted` or `no_confirmed_contact` (`rules_test.go`)."
  - "[C-31.FR-5] The documentation has the page Concepts → Notification rules with \"Normal and important\", \"The switch\" and \"Custom rules\" in the navigation; deleting a User deletes their rules; P-85 is confirmed in defaults.md and L2.md §5.3; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: null
---

# S-152. Notification rules, the Organization's defaults, the four states and the switch that adds a call (BE)

## Scope

**IN**

- Two Notification rules per User, normal and important, as ordered actions within `notification_rule.limits`
  (C-31.FR-1).
- The Organization's default rules (`organization.notification_rules`) and the four states of a User's rules — as in the
  Organization, "Call", "Don't call", custom — with "Reset to the Organization's rules" (C-31.FR-2, FR-5; C-20.FR-1
  revised).
- The API for a person's own rules from the web session and for an Admin on any User, with the checks of a call and the
  Audit log (C-31.FR-4).
- The first sections of the documentation page on Notification rules.

**OUT**

- Running the rules when a step Pages a person — Mentions in the Threads of the chain (S-155) and calls through the
  queue of S-148 (S-157) — and skipping a call action at that time. The rules are stored and read here; nothing Pages
  anyone until S-155.
- The profile, the User page and the Organization page: S-153. The rest of the documentation: S-161.
- An editor of whole sequences in the profile: specified later in L2 (L2.md §1.5); the API takes sequences from here.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; a new tag "Notification rules"):
  - Operations: `getMyNotificationRules` (`GET /me/notification-rules`), `updateMyNotificationRules` (`PUT` with
    `If-Match`), `resetMyNotificationRules` (`POST /me/notification-rules/reset`) — `x-permission: authenticated`, the
    web session only (C-03.FR-27); `getUserNotificationRules` (`GET /users/{user_id}/notification-rules`,
    `users:read`) and `updateUserNotificationRules` (`PUT`, `users:write`, `If-Match`), session or token.
  - Schemas: `NotificationRuleAction` {`kind` (`mention`, `call`, `wait`), `gateway_id` (with `call`), `minutes` (with
    `wait`)}; in reads also `gateway` (`EntityRef`), `available`, `unavailable_reason` (`gateway_deleted`,
    `no_confirmed_contact`, or null); `NotificationRuleSet` {`normal`, `important`} (`NotificationRuleAction`[]);
    `NotificationRulesState` (`organization`, `call`, `no_call`, `custom`); `NotificationRules` {`state`, `gateway`
    (`EntityRef` or null, with `call`), `normal`, `important` — always the sequences in effect —, `etag`};
    `NotificationRulesUpdate` {`state`, `gateway_id` (with `call`), `normal` and `important` (both, with `custom`
    only)}.
  - `OrganizationBase` gains the optional `notification_rules` (`NotificationRuleSet`); `Organization` requires it.
  - Problem codes: `gateway_required` and `contact_required` (`validation-failed`, in `errors[].code`); `out_of_range`
    and `required` at the action or the rule.
- **Schema** (migration `0039_notification_rules`, expand only; `schema.md` §4.2, §4.3 and a new §4.18 Paging):
  - `organizations.notification_rules jsonb NOT NULL DEFAULT` the defaults of C-31.FR-2, with a CHECK that it is an
    object with the arrays `normal` and `important`.
  - `notification_rules`: `org_id`, `user_id` (FK, `ON DELETE CASCADE`, primary key), `state` (`CHECK IN ('call',
    'no_call', 'custom')` — `organization` is the absence of a row), `gateway_connection_id` (FK `connections`, set
    exactly with `call`), `normal jsonb NOT NULL`, `important jsonb NOT NULL`, `version`, `updated_at`; index
    `(org_id, gateway_connection_id)`.
  - The down migration drops both.
- **Notification rules** (`internal/notificationrules`, a new flat domain package, ADR-0016): `rules.go` — validation
  within `notification_rule.limits`, the reads with availability (`gateways.ConfirmedContact`, S-167), the Audit log
  `notification_rules.updated` with a before/after diff, `Effective(user)` for the runs of S-155; `states.go` — the
  sequences of `call` (a call through the chosen gateway inserted at the start of the important rule, together with its
  first action, unless that rule already calls through that gateway), `no_call` (every call removed; a rule left without
  an action that Pages gets a Mention in place of its first call, Open question 1) and `organization`, computed when the
  state is chosen and stored, except `organization`, which is always read from the Organization.
- **Organization** (`internal/organization`): `notification_rules` in the read, update, validation and diff; the
  validation of a call there checks only that its gateway exists.
- **Users** (`internal/users/admin.go`): `Delete` deletes the User's rules in its transaction.
- **Settings**: `notification_rule.limits` (P-85, confirmed here), `organization.notification_rules`.
- **Log events, metrics**: none new; the Audit log records every change.
- **Documentation**: the new page `docs/concepts/notification-rules.md` — "Normal and important", "The switch" and
  "Custom rules" — under Concepts in `mkdocs.yml`; S-161 adds how the rules combine with the steps of a chain.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; an existing Organization
   reads the defaults.
2. Write the validation, the states and the reads. Check: `rules_test.go`, `states_test.go` cover every state from the
   default and from an Organization whose important rule already calls.
3. Add the operations and the Organization field; delete rules with their User. Check: `make generate-check`;
   `notificationrules_test.go`, `organization_test.go`, `admin_integration_test.go`.
4. Confirm P-85, write the documentation and `test/e2e/notification_rules_test.go`. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-167: A, P (petr's token), S (petr's web session) with petr's contact confirmed at the demo gateway GW.
API=localhost:8080/api/v1
curl -s "${S[@]}" $API/me/notification-rules | jq -c '{state, n: [.normal[].kind], i: [.important[] | .kind]}'
# {"state":"organization","n":["mention"],"i":["mention","wait","mention"]}
E=$(curl -s "${S[@]}" $API/me/notification-rules | jq -r .etag)
curl -s "${S[@]}" -X PUT -H "If-Match: $E" $API/me/notification-rules -d "{\"state\":\"call\",\"gateway_id\":\"$GW\"}" |
  jq -c '{state, i: [.important[] | .kind]}'
# {"state":"call","i":["call","mention","wait","mention"]}
curl -s -o /dev/null -w '%{http_code}\n' "${P[@]}" $API/me/notification-rules                      # 403

# the Organization changes its important rule: petr ("call") stays, ivan ("organization") follows
O=$(curl -s "${A[@]}" $API/organization)
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c 'del(.etag, .id, .working_hours) |
  .notification_rules.important = [{"kind":"mention"},{"kind":"wait","minutes":10},{"kind":"mention"}]' <<<"$O")" > /dev/null
IVAN=$(curl -s "${A[@]}" "$API/users?q=ivan" | jq -r '.items[0].id'); PETR=$(curl -s "${A[@]}" "$API/users?q=petr" | jq -r '.items[0].id')
curl -s "${A[@]}" $API/users/$IVAN/notification-rules | jq -c '[.important[] | .minutes // .kind]'   # ["mention",10,"mention"]
curl -s "${A[@]}" $API/users/$PETR/notification-rules | jq -c '[.important[] | .minutes // .kind]'   # ["call","mention",5,"mention"]

# an Admin sets three Mentions; the Audit log shows the diff; the limit holds
E=$(curl -s "${A[@]}" $API/users/$PETR/notification-rules | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/users/$PETR/notification-rules -d '{"state":"custom",
  "normal":[{"kind":"mention"},{"kind":"mention"},{"kind":"mention"}],"important":[{"kind":"mention"}]}' | jq -r .state   # custom
curl -s "${A[@]}" "$API/audit-log?action=notification_rules.updated" | jq -r '.items[0].actor.login'      # admin
E=$(curl -s "${A[@]}" $API/users/$PETR/notification-rules | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/users/$PETR/notification-rules -d '{"state":"custom",
  "normal":[{"kind":"mention"},{"kind":"wait","minutes":61}],"important":[{"kind":"mention"}]}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/normal/1/minutes"]
curl -s "${S[@]}" -X POST $API/me/notification-rules/reset | jq -r .state                             # organization
```

`test/e2e/notification_rules_test.go` repeats these checks and adds `call` without a contact (`contact_required`),
`call` while no gateway exists (`gateway_required`), two gateways with and without `gateway_id`, `no_call` on an
Organization whose important rule calls, `call` on one that already calls through that gateway, a contact removed after
`call` was chosen (`available` false), and a deleted User.

## Open questions

1. **"Don't call" on an Organization's rule made only of calls.** C-31.FR-2 says "Don't call" keeps the Organization's
   rules without any call, and C-31.FR-1 requires an action that Pages, so a rule such as "call, wait 5, call" would be
   left with none. Options: (a) a Mention takes the place of its first call, keeping the waits; (b) `no_call` is refused
   with `422` while such a rule exists. Recommended: (a), so that every person can opt out of calls; the contract
   follows it.

## Notes

- Suggested commit: `feat(notificationrules): add personal notification rules with organization defaults`.
- The maintainer confirms a provisional value with this story (operator attention): P-85, at most 10 actions and waits
  of 1 to 60 minutes in a Notification rule.
- "Call" and "Don't call" keep the Organization's rules as they were when the User chose them (journal D569), so a
  later change of the Organization's rules reaches only the Users in `organization`.
- Migration `0039` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-31.FR-1 | full | |
| C-31.FR-2 | full | |
| C-31.FR-4 | partial | the API, its checks and the Audit log; the profile and the User page are S-153 |
| C-31.FR-5 | partial | the states in the API; the switch on the profile is S-153 |
| C-31.AC-3 | full | |
| C-31.AC-4 | full | |
| C-31.AC-6 | partial | the sequences of the states and the custom rules; the profile is S-153, the call at once S-157 |
| C-20.FR-1 | partial | revised for L2: the default Notification rules among the Organization's settings; the page is S-153 |
