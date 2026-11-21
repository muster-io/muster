---
id: S-237
title: The step-by-step test on the Destination page with its steps, requests, the people a step would mention and the presses (FE)
capability: C-42
kind: fe
layer: L2
depends_on: [S-231, S-232, S-233, S-234, S-048, S-090]
covers: [C-42.FR-1, C-42.FR-2, C-42.FR-3, C-42.FR-4, C-42.FR-5, C-42.FR-11, C-42.AC-1, C-42.AC-3, C-42.AC-4, C-42.AC-5, C-42.AC-9]
files_touched:
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/destination-test-panel.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/destination-step-test.tsx
  - web/src/components/destination-step-test.test.tsx
  - web/src/components/destination-test-step.tsx
  - web/src/components/destination-test-step.test.tsx
  - web/src/components/test-source-picker.tsx
  - web/src/lib/destination-test-run.ts
  - web/src/lib/destination-test-run.test.ts
  - web/src/test/fixtures/destination-test-run.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/destination-test-run.spec.ts
  - web/e2e/destination-test.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-42.FR-1, C-42.AC-1] Destination → \"Test\" offers \"Send test message\" (C-16, unchanged) and \"Step-by-step test\" with the source selector of C-16 — a recent Alert Group of one of the Destination's Routes or \"Example\" — and \"Severity level\", by default the source's; \"Start\" makes a run with `createDestinationTestRun` and shows the Test message's first step; the page offers it to a User with `destinations:test` only, and an Admin sees every step through \"Next: {step}\" in order — New Alert, Acknowledge, Reminder, Takeover, Snooze, Resolve, Unresolve — making each step only when pressed (`destination-step-test.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-2, C-42.FR-3] Each step is a block with its name, its outcome (\"Sent\" or the text of its error class), the duration, and under folds \"Request\" and \"Answer\" with Secrets, the Signing secret and tokens masked and the answer as plain text; \"Next: {step}\" is disabled while a step is being made; a step that ends `limited` or fails shows \"Try again\", which sends the same step again, and the run stops there; \"End the test\" deletes the run, and the page says that the Test messages stay in the Destination (`destination-test-step.test.tsx`, `destination-step-test.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-4, C-42.AC-4] Each Loud step shows \"Would mention:\" with each person, group, Team and On-call by name, and an entry that would mention nobody with its reason, such as \"has no linked Mattermost account\"; the Reminder and the Takeover show the Owner and the previous Owner; the page says that a Test message mentions nobody, so that a test notifies nobody's devices, and in the browser the fake Mattermost server's Test message carries no `@` Mention markup (`destination-test-step.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-5, C-42.AC-3] The buttons of the Test message follow from the step's status; \"Presses that reached Muster\" lists each press the run read (`getDestinationTestRun`) with the person, the button and the time, refreshed while the run is open, and says that a press changes nothing; for a Slack or a Bridge Destination it says that the bot cannot press its own button, so a person's press on the Test message in the messenger checks presses; in a real browser a press by Ivan on Acknowledge in the fake Mattermost server shows as \"A press reached Muster: @ivan, Acknowledge\" (`destination-test-step.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-1, C-42.AC-5] A run left alone expires: after the development clock advances past `destination_test.idle_timeout` the next \"Next: {step}\" answers `404` and the page says that the test ended after an hour without a step, offering \"Start\" again; the Test messages stay in the fake server; two runs of one Admin in two tabs go on without touching each other (`destination-step-test.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-3] A Broken Destination whose first step succeeds shows \"Healthy\" without a reload, as the one-message test does, and the health of a failed step leaves it Broken (`destination-test-panel.test.tsx`, `destination-test-run.spec.ts`)."
  - "[C-42.FR-11, C-42.AC-9] At 360 CSS pixels wide the steps stand one under the other, requests and answers are folded, and the page does not scroll sideways, in English and in Russian; every new text exists in English and Russian with Russian plurals, and a missing key fails `make ci` (`phone-width.spec.ts`, `destination-test-run.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-237. The step-by-step test on the Destination page with its steps, requests, the people a step would mention and the presses (FE)

## Scope

**IN**

- "Step-by-step test" in the Test panel of the Destination page: the source and Severity level, "Start", the steps with
  "Next: {step}", "Try again" and "End the test", each step's "Request", "Answer", "Would mention:" and "Presses that
  reached Muster", the expiry and the phone layout (C-42.FR-1 to FR-5, FR-11, UI).

**OUT**

- The "Escalation chains" part of the panel, with the offered chains and the timeline: S-238; this story leaves its
  place
  in the panel.
- The API: S-231 to S-236. The documentation and its link: S-239.
- The one-message test "Send test message": unchanged (S-048).

## Contracts

- **API used**: `createDestinationTestRun`, `getDestinationTestRun`, `deleteDestinationTestRun`,
  `createDestinationTestRunStep` of the generated client; `testDestination` for the old button; `listAlertGroups`
  of the source picker as S-048 uses it; `getDestination` for the health after a step.
- **Components**: `destination-test-panel.tsx` — gains the section "Step-by-step test" beside "Send test message", with
  the hint of what each type sends in a step, and a slot for S-238's "Escalation chains"; `destination-step-test.tsx` —
  the run: the source and Severity form, "Start", the list of steps, the state of the run in the page's own state (the
  run id; not persisted: a reload ends the page's view, not the run), the polling of the run for presses while it is
  open, `404` handling; `destination-test-step.tsx` — one step: heading, outcome, folds of request and answer reusing
  `rendered-request.tsx`, "Would mention:", the buttons and "Presses that reached Muster". `test-source-picker.tsx` is
  reused and gains the Severity level where it needs a prop. `web/src/lib/destination-test-run.ts` holds the step names,
  the order, the text of "Next: {step}" and the expiry message.
- **Permissions**: `destinations:test`, which only Admins have; the section is absent without it.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Step-by-step test" /
  «Пошаговый тест»; "Start" / «Начать»; "Next: {step}" / «Дальше: {step}»; "Try again" / «Повторить»; "End the test" /
  «Завершить тест»; the step names — "Created", "New Alert", "Acknowledge", "Reminder", "Takeover", "Snooze", "Resolve",
  "Unresolve" / «Создан», «Новый алерт», «Подтверждение», «Напоминание», «Перехват», «Откладывание», «Решение»,
  «Возобновление»; "Would mention:" / «Упомянул бы:»; "Presses that reached Muster" / «Нажатия, дошедшие до Muster»;
  "A press reached Muster: {person}, {button}" / «Нажатие дошло до Muster: {person}, {button}»; "Severity level" as the
  page's word; the hints and the expiry text.
- **Fixtures**: `web/src/test/fixtures/destination-test-run.ts` — builders of runs, steps, `WouldMention` and presses,
  used by S-238's tests too.

## Steps

1. Write `web/src/lib/destination-test-run.ts`. Check: `destination-test-run.test.ts` (the order, the names, the
   expiry).
2. Write the step component. Check: `destination-test-step.test.tsx` (the folds, masking shown, "Would mention:" with a
   reason, the presses, the Slack and Bridge hint).
3. Write the run and the panel section. Check: `destination-step-test.test.tsx` (Start, Next, a failed step and "Try
   again", End, `404`, two runs), `destination-test-panel.test.tsx` (the health).
4. Add the texts. Check: `make ci` (missing keys).
5. Write `destination-test-run.spec.ts`, extend `destination-test.spec.ts` and `phone-width.spec.ts`. Check: the
   Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian, with the Mattermost
Destination "infra-critical" whose Mention entries name `@devops` and olga, who has no Account link, and the fake
Mattermost server.

1. Sign in as the Admin, open Destinations → infra-critical → "Test" → "Step-by-step test", choose "Example" and
   "critical",
   press "Start". Expect the first step "Created" with "Sent", "Request" and "Answer" folded and, opened, the bot token
   shown as `[redacted]`, and "Would mention: @devops, olga (has no linked Mattermost account)"; the fake server holds
   the post marked "🧪 Test message" and `curl -s $FMM/posts | jq -r '.[0].message' | grep -c '@olga'` prints 0.
2. Press "Next: New Alert": expect the Root message edited to two Alerts and a Thread reply in the fake server. Press
   through "Acknowledge", "Reminder", "Takeover", "Snooze", "Resolve" and "Unresolve", each showing its requests and
   answers; expect "Next:" to stop after the last and the run to end.
3. Start a run again. In the fake Mattermost server press Acknowledge as Ivan (`POST $FMM/press`): expect "A press
   reached
   Muster: @ivan, Acknowledge" within the polling interval and the private answer "This is a test message; nothing was
   changed". For a Slack Destination expect the hint that the bot cannot press its own button.
4. Script a fault in the fake server (`/_fake/faults`) for the next request: expect the step's error and "Try again",
   and
   no "Next:" until it succeeds; press "Try again": the step is made. Press "End the test": expect the run gone and the
   Test messages still in the fake server.
5. Start a run, advance the clock with `curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 3660}'`, press
   "Next:": expect the notice that the test ended after an hour without a step and "Start". Open the page in a second
   tab
   and run two runs at once: each shows its own steps.
6. Make the Destination Broken (the fake server refuses), run the first step after it recovers: expect "Healthy"
   without a
   reload. Sign in as a Responder: expect no "Step-by-step test".
7. At 360 px: the steps one under the other, requests folded, no horizontal scrolling. Switch to Russian: the Russian
   texts and the step names.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the step-by-step test of a destination with its steps and presses`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The run lives in the database, not in the page, so closing the page leaves a run that ends an hour later (C-42
  scenario
  6); the page does not offer to resume it, which the capability does not ask for.
- The design pass of the UI belongs to the later layers (journal D315); this panel uses the existing components.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-1 | partial | the form, "Start" and the expiry message; the run and its expiry are S-231 |
| C-42.FR-2 | partial | the steps in the browser; the steps themselves are S-231 to S-233 |
| C-42.FR-3 | partial | the results, "Try again" and the health; the path and masking are S-231 to S-233 |
| C-42.FR-4 | partial | "Would mention:" and the Mention-free message in the browser; the computation is S-231 to S-233 |
| C-42.FR-5 | partial | the presses listed in the browser; the recording is S-234 |
| C-42.FR-11 | partial | the step-by-step part at 360 px and in Russian; the chains' part is S-238 |
| C-42.AC-1 | partial | the eight steps in the browser; the messenger's requests are S-231 |
| C-42.AC-3 | partial | the press listed; the answer in each messenger is S-234 |
| C-42.AC-4 | partial | the reason shown; the computation is S-231 to S-233 |
| C-42.AC-5 | partial | the expiry in the browser; the run's expiry is S-231 |
| C-42.AC-9 | partial | the step-by-step part at 360 px; the timeline is S-238 |
