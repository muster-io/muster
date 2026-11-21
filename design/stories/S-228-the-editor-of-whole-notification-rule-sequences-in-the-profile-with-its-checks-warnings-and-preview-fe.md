---
id: S-228
title: The editor of whole Notification rule sequences in the profile, with its checks, warnings and preview (FE)
capability: C-41
kind: fe
layer: L2
depends_on: [S-153, S-152, S-157, S-150, S-167]
covers: [C-31.FR-5, C-41.FR-1, C-41.FR-2, C-41.FR-3, C-41.FR-4, C-41.FR-5, C-41.FR-6, C-41.FR-8, C-41.FR-10, C-41.AC-1, C-41.AC-2, C-41.AC-3, C-41.AC-4, C-41.AC-5, C-41.AC-7, C-41.AC-8, C-41.AC-9]
files_touched:
  - web/src/components/notification-rules-editor.tsx
  - web/src/components/notification-rules-editor.test.tsx
  - web/src/components/notification-rule-list.tsx
  - web/src/components/notification-rule-list.test.tsx
  - web/src/components/notification-rule-preview.tsx
  - web/src/components/notification-rule-preview.test.tsx
  - web/src/components/notification-rules-section.tsx
  - web/src/components/notification-rules-section.test.tsx
  - web/src/lib/notification-rule-editor.ts
  - web/src/lib/notification-rule-editor.test.ts
  - web/src/lib/notification-rules.ts
  - web/src/lib/notification-rules.test.ts
  - web/src/test/fixtures/notification-rules.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/notification-rules-editor.spec.ts
  - web/e2e/profile.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-31.FR-5, C-41.FR-1, C-41.AC-1] Profile → \"Personal notification rules\" has \"Edit sequences\" next to the rules in words, which opens an editor of \"Normal\" and \"Important\" together, each starting from the sequence in effect — the Organization's rules, those of \"Call\" or \"Don't call\", or the custom ones; the switch \"Also call through the Notification gateway\" stays and custom rules keep \"Reset to the Organization's rules\" (`notification-rules-section.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-2, C-41.AC-7] Each rule is an ordered list of actions — \"Mention in the Thread\", \"Call through a Notification gateway\" with \"Call through\" and the gateway when more than one exists, and \"Wait\" with \"minutes\" —; the editor adds an action at the end or after any action with \"Add action\", removes one with \"Remove\", changes its kind, and moves it with \"Move up\" and \"Move down\" buttons that work from the keyboard and at 360 px; actions without a wait between them are shown as one moment, and dragging, where offered, is never the only way (`notification-rule-list.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-3, C-41.AC-3] The call action is offered only while a Notification gateway exists and only for gateways where the person has a confirmed contact; the others are listed as unavailable with \"Confirm your contact for calls and SMS below first.\", and without any gateway the action reads \"Unavailable: a Notification gateway is needed (a bridge for calls and SMS)\"; with two gateways and a confirmed contact at one only that one is offered (`notification-rule-list.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-3, C-41.AC-4] A stored call whose gateway was deleted or whose contact is gone shows the reason the API reads (`gateway_deleted`, `no_confirmed_contact`) as \"unavailable: the Notification gateway was deleted\" or the contact text; where the person's own rules are stored it may be kept — saving succeeds and a second call through that gateway is refused by the API — or removed; where the editor starts from the Organization's rules, a call at a gateway where the person has no confirmed contact is shown as a problem and \"Save\" stays unavailable until it is removed, as the API would refuse it (`contact_required`) (`notification-rules-editor.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-4, C-41.AC-2] The editor checks each rule as the API does (`notification_rule.limits`): at most 10 actions, waits included, so \"Add action\" is unavailable at 10 with \"A rule has at most 10 actions, waits included.\"; each wait a whole number of minutes from 1 to 60, with \"A wait is 1 to 60 minutes.\" at a wait of 0 or 61; at least one Mention or call, with \"A rule needs a Mention or a call.\"; each problem is shown at its action or rule while the person edits, \"Save\" stays unavailable while one remains, and a `422` from the server shows its problem at the same places by the pointers `/normal/{i}/wait_seconds`, `/important/{i}/gateway_id` and `/normal` (`notification-rule-editor.test.ts`, `notification-rules-editor.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-4, C-41.AC-8] A wait at the end of a rule and two waits in a row stay allowed and save, with the warnings \"A wait at the end does nothing\" and \"Two waits in a row add up\" at the action; for two in a row whose sum is at most 60 minutes \"Merge the waits\" makes one wait of their sum: waits of 20 and 30 minutes become one of 50, and it is not offered for 40 and 30; a warning never keeps \"Save\" unavailable (`notification-rule-editor.test.ts`, `notification-rules-editor.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-5, C-41.AC-1] Under each rule \"What happens when you are Paged {importance}\" lists the moments from the Page — \"At once\", \"After N min\" — with what is made then: a Mention in the Thread, a call to the masked contact through the named gateway, or \"the same again\"; then \"Stops as soon as someone acknowledges, snoozes or resolves the Alert Group, or the chain stops or starts over\"; an unavailable call reads \"a call that reaches nobody: {reason}\"; it follows every change at once, makes no request, and for Mention, wait 3 minutes, call, wait 10 minutes, call reads three moments \"At once\", \"After 3 min\" and \"After 13 min\" (`notification-rule-preview.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-6, C-41.AC-1, C-41.AC-5] \"Save\" stores both rules with `updateMyNotificationRules`, state `custom`, from the web session with `If-Match`; before the first save of a person who follows the Organization the editor says \"Your rules will no longer follow the Organization's rules. 'Reset to the Organization's rules' brings that back.\"; a stale `If-Match` (`412`) shows \"The rules changed in the meantime. They are shown as they are now; try again.\" with the current rules and the person's edits kept beside them; \"Cancel\" leaves the rules as they were and leaving the profile with unsaved edits asks first; after \"Reset to the Organization's rules\" the API reads the state `organization` (`notification-rules-editor.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-6, C-41.AC-9] Rules edited to equal the Organization's current ones show \"These are the Organization's rules. Saved here, they will no longer follow its changes.\" and \"Reset to the Organization's rules\" beside \"Save\"; saved anyway, they read the state `custom` and stay as they were after an Admin changes the Organization's rules; a save that changes nothing sends nothing (`notification-rules-editor.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-6, C-41.FR-8] The profile's text for custom rules reads that they were set by the person, through the API or by an Admin, instead of only through the API or by an Admin, and the editor of a person's rules shows no \"Managed by\" marker, since a person's rules are never managed by the Terraform provider (`notification-rules-section.test.tsx`, `notification-rules-editor.spec.ts`)."
  - "[C-41.FR-10, C-41.AC-7] At 360 CSS pixels wide the editor is one rule under the other with the preview under each, every control reachable with no horizontal scrolling of the page and the actions moved with the buttons alone, in English and in Russian; every new text exists in English and Russian with Russian plurals, and a missing key fails `make ci` (`phone-width.spec.ts`, `notification-rules-editor.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-228. The editor of whole Notification rule sequences in the profile, with its checks, warnings and preview (FE)

## Scope

**IN**

- The editor component, shared by every place that edits rules: the list of actions with its buttons, the checks and
  warnings of the API's limits, the preview, the notes about the Organization's rules, and the save with `If-Match` —
  mounted in the profile (C-41.FR-1 to FR-6, FR-8, FR-10).
- The profile's "Edit sequences", its changed text for custom rules and the leave guard.

**OUT**

- The same editor on a User's page for Admins (C-41.FR-7) and on Organization → Alert handling (C-41.FR-9): S-229.
- The documentation and the revision marker of C-31.FR-5: S-230.
- The API: it exists from 0.2.0 (S-152, S-153, S-157) and C-41 adds none, so this capability has no backend story; the
  validation of `NotificationRuleSet` stays as it is (C-41 API surface).

## Contracts

- **API used**: `getMyNotificationRules`, `updateMyNotificationRules` (`If-Match`; state `custom`, `normal`,
  `important`), `resetMyNotificationRules`, `listMyGatewayContacts` (the person's contacts with their masked text) and
  `listConnections` for the gateways, all of the generated client (`NotificationRuleSet`, `NotificationRuleAction`,
  `NotificationRuleUnavailableReason`). The editor sends `gateway_id` and `wait_seconds` as the schema takes them and
  never sends the read-only `gateway`, `available` or `unavailable_reason`, though the API ignores them.
- **Components**: `notification-rules-editor.tsx` — the editor: the draft of both rules, `dirty`, the checks, the save,
  the notes, `412` handling and the leave guard (the router's blocker plus the browser's `beforeunload`, as S-217's
  editor does), taking the owner of the rules (the profile here; S-229 adds the User and the Organization) so that the
  unavailable text, the contacts offered and the save differ; `notification-rule-list.tsx` — one rule: rows of actions
  each with its kind select, the gateway select, the wait field and "Move up", "Move down", "Remove", then "Add action",
  with the warnings and problems at their rows; `notification-rule-preview.tsx` — the moments of one rule and the stop
  line, from the draft alone. `notification-rules-section.tsx` gains "Edit sequences", the new text for custom rules and
  the use of the editor.
- **Library**: `web/src/lib/notification-rule-editor.ts` — the draft, its conversion to and from
  `NotificationRuleSet`, the operations (add after, remove, change kind, move, merge waits), the checks with the limits
  of `notification_rule.limits` as built-in values (10 actions, 1 to 60 minutes), the warnings, the mapping of the
  server's pointers to actions, and the moments of the preview; `notification-rules.ts` keeps `ruleWords` and
  `confirmedContact`, which the editor reuses.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Edit sequences" /
  «Редактировать цепочки»; "Normal" / «Обычное»; "Important" / «Важное»; "Mention in the Thread" / «Упоминание в
  треде»; "Call through a Notification gateway" / «Звонок через шлюз уведомлений»; "Call through" / «Звонить через»;
  "Wait" / «Ожидание»; "minutes" with Russian plurals; "Move up" / «Выше»; "Move down" / «Ниже»; "Remove" / «Убрать»;
  "Add action" / «Добавить действие»; "What happens when you are Paged {importance}" / «Что произойдёт, когда вас
  вызовут
  {importance}»; "At once" / «Сразу»; "After {N} min" / «Через {N} мин»; the problems, warnings and notes quoted in the
  acceptance statements, each in both languages; "Merge the waits" / «Объединить ожидания»; "Save", "Cancel" as the
  profile's words; the changed text for custom rules.
- **Permissions**: the person's own rules from the web session (C-03.FR-27); no new Permission.
- **Fixtures**: `web/src/test/fixtures/notification-rules.ts` — builders of `NotificationRules` in each state, of
  actions and of contacts, used by S-229's tests too.

## Steps

1. Write `web/src/lib/notification-rule-editor.ts` with the draft, the checks, warnings and moments. Check:
   `notification-rule-editor.test.ts` (the limits, the three warnings, "Merge the waits" at 20 + 30 and not at 40 + 30,
   the moments of 0, 3 and 13 minutes, the pointers of a `422`).
2. Write the list and the preview. Check: `notification-rule-list.test.tsx` (keyboard moves, add after, change kind,
   unavailable calls), `notification-rule-preview.test.tsx`.
3. Write the editor and mount it in the profile with the new texts. Check: `notification-rules-editor.test.tsx` (save,
   `412` with the edits kept, cancel, the leave guard, the notes before the first save and for equal rules, the
   Organization's call without a contact), `notification-rules-section.test.tsx`; `make ci` (missing keys).
4. Write `notification-rules-editor.spec.ts`, extend `profile.spec.ts` and `phone-width.spec.ts`. Check: the
   Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian, as a person with a
confirmed contact at the demo "Example Gateway" (S-150's setup) and a Route whose chain Pages them importantly.

1. Open Profile → "Personal notification rules" → "Edit sequences". Make the important rule Mention, "Wait" 3 minutes,
   "Call through a Notification gateway", "Wait" 10 minutes, a second call: expect the preview "At once: a Mention in
   the
   Thread · After 3 min: a call to <the masked contact> through Example Gateway · After 13 min: the call again", and the
   stop line. Expect the note "Your rules will no longer follow the Organization's rules…" before saving. "Save":
   `curl -s "${A[@]}" $API/me/notification-rules | jq -c '{s: .state, i: [.important[].kind]}'` answers
   `{"s":"custom","i":["mention","wait","call","wait","call"]}`.
2. A Page of that important rule: an Escalation step Pages the person importantly on a new Alert Group (the setup of
   S-157); advance the development clock with `POST localhost:8082/_dev/clock` by 3 minutes and again by 10: expect the
   Mention at once, `curl -s 127.0.0.1:18094/_fake/notifications` listing the call after 3 minutes and again after 13.
3. Add actions to 10: expect "Add action" unavailable with "A rule has at most 10 actions, waits included."; set a wait
   of 0 and of 61: expect "A wait is 1 to 60 minutes." at the action and "Save" unavailable; remove every Mention and
   call: expect "A rule needs a Mention or a call.". A request that bypasses the editor with a wait of 61 minutes
   returns
   `422` with the pointer `/important/1/wait_seconds`, which the editor shows at that action when it sends it
   (Playwright sends the draft past the checks).
4. Make a rule that ends with a wait: expect "A wait at the end does nothing" and "Save" available; waits of 20 and 30
   in
   a row: expect "Two waits in a row add up" and "Merge the waits" making one of 50; waits of 40 and 30: no merge.
5. Delete the gateway of a stored call through the API: reopen the editor and expect "unavailable: the Notification
   gateway was deleted" at that call; "Save" with it kept succeeds; adding a second call through that gateway answers
   `422`. For a person in the state "As in the Organization" whose Organization's important rule calls through a gateway
   where they have no confirmed contact: expect the call shown as a problem and "Save" unavailable until it is removed.
6. Save in another tab, then "Save" here: expect "The rules changed in the meantime…" with the current rules and the
   edits kept. Edit the rules to equal the Organization's: expect "These are the Organization's rules…" and "Reset to
   the
   Organization's rules"; "Save" anyway, then change the Organization's important rule through the API: the person's
   rule
   stays. "Reset to the Organization's rules": `.state` is `organization`. Edit and navigate away: expect the
   confirmation.
7. With no gateway: expect "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)" on the call
   action. With two gateways and a contact at one: only that one offered, the other listed as unavailable with "Confirm
   your contact for calls and SMS below first.".
8. At 360 px: one rule under the other, the preview under each, the actions moved with the buttons alone, no horizontal
   scrolling. Switch to Russian: the Russian texts and plurals.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the editor of whole notification rule sequences to the profile`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- C-41 needs no backend story: `updateMyNotificationRules`, `updateUserNotificationRules`, the limits and every
  pointer already exist from 0.2.0 (C-41 API surface), so no frontend story of this capability depends on a backend one;
  the one revision of the spec, the description of `notification_rules` in the Organization's schema, is S-229's.
- The editor warns about a wait at the end and two waits in a row instead of forbidding them, because the API allows
  them and rules already stored may have them (C-41.FR-4).
- Dragging is not offered: "Move up" and "Move down" serve the keyboard and touch (C-41.FR-2) and no drag library is
  added; a library would need the licence statement of ADR-0001.
- The design pass of the UI belongs to the later layers (journal D315); this editor uses the existing components.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-31.FR-5 | partial | revised for L2: the editor of whole sequences in the profile; a User's page and Alert handling are S-229, the revision marker S-230 |
| C-41.FR-1 | full | |
| C-41.FR-2 | full | |
| C-41.FR-3 | partial | the profile's texts and the person's contacts; the User page's text and the Organization's gateways are S-229 |
| C-41.FR-4 | full | |
| C-41.FR-5 | full | |
| C-41.FR-6 | full | |
| C-41.FR-8 | partial | no marker in the person's editor; the Organization's editor with its warning is S-229 |
| C-41.FR-10 | partial | 360 px in the profile; the other two places are S-229 |
| C-41.AC-1 | full | |
| C-41.AC-2 | full | |
| C-41.AC-3 | partial | the profile's text; the User page's text is S-229 |
| C-41.AC-4 | full | |
| C-41.AC-5 | full | |
| C-41.AC-7 | partial | the profile at 360 px; the other places are S-229 |
| C-41.AC-8 | full | |
| C-41.AC-9 | full | |
