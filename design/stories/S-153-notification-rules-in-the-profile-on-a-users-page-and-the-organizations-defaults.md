---
id: S-153
title: Notification rules in the profile with the switch, on a User's page, and the Organization's defaults (FE)
capability: C-31
kind: fe
layer: L2
depends_on: [S-150, S-152]
covers: [C-31.FR-4, C-31.FR-5, C-31.AC-5, C-31.AC-6, C-30.FR-9, C-20.FR-1]
files_touched:
  - web/src/routes/profile.tsx
  - web/src/components/notification-rules-section.tsx
  - web/src/components/notification-rules-section.test.tsx
  - web/src/lib/notification-rules.ts
  - web/src/lib/notification-rules.test.ts
  - web/src/components/default-notification-rules.tsx
  - web/src/components/default-notification-rules.test.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/components/user-notification-rules.tsx
  - web/src/components/user-notification-rules.test.tsx
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/profile.spec.ts
  - web/e2e/users.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/docs-links-admin.spec.ts
acceptance:
  - "[C-31.FR-5, C-31.AC-5, C-30.FR-9] In a real browser the profile shows \"Linked accounts\" first with \"A Page is a Mention in the Thread; it reaches only a linked account.\", then \"Personal notification rules\" with its hint, \"Normal: a Mention in the Thread of the Root message.\" and \"Important: a Mention in the Thread and again after 5 minutes if you did not answer.\" as the User's current sequences in words, and the switch \"Also call through the Notification gateway\" with \"As in the Organization\", \"Call\" and \"Don't call\"; without a gateway the switch is unavailable with \"Unavailable: a Notification gateway is needed (a bridge for calls and SMS)\"; with the fake gateway and no confirmed contact it says \"Confirm your contact for calls and SMS below first.\"; once the contact is confirmed it is available; in English and in Russian (`notification-rules-section.test.tsx`, `profile.spec.ts`)."
  - "[C-31.FR-5, C-31.AC-6] Choosing \"Call\" shows the important rule as \"a Mention in the Thread and a call to {contact} at the same time, again after 5 minutes\" — exactly a Mention with a call, a wait of 5 minutes and a Mention — and \"As in the Organization\" brings back the Organization's rules; with two gateways at which the User has confirmed contacts, the profile asks which one the call uses next to the switch; after an Admin set three Mentions through the API, the profile shows them in words without the switch, with \"Reset to the Organization's rules\", which brings the switch back to \"As in the Organization\" (`notification-rules.test.ts`, `profile.spec.ts`)."
  - "[C-31.FR-4] A User's page shows their rules in words with the switch and \"Reset to the Organization's rules\", which Admins change, and read-only for others; a change appears in the Audit log with its diff (`user-notification-rules.test.tsx`, `users.spec.ts`)."
  - "[C-20.FR-1, C-31.FR-4] Organization → Alert handling shows \"Default notification rules\" with the normal and the important rule in words and the note that they change through the API (`updateOrganization`), with a link to the documentation; a change made through the API shows there at once (Open question 1) (`default-notification-rules.test.tsx`, `organization-settings.spec.ts`)."
  - "Every new text exists in English and Russian with plural forms; \"Personal notification rules\" has \"Learn more\" to Concepts → Notification rules; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: 442
---

# S-153. Notification rules in the profile with the switch, on a User's page, and the Organization's defaults (FE)

## Scope

**IN**

- The profile's "Personal notification rules" (C-31 UI): both rules in words, the switch "Also call through the
  Notification gateway" with its three states and the choice of a gateway, custom rules in words with "Reset to the
  Organization's rules" (C-31.FR-5), with the note that a gateway is needed (C-30.FR-9); "Linked accounts" first with
  its hint.
- A User's rules on their page, changed by Admins (C-31.FR-4).
- The Organization's default rules, in words, on Organization → Alert handling (C-20.FR-1 revised; Open question 1).

**OUT**

- The API: S-152. The contact block: S-150. An editor of whole sequences in the profile: specified later in L2 (L2.md
  §1.5). The documentation: S-161.

## Contracts

- **API used**: `getMyNotificationRules`, `updateMyNotificationRules`, `resetMyNotificationRules`,
  `getUserNotificationRules`, `updateUserNotificationRules`, `getOrganization`, `updateOrganization`,
  `listMyGatewayContacts`, `listConnections` (`role=gateway`).
- **Components**: `notification-rules-section.tsx` — the section of the profile, placed after "Linked accounts" and
  before "Contact for calls and SMS" as the approved mockup orders them, with the switch and the choice of a gateway
  when the User has confirmed contacts at more than one, and `gateway-unavailable.tsx` of S-150 when none exists;
  `lib/notification-rules.ts` — a rule in words ("a Mention in the Thread", "a call to {contact}", "at the same time",
  "again after {N} minutes"); `user-notification-rules.tsx` — the same switch and reset on a User's page for
  `users:write`; `default-notification-rules.tsx` — the Organization's two rules in words with the note about the API;
  `audit-diff.tsx` — `notification_rules.updated` and the field `notification_rules` of the Organization.
- **Documentation links** (`docs-links.json`): `field.notification_rules` → `concepts/notification-rules.md`,
  `field.notification_rules_switch` → `concepts/notification-rules.md#the-switch`; the sections exist from S-152.
- **Texts** (EN / RU), from C-31's UI and the approved Profile mockup: "Linked accounts" / «Привязанные аккаунты», "A
  Page is a Mention in the Thread; it reaches only a linked account." / «Призыв — это упоминание в треде; он доходит
  только до привязанного аккаунта.», "Personal notification rules" / «Личные правила уведомлений», its hint / «Что
  происходит, когда шаг цепочки зовёт вас «обычно» или «важно». Личных сообщений Muster не шлёт.», "Normal" / «Обычное»,
  "Important" / «Важное», "Also call through the Notification gateway" / «Ещё звонок через шлюз оповещений», "As in the
  Organization" / «Как в организации», "Call" / «Звонить», "Don't call" / «Не звонить», "Confirm your contact for calls
  and SMS below first." / «Сначала подтвердите контакт для звонков/SMS ниже.», "Reset to the Organization's rules" /
  «Вернуть правила организации», "Default notification rules" / «Правила уведомлений по умолчанию», "They change through
  the API until an editor comes." / «Пока их меняют через API; редактор появится позже.».

## Steps

1. Write the rule in words and the profile section. Check: `notification-rules.test.ts`,
   `notification-rules-section.test.tsx`.
2. Write a User's rules. Check: `user-notification-rules.test.tsx`.
3. Show the defaults on the Organization page. Check: `default-notification-rules.test.tsx`.
4. Add the links, texts and end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`; petr is a Responder with a Mattermost Account link. Remove the demo gateway "Example Gateway" for step
1 and add it back after it (S-145). In Playwright at 1280 × 800, in English and in Russian:

1. As petr, Profile: "Linked accounts" with its hint comes first; "Personal notification rules" shows "Normal: a Mention
   in the Thread of the Root message." and "Important: a Mention in the Thread and again after 5 minutes if you did not
   answer."; the switch is unavailable with "Unavailable: a Notification gateway is needed (a bridge for calls and
   SMS)".
2. With the gateway back: the switch shows "Confirm your contact for calls and SMS below first."; confirm the contact
   (S-150) → the switch is available; "Call" → "Important: a Mention in the Thread and a call to +10000000002 at the
   same time, again after 5 minutes"; "As in the Organization" → the Organization's wording again.
3. From a terminal the Admin sets petr's normal rule to three Mentions (S-152's Verification); reload → the rule in
   words without the switch, and "Reset to the Organization's rules" → the switch reads "As in the Organization".
4. As the Admin: Users → petr shows his rules with the switch; "Call" saves, and the Audit log shows
   `notification_rules.updated` with its diff. As a Responder the same section is read-only.
5. As the Admin: Organization → Alert handling → "Default notification rules" shows both rules in words and the note
   about the API; after S-152's Verification changes the important rule's wait to 10 minutes through the API, the page
   and ivan's profile show "again after 10 minutes".

`make e2e` runs these steps in `profile.spec.ts`, `users.spec.ts` and `organization-settings.spec.ts`;
`docs-links-admin.spec.ts` follows the new links.

## Open questions

1. **Editing the Organization's default rules in the UI.** Settled by the maintainer: in 0.2.0 Organization → Alert
   handling shows the defaults in words and they change only through the API (`updateOrganization`); C-31's UI says so,
   and the editor of whole sequences comes later in L2 (L2.md §1.5).

## Notes

- The Russian texts follow the vocabulary settled in S-162 (Open question 1).
- Suggested commit: `feat(web): show and switch notification rules in the profile and the organization`.
- The rules in words follow the mockup's sentences and the sequence, so custom rules read naturally too; a call names
  the contact of its gateway, or "Notification gateway" for an action whose contact is gone, marked unavailable.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-31.FR-4 | partial | the profile and a User's page; the API is S-152 |
| C-31.FR-5 | partial | the switch in the profile; the states are S-152 |
| C-31.AC-5 | full | |
| C-31.AC-6 | partial | the profile; the API is S-152, the call at once S-157 |
| C-30.FR-9 | partial | the note on the rules |
| C-20.FR-1 | partial | revised for L2: the default rules on the Organization page |
