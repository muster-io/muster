---
id: S-119
title: Teams list and Team page with people, identity provider groups and Working hours, the claims of the Role mapping, and the filters by Team (FE)
capability: C-25
kind: fe
layer: L2
depends_on: [S-115, S-117, S-118, S-121, S-112]
covers: [C-25.FR-2, C-25.FR-3, C-25.FR-4, C-25.FR-9, C-25.AC-10, C-25.AC-11, C-26.FR-7, C-03.FR-5, C-20.FR-1, C-20.FR-9]
files_touched:
  - web/src/routes/teams.index.tsx
  - web/src/routes/teams.new.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/components/team-form.tsx
  - web/src/components/team-form.test.tsx
  - web/src/test/fixtures/teams.ts
  - web/src/components/team-people.tsx
  - web/src/components/team-people.test.tsx
  - web/src/components/claim-entries-editor.tsx
  - web/src/components/claim-entries-editor.test.tsx
  - web/src/components/group-mapping-editor.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/components/working-hours-editor.tsx
  - web/src/components/working-hours-editor.test.tsx
  - web/src/routes/admin.organization.general.tsx
  - web/src/components/team-filter.tsx
  - web/src/components/team-filter.test.tsx
  - web/src/routes/routes.index.tsx
  - web/src/components/route-list.tsx
  - web/src/routes/destinations.index.tsx
  - web/src/components/route-severity-and-owner.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/teams.spec.ts
  - web/e2e/oidc-settings.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/navigation.spec.ts
  - web/e2e/routes.spec.ts
  - web/e2e/destinations.spec.ts
acceptance:
  - "[C-25.FR-3, C-25.AC-10] The main navigation has \"Teams\" for every Role; the Teams list shows each Team with its name, slug and how many people are in it, and \"New Team\" for an Admin; the Team page shows the name, slug and description, and \"People\" with each person's Role, source — \"added by hand\" or the identity provider group that matched — linked messengers, and \"Remove\" only for people added by hand; \"Add person\" picks a User; a Responder sees the pages without the controls that change them, in English and in Russian (`team-people.test.tsx`, `teams.spec.ts`)."
  - "[C-25.FR-4, C-03.FR-5] The Team page's \"Identity provider groups\" and the Role mapping of the OIDC settings both edit entries of a claim and a group, the claim offered from the `claims_supported` of the last connection check with the groups claim first, with the hint \"Checked at every sign-in, like the Role mapping: each entry reads its own claim, and `a/b/*` takes the whole subtree. The Role of the people does not change.\" on the Team page; an entry the server refuses shows its message at the entry (`claim-entries-editor.test.tsx`, `oidc-settings.spec.ts`)."
  - "[C-25.FR-9, C-20.FR-9] The Team page's \"Working hours\" reads \"Organization default: Mon–Fri 09:00–18:00, Europe/Berlin\" until \"Own\" is chosen, then edits days, from, to and the time zone; Organization → General edits the Organization's working days and end beside the start of the working day; an end not later than the start shows the server's message at the field (`working-hours-editor.test.tsx`, `organization-settings.spec.ts`)."
  - "[C-25.FR-2, C-25.AC-11, C-26.FR-7] The Route form's section \"Severity, Escalation chains and owner\" and the Destination form have \"Owning Team\" with the hint \"For ownership and the filter of the UI; does not affect delivery.\"; the Routes and Destinations lists have the filter \"Team\" with every Team and \"My Teams\", and with \"My Teams\" a User in \"payments\" and not in \"search\" sees only what \"payments\" owns (`team-filter.test.tsx`, `teams.spec.ts`)."
  - "Every new text exists in English and Russian; the Teams page and the Team page have \"Learn more\" to Concepts → Teams, and the identity provider groups to its section on membership from the identity provider; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-119. Teams list and Team page with people, identity provider groups and Working hours, the claims of the Role mapping, and the filters by Team (FE)

## Scope

**IN**

- "Teams" in the main navigation; the Teams list; the new Team form; the Team page with its details, "People",
  "Identity provider groups" and "Working hours" (C-25 UI).
- The claim of each entry in the Role mapping editor of the OIDC settings (C-03 UI, revised).
- The Organization's Working hours on Organization → General (C-20.FR-1, revised).
- "Owning Team" on the Route and Destination forms, and the filter by Team and "My Teams" of the Routes and Destinations
  lists (C-25.FR-2, C-26.FR-7).

**OUT**

- The Team label with its hint, "Unknown values of {team label}" and "Create Team", "Which Alerts belong to this Team",
  the Team label on Organization → General, and the Teams of Alert Groups and Alerts on their pages: S-120.
- "Destinations of the Team": S-123. "How to mention the Team" and the Team mentions on the list: S-143. Schedules and
  "On call now": S-135, S-136. Escalation chains: S-158.
- The API: S-115, S-117.

## Contracts

- **API used**: `listTeams`, `createTeam`, `getTeam`, `updateTeam` (`name`, `slug`, `description`,
  `own_working_hours`, `working_hours`, `idp_entries`), `deleteTeam`, `listTeamUsers`, `addTeamUser`,
  `removeTeamUser`, `listUsers` (the picker, `users:read`), `getOidcSettings` (`claims_supported`, `groups_claim`),
  `updateOidcSettings` (`group_mappings` with `claim`), `getOrganization`, `updateOrganization` (`working_days`,
  `workday_end`), `listRoutes` and `listDestinations` with `team`, `createRoute`, `updateRoute`, `createDestination`,
  `updateDestination` (`owning_team_id`), `getMe` (the Permissions that show or hide the controls).
- **Routes** (file-based): `/teams` (`teams.index.tsx`), `/teams/new` (`teams.new.tsx`), `/teams/$teamId`
  (`teams.$teamId.tsx`); "Teams" in `app-shell.tsx` after "Alert Groups", for `teams:read`.
- **Components**: `team-form.tsx` (name, slug — with the format of `team.slug_format` as its hint — description);
  `team-people.tsx` (the table User, Role, "Source", "Mattermost/Telegram/Slack/Bridge" links, "Remove"; "Add person");
  `claim-entries-editor.tsx`, shared by the Team page and the Role mapping: a row per entry with "Claim" (a combobox of
  `claims_supported`, the groups claim first, free text allowed) and "Identity provider group", and for the Role mapping
  the Role; `group-mapping-editor.tsx` uses it; `working-hours-editor.tsx` (the Organization default line, or "Own" with
  the days Monday to Sunday, "From", "To" and the time zone picker of `time-zone-picker.tsx`); `team-filter.tsx` (a
  select "Team": "All", "My Teams", then each Team), whose value goes into the list's search parameter `team`.
- **Forms**: "Owning Team" (a select of the Teams with "None") in the section "Severity, Escalation chains and owner"
  of `route-severity-and-owner.tsx` and in `destination-form.tsx`, with the hint above; the Audit log names
  `/owning_team_id` "Owning Team", `/idp_entries` "Identity provider groups", `/working_hours` "Working hours".
- **Web test fixtures** (`web/src/test/fixtures/teams.ts`, new): `teamRead` builds a complete `Team` read with neutral values, overridable field by field, as S-166's builders do for Destinations and Connections; this story's tests and every later one build Team reads only through it, and a later story that adds a required field to the Team read (S-139) sets its neutral value there. This story waits for S-118 and S-121, which add Team fields before it.
- **Documentation links** (`docs-links.json`): `page.teams` → `concepts/teams.md`, `field.team_idp` →
  `concepts/teams.md#membership-from-the-identity-provider`, `field.working_hours` → `concepts/teams.md#working-hours`;
  the anchors exist from S-115 and S-117.
- **Texts** (EN / RU), from C-25's UI and the approved mockups: "Teams" / «Команды», "New Team" / «Новая команда»,
  "People" / «Участники», "Add person" / «Добавить участника», "added by hand" / «вручную», "Identity provider groups" /
  «Группы провайдера учётных записей», the hint above / «Проверяются при каждом входе, как сопоставления ролей: у
  каждой строки свой claim, a/b/* берёт всё поддерево. Роль участника при этом не меняется.», "Working hours" /
  «Рабочее время», "Organization default: {{days}} {{from}}–{{to}}, {{zone}}" / «В организации по умолчанию: {{days}}
  {{from}}–{{to}}, {{zone}}», "Own" / «Своё», "Owning Team" / «Команда-владелец», its hint / «Для владения и фильтра в
  интерфейсе; на доставку не влияет.», "My Teams" / «Мои команды».

## Steps

1. Write the routes, the form, the people table and the navigation entry. Check: `team-form.test.tsx`,
   `team-people.test.tsx`, `app-shell.test.tsx`; `make lint`.
2. Write the claim entries editor and use it on the Team page and in the Role mapping. Check:
   `claim-entries-editor.test.tsx`.
3. Write the Working hours editor on the Team page and on General. Check: `working-hours-editor.test.tsx`.
4. Add "Owning Team" and the filters. Check: `team-filter.test.tsx`.
5. Write the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal create the Responder "bob" and,
as in S-117, let "olga" sign in through the fake IdP with the group `teams/payments/oncall`. Then in Playwright at
1280 × 800, in English and in Russian:

1. "Teams" → "New Team" → name "payments", slug `payments` → "Save" → the Team page.
2. "People" → "Add person" → "bob" → the row shows "bob", "Responder", "added by hand" and "Remove".
3. "Identity provider groups" → "Add" → the claim combobox offers `groups` first → group `teams/payments/*` → "Save";
   the hint reads as above. From a terminal, olga signs in again; reload → "olga" with the source
   `teams/payments/*` and no "Remove".
4. "Working hours" reads "Organization default: Mon–Fri 09:00–18:00, UTC"; "Own" → Mon–Fri, 11:00 to 19:00,
   Europe/Berlin → "Save"; "To" 10:00 → "Save" → the message at "To".
5. Access → OIDC settings → the Role mapping has "Claim" on each row; add `https://gitlab.org/claims/groups/owner` /
   `example/platform` / Admin → "Save" → saved.
6. Organization → General → "Working days" Mon–Fri and "End of the working day" 18:00 are shown under the start.
7. Routes → a Route → "Owning Team" "payments" with its hint → "Save"; Routes → "Team" "My Teams" as admin → the list is
   empty; signed in as bob → the Route is listed.
8. Sign in as bob: "Teams" is in the navigation, the Team page has no "Add person", "Remove" or "Save".

`make e2e` runs these steps in `teams.spec.ts`, `oidc-settings.spec.ts` and `organization-settings.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the teams pages and the owning team of routes and destinations`.
- The claim combobox lets an Admin type a claim that discovery does not list, because the list is optional
  (C-03.FR-6); suggestions come from the last connection check.
- Depends on S-112 for the Route form's section "Severity, Escalation chains and owner", which this story extends.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-2 | partial | the owning Team on the forms and the filters of Routes and Destinations; Alert Groups are S-120 |
| C-25.FR-3 | partial | the page; the API is S-115 |
| C-25.FR-4 | partial | the page; the API is S-117 |
| C-25.FR-9 | partial | the pages; the API is S-115 |
| C-25.AC-10 | partial | each person's source on the Team page; unknown values and "Create Team" S-120, Team mention fields S-143 |
| C-25.AC-11 | partial | Routes and Destinations on the pages; Alert Groups S-120 |
| C-26.FR-7 | partial | the hint next to the owning Team on the Route form; the rule is S-121 |
| C-03.FR-5 | partial | revised for L2: the claim of each entry in the editor |
| C-20.FR-1 | partial | revised for L2: the Working hours on General |
| C-20.FR-9 | partial | revised for L2: the Working hours beside the start of the working day on General |
