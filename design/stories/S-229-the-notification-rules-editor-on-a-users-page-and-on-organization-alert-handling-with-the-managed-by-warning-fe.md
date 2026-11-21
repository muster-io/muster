---
id: S-229
title: The Notification rules editor on a User's page and on Organization → Alert handling, with the Managed by warning (FE)
capability: C-41
kind: fe
layer: L2
depends_on: [S-228, S-153, S-180, S-181, S-150]
covers: [C-31.FR-5, C-41.FR-3, C-41.FR-7, C-41.FR-8, C-41.FR-9, C-41.FR-10, C-41.AC-3, C-41.AC-6, C-41.AC-7, C-41.AC-10]
files_touched:
  - api/openapi.yaml
  - web/src/components/user-notification-rules.tsx
  - web/src/components/user-notification-rules.test.tsx
  - web/src/components/default-notification-rules.tsx
  - web/src/components/default-notification-rules.test.tsx
  - web/src/components/notification-rules-editor.tsx
  - web/src/components/notification-rules-editor.test.tsx
  - web/src/components/notification-rule-list.tsx
  - web/src/lib/notification-rule-editor.ts
  - web/src/lib/notification-rule-editor.test.ts
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/notification-rules-editor.spec.ts
  - web/e2e/users.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/audit-log.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-31.FR-5, C-41.FR-7, C-41.AC-6] An Admin edits any User's rules with the editor of S-228 on the User's page, saving through `updateUserNotificationRules` with `If-Match` and the state `custom`; a call is offered for the gateways at which that User has a confirmed contact (`listUserGatewayContacts`); the User is not notified of it, and the User's own profile shows the rules as custom; the Audit log shows `notification_rules.updated` with the diff of `normal` and `important`; a Viewer or a Responder sees the User's rules in words, with no \"Edit sequences\", and a request of theirs for another User's rules gets `403` (`user-notification-rules.test.tsx`, `notification-rules-editor.spec.ts`, `users.spec.ts`)."
  - "[C-41.FR-3, C-41.AC-3] On a User's page a gateway where the User has no confirmed contact is listed as unavailable with \"{name} has no confirmed contact for calls and SMS.\", and in the profile with \"Confirm your contact for calls and SMS below first.\"; without any gateway the call action reads \"Unavailable: a Notification gateway is needed (a bridge for calls and SMS)\" in both (`user-notification-rules.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-9, C-41.AC-10] Organization → Alert handling shows the Organization's default rules in words to everyone who reads it and, to an Admin with `organization:write`, \"Edit sequences\" opening the same editor, with the checks and warnings of S-228, a call offered through any Notification gateway since the Organization has no contacts, and the note that they change for every User who follows them; \"Save\" stores them with `updateOrganization` and `If-Match`; a Responder sees them only in words (`default-notification-rules.test.tsx`, `notification-rules-editor.spec.ts`, `organization-settings.spec.ts`)."
  - "[C-41.FR-9, C-41.AC-10] Saving the Organization's important rule as a Mention, a wait of 2 minutes, a wait of 3 minutes and a Mention — after \"Two waits in a row add up\" and \"Merge the waits\" — is recorded in the Audit log as `organization.updated` with a before/after diff at `/notification_rules/important`; a User in the state \"As in the Organization\" reads the new rule at once from `getMyNotificationRules`; a stale `If-Match` after any other change of the Organization's settings shows the current rules with the edits kept, as in S-228 (`default-notification-rules.test.tsx`, `notification-rules-editor.spec.ts`, `audit-log.spec.ts`)."
  - "[C-41.FR-9, C-41.FR-8, C-41.AC-10] With `managed_by` `terraform` on the Organization's settings, the Alert handling editor shows the warning of S-181 — the next run of the tool undoes changes saved here —, the save succeeds and the mark stays; the editor of a person's rules and of a User's rules shows no such marker, since a person's rules are never managed by the provider (`default-notification-rules.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-9] The description of `notification_rules` in the Organization's schema of `api/openapi.yaml` no longer says that 0.2.0 changes them only through the API; it says that they are edited on Organization → Alert handling or through the API, with the same checks, and the generated clients and the reference pages follow it (`make generate-check`, `make lint`)."
  - "[C-41.FR-10, C-41.AC-7] At 360 CSS pixels wide the editor on both pages is one rule under the other with the preview under each and works with the buttons alone, with no horizontal scrolling of the page, in English and in Russian; every new text exists in English and Russian with Russian plurals (`phone-width.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-229. The Notification rules editor on a User's page and on Organization → Alert handling, with the Managed by warning (FE)

## Scope

**IN**

- The editor of S-228 for an Admin on a User's page (C-41.FR-7) with the gateways of that User's contacts and the page's
  texts (C-41.FR-3).
- The editor of the Organization's default rules on Organization → Alert handling with `If-Match`, the Audit log,
  "follow at once" and the `managed_by` warning (C-41.FR-9, FR-8).
- The correction of the description of `notification_rules` in the spec.

**OUT**

- The editor itself, its checks, warnings, preview and the profile: S-228.
- The Terraform provider's resource of the Organization's default rules and the `managed_by` mark: C-33 (S-180, S-181,
  S-183). The editor only shows the warning, which never refuses a write (journal D757).
- The documentation and the revision marker of C-31.FR-5: S-230.

## Contracts

- **API used**: `getUserNotificationRules`, `updateUserNotificationRules` (`If-Match`), `listUserGatewayContacts`,
  `getOrganization`, `updateOrganization` (`If-Match`; `notification_rules`, `managed_by` kept as read),
  `listConnections` for the gateways, `getMyNotificationRules` to read what a User in the state `organization` follows.
  The spec change is one description: `Organization.notification_rules` (see Notes).
- **Components**: `notification-rules-editor.tsx` takes its owner — the profile (S-228), a User, or the Organization —
  and from it the contacts it offers (the person's, the User's, none for the Organization, whose call is offered for
  every Notification gateway), the unavailable text, the save operation, the note "…follow the Organization's rules"
  (shown for a person, and for the Organization as the note that Users who follow its rules follow the change at once),
  and whether the `managed_by` warning shows; `user-notification-rules.tsx` mounts it on the User's page for
  `users:write` and keeps the words for the rest; `default-notification-rules.tsx` mounts it for `organization:write`
  with the warning component of `managed-by.tsx` (S-181) and keeps the words for the rest. A stale `If-Match` on the
  Organization shows the current rules with the edits kept beside them, since the ETag covers every setting.
- **Permissions**: `users:write` for a User's rules, `organization:write` for the Organization's; reads as before.
- **Texts** (EN / RU; a missing key fails CI): "{name} has no confirmed contact for calls and SMS." / «У {name} нет
  подтверждённого контакта для звонков и SMS.»; the note for the Organization's rules; the warning of the
  Organization's editor reuses S-181's text; every other text is S-228's.

## Steps

1. Extend `notification-rules-editor.tsx` and `notification-rule-editor.ts` with the owner, the contacts and the notes.
   Check: `notification-rules-editor.test.tsx` and `notification-rule-editor.test.ts` for the User and Organization
   owners.
2. Mount it on the User's page. Check: `user-notification-rules.test.tsx` (the page's text, a Viewer's words, `403`
   shown).
3. Mount it on Alert handling with the warning and the saving through `updateOrganization`. Check:
   `default-notification-rules.test.tsx` (the managed mark kept, a stale `If-Match`).
4. Correct the description in `api/openapi.yaml`. Check: `make generate-check`, `make lint`.
5. Extend the specs. Check: the Verification below; `make ci` (missing keys).

## Verification

Playwright against `make dev`, in English and then Russian, as the Admin, with the User "sergey" whose gateway contact
is
confirmed at the demo "Example Gateway" and "ivan" with none.

1. Open Administration → Users → sergey. Expect the section "Notification rules" in words and "Edit sequences"; change
   his normal rule to a Mention, a wait of 10 minutes, a Mention; expect the preview and "Save": `curl -s "${A[@]}"
   $API/users/$SERGEY/notification-rules | jq -c '{s: .state, n: [.normal[].kind]}'` answers
   `{"s":"custom","n":["mention","wait","mention"]}`; `curl -s "${A[@]}" "$API/audit-log?limit=1" | jq -c '.items[0] |
   {action}'` is `notification_rules.updated` with the diff of `normal`. Sign in as sergey: his profile shows the rules
   as custom with "Reset to the Organization's rules".
2. Open ivan's page, add a call: expect the gateway listed as unavailable with "ivan has no confirmed contact for calls
   and SMS."; in ivan's own profile expect "Confirm your contact for calls and SMS below first.". Without any gateway
   (delete it in the setup): "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)".
3. Sign in as a Responder and then a Viewer: expect the User's rules in words with no "Edit sequences"; `curl -X PUT
   ... $API/users/$SERGEY/notification-rules` with their token answers `403`.
4. Open Organization → Alert handling. Expect "Edit sequences"; make the important rule Mention, wait 2, wait 3,
   Mention: expect "Two waits in a row add up", press "Merge the waits" (one wait of 5), "Save". Expect the Audit log
   row `organization.updated` with `/notification_rules/important` before and after, and a User in the state "As in the
   Organization" reading the new rule at once (`getMyNotificationRules`). Change the Organization's name in another tab
   first: expect the stale `If-Match` to show the current rules with the edits kept beside them.
5. `PUT` the Organization with `managed_by` `terraform` through the Terraform provider's path or a Service account
   (the mark is set as S-180's test sets it): expect the warning that the next run of the tool undoes changes saved
   here, "Save" succeeding and `managed_by` still `terraform`. Sign in as a Responder: the rules in words only.
6. At 360 px on both pages: one rule under the other, the buttons alone, no horizontal scrolling. Switch to Russian.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the notification rules editor to a user's page and the organization's alert
  handling`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The Organization's
  `managed_by` mark comes from S-180 and S-181, which ship in 0.2.0, so the warning exists when this story merges.
- This story edits `api/openapi.yaml` for one description, which C-41 asks it to revise (C-41 API surface); that makes
  the generated clients and the reference pages change, which are regenerated and never listed.
- A person's and a User's rules are never managed by the provider (journal D757); only the Organization's default rules
  carry a mark, so only that editor warns.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-31.FR-5 | partial | revised for L2: the editor on a User's page and on Alert handling; the profile is S-228, the revision marker S-230 |
| C-41.FR-3 | partial | the User page's text and the Organization's gateways; the profile's text is S-228 |
| C-41.FR-7 | full | |
| C-41.FR-8 | partial | the Organization's warning and the absence of a marker on a User's rules; the profile is S-228 |
| C-41.FR-9 | full | |
| C-41.FR-10 | partial | 360 px on the User page and Alert handling; the profile is S-228 |
| C-41.AC-3 | partial | the User page's text; the profile's is S-228 |
| C-41.AC-6 | full | |
| C-41.AC-7 | partial | the two other places at 360 px; the profile is S-228 |
| C-41.AC-10 | full | |
