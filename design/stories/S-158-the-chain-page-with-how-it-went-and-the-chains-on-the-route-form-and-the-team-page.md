---
id: S-158
title: The Escalation chain page with its steps and "How it went", and the chains on the Route form and the Team page (FE)
capability: C-32
kind: fe
layer: L2
depends_on: [S-123, S-150, S-157]
covers: [C-32.FR-1, C-32.FR-2, C-32.FR-3, C-32.FR-17, C-32.AC-14, C-32.AC-17, C-30.FR-9, C-25.FR-2, C-08.FR-1]
files_touched:
  - web/src/routes/escalation-chains.index.tsx
  - web/src/routes/escalation-chains.new.tsx
  - web/src/routes/escalation-chains.$chainId.tsx
  - web/src/components/escalation-chain-form.tsx
  - web/src/components/escalation-chain-form.test.tsx
  - web/src/components/escalation-steps-editor.tsx
  - web/src/components/escalation-steps-editor.test.tsx
  - web/src/lib/escalation-steps.ts
  - web/src/lib/escalation-steps.test.ts
  - web/src/components/escalation-chain-runs.tsx
  - web/src/components/escalation-chain-runs.test.tsx
  - web/src/components/route-chains.tsx
  - web/src/components/route-chains.test.tsx
  - web/src/components/route-form.tsx
  - web/src/components/route-list.tsx
  - web/src/components/team-chains.tsx
  - web/src/components/team-chains.test.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/routes/teams.index.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/escalation-chains.spec.ts
  - web/e2e/routes.spec.ts
  - web/e2e/teams.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-32.FR-1, C-32.FR-3, C-32.AC-14, C-30.FR-9] In a real browser an Admin builds the payments chain of scenario 1 of C-32 on its page: \"Steps\" numbered, each with its target, normal or important, and its limits as chips (\"Severity: any\", \"always\", \"only in the working hours of payments (Mon–Fri 11–19)\", \"Severity = critical\"), and \"Add step\" with \"Page the On-call of a Schedule\", \"Page people\", \"Page the whole Team\", \"Wait\", \"Send to an outgoing webhook\", \"Repeat from step\" and \"Call through the Notification gateway\" — the last greyed with \"Unavailable: a Notification gateway is needed (a bridge for calls and SMS)\" when there is none — the hint about limits, \"Whom 'Page the whole Team' calls\" with the unavailable choices greyed with their reason, and \"Normal and important\" with its text; saved, it reads back the same, in English and in Russian (`escalation-chain-form.test.tsx`, `escalation-steps-editor.test.tsx`, `escalation-chains.spec.ts`)."
  - "[C-32.FR-2, C-32.FR-17] The chain page says \"Writes only in the Threads of payments-critical and payments-notice. Starts at publication.\" and when it stops and starts over, \"Run for\" with \"any severity\", \"Severity = critical\" and \"Severity < critical\" for the Team's reference, whose Alert Groups it runs on, and the chain's warnings, such as a Telegram Destination that cannot be mentioned for everyone (`escalation-chain-form.test.tsx`)."
  - "[C-32.AC-17] \"How it went\" lists the recent runs with their Alert Groups, Pages, undelivered Pages, skipped steps with their reasons and the acknowledgement that stopped the chain (`escalation-chain-runs.test.tsx`, `escalation-chains.spec.ts`)."
  - "[C-32.FR-2, C-32.FR-17, C-32.AC-17, C-08.FR-1] The Route form's \"Escalation chains\" names chains with their Severity conditions and the hint \"Pages only in the Threads of the Route's Destinations. The Teams from the label Page with their own chains.\"; a Route with a chain, no Destination and \"Also to the Teams from the label\" off shows \"Only a Notification gateway can reach anyone from this Escalation chain: the Route has no Destination.\", which goes once a Destination is named; the Routes list shows each Route's chains (`route-chains.test.tsx`, `routes.spec.ts`)."
  - "[C-32.FR-2, C-25.FR-2] The Team page's \"Escalation chain of the Team\" shows the chains the Team names with their conditions, and the chains it owns; the list of chains filters by Team and \"My Teams\"; Admins edit, others read (`team-chains.test.tsx`, `teams.spec.ts`)."
  - "Every new text exists in English and Russian; the chain page has \"Learn more\" to Concepts → Escalation chains and the Route form's section to \"Naming chains on Routes and Teams\"; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-158. The Escalation chain page with its steps and "How it went", and the chains on the Route form and the Team page (FE)

## Scope

**IN**

- The list of Escalation chains and the chain page — its Destinations, when it starts and stops, the Severity condition
  of the Team's reference, the steps with their chips and "Add step" with the seven kinds, the choices of "Page the
  whole Team", "Normal and important", the warnings and "How it went" (C-32 UI, C-32.FR-1, FR-3, FR-17).
- The chains on the Route form and in the Routes list, with the warning of a Route without a Destination (C-32.FR-2,
  FR-17; C-08.FR-1 revised).
- "Escalation chain of the Team" on the Team page and the filters of the chain list by Team (C-32.FR-2, C-25.FR-2).

**OUT**

- The API: S-154, S-169, S-155, S-157. Running chains and Assignees on the Alert Group page, the list and Home: S-159.
- The step-by-step test of a Destination with an accelerated run of chains: specified later in L2 (L2.md §1.5).

## Contracts

- **API used**: `listEscalationChains`, `createEscalationChain`, `getEscalationChain`, `updateEscalationChain`,
  `deleteEscalationChain`, `listEscalationChainRuns`, `getRoute`, `updateRoute`, `getTeam`, `updateTeam`,
  `listSchedules`, `listTeams`, `listUsers`, `listDestinations`, `listConnections` (`role=gateway`).
- **Routes**: `/escalation-chains` (the list, with the filter by Team of S-119), `/escalation-chains/new` and
  `/escalation-chains/$chainId`; the Teams list and the Team page link to them, and the main navigation keeps its
  entries (S-072).
- **Components**: `escalation-chain-form.tsx` — name, description, owning Team, the Destinations it writes in, the start
  and stop texts, the Team's "Run for", whose Alert Groups it runs on, the warnings; `escalation-steps-editor.tsx` — the
  numbered steps, "Add step" with the seven kinds and their fields, the chips of limits ("only in the working hours of
  {Team} ({hours})", or "of the Organization" for a chain without an owning Team), the call step through
  `gateway-unavailable.tsx` of S-150; `lib/escalation-steps.ts` — a step and its limits in words;
  `escalation-chain-runs.tsx` — "How it went"; `route-chains.tsx` — the chains of the Route form with their conditions,
  the hint and the warning; `team-chains.tsx` — the Team page's section; `route-list.tsx` — the chains column;
  `audit-diff.tsx` — the entries `escalation_chain.*` and `chain_entries`.
- **Documentation links** (`docs-links.json`): `title.escalation_chain` → `concepts/escalation-chains.md`,
  `field.route_chains` → `concepts/escalation-chains.md#naming-chains-on-routes-and-teams`, `field.chain_steps` →
  `concepts/escalation-chains.md#steps`; the sections exist from S-154.
- **Texts** (EN / RU), from C-32's UI and the approved Escalation mockup: "Escalation chain of the Team" / «Цепочка
  эскалации команды», "Writes only in the Threads of {{destinations}}. Starts at publication." / «Пишет только в треды
  {{destinations}}. Стартует при публикации.», the stop text / «Останавливается при подтверждении, отложении или
  закрытии группы; запускается заново при снятии подтверждения, конце отложения, переоткрытии и росте до critical,
  который снимает подтверждение или отложение.», "Run for" / «Запускать для», "any severity" / «любая важность»,
  "Severity = critical" / «важность = critical», "Severity < critical" / «важность < critical», "Steps" / «Шаги», "Add
  step" / «Добавить шаг», the seven kinds / «Позвать дежурного по расписанию», «Позвать людей», «Позвать всю команду»,
  «Ждать», «Отправить в исходящий вебхук», «Повторить с шага», «Позвонить через шлюз оповещений», the hint about limits
  / «Любой шаг можно ограничить рабочим временем и условием по важности. Шаги через шлюз оповещений не зависят от
  мессенджера, но нужен сам шлюз.», "Whom 'Page the whole Team' calls" / «Кого зовёт „Позвать всю команду“», "Normal and
  important" / «Обычно и важно», its text / «Что именно происходит, решают личные правила уведомлений человека. Личных
  сообщений Muster не шлёт: призыв — это упоминание в треде корневого сообщения.», "How it went" / «Как прошла»,
  "Escalation chains" / «Цепочки эскалации», the Route hint / «Зовёт только в тредах мест доставки маршрута. Команды из
  лейбла зовут своими цепочками.», the warning of a Route without a Destination / «Только шлюз оповещений может до
  кого-то дозвониться из этой цепочки: у маршрута нет места доставки.».

## Steps

1. Write the step in words and the steps editor. Check: `escalation-steps.test.ts`, `escalation-steps-editor.test.tsx`.
2. Write the chain page with its texts, warnings and "How it went", and the list. Check:
   `escalation-chain-form.test.tsx`, `escalation-chain-runs.test.tsx`.
3. Add the chains to the Route form and the Routes list, with the warning. Check: `route-chains.test.tsx`.
4. Add the Team page's section and the links from the Teams pages. Check: `team-chains.test.tsx`.
5. Add the links, texts and end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev` with the Teams "devops" and "payments" of scenario 1 of C-32, their Schedules and Destinations, and
the demo gateway removed for step 1 (S-145). In Playwright at 1280 × 800, in English and in Russian, as the Admin:

1. Teams → payments → "Escalation chain of the Team" → "New chain": build the seven steps of scenario 1; "Add step"
   shows "Call through the Notification gateway" greyed with "Unavailable: a Notification gateway is needed (a bridge
   for calls and SMS)"; step 6 shows "only in the working hours of payments (Mon–Fri 11–19)"; Save; the page reads
   "Writes only in the Threads of payments-critical and payments-notice. Starts at publication."
2. With the demo gateway back, "Call through the Notification gateway" is available.
3. Add a Telegram Destination to payments → the chain page warns that step 6 cannot mention everyone there.
4. Routes → "Infrastructure" → "Escalation chains" → name "devops" with "any severity" and remove both Destinations,
   with "Also to the Teams from the label" off → "Only a Notification gateway can reach anyone from this Escalation
   chain: the Route has no Destination."; name a Destination → the warning is gone.
5. After an Alert Group ran through the payments chain and was acknowledged (S-155's Verification), "How it went" lists
   it with "Step 1", "Page not delivered: @petr has no linked Mattermost account", "Step 3 skipped: only for critical"
   and the acknowledgement that stopped the chain.
6. As a Responder the chain page is read-only.

`make e2e` runs these steps in `escalation-chains.spec.ts`, `routes.spec.ts` and `teams.spec.ts`;
`docs-links-forms.spec.ts` follows the new links.

## Open questions

1. **Where the list of Escalation chains is reached.** The approved mockups reach a chain from its Team, and the main
   navigation has eight entries (S-072). Options: (a) no new entry in the main navigation; the list at
   `/escalation-chains` is linked from the Teams list, each Team page and the Route form; (b) an entry "Escalation
   chains" in the main navigation. Recommended: (a); the contract follows it.

## Notes

- The Russian texts follow the vocabulary of S-162's Open question 1.
- Suggested commit: `feat(web): build escalation chains and name them on routes and teams`.
- The approved Route mockup chooses one chain; the form names several, as C-26.FR-2 and C-32.FR-2 do (L2.md §5.2).
- The chip of a step limited to Working hours names the Organization when the chain has no owning Team (C-25.FR-9).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-1 | partial | the chain page |
| C-32.FR-2 | partial | the Route form and the Team page |
| C-32.FR-3 | partial | the form of the steps |
| C-32.FR-17 | partial | the Route's warning and the chain's warnings on the page |
| C-32.AC-14 | partial | the chain page; the Alert Group page is S-159 |
| C-32.AC-17 | partial | the Route's warning and "How it went" |
| C-30.FR-9 | partial | the call step unavailable without a gateway |
| C-25.FR-2 | partial | the chains of a Team and the filter of their list |
| C-08.FR-1 | partial | revised for L2: the chains on the Route form |
