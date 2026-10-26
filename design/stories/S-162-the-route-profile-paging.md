---
id: S-162
title: The Route profile Paging, formerly On-call, with the id paging in the API, the UI and every client (BE)
capability: C-26
kind: be
layer: L2
depends_on: [S-111]
covers: [C-26.FR-11, C-26.AC-8, C-08.FR-7, C-08.AC-11, C-10.AC-23]
files_touched:
  - api/openapi.yaml
  - internal/routing/profiles.go
  - internal/routing/suggestions.go
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/suggestions_test.go
  - internal/routing/preview_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/groups/events_test.go
  - internal/groups/timers_test.go
  - internal/groups/dispatcher_test.go
  - internal/groups/groups_integration_test.go
  - internal/timers/schedule_test.go
  - test/e2e/routing_test.go
  - test/e2e/mattermost_test.go
  - web/src/components/profile-picker.tsx
  - web/src/components/route-policy-timers.test.tsx
  - web/src/lib/commands.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/support.ts
  - web/e2e/account-links.spec.ts
  - web/e2e/commands.spec.ts
  - web/e2e/delivery-state.spec.ts
  - web/e2e/destination-test.spec.ts
  - web/e2e/docs-links-forms.spec.ts
  - web/e2e/request-log.spec.ts
  - web/e2e/routes.spec.ts
  - web/e2e/route-preview.spec.ts
  - web/e2e/route-message.spec.ts
  - web/e2e/snooze-presets.spec.ts
  - web/e2e/several-connections.spec.ts
  - web/e2e/slack-destination.spec.ts
  - web/e2e/statistics.spec.ts
  - web/e2e/system-status.spec.ts
  - web/e2e/telegram-destination.spec.ts
  - web/e2e/timers.spec.ts
  - web/e2e/screenshots/routes-and-templates.spec.ts
  - web/e2e/screenshots/first-alert.spec.ts
  - docs/concepts/routes-and-group-keys.md
  - docs/concepts/statuses-and-commands.md
  - docs/first-alert.md
  - docs/outgoing-webhooks/recipes.md
  - docs/messengers/bridge.md
  - docs/messengers/write-a-bridge.md
  - docs/install/versions.md
acceptance:
  - "[C-26.FR-11, C-26.AC-8, C-08.FR-7, C-08.AC-11] `listRouteProfiles` returns the ids `paging` and `informational`, named \"Paging\" and \"Informational\", with the policy values of the Paging and Informational columns of defaults.md; the `RouteProfile.id` enum of the specification is `paging`, `informational`; the Default route created on a new database and both Route suggestions take the values of the Paging profile (`routes_test.go`, `suggestions_test.go`, `routing_test.go`)."
  - "[C-26.FR-11] Neither the id nor the name of the old profile is left: `grep -rnE 'on_call|onCall|On-call' internal test web/src web/e2e docs` lists only the upgrade notes and uses that mean the person On-call (CONTEXT.md) — the clicks on the button \"On-call\" and \"Profile: On-call\" of `route-preview.spec.ts`, `route-message.spec.ts` and `timers.spec.ts`, `gen.OnCall` of `routes_test.go`, the helpers `onCall()`, and every comment that names the profile, included — and `make e2e` passes."
  - "[C-26.FR-11, C-10.AC-23] In a real browser, Routes → \"Create route\" offers \"Paging\" with \"For alerts someone must act on: ack timeout and Reminders on.\" and \"Informational\"; choosing \"Paging\" pre-fills the Snooze durations 1 h, 4 h and 24 h, and a firing Root message of such a Route in the fake Mattermost server still shows the Snooze presets after \"Snooze 24 h\"; in Russian the profile reads «Оповещение» (`routes.spec.ts`, `commands.spec.ts`)."
  - "[C-26.FR-11] The documentation calls the profile Paging everywhere, and the upgrade notes of 0.2.0 in Requirements and tested versions say that `route-profiles` returns `paging` instead of `on_call`, which breaks clients that type the enum, and that nothing stored changes; `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-162. The Route profile Paging, formerly On-call, with the id paging in the API, the UI and every client (BE)

## Scope

**IN**

- The rename of C-26.FR-11: the Route profile On-call becomes Paging, with the id `paging`, in the API, the server,
  the SPA, the end-to-end suites, the screenshot specs and the documentation.
- The upgrade note of the rename.

**OUT**

- The Minimum severity of the profiles (`none` for both), which S-111 added.
- The rest of the Route form: S-112, S-119, S-123.

## Contracts

- **Spec** (`api/openapi.yaml`): `RouteProfile.id` enum `paging`, `informational`; the description of
  `listRouteProfiles` names Paging and Informational. Profiles are not stored, so no migration (C-26.FR-11).
- **Routing** (`internal/routing`): `ProfilePaging = "paging"` replaces `ProfileOnCall`, the profile is named "Paging";
  the Default route created at first start (`routes.go`) and the suggestions (`suggestions.go`) take it, with the same
  values as before.
- **SPA** (`profile-picker.tsx`, locales): the id `paging`; the keys `routes.profiles.paging` "Paging" / «Оповещение»
  and `routes.profiles.pagingLine` with the text of `onCallLine`, which replace `onCall` and `onCallLine`; the test id
  `route-profile-paging`.
- **End-to-end suites**: `web/e2e/support.ts` and the specs that read the profile's policy by its id or click its
  button — among them `route-preview.spec.ts` ("Profile: On-call"), `route-message.spec.ts` and `timers.spec.ts` —
  and the screenshot specs `routes-and-templates.spec.ts` and `first-alert.spec.ts`, which click "On-call", use the
  new id and name; `test/e2e/routing_test.go` and `internal/api/routes_test.go` (`gen.OnCall`) assert the new ids.
- **Names in code**: the helpers `onCall()` of `internal/routing` and of the groups tests become `paging()`, and the
  comments that name the On-call profile — in `internal/api/routes.go`, the tests of `internal/groups` and
  `internal/timers`, `test/e2e/mattermost_test.go`, `route-policy-timers.test.tsx`, `lib/commands.ts` and
  `snooze-presets.spec.ts` — name the Paging profile, so that the check of the acceptance finds nothing.
- **Documentation**: every "On-call" that names the profile becomes "Paging" (Routes and Group keys, Statuses and
  commands, First alert in a messenger, the recipes of outgoing webhooks, the Bridge and Write a bridge pages); the word
  On-call stays where it means the person (CONTEXT.md). `docs/install/versions.md` adds the rename to the entry 0.2.0
  of its upgrade notes.
- **Settings**: the Paging column of defaults.md, already named so; no value changes.

## Steps

1. Rename in the spec, routing and the SPA. Check: `make generate-check`; `make lint`; the Go tests.
2. Rename in the end-to-end suites, the screenshot specs, the comments and the documentation. Check: `grep -rnE
   'on_call|onCall|On-call' internal test web/src web/e2e docs` finds only the upgrade notes; `make e2e`;
   `make docs-check`.
3. Take the screenshots of the two guides again with `make docs-screenshots GUIDES="routes-and-templates first-alert"`.
   Check: the images show "Paging".

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's Personal access token A as in S-032
curl -s "${A[@]}" localhost:8080/api/v1/route-profiles | jq -c '[.items[] | {id, name}]'
# [{"id":"paging","name":"Paging"},{"id":"informational","name":"Informational"}]
curl -s "${A[@]}" localhost:8080/api/v1/routes | jq -c '.items[] | select(.is_default) | .policy.snooze_durations_seconds'
# [3600,14400,86400]
grep -rnE 'on_call|onCall|On-call' internal test web/src web/e2e docs | grep -v 'install/versions.md'      # (nothing)
```

Then in Playwright at 1280 × 800: Routes → "Create route" shows "Paging" with its line and "Informational"; "Paging" →
the Snooze section shows "1 h", "4 h", "24 h"; switch the language to Russian → «Оповещение». `make e2e` runs the
suites with the new id.

## Open questions

1. **The Russian name of the profile Paging.** The PRD gives only the English name, and the approved mockups do not show
   the profile. Options: (a) «Оповещение», after the mockups' «Оповестить дежурного» for a Page; (b) «Вызов»; (c) keep
   «Дежурный», the L1 name. Recommended: (a); the contract above uses it, and (c) is ruled out because «Дежурный» is the
   On-call person.

## Notes

- Suggested commit: `feat(routing): rename the route profile on-call to paging`.
- Split from S-111 when the contracts were written: the id appears in thirteen web end-to-end specs, their support
  file and two screenshot specs, and together with the Minimum severity the story touched about 55 files. It keeps its
  place right after S-111 in the story map.
- With 47 files the story is at the edge of the size signal, but every change is the same rename; the review of the
  contracts asked for the comments too, so that the check of the acceptance can be strict.
- The screenshots are taken by hand and committed, as `make docs-screenshots` says; they are never listed.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-11 | full | |
| C-26.AC-8 | partial | the profile ids; the migration and the read-only fields are S-111 |
| C-08.FR-7 | full | revised for L2 |
| C-08.AC-11 | full | revised for L2 |
| C-10.AC-23 | partial | revised for L2: the Paging durations; the buttons of the Destination's set are S-113 |
