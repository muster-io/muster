---
id: S-123
title: The Route form with Destination conditions, the Teams from the label and the preview, the Destinations of a Team, and the delivery state by entry (FE)
capability: C-26
kind: fe
layer: L2
depends_on: [S-112, S-120, S-122]
covers: [C-26.FR-1, C-26.FR-2, C-26.FR-5, C-26.FR-12, C-26.AC-9, C-08.FR-1, C-09.FR-11, C-09.FR-14]
files_touched:
  - web/src/components/route-form.tsx
  - web/src/components/route-destinations.tsx
  - web/src/components/route-destinations.test.tsx
  - web/src/components/route-teams-option.tsx
  - web/src/components/route-teams-option.test.tsx
  - web/src/components/group-key-editor.tsx
  - web/src/components/route-delivery-preview.tsx
  - web/src/components/route-delivery-preview.test.tsx
  - web/src/components/route-list.tsx
  - web/src/routes/routes.new.tsx
  - web/src/routes/routes.$routeId.tsx
  - web/src/components/team-destinations.tsx
  - web/src/components/team-destinations.test.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/routes.spec.ts
  - web/e2e/route-preview.spec.ts
  - web/e2e/teams.spec.ts
  - web/e2e/delivery-state.spec.ts
  - web/e2e/docs-links-forms.spec.ts
  - web/e2e/screenshots/routes-and-templates.spec.ts
acceptance:
  - "[C-26.FR-1, C-08.FR-1] The Route form's \"Route Destinations\" reads \"Where the Route sends the messages of its Alert Groups; each Destination may have a Severity condition\" and gives each Destination the choice \"Any severity\", \"Severity = critical\" or \"Severity < critical\", saved as `destination_entries`; the Routes list shows each Route's Destinations with their conditions and \"+ Teams from the label\" where it delivers to Teams (`route-destinations.test.tsx`, `routes.spec.ts`)."
  - "[C-26.FR-2, C-26.FR-12, C-26.AC-9] Under the Destinations, \"Also to the Teams from the label team\" with its hint \"The Alert Group also goes to the Destinations of the Teams whose values are in its Alerts, with their Severity conditions. A new Team adds channels and removes none.\" and, under it, \"Run the Teams' Escalation chains\"; with the option on and no `team` in the Group key, the form shows \"Alerts of different Teams with the same key share one Alert Group, shown in the channels of all of them. Add team to the key if each Team needs its own.\" and \"Add team to the key\", which adds it (`route-teams-option.test.tsx`)."
  - "[C-26.FR-12, C-26.AC-9] The preview \"Where alerts go\" follows the unsaved settings and lists for recent Alerts the Alert Groups with their Teams and the Destinations each reaches, marking those reached through a Team, and how many Alert Groups reach each Destination (`route-delivery-preview.test.tsx`, `route-preview.spec.ts`)."
  - "[C-26.FR-1, C-26.AC-9] The Team page has \"Destinations of the Team\" — Destination, Connection and condition, with \"Add\" and \"Remove\" for an Admin — and the line \"The Alert Groups of payments arrive here from the Routes that deliver to the Teams from the label: Infrastructure.\"; a Destination's page lists the Teams that name it (`team-destinations.test.tsx`, `teams.spec.ts`)."
  - "[C-26.FR-5, C-09.FR-11, C-09.FR-14] The Alert Group page lists, in its delivery state, by which entry each Destination was reached — \"Route\" or \"Team payments\", with the condition — and its Timeline shows `destination_joined` as \"Joined search-notice\" (`alert-group-deliveries.test.tsx`, `timeline.test.tsx`, `delivery-state.spec.ts`)."
  - "Every new text exists in English and Russian; \"Learn more\" next to the option opens Routing to Teams; the screenshot of the Route's Destinations is taken again, and `make e2e` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 412
---

# S-123. The Route form with Destination conditions, the Teams from the label and the preview, the Destinations of a Team, and the delivery state by entry (FE)

## Scope

**IN**

- The Route form: a Severity condition per Destination, "Also to the Teams from the label {team label}" with "Run the
  Teams' Escalation chains", the Group key hint with "Add {team label} to the key", and the preview "Where alerts go"
  (C-26 UI).
- The Routes list with each Route's Destinations, conditions and "+ Teams from the label".
- "Destinations of the Team" on the Team page, and the Teams of a Destination on its page.
- The entries that reached each Destination in the delivery state of the Alert Group page, and `destination_joined` in
  its Timeline.

**OUT**

- The API: S-121, S-170, S-122. The Minimum severity and the owning Team on the form: S-112, S-119.
- The Escalation chains of the Route and of the Routes list: S-158.
- The documentation of routing to Teams beyond the sections of S-121 and S-122: S-124.

## Contracts

- **API used**: `getRoute`, `createRoute`, `updateRoute` (`destination_entries`, `deliver_to_teams`,
  `run_team_chains`, `group_key`), `listRoutes` (the list and the Routes that deliver to Teams), `previewGroupKey`
  (`delivery`, `shared_key_warning`), `getOrganization` (`team_label`), `getTeam`, `updateTeam`
  (`destination_entries`), `listDestinations`, `getDestination` (`teams`), `listAlertGroupDeliveries` (`entries`),
  `getAlertGroupTimeline` (`destination_joined` with `destination`).
- **Route form** (`route-form.tsx`, `route-destinations.tsx`, `route-teams-option.tsx`, `group-key-editor.tsx`,
  `route-delivery-preview.tsx`): the Destinations section with a condition select per row (test id
  `route-destination-condition-<id>`); the option and its sub-option (`route-deliver-to-teams`,
  `route-run-team-chains`), the sub-option disabled while the option is off; the hint of reference.md under the Group
  key when the option is on and the key lacks the Team label, with the button that appends the label to the key; the
  preview panel after the section "Severity, Escalation chains and owner", refreshed on a change of the Matchers, the
  key, the Minimum severity, the entries or the option, as the Group key preview is, a Team Destination marked "via
  {team}".
- **Routes list** (`route-list.tsx`): the Destinations with their conditions ("critical", "< critical") and "+ Teams
  from the label".
- **Team page** (`team-destinations.tsx`): the table Destination, Connection, condition, with "Add Destination" and
  "Remove" for an Admin, the line with the Routes that deliver to Teams, or "No Route delivers to the Teams from the
  label yet." when none does.
- **Destination page** (`destinations.$destinationId.tsx`): "Teams" beside "Routes".
- **Alert Group page** (`alert-group-deliveries.tsx`, `timeline.tsx`): each delivery's entries — "Route" or "Team
  {team}", with the condition; `destination_joined` "Joined {destination}".
- **Audit log** (`audit-diff.tsx`): `/destination_entries` "Destinations", `/deliver_to_teams` "Also to the Teams from
  the label", `/run_team_chains` "Run the Teams' Escalation chains".
- **Documentation links**: `field.deliver_to_teams` → `concepts/routing-to-teams.md#also-to-the-teams-from-the-label`,
  `field.severity_condition` → `concepts/routing-to-teams.md#severity-conditions`; the anchors exist from S-121.
- **Texts** (EN / RU), from C-26's UI, reference.md and the approved mockups: "Route Destinations" / «Места доставки
  маршрута», its hint / «Куда маршрут отправляет сообщения своих групп алертов. У каждого места доставки может быть
  условие по важности.», "Any severity" / «любая важность», "Severity = critical" / «важность = critical», "Severity <
  critical" / «важность < critical», "Also to the Teams from the label {{label}}" / «Ещё командам из метки {{label}}»,
  its hint / «Группа приходит и в места доставки команд, чьи значения {{label}} есть в её алертах, с их условиями
  важности. Новая команда добавляет места доставки, ничего не убирает.», "Run the Teams' Escalation chains" / «Запускать
  цепочки эскалации команд», "Add {{label}} to the key" / «Добавить {{label}} в ключ», "Where alerts go" / «Куда придут
  алерты», "Destinations of the Team" / «Места доставки команды», the Team line / «Сюда приходят группы команды {{team}}
  из маршрутов, где отмечено «ещё командам из метки {{label}}»: {{routes}}.».

## Steps

1. Write the conditions, the option and the Group key hint on the form. Check: `route-destinations.test.tsx`,
   `route-teams-option.test.tsx`; `make lint`.
2. Write the preview panel. Check: `route-delivery-preview.test.tsx` with a mocked `previewGroupKey`.
3. Write the Destinations of the Team and the Teams of a Destination. Check: `team-destinations.test.tsx`.
4. Show the entries and `destination_joined` on the Alert Group page. Check: `alert-group-deliveries.test.tsx`,
   `timeline.test.tsx`.
5. Write the end-to-end specs and take the screenshot again. Check: the Verification below; `make e2e`;
   `make docs-screenshots GUIDES="routes-and-templates"`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal, as in S-121, create the
Destinations `infra-critical`, `infra-notice`, `payments-critical`, `payments-notice` and `search-notice`, the Teams
"payments" and "search", and fire Alerts of `category="infra"` for "payments" (warning) and "devops" (critical). Then in
Playwright at 1280 × 800, in English and in Russian:

1. Routes → "Create route" → "Paging" → Matcher `category="infra"`, Group key `alertname`, `system`; Destinations
   `infra-critical` "Severity = critical" and `infra-notice` "Severity < critical"; "Also to the Teams from the label
   team" on → the hint and "Add team to the key" appear; the preview "Where alerts go" lists the Alert Group of
   payments with `infra-notice` and `payments-notice` "via payments", and that of devops with `infra-critical`.
2. "Add team to the key" → the key ends with `team` and the hint goes; "Save" → the Routes list shows "infra-critical
   (critical), infra-notice (< critical) + Teams from the label".
3. Teams → "payments" → "Destinations of the Team" → "Add Destination" `payments-critical` "Severity = critical" and
   `payments-notice` "Severity < critical" → "Save"; the line names the Route.
4. Fire a new Alert of "payments" → its Alert Group → the delivery state lists `infra-notice` "Route" and
   `payments-notice` "Team payments"; fire an Alert of "search" into it → the Timeline shows "Joined search-notice".
5. "Learn more" next to the option opens Routing to Teams at "Also to the Teams from the label".

`make e2e` runs these steps in `routes.spec.ts`, `route-preview.spec.ts`, `teams.spec.ts` and `delivery-state.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): route to the teams from the label with severity conditions and a delivery preview`.
- The approved Route mockup chooses one Escalation chain; that part is S-158's and follows C-32.FR-2 (L2.md §5.2).
- "Run the Teams' Escalation chains" is on the form from now on, as the API stores it (S-121); it acts once chains
  run (S-169).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-1 | partial | the forms; the API is S-121 |
| C-26.FR-2 | partial | the form; the API is S-121 |
| C-26.FR-5 | partial | `destination_joined` on the Alert Group page |
| C-26.FR-12 | partial | the form, the hint and the preview; the API is S-122 |
| C-26.AC-9 | full | |
| C-08.FR-1 | partial | revised for L2: the Route form's Destinations with conditions and the option |
| C-09.FR-11 | partial | revised for L2: `destination_joined` in the Timeline |
| C-09.FR-14 | partial | revised for L2: the entries that reached each Destination in the delivery state; the Teams in the header are S-120, the Assignees and running chains S-159 |
