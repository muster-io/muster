---
id: S-200
title: The "Loud and Quiet" section of the Route form (FE)
capability: C-36
kind: fe
layer: L2
depends_on: [S-197, S-198, S-199, S-112, S-123, S-158]
covers: [C-36.FR-9, C-36.FR-11, C-36.AC-1, C-36.AC-10, C-08.FR-1, C-08.FR-7]
files_touched:
  - web/src/components/route-policy-loud-quiet.tsx
  - web/src/components/route-policy-loud-quiet.test.tsx
  - web/src/components/route-form.tsx
  - web/src/components/route-form.test.tsx
  - web/src/components/route-editor.test.tsx
  - web/src/components/route-policy-timers.tsx
  - web/src/components/route-policy-timers.test.tsx
  - web/src/components/profile-picker.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/support.ts
  - web/e2e/route-loud-quiet.spec.ts
  - web/e2e/routes.spec.ts
acceptance:
  - "[C-36.AC-1, C-36.AC-10, C-08.FR-1, C-08.FR-7] In a real browser, creating a Route and choosing the Informational profile fills the section \"Loud and Quiet\" with only \"New Alert Group\" ticked and \"New Alerts remove the acknowledgement\" unticked, and saving stores `loud_events` `[new_alert_group]` and `new_alerts_remove_ack` false; choosing Paging fills all seven ticked (`route-policy-loud-quiet.test.tsx`, `route-form.test.tsx`, `route-loud-quiet.spec.ts`)."
  - "[C-36.FR-9, C-36.AC-10] On a Route whose ack timeout is off the tick box \"Ack timeout\" is unavailable with \"The ack timeout is off on this Route\" and keeps its stored value; turning the ack timeout on in the form ticks it, and the Admin may untick it before saving; a Route that gains an ack timeout on an Informational profile therefore sends no Quiet notice unless the tick is removed (`route-policy-loud-quiet.test.tsx`, `route-policy-timers.test.tsx`, `route-loud-quiet.spec.ts`)."
  - "[C-36.AC-10] The section lists the seven tick boxes \"New Alert Group\", \"New Alerts\", \"Reopen\", \"Ack timeout\", \"End of Snooze\", \"Rise to critical\" and \"Start of a Storm\", the note \"Pages of Escalation chains, Reminders, Takeovers and the Mention of an Owner who loses an acknowledgement are always Loud.\" and the tick box \"New Alerts remove the acknowledgement\" with its explanation; a change is saved with `If-Match` and the Audit log shows it as a diff of `loud_events` and `new_alerts_remove_ack`, in English and in Russian (`audit-diff.test.tsx`, `route-loud-quiet.spec.ts`)."
  - "[C-36.FR-11] The section links to the documentation page on Loud and Quiet with \"Learn more\", through the existing entry `field.loud_quiet`; without `routes:write` the section is read-only and shows the same ticks (`route-policy-loud-quiet.test.tsx`, `route-form.test.tsx`)."
  - "[C-36.AC-10] Every new text exists in English and Russian, and the Routes list needs no change (`route-loud-quiet.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-200. The "Loud and Quiet" section of the Route form (FE)

## Scope

**IN**

- The section "Loud and Quiet" of the Route form: seven tick boxes, the always-Loud note, "New Alerts remove the
  acknowledgement", the pre-fill from the Route profile, and the unavailable "Ack timeout" that ticks when the ack
  timeout is turned on (C-36 UI, FR-9).
- Read-only display without `routes:write`, the "Learn more" link, the Audit log diff of the two fields, and texts in
  English and Russian.

**OUT**

- The API, the fields and the profiles: S-197. The behaviour behind the ticks: S-198 and S-199.
- The documentation page: S-201. The Routes list is unchanged.

## Contracts

- **API used**: `listRouteProfiles` (both fields of `policy`), `getRoute`, `createRoute`, `updateRoute` (`If-Match`),
  `listAuditLog`. The fields are optional in the shared `RoutePolicy`, so the form reads an absent field as all seven
  Loud events and `new_alerts_remove_ack` false, as the Paging profile does, and always sends both.
- **Components**: `route-policy-loud-quiet.tsx` — the section, in the style of `route-policy-lifecycle.tsx` and
  `route-policy-timers.tsx`: `loudQuietSchema`, `LoudQuietValues`, `loudQuietValues(policy)`, `withLoudQuiet(policy,
  values)` and a read-only form of the section; `route-form.tsx` mounts it after the timers section and passes it the
  watched value of `ack_timeout_enabled`, and sets `loud_ack_timeout` to true when that value goes from off to on by the
  person's change (never on the first render of a stored Route); `route-policy-timers.tsx` exposes the watched value if
  it does not already; `audit-diff.tsx` — the labels of the two fields in the diff of `route.updated`.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): title "Loud and Quiet" /
  «Громкие и тихие события»; the introduction "Which events of this Route are Loud. A Quiet event is still delivered,
  without Mentions and without a sound where the messenger allows it. Whom to mention is set on each Destination." /
  «Какие события этого маршрута громкие. Тихое событие всё равно доставляется — без упоминаний и, где мессенджер это
  позволяет, без звука. Кого упоминать, задаётся в каждом месте доставки.»; "New Alert Group" / «Новая группа алертов»,
  "New Alerts" / «Новые алерты», "Reopen" / «Переоткрытие», "Ack timeout" / «Тайм-аут подтверждения», "End of
  Snooze" / «Конец откладывания», "Rise to critical" / «Переход в критические», "Start of a Storm" / «Начало шторма»;
  "The ack timeout is off on this Route" / «На этом маршруте тайм-аут подтверждения выключен»; the note / «Вызовы
  людей цепочками эскалации, напоминания, перехваты и упоминание владельца, потерявшего подтверждение, всегда громкие.»; "New
  Alerts remove the acknowledgement" / «Новые алерты снимают подтверждение» with "An acknowledged Alert Group becomes
  firing without an Owner when a new Alert joins it; its Escalation chains, or its ack timeout, start over." /
  «Подтверждённая группа алертов снова становится активной без владельца, когда в неё приходит новый алерт; её цепочки
  эскалации или тайм-аут подтверждения начинаются заново.»
- **Documentation link**: `field.loud_quiet` already exists in `web/src/lib/docs-links.json`; the section uses it with
  `about` = "Loud and Quiet" and adds no entry, so `docslinkcheck` is unaffected (S-201 extends the page that it names).
- **Permissions**: `routes:write` edits; without it the section is read-only, as the other sections are.
- **Fixtures**: the builders of a Route policy in the web tests (`route-form.test.tsx`, `route-editor.test.tsx`,
  `profile-picker.test.tsx`, `route-policy-timers.test.tsx`) carry the two fields.

## Steps

1. Write `route-policy-loud-quiet.tsx` with the schema, the values and the read-only form. Check:
   `route-policy-loud-quiet.test.tsx` (the seven boxes, the always-Loud note, the unavailable "Ack timeout").
2. Mount it in the Route form, pre-fill it from the profile and tick "Ack timeout" on the turn-on of the ack timeout.
   Check: `route-form.test.tsx`, `route-policy-timers.test.tsx`, `profile-picker.test.tsx`.
3. Add the Audit log labels and the texts. Check: `audit-diff.test.tsx`; `make ci` (missing keys).
4. Write `route-loud-quiet.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian.

1. Sign in as the Admin, open Routes → New Route, choose the Informational profile. Expect the section "Loud and Quiet"
   with the introduction, only "New Alert Group" ticked and "Ack timeout" unavailable with "The ack timeout is off on
   this Route".
2. Turn the ack timeout on in the timers section. Expect "Ack timeout" available and ticked; untick it, name the Route
   "certificates", save. Expect the Route page with "New Alert Group" ticked and the other six unticked.
3. Choose Paging on another new Route. Expect all seven ticked and "New Alerts remove the acknowledgement" unticked.
4. Open "certificates", tick "New Alerts remove the acknowledgement" and save. Open the Audit log. Expect `route.updated`
   with a diff of `new_alerts_remove_ack`. Check `curl -s -H "$A" $API/routes/$R | jq -c .policy.new_alerts_remove_ack`
   answers `true`.
5. Sign in as a Viewer. Expect the section read-only with the same ticks.
6. Switch to Russian. Expect the Russian texts, including «Новые алерты снимают подтверждение», and the Routes list
   unchanged.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the loud and quiet section to the route form`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The existing "Learn more" in the Delivery section of the form already points to the page on Loud and Quiet for the
  batching window; this story adds a second one beside the new title, which is the link C-36.FR-11 asks for.
- The form does not test the Route in a messenger: what a Quiet event looks like in each messenger is S-201's page.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-36.FR-9 | partial | the unavailable box that ticks on; the stored value is completed by S-197 |
| C-36.FR-11 | partial | the link from the form; the page is S-201 |
| C-36.AC-1 | partial | the form filled from the profile; the profiles and the stored Route are completed by S-197 |
| C-36.AC-10 | full | |
| C-08.FR-1 | partial | revised for L2: the Loud and Quiet section of the Route form; the fields are S-197's |
| C-08.FR-7 | partial | revised for L2: the profiles' Loud events and removal of an acknowledgement pre-fill the section; the profiles are S-197's |
| C-08 UI | partial | revised for L2: the section "Loud and Quiet" of the Route form |
