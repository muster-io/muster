---
id: S-167
title: Contacts at Notification gateways confirmed by a code, erased with their User and masked in the Request log (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-145, S-164]
covers: [C-30.FR-3, C-30.FR-9, C-30.FR-10, C-30.AC-2, C-27.FR-8, C-03.FR-13, C-18.FR-6, C-23.FR-3]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0036_gateway_contacts.up.sql
  - internal/db/migrations/0036_gateway_contacts.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/gateways/gateways.go
  - internal/gateways/contacts.go
  - internal/gateways/contacts_test.go
  - internal/gateways/contacts_integration_test.go
  - internal/gateways/query.sql
  - internal/api/gatewaycontacts.go
  - internal/api/gatewaycontacts_test.go
  - internal/api/server.go
  - internal/runtime/runtime.go
  - internal/users/admin.go
  - internal/users/admin_integration_test.go
  - internal/audit/list.go
  - internal/audit/audit_test.go
  - internal/requestlog/mask.go
  - internal/requestlog/mask_test.go
  - internal/archlint/secretleak.go
  - internal/schedules/reach.go
  - internal/schedules/reach_test.go
  - internal/schedules/oncallnow.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/logging/events.go
  - test/e2e/gateway_contacts_test.go
  - docs/messengers/notification-gateways.md
  - mkdocs.yml
acceptance:
  - "[C-30.FR-3, C-30.AC-2] `createMyGatewayContact` with a Notification gateway's `connection_id` and a contact creates an unconfirmed contact of the caller and sends a code of `gateway.contact_code` through that gateway with `sendNotification` on the interactive path, without action data, in the language of the caller's profile; `confirmMyGatewayContact` with the code the fake gateway received confirms it; a wrong code answers `422` `contact_code_invalid` at `/code` with `attempts_remaining`, and after the last attempt, or once the code expired, the code is void (`contacts_test.go`, `gateway_contacts_test.go`)."
  - "[C-30.FR-3] A code is tied to the web session that asked for it (ADR-0013): `confirmMyGatewayContact` with the right code from another session of the same person answers `422` `contact_code_invalid` and counts as an attempt; a session that ends voids its codes (`contacts_test.go`)."
  - "[C-30.FR-3] Code requests — creating a contact and `sendMyGatewayContactCode` — are limited by `gateway.contact_code_requests`: the sixth within an hour of one User, or the fourth within an hour for one contact, answers `429` `contact_code_requests_exceeded` with `Retry-After`; a `connection_id` that names no Connection in the role `gateway` answers `422` `unknown_id`; a contact that is empty or longer than 200 characters answers `422` `invalid_format` at `/contact` (`contacts_test.go`)."
  - "[C-30.FR-3, C-18.FR-6, C-30.AC-2] The operations that change `me/gateway-contacts` take only the web session: a Personal access token gets `403` `session_required`; `createUserGatewayContact` — an Admin's attempt to set a contact for another User — always answers `403` `forbidden` with the detail that only the person sets their contact, and changes nothing; `listUserGatewayContacts` (`users:read`) lists a User's contacts with their state and `deleteUserGatewayContact` (`users:write`) removes one; adding, confirming and removing are Audit log entries `gateway_contact.created`, `gateway_contact.confirmed` and `gateway_contact.deleted` (`gatewaycontacts_test.go`)."
  - "[C-30.FR-3] A new contact for a gateway at which the User has a confirmed one replaces it only when it is confirmed; until then the confirmed contact stays the one called, and both are listed (`contacts_integration_test.go`)."
  - "[C-30.FR-10, C-23.FR-3, C-30.AC-2] The Request log shows the `contact` of every request to `sendNotification` as `[redacted]`, and the `text` of one without `action` — a confirmation code — as `[redacted]` too; the secret-leak probe `gateway_contacts` finds neither the contact nor the code in a log line or a returned error (`mask_test.go`, `secretleak.go`)."
  - "[C-03.FR-13, C-30.AC-2] Deleting a User erases their contacts in the same transaction; the Audit log keeps the entries and shows their `/contact` as `[erased]` when it is read (`admin_integration_test.go`, `audit_test.go`)."
  - "[C-27.FR-8] In `getScheduleOnCall` and `getOnCallNow`, each warning `unreachable` carries `gateway_reaches`: true when the person has a confirmed contact at a Notification gateway, else false (`reach_test.go`)."
  - "[C-30.FR-3, C-30.FR-9] The documentation has the page Messengers → Notification gateways with \"Connect a Notification gateway\" and \"Contact for calls and SMS\" — what a gateway is, that Muster ships none, the code and its limits, that an Admin sees and removes contacts but never sets one — in the navigation; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-167. Contacts at Notification gateways confirmed by a code, erased with their User and masked in the Request log (BE)

## Scope

**IN**

- One contact per person and Notification gateway, added and confirmed from the person's own web session with a code
  that the gateway delivers, within the limits of `gateway.contact_code` and `gateway.contact_code_requests` (C-30.FR-3,
  ADR-0013 as amended).
- An Admin's view and removal of a User's contacts, never setting one (C-18.FR-6 revised).
- Contacts erased when their User is deleted and shown as `[erased]` in earlier Audit log diffs (C-03.FR-13 revised).
- Contacts and confirmation codes masked in the Request log (C-23.FR-3 revised), and a probe of the secret-leak lint.
- Whether a call still reaches an unreachable On-call, on the warnings of Schedules (C-27.FR-8).
- The first form of the documentation page on Notification gateways.

**OUT**

- The protocol, the role and the fake gateway: S-145. Calls: S-148. Broken gateways: S-149.
- The profile's contact block, a User's contacts on their page and "A call through the Notification gateway works." on
  the Schedule pages: S-150. Writing a gateway bridge: S-151.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; a new tag "Notification gateways"):
  - Operations: `listMyGatewayContacts` (`GET /me/gateway-contacts`), `createMyGatewayContact` (`POST
    /me/gateway-contacts`, `202` with the contact; the code is sent), `sendMyGatewayContactCode` (`POST
    /me/gateway-contacts/{gateway_contact_id}/code`, `202`), `confirmMyGatewayContact` (`POST
    /me/gateway-contacts/{gateway_contact_id}/confirmation`, `200`), `deleteMyGatewayContact` (`DELETE
    /me/gateway-contacts/{gateway_contact_id}`) — `x-permission: authenticated`, the session with its CSRF token only
    (C-03.FR-27), as the Account link operations; `listUserGatewayContacts` (`GET /users/{user_id}/gateway-contacts`,
    `users:read`) and `deleteUserGatewayContact` (`DELETE /users/{user_id}/gateway-contacts/{gateway_contact_id}`,
    `users:write`), session or token; `createUserGatewayContact` (`POST /users/{user_id}/gateway-contacts`,
    `users:write`), declared only so that the API says it: it always answers `403` `forbidden` with the detail "Only the
    person sets their contact, from their own web session." (C-30.AC-2, ADR-0013). The code operations run on the
    interactive path: `503` with `Retry-After` when no limiter token is free within `delivery.interactive_budget`; a
    refusal of the gateway answers `502` `gateway_refused` with its class.
  - Schemas: `GatewayContactInput` {`connection_id`, `contact`}; `GatewayContact` {`id`, `connection` (`EntityRef`),
    `contact`, `confirmed` (boolean), `confirmed_at`, `replaces` (the `public_id` of the confirmed contact it will
    replace, or null), `created_at`}; `GatewayContactList`; `GatewayContactConfirm` {`code`}; the path parameter
    `GatewayContactId`.
  - Problem codes: `contact_code_invalid` (`validation-failed`, with `attempts_remaining`),
    `contact_code_requests_exceeded` (`429`), `gateway_refused` (`502`), `unknown_id` at `/connection_id`,
    `invalid_format` at `/contact`.
  - `ScheduleWarning` and `OnCallReach` gain the optional `gateway_reaches` (boolean, not in `required`, present on
    every `unreachable` warning), so that the web tests of S-135 and S-136 that build these reads by hand need no edit.
  - `PublicId` and `api/README.md` gain the prefix `GC` (gateway contact).
- **Schema** (migration `0036_gateway_contacts`, expand only; `schema.md` §2 and a new §4.17 Notification gateways):
  - `gateway_contacts`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^GC[0-9A-HJKMNP-TV-Z]{12}$')`), `user_id`
    (FK `users`), `connection_id` (FK `connections`), `contact text NOT NULL` (1–200 characters), `confirmed_at`,
    `created_at`; unique partial index `(user_id, connection_id) WHERE confirmed_at IS NOT NULL` — one confirmed contact
    per person and gateway — and index `(org_id, connection_id)`.
  - `gateway_contact_codes`: `id`, `org_id`, `contact_id` (FK, `ON DELETE CASCADE`), `code_hash bytea NOT NULL`,
    `session_id` (FK `sessions`, `ON DELETE CASCADE` — the web session that asked for the code), `attempts`,
    `expires_at`, `used_at`, `created_at`; index `(org_id, contact_id, created_at)` and a partial index of
    the requests per User through `contact_id` for the hourly limits. Pruned with the expired Account link requests.
  - The down migration drops both.
- **Gateways** (`internal/gateways`, a new flat domain package, ADR-0016): `gateways.go` — the Notification gateways of
  the Organization (Bridge Connections whose stored `info` has `gateway`, not deleted) and `ConfirmedContact(user,
  gateway)`; `contacts.go` — create, send the code, confirm, list and delete, with the Audit log
  (`gateway_contact.created`, `gateway_contact.confirmed`, `gateway_contact.deleted`, each with `/contact` in its diff),
  the code of `gateway.contact_code` hashed at rest, compared in constant time and only in the session that asked for it
  (ADR-0013), its expiry and the hourly limits of `gateway.contact_code_requests` per User and per contact text on the
  business clock, as the Account link codes and their limits are (S-020), and the replacement on confirmation in one
  transaction; the code goes out through `delivery`'s interactive `Notify` (S-145) with the key of the code request, the
  text "Your Muster code: {code}. It is valid for {minutes} minutes. Do not share it.", {minutes} from
  `gateway.contact_code` (P-86), in the person's language (`internal/messages/texts`), and no action.
- **Users** (`internal/users/admin.go`): `Delete` erases the User's contacts and codes in its transaction (C-03.FR-13).
- **Audit** (`internal/audit/list.go`): the entries about a gateway contact of a deleted User show `/contact` as
  `[erased]` when read, as the erased fields of a User are.
- **Request log** (`internal/requestlog/mask.go`): in requests to `/v1/notifications`, the member `contact`, and `text`
  when the request has no `action`, are `[redacted]`, in the record and its answer; a probe `gateway_contacts` joins the
  secret-leak lint (`internal/archlint/secretleak.go`).
- **Schedules** (`internal/schedules/reach.go`, `oncallnow.go`): `gateway_reaches` on each `unreachable` warning and
  mark — a confirmed contact at a Notification gateway; whether the gateway is Broken is not considered (S-149 shows
  the gateway's state).
- **Log events** (`internal/logging/events.go`): `gateway_contact_code_sent` (info; the User, the Connection, the
  outcome; never the contact or the code), `gateway_contact_code_rejected` (warn; the reason), C-30.
- **Settings**: `gateway.contact_code` and `gateway.contact_code_requests` (both P-86, which stays provisional until
  the test environment confirms it), `delivery.interactive_budget`.
- **Documentation**: the new page `docs/messengers/notification-gateways.md` — "Connect a Notification gateway" (a
  Bridge Connection for a bridge in the role `gateway`; Muster ships none) and "Contact for calls and SMS" (adding and
  confirming a contact, the limits, Admins) — under Messengers in `mkdocs.yml`; S-151 completes it.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write `internal/gateways` with the contacts, the codes, their limits and the Audit log; add the spec. Check: `make
   generate-check`; `contacts_test.go` with a manual clock covers expiry, attempts and both limits;
   `contacts_integration_test.go` covers the replacement.
3. Erase contacts with their User and mask them in the Audit log and the Request log; add the probe. Check:
   `admin_integration_test.go`, `audit_test.go`, `mask_test.go`; `make lint-arch`.
4. Add `gateway_reaches`. Check: `reach_test.go`.
5. Write `test/e2e/gateway_contacts_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# A: the Admin's token; as in S-131 the Responder petr with his token P; S: petr's web session cookie jar and CSRF
# header (a sign-in with curl, as in S-010); GW: the demo Connection "Example Gateway" (S-145).
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake; CLK=localhost:8082/_dev/clock
C=$(curl -s "${S[@]}" $API/me/gateway-contacts -d "{\"connection_id\":\"$GW\",\"contact\":\"+10000000002\"}" | jq -r .id)
curl -s $FB/notifications | jq -c 'last | {contact, action, code: (.text | test("[0-9]{6}"))}'
# {"contact":"+10000000002","action":null,"code":true}
CODE=$(curl -s $FB/notifications | jq -r 'last.text' | grep -o '[0-9]\{6\}')
curl -s "${S[@]}" $API/me/gateway-contacts/$C/confirmation -d '{"code":"000000"}' | jq -c '{status, code: .errors[0].code, left: .errors[0].attempts_remaining}'
# {"status":422,"code":"contact_code_invalid","left":4}
curl -s "${S[@]}" $API/me/gateway-contacts/$C/confirmation -d "{\"code\":\"$CODE\"}" | jq -c '{confirmed, contact}'
# {"confirmed":true,"contact":"+10000000002"}

# a token cannot; an Admin cannot set; an Admin lists and removes
curl -s -o /dev/null -w '%{http_code}\n' "${P[@]}" -X DELETE $API/me/gateway-contacts/$C          # 403
PETR=$(curl -s "${A[@]}" "$API/users?q=petr" | jq -r '.items[0].id')
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" $API/users/$PETR/gateway-contacts -d '{"contact":"+1"}'   # 403
curl -s "${A[@]}" $API/users/$PETR/gateway-contacts | jq -c '[.items[] | {contact, confirmed}]' # [{"contact":"+10000000002","confirmed":true}]

# the limits: 3 code requests per hour for one contact — the create was the first
for i in 1 2 3; do curl -s -o /dev/null -w '%{http_code} ' "${S[@]}" -X POST $API/me/gateway-contacts/$C/code; done; echo
# 202 202 429

# masked in the Request log
curl -s "${A[@]}" "$API/request-log?connection=$GW&operation=sendNotification&limit=1" |
  jq -r '.items[0].id' | xargs -I{} curl -s "${A[@]}" $API/request-log/{} | jq -r '.request.body' | grep -c '+10000000002'   # 0

# deleted with the User; [erased] in the Audit log
curl -s "${A[@]}" -X DELETE $API/users/$PETR > /dev/null
curl -s "${A[@]}" "$API/audit-log?action=gateway_contact.confirmed" | jq -r '[.items[0].diff[] | select(.pointer == "/contact")][0].after'   # [erased]
psql postgres://muster:muster@127.0.0.1:55432/muster -tAc "SELECT count(*) FROM gateway_contacts"                        # 0
```

`test/e2e/gateway_contacts_test.go` repeats these checks and adds an expired code after an advance of the development
clock, the sixth request of one User across two contacts, a new contact that replaces the confirmed one only once
confirmed, `deleteMyGatewayContact`, and `gateway_reaches` on the warning of an On-call without an Account link who has
a confirmed contact.

## Open questions

1. **A second contact for the same gateway.** Settled by the maintainer: the confirmed contact stays the one called
   until the new one is confirmed, and then the new one replaces it, so that changing a number never leaves a person
   unreachable.
2. **What counts as one contact for the limit of 3 per hour.** Settled by the maintainer: the contact text as typed,
   trimmed — Muster does not check phone numbers (C-30 UI), and a gateway may use any address.

## Notes

- Suggested commit: `feat(gateways): confirm contacts at notification gateways with a code`.
- A contact is personal data, not a Secret (C-30.FR-3): it is stored in plain text, read by its person, Admins and the
  gateway, and masked only where it would be logged. A confirmation code is masked like a Secret.
- P-86 stays provisional; the test environment confirms it, not this story.
- Migration numbers follow the story map: `0036` here, then `0037` to `0044` for S-148, S-149, S-152, S-154, S-169,
  S-155, S-168 and S-157. A story merged out of that order renumbers its migration to the next free number in its own
  pull request, with its tests and `schema.md`.
- The development token and the contacts of the checks are not real numbers; the documentation uses `+10000000001`
  and the like.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-3 | partial | the API, the code and its limits, the erasure and the masking; the profile is S-150 |
| C-30.FR-9 | partial | the documentation that the UI's note links to; the notes themselves are S-150, S-153 and S-158 |
| C-30.FR-10 | partial | contacts masked in the Request log; the metrics are S-148 |
| C-30.AC-2 | full | |
| C-27.FR-8 | partial | whether a gateway call still reaches the person, in the API; the pages are S-150, the chains' Destinations S-155 |
| C-03.FR-13 | partial | revised for L2: contacts erased; out of every Team was S-115 |
| C-18.FR-6 | partial | revised for L2: contacts set only by their person, seen and removed by Admins; the pages are S-150 |
| C-23.FR-3 | full | revised for L2 |
