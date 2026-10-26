---
id: S-150
title: The contact for calls and SMS in the profile, the roles and health of a Notification gateway, and a User's contacts (FE)
capability: C-30
kind: fe
layer: L2
depends_on: [S-136, S-149]
covers: [C-30.FR-1, C-30.FR-3, C-30.FR-8, C-30.FR-9, C-30.AC-1, C-30.AC-6, C-27.FR-8, C-18.FR-6]
files_touched:
  - web/src/routes/profile.tsx
  - web/src/components/gateway-contact-section.tsx
  - web/src/components/gateway-contact-section.test.tsx
  - web/src/components/gateway-unavailable.tsx
  - web/src/components/gateway-unavailable.test.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/components/user-gateway-contacts.tsx
  - web/src/components/user-gateway-contacts.test.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/connections.index.tsx
  - web/src/components/bridge-connection-fields.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/components/gateway-health.tsx
  - web/src/components/gateway-health.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/connection-check.test.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/schedule-warnings.tsx
  - web/src/components/schedule-warnings.test.tsx
  - web/src/components/on-call-now.tsx
  - web/src/components/on-call-now.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/profile.spec.ts
  - web/e2e/bridge-connection.spec.ts
  - web/e2e/users.spec.ts
  - web/e2e/schedules.spec.ts
  - web/e2e/destinations.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-30.FR-3, C-30.FR-9, C-30.AC-6] In a real browser, without a Notification gateway the profile's \"Contact for calls and SMS\" shows its hint and \"Unavailable: a Notification gateway is needed (a bridge for calls and SMS)\" with \"How to connect a Notification gateway\" and, for Admins only, \"Connect a bridge\", and no phone field; with the fake gateway it shows the phone field and \"Send code\", then \"Code sent through the Notification gateway to {contact}.\", the code field, \"Confirm\" and \"Another number\"; the code the fake gateway received confirms the contact, shown with \"calls and SMS\", \"confirmed\" and \"Remove\", and the note about acknowledging from a call; in English and in Russian (`gateway-contact-section.test.tsx`, `profile.spec.ts`)."
  - "[C-30.FR-3] A wrong code shows the attempts left; \"Too many code requests\" with the time to wait shows on `429`; with several gateways the section has one block per gateway (`gateway-contact-section.test.tsx`)."
  - "[C-30.FR-3, C-18.FR-6] A User's page shows their contacts with the gateway, the contact and its state, and \"Remove\" for Admins, which asks for confirmation; it offers no way to add one; the Audit log shows the contact's entries (`user-gateway-contacts.test.tsx`, `users.spec.ts`)."
  - "[C-30.FR-1, C-30.AC-1, C-30.FR-8] The page of a Bridge Connection in the role `gateway` shows \"Notification gateway\" among its roles from `info`, its health — \"Healthy\", or the banner \"Broken since HH:MM: {reason}. Muster tries again every {interval}.\" — and the Connections list marks it \"Notification gateway\"; its check lists the roles; the Destination form does not offer a Connection whose only role is `gateway`; a Connection whose only role is `gateway` and that still has Destinations, saved while its bridge was a messenger, shows \"This bridge now declares only the role gateway: its {count} Destinations no longer receive messages. Remove them or change the bridge.\" (S-145's Open question 1) (`gateway-health.test.tsx`, `connection-check.test.tsx`, `destination-form.test.tsx`, `bridge-connection.spec.ts`, `destinations.spec.ts`)."
  - "[C-27.FR-8] On the Schedule page and in \"On call now\", an unreachable mark of a person with a confirmed contact at a gateway is followed by \"A call through the Notification gateway works.\" (`schedule-warnings.test.tsx`, `on-call-now.test.tsx`, `schedules.spec.ts`)."
  - "Every new text exists in English and Russian; \"How to connect a Notification gateway\" and the link of the Connection page open sections of the page on Notification gateways (`docs-links.json`); `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-150. The contact for calls and SMS in the profile, the roles and health of a Notification gateway, and a User's contacts (FE)

## Scope

**IN**

- The profile's "Contact for calls and SMS" (C-30 UI, C-30.FR-3), with the note that a gateway is needed where there is
  none (C-30.FR-9) as a component the rules (S-153) and the chain form (S-158) reuse.
- A User's contacts on the User's page, with "Remove" for Admins (C-18.FR-6 revised).
- The roles of a Bridge Connection, "Notification gateway" with its health and Broken banner on the Connection page and
  in the list, the roles in the check, and gateway-only Connections left out of the Destination form (C-30.FR-1,
  FR-8).
- "A call through the Notification gateway works." after the unreachable marks of the Schedule page and "On call now"
  (C-27.FR-8).

**OUT**

- The API: S-145, S-167, S-148, S-149. The Notification rules in the profile: S-153. The documentation: S-151.
- The theme switch that the profile mockup also shows: specified later in L2 (L2.md §1.5).

## Contracts

- **API used**: `listMyGatewayContacts`, `createMyGatewayContact`, `sendMyGatewayContactCode`,
  `confirmMyGatewayContact`, `deleteMyGatewayContact`, `listUserGatewayContacts`, `deleteUserGatewayContact`,
  `listConnections` (`role=gateway`), `getConnection` (`roles`, `gateway`), `checkConnection`, `getScheduleOnCall` and
  `getOnCallNow` (`gateway_reaches`), `getMe`.
- **Components**: `gateway-contact-section.tsx` — one block per Notification gateway, in the order of the list: the
  phone field, "Send code", the code step with "Confirm" and "Another number", the confirmed contact with "Remove" and
  the replaced one while a new one waits for its code (S-167), the errors of `422` and `429`; `gateway-unavailable.tsx`
  — "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)", the link "How to connect a
  Notification gateway" and, for a User with `connections:write`, "Connect a bridge" to the new Connection form with the
  type Bridge; `user-gateway-contacts.tsx` — the table of a User's contacts with "Remove"; `gateway-health.tsx` — the
  health of the gateway role with its banner, and the warning about Destinations left on a gateway-only Connection
  (`roles` without `messenger` and `destination_count` above 0); `bridge-connection-fields.tsx` and
  `connection-check.tsx` — the roles; `destination-form.tsx` — Connections whose `roles` lack `messenger` are not
  offered; `schedule-warnings.tsx` and `on-call-now.tsx` — the note after an unreachable mark when `gateway_reaches`;
  `audit-diff.tsx` — the entries `gateway_contact.*`.
- **Documentation links** (`docs-links.json`): `field.gateway_contact` →
  `messengers/notification-gateways.md#contact-for-calls-and-sms`, `title.notification_gateway` →
  `messengers/notification-gateways.md#connect-a-notification-gateway`; the sections exist from S-167.
- **Texts** (EN / RU), from C-30's UI and the approved Profile mockup: "Contact for calls and SMS" / «Контакт для
  звонков/SMS», its hint / «Muster сам не звонит, не шлёт SMS и не проверяет телефоны: это делает шлюз оповещений (мост
  для звонков и SMS), и от мессенджера он не зависит. Администратор видит контакт и может удалить его, но не изменить.»,
  "Phone" / «Телефон», "Send code" / «Отправить код», "Code sent through the Notification gateway to {{contact}}." /
  «Код отправлен через шлюз оповещений на {{contact}}.», "Code" / «Код», "Confirm" / «Подтвердить», "Another number" /
  «Другой номер», "calls and SMS" / «звонок и SMS», "confirmed" / «подтверждён», "Remove" / «Удалить», the note about
  calls / «Из звонка можно подтвердить группу, только если у неё ещё нет владельца; иначе звонок ответит «уже взял(а)
  {{name}}».», "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)" / «Недоступно: нужен шлюз
  оповещений (мост для звонков и SMS)», "How to connect a Notification gateway" / «Как подключить шлюз оповещений»,
  "Connect a bridge" / «Подключить мост», "Notification gateway" / «Шлюз оповещений», "Broken since {{time}}:
  {{reason}}. Muster tries again every {{interval}}." / «Сломан с {{time}}: {{reason}}. Muster пробует снова каждые
  {{interval}}.», "A call through the Notification gateway works." / «Звонок через шлюз оповещений доходит.», "This
  bridge now declares only the role gateway: its {{count}} Destinations no longer receive messages. Remove them or
  change the bridge." / «Мост теперь объявляет только роль gateway: его места доставки ({{count}}) больше не получают
  сообщений. Удалите их или измените мост.», with plurals.

## Steps

1. Write the shared note and the profile section. Check: `gateway-unavailable.test.tsx`,
   `gateway-contact-section.test.tsx`.
2. Write a User's contacts. Check: `user-gateway-contacts.test.tsx`.
3. Show the roles, the health and the check on the Connection pages, and filter the Destination form. Check:
   `gateway-health.test.tsx`, `bridge-connection-fields.test.tsx`, `connection-check.test.tsx`,
   `destination-form.test.tsx`.
4. Add the note to the warnings. Check: `schedule-warnings.test.tsx`, `on-call-now.test.tsx`.
5. Add the links, the texts and the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`; from a terminal remove the demo Connection "Example Gateway" for the first steps and add it back after
step 2 (as in S-145). Then in Playwright at 1280 × 800, in English and in Russian:

1. Sign in as petr (a Responder): Profile → "Contact for calls and SMS" shows the hint and "Unavailable: a Notification
   gateway is needed (a bridge for calls and SMS)" with "How to connect a Notification gateway", no "Connect a bridge"
   and no phone field. As the Admin, "Connect a bridge" is shown.
2. With "Example Gateway" back, as petr: type `+10000000002`, "Send code" → "Code sent through the Notification gateway
   to +10000000002."; type `000000` → the attempts left; type the code from `/_fake/notifications` → "+10000000002 ·
   calls and SMS · confirmed" with "Remove" and the note "From a call you can acknowledge an Alert Group only while
   nobody has it; …".
3. As the Admin: Users → petr shows the contact with "Remove"; "Remove" → confirm → the table is empty; Audit log shows
   `gateway_contact.deleted`.
4. Connections → "Example Gateway" is marked "Notification gateway"; its page shows the roles and "Healthy"; after a
   scripted `401` on a call (S-149) it shows "Broken since …: …. Muster tries again every 5 min."; "New Destination" →
   Bridge does not offer "Example Gateway". With a Destination of the demo "Example Bridge" and its roles set to
   `["gateway"]` through `/_fake/config` and a check (S-145's Verification), its page shows the warning "This bridge now
   declares only the role gateway: …" about its one Destination.
5. With petr On-call in a Schedule of "payments" whose Destination he cannot be mentioned in, and his contact confirmed
   again: the Schedule page and Home's "On call now" show "Unreachable in Mattermost: no linked account." followed by "A
   call through the Notification gateway works.".

`make e2e` runs these steps in `profile.spec.ts`, `users.spec.ts`, `bridge-connection.spec.ts`, `destinations.spec.ts`
and `schedules.spec.ts`; `docs-links-forms.spec.ts` follows the new links.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): confirm a contact for calls and show notification gateways`.
- The profile mockup also shows a theme switch; it is specified later in L2 and is not part of this story.
- The Notification rules of the profile are placed above this section by S-153, after "Linked accounts", as the mockup
  orders them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-1 | partial | the roles on the Connection page and its check |
| C-30.FR-3 | partial | the profile and a User's page; the API is S-167 |
| C-30.FR-8 | partial | the Connection page with its banner; the state is S-149 |
| C-30.FR-9 | partial | the note in the profile; the rules S-153, the chain form S-158 |
| C-30.AC-1 | partial | shown as a Notification gateway; the API is S-145 |
| C-30.AC-6 | full | |
| C-27.FR-8 | partial | the note on the pages; the API is S-167 |
| C-18.FR-6 | partial | revised for L2: a User's contacts on their page |
