---
id: S-027
title: Routes list, Route editor, Matcher builder, Group key preview and suggestions (FE)
capability: C-08
kind: fe
layer: L1
depends_on: [S-022, S-026]
covers: [C-08.FR-1, C-08.FR-2, C-08.FR-3, C-08.FR-5, C-08.FR-7, C-08.FR-11, C-08.FR-13, C-08.AC-8, C-08.AC-9, C-06.FR-19]
files_touched:
  - web/src/routes/routes.index.tsx
  - web/src/routes/routes.new.tsx
  - web/src/routes/routes.$routeId.tsx
  - web/src/components/route-list.tsx
  - web/src/components/route-suggestions.tsx
  - web/src/components/route-form.tsx
  - web/src/components/matcher-builder.tsx
  - web/src/components/matcher-builder.test.tsx
  - web/src/components/group-key-editor.tsx
  - web/src/components/group-key-preview.tsx
  - web/src/components/profile-picker.tsx
  - web/src/components/route-delete-dialog.tsx
  - web/src/components/integration-alerts.tsx
  - web/src/components/integration-alerts.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/lib/api.ts
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/routes.spec.ts
  - web/e2e/route-preview.spec.ts
  - web/e2e/integration-page.spec.ts
acceptance:
  - "[C-08.FR-3] The Routes page lists the Routes in evaluation order with their Matchers, urgent mark and Group key, the Default route pinned last as \"Default\"; dragging a Route saves the new order, the Default route cannot be dragged, and a save over a newer order shows \"Someone else changed the order. Reload to see it.\""
  - "[C-08.FR-7, C-08.FR-1] \"Create route\" first asks for a profile, On-call or Informational, and opens the editor pre-filled with its values; the editor takes the name, description, Matchers, urgent mark and Group key."
  - "[C-08.FR-2] The Matcher builder offers `=`, `!=`, `=~` and `!~` per row; an invalid RE2 expression shows the server's error under that row's value."
  - "[C-08.FR-5] The Group key editor previews, for a period of 24 hours by default, \"Current: N Alert Groups\" and \"Proposed: M Alert Groups\" with the largest groups as examples; while a Route is being created it shows the proposed side only, using the unsaved Matchers."
  - "[C-08.FR-11, C-08.AC-8] With a Heartbeat configured and no Route but the Default one for `MusterHeartbeatLost`, the Routes page shows the suggestion at the top; \"Create the route\" adds it at the top of the list and the suggestion does not come back; \"Dismiss\" hides it for this user."
  - "[C-08.FR-13, C-08.AC-9, C-06.FR-19] The Integration's Alerts view shows each Alert's Route, linked, and its Severity level — `severity=\"none\"` as \"info\", `severity=\"P5\"` as \"warning (P5)\", no `severity` label as \"info\"."
  - "[C-08.FR-1] Without `routes:write` the list and the editor are read-only: no drag handles, no \"Create route\", no suggestion actions, no Save or Delete."
verify: "make ci e2e"
operator_attention: false
issue: 27
---

# S-027. Routes list, Route editor, Matcher builder, Group key preview and suggestions (FE)

## Scope

**IN**

- The Routes list in evaluation order with drag-to-reorder, the pinned Default route and the suggestions on top.
- Creating a Route from a profile; the editor with the Matcher builder, the urgent mark and the Group key editor with
  its preview; deleting a Route that has no open Alert Groups.
- The Route and Severity level columns of the Integration's Alerts view.
- The navigation entry "Routes", and a name in English and Russian for every field of a Route that the Audit log
  records, so that it shows no JSON pointers for Routes.

**OUT**

- The Lifecycle section of the editor and the move dialog for Routes with open Alert Groups (S-031); Snooze durations
  (S-033); the message, Destination and timer sections (S-038, S-040, S-050).

## Contracts

- **API used**: `listRoutes`, `createRoute`, `getRoute`, `updateRoute`, `deleteRoute`, `reorderRoutes`,
  `listRouteProfiles`, `previewGroupKey`, `listRouteSuggestions`, `acceptRouteSuggestion`, `dismissRouteSuggestion`,
  `listIntegrationAlerts`.
- **Routes and navigation** (navigation entry "Routes" with `routes:read`):

  | Route | Permission | Content |
  |---|---|---|
  | `/routes` | `routes:read` | suggestions, then the list in evaluation order; "Create route" and drag handles with `routes:write` |
  | `/routes/new` | `routes:write` | profile choice, then the editor |
  | `/routes/$routeId` | `routes:read` | the editor, read-only without `routes:write`; "Delete" except for the Default route |

- **List** (C-08.FR-3): each row shows the name, the Matchers in Alertmanager syntax (none for the Default route), the
  urgent mark, the Group key and the open Alert Group count. Dragging uses the browser's own drag and drop, with no
  library, since nothing it does adds a `<style>` element or attribute that the Content Security Policy refuses; "Move
  up" and "Move down" on each row give the keyboard and touch screens the same moves. Every move saves the whole order
  with `If-Match` from the list ETag (read from the `ETag` header, which `src/lib/api.ts` exposes for this list); the
  Default route has no handle and no move buttons. A `412`, or a `422` `route_set_mismatch` (another set of Routes),
  shows "Someone else changed the order. Reload to see it." and restores the order as it was read.
- **Creating** (C-08.FR-7): `/routes/new` shows the two profiles from `listRouteProfiles` with a line each — On-call:
  "For alerts someone must act on: ack timeout and Reminders on." Informational: "For alerts to read later: no ack
  timeout, no Reminders, long Snooze durations." — and opens the editor with the chosen profile's values, of which this
  story shows urgent and Group key; the other policy fields are kept and sent unchanged.
- **Editor** (C-08.FR-1, FR-2): name, description, the urgent switch ("Urgent: every Alert Group of this Route is
  urgent"), the Matcher builder — rows of label, operator (`=`, `!=`, `=~`, `!~`) and value, "Add matcher", combined
  with AND — and the Group key editor. `Problem` pointers such as `/matchers/1/value` (`invalid_regex`) map onto the
  row. Saves send `If-Match`; a `412` shows "Someone else changed this route. Reload to see the changes."
- **Group key preview** (C-08.FR-5): a panel beside the Group key with the period (1 hour, 24 hours by default, 7 days,
  14 days) and "Preview": "Current: N Alert Groups" (saved Routes only) and "Proposed: M Alert Groups", each with a
  table of the example key values and their Alert counts; while creating, the request carries the unsaved Matchers.
- **Suggestions** (C-08.FR-11): for `heartbeat_lost`, "Muster raises MusterHeartbeatLost when an Integration loses its
  Heartbeat, and only the Default route takes it now." with "Create the route" (`acceptRouteSuggestion`) and "Dismiss";
  a `409 suggestion_obsolete` reloads the suggestions.
- **Delete dialog**: "Delete route {name}? Alerts it would take go to the next matching Route." S-031 adds the case of
  open Alert Groups.
- **Alerts view columns** (C-08.FR-13): "Route" linked to `/routes/$routeId`, "Severity" with `severity_raw` in
  parentheses when it is set.
- **Live hints**: `route` hints refresh the list and an open editor shows "This route was changed elsewhere." when its
  ETag moved.

## Steps

1. Write the list with drag-to-reorder and the Default route pinned. Check: Playwright reorders and sees the stale
   message from a second context.
2. Write the profile choice, the editor and the Matcher builder. Check: the component test maps a `Problem` pointer onto
   its row; Playwright creates a Route.
3. Write the Group key editor and its preview. Check: Playwright sees current and proposed counts.
4. Write the suggestions, the delete dialog and the Alerts view columns. Check: Verification below.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; feed the Integration "pv" with the fake group
k1 of S-026 and the Integration "lab" with the group r2 of S-025 from a terminal. Then in Playwright:

1. Routes → the list ends with "Default" and has no drag handle on it.
2. "Create route" → "On-call" → Name "disk", Matcher `alertname` `=` `Disk`, Group key `alertname` → Preview →
   "Proposed: 1 Alert Group" and no "Current" → "Create" → the list shows "disk" above "Default".
3. Open "disk" → add `cluster` to the Group key → Preview → "Current: 1 Alert Group" and "Proposed: 2 Alert Groups",
   with the rows `cluster` = "" and `cluster` = `a`, 2 Alerts each.
4. Add a Matcher `pod` `=~` `api-(` → "Save" → under that row: the error text of `invalid_regex`.
5. "Create route" → "Informational" → Name "info", Matcher `severity` `=` `info` (a Route without Matchers would
   take `MusterHeartbeatLost` too, and step 6 would have no suggestion) → "Create" → drag "info" above "disk" → reload
   → "info" is first;
   in a second context drag "disk" back; in the first, whose live updates are cut off (otherwise the `route` hint
   refreshes its list first), drag again → "Someone else changed the order. Reload to see it."
6. With the Integration "hb" of S-023 having its Heartbeat on → the top of the list shows "Muster raises
   MusterHeartbeatLost …" → "Create the route" → the first row is "Muster: Heartbeat lost" and the suggestion is gone.
7. Integrations → "lab" → Alerts → the row `k="p5"` shows Route "Default" and "warning (P5)"; `k="none"` shows "info".
8. Sign in as a Responder → Routes shows no "Create route", no drag handles, and "disk" opens read-only.

`make e2e` runs these steps as `web/e2e/routes.spec.ts` and `route-preview.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the routes list, route editor, matcher builder and group key preview`.
- Later stories add their sections to `route-form.tsx`; until then the policy values of the profile, or of the stored
  Route, travel unchanged with every save.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-08.FR-1 | partial | the Route pages, together with S-025; later policy sections and the Destinations picker come with their capabilities (S-031, S-033, S-038, S-040, S-050) |
| C-08.FR-2 | full | together with S-025 |
| C-08.FR-3 | full | together with S-025 |
| C-08.FR-5 | full | together with S-026 |
| C-08.FR-7 | full | together with S-025 |
| C-08.FR-11 | full | together with S-026; the `internal_alerts` suggestion is C-13.FR-11 (S-061, S-040) |
| C-08.FR-13 | full | together with S-025 |
| C-08.AC-8 | full | together with S-026 |
| C-08.AC-9 | full | together with S-025 |
| C-06.FR-19 | partial | the Route and Severity level columns; the Alert Group column is S-031 |
