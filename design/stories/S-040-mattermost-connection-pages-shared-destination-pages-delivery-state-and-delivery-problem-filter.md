---
id: S-040
title: Mattermost Connection pages, shared Destination pages, delivery state and Delivery problem filter (FE)
capability: C-13
kind: fe
layer: L1
depends_on: [S-033, S-038, S-061]
covers: [C-13.FR-1, C-13.FR-2, C-13.FR-9, C-13.FR-10, C-13.FR-11, C-13.FR-12, C-13.FR-13, C-13.AC-3, C-13.AC-8, C-13.AC-9, C-13.AC-10, C-11.FR-9, C-11.FR-10, C-11.FR-16, C-12.FR-8, C-09.FR-13, C-09.FR-14, C-08.FR-1, C-08.FR-11]
files_touched:
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/callback-hint.tsx
  - web/src/routes/destinations.index.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/mattermost-destination-fields.tsx
  - web/src/components/channel-picker.tsx
  - web/src/components/mention-settings.tsx
  - web/src/components/mention-settings.test.tsx
  - web/src/components/limiter-field.tsx
  - web/src/components/destination-health.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/route-destinations.tsx
  - web/src/components/route-form.tsx
  - web/src/components/route-suggestions.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/mattermost-connection.spec.ts
  - web/e2e/destinations.spec.ts
  - web/e2e/delivery-state.spec.ts
acceptance:
  - "[C-13.FR-1, C-13.FR-13] Connections lists the Connections with type, name and the number of Destinations; the Mattermost form takes the name, server URL, bot token (write-only: \"Set\" with the time of the last change, \"Replace\"), the proxy form and the limiter, and the page shows the callback address with the hint \"Mattermost calls {callback address} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it.\""
  - "[C-13.FR-2] \"Check connection\" shows \"Connected as muster-bot\" with the latency and the path (direct or through the proxy), or the failing step's message; deleting a Connection that one Destination uses shows \"This Connection is used by 1 Destination. Delete it first.\""
  - "[C-13.FR-9, C-13.AC-3] Destinations lists every Destination with its type, health and Routes; a Broken one shows \"Broken since HH:MM: {reason}. Muster tries again every 5 min.\" in the list, on its page and in the Destinations section of each of its Routes."
  - "[C-13.FR-2, C-13.FR-9, C-12.FR-8] The Mattermost Destination form picks the Connection, then the team and the channel from lists loaded through the bot, and has the Mention section — per kind of Loud event: nobody, @channel, @all or @here, chosen users, groups — and the limiter, pre-filled from the Connection; a channel without the bot is refused with the check's message next to the channel."
  - "[C-13.FR-10, C-13.AC-8] The Destination page of a type with a Destination check has \"Check\": on a Broken Destination whose bot is back it shows \"Check passed\" and the page shows the Destination healthy without a reload."
  - "[C-08.FR-1, C-13.FR-11, C-13.AC-9] The Route editor has a Destinations section whose picker lists Destinations of every type with their health; with a Mattermost Destination and no Route for Muster's Internal alerts, the Routes page offers \"Send Muster's own alerts to a Destination\", and accepting it with that Destination creates the Route at the top of the list."
  - "[C-11.FR-16, C-11.FR-10, C-09.FR-14] The Alert Group page has a Delivery section with one row per Destination: \"Delivered\" with a link to the message, \"Pending\", \"Not delivered: {error}\", \"Waiting: {Destination} is Broken\", \"Deleted in the messenger\", \"Thread not attached to the post\", \"Possible duplicate\", \"Not posted: resolved during a Storm or while the Destination was Broken\" or \"No longer updated here\"."
  - "[C-13.FR-12, C-13.AC-10, C-09.FR-13] The Alert Group list has the filter \"Delivery problem\", kept in the URL, and marks such rows; an Alert Group whose delivery ended as Not delivered is listed and marked, and after a later successful delivery it is not."
  - "[C-08.FR-1] The Route editor shows \"Storm since HH:MM: N new Alert Groups. Destinations receive a Storm summary.\" from `Route.storm` while the Route's Storm is active."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-040. Mattermost Connection pages, shared Destination pages, delivery state and Delivery problem filter (FE)

## Scope

**IN**

- Connections: the list and the Mattermost Connection page with its check, proxy form, callback address and hint.
- The shared Destination pages that C-14 and C-15 reuse: the list, the page with health, Routes, Mention and limiter
  sections and "Check", the type switch of the form; the Mattermost Destination form with team and channel pickers.
- The Destinations section of the Route editor with the Broken and Storm warnings, and the Internal alerts suggestion.
- The Delivery section of the Alert Group page; the "Delivery problem" filter and mark in the list.

**OUT**

- The Telegram forms (S-043), the outgoing webhook form (S-046), the Test and Preview panels (S-048).

## Contracts

- **API used**: `listConnections`, `createConnection`, `getConnection`, `updateConnection`, `deleteConnection`,
  `checkConnection`, `listConnectionChannels`, `listDestinations`, `createDestination`, `getDestination`,
  `updateDestination`, `deleteDestination`, `checkDestination`, `listUserDirectory`, `getRoute`, `updateRoute`,
  `listRouteSuggestions`, `acceptRouteSuggestion`, `dismissRouteSuggestion`, `listAlertGroupDeliveries`,
  `listAlertGroups`, `getAlertGroupCounts`, `liveUpdates` (hints `connection`, `destination`, `alert-group`).
- **Routes and navigation**:

  | Route | Permission | Content |
  |---|---|---|
  | `/connections` | `connections:read` | list: type, name, Destinations; "Create connection" with `:write` |
  | `/connections/new` | `connections:write` | type choice (Telegram from S-043), then the form |
  | `/connections/$connectionId` | `connections:read` | the form, read-only without `:write`; "Check connection"; the callback address and hint for Mattermost; "Delete" |
  | `/destinations` | `destinations:read` | list: type, name, health, Routes; a Broken banner per Broken Destination |
  | `/destinations/new` | `destinations:write` | type choice (Telegram from S-043, outgoing webhook from S-046), then the form |
  | `/destinations/$destinationId` | `destinations:read` | health, Routes, the type's fields, Mentions, limiter; "Check" with `destinations:test` for types with a check; "Delete" |

- **Connection form** (`connection-form.tsx`): name, server URL, the secret field of S-015 for the bot token, the proxy
  form of S-015, the limiter (`limit` per `per_seconds`). The callback address comes from the read-only
  `MattermostConnection.callback_url` and is shown with a copy button and the hint of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices). `409 in_use` shows "This Connection is used by
  N Destinations. Delete them first." in its plural forms ("… by 1 Destination. Delete it first.").
- **Connection check** (`connection-check.tsx`): each step with "ok" or its message, `latency_ms` and `via`; for
  Mattermost "Connected as {bot_name}"; `503` shows "The messenger is busy; try again in N s."
- **Destination form** (`destination-form.tsx`): common fields — name, the Mention section, the limiter — and a slot
  for the type's fields; `422` errors are shown at their pointers, including `destination_check_failed` next to the
  channel with the check's message.
- **Mattermost fields** (`mattermost-destination-fields.tsx`, `channel-picker.tsx`): Connection (Mattermost
  Connections), team and channel from `listConnectionChannels` (with `connections:write`; without it the stored team and
  channel names are shown read-only).
- **Mention section** (`mention-settings.tsx`): one row per kind — "New Alert Group", "New Alerts", "Reopen", "Ack
  timeout notice", "Snooze ended", "Rise to Urgent" — with "Nobody" (default), the everyone choice the type allows
  (`@channel`, `@all`, `@here` for Mattermost; none for Telegram), users from `listUserDirectory` and group names
  (Mattermost only; C-12.FR-8); a note
  "Reminders and auto-unacknowledge always mention the Owner; a Takeover mentions the previous Owner."
- **Health** (`destination-health.tsx`): "Healthy", or the Broken banner "Broken since HH:MM: {reason}. Muster tries
  again every {interval}." in the user's time zone, `{interval}` from `delivery.broken_probe_interval`; the reason as
  untrusted text. The same banner appears in the list, on the page and in the Route editor's Destinations section.
- **Check** (`destination-check.tsx`): `checkDestination`; each check with "ok" or its message; "Check passed" or
  "Check failed"; the health refreshes from the result and from the `destination` hint.
- **Route editor** (`route-destinations.tsx`, `route-form.tsx`): the Destinations section edits `destination_ids` with
  a picker of every Destination with its type and health, and shows the Broken banner per Broken Destination and the
  Storm banner of [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) from `storm` (`since` in the
  user's time zone, `alert_group_count`) while `storm_active`.
- **Suggestion** (`route-suggestions.tsx`): the `internal_alerts` card "Send Muster's own alerts to a Destination" with a
  Destination picker; "Create the route" accepts it, "Dismiss" dismisses it.
- **Delivery section** (`alert-group-deliveries.tsx`): one row per item of `listAlertGroupDeliveries` with the
  Destination's name and health and the state texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices); "Delivered" links to `message_url` in a new tab;
  `error` is untrusted text. Refreshed by the `alert-group` hint.
- **Delivery problem** (`alert-group-filters.tsx`, `alert-group-table.tsx`, `alert-group-search.ts`): the filter
  "Delivery problem" (`delivery_problem=true`) in the URL and the counts; rows with `delivery_problem` carry a mark with
  the tooltip "Delivery problem: open the Alert Group to see which Destination".

## Steps

1. Add the Connection pages with the check and the hint. Check: Playwright steps 1 and 2 of Verification.
2. Add the shared Destination pages, the form with the Mention section and the Mattermost fields. Check:
   `destination-form.test.tsx`, `mention-settings.test.tsx` and steps 3 and 4.
3. Add the health banners, "Check" and the Route editor's Destinations section with the suggestion. Check: steps 5 to 7.
4. Add the Delivery section and the Delivery problem filter. Check: steps 8 and 9.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; the fake Mattermost server of S-039 runs with
its team `dev` and channels. Then in Playwright, with alerts sent from a terminal through the fake Alertmanager as in
S-061:

1. Connections → "Create connection" → Mattermost → name "mm", server URL `http://127.0.0.1:18065`, bot token
   `mm-dev-token` → "Save" → the page shows the bot token as "Set", the callback address
   `http://localhost:8081/api/v1/callbacks/mattermost/CN…` and the hint starting "Mattermost calls".
2. "Check connection" → "Connected as muster-bot", "direct" and a latency in ms.
3. Destinations → "Create destination" → Mattermost → Connection "mm" → team "dev" → channel "no-bot" → "Save" → next
   to the channel: "The bot is not a member of this channel." → channel "alerts" → "New Alerts": "@channel" → "Save" →
   the list shows "alerts", Mattermost, "Healthy".
4. Routes → "db" → Destinations → add "alerts" → "Save"; the Routes page shows the card "Send Muster's own alerts to a
   Destination" → pick "alerts" → "Create the route" → the first row of the list is "Muster internal alerts".
5. From a terminal remove the bot from `ch-alerts` in the fake server and send a new Alert Group of "db" → Destinations
   shows "Broken since HH:MM: 403 …. Muster tries again every 5 min."; Routes → "db" → Destinations shows the same
   banner.
6. Add the bot back from a terminal → Destinations → "alerts" → "Check" → "Check passed" → the page shows "Healthy"
   without a reload.
7. Connections → "mm" → "Delete" → "This Connection is used by 1 Destination. Delete it first."
8. From a terminal script a `400` for the next post edit and acknowledge an Alert Group of "db" → its page → Delivery
   shows "Not delivered: …"; the list with "Delivery problem" lists it with the mark; acknowledge another change → the
   row loses the mark after "Delivered".
9. An Alert Group page → Delivery → "Delivered" opens the post's URL of the fake server in a new tab.
10. As a Viewer → Connections and Destinations show no "Create", "Save", "Check" or "Delete".

`make e2e` runs these steps as `web/e2e/mattermost-connection.spec.ts`, `destinations.spec.ts` and
`delivery-state.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add connection and destination pages, delivery state and the delivery problem filter`.
- The Destination pages are built with a slot for each type's fields; S-043 and S-046 add theirs without changing the
  shared parts.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-1 | partial | the Connection pages; with S-039 complete |
| C-13.FR-2 | partial | the check on the pages; with S-039 complete |
| C-13.FR-9 | full | |
| C-13.FR-10 | partial | "Check" on the page; with S-039 and S-061 complete |
| C-13.FR-11 | partial | the suggestion and the picker; with S-061 complete |
| C-13.FR-12 | partial | the filter and the mark; with S-061 complete |
| C-13.FR-13 | partial | the address and the hint; the test press is S-047 and S-048 |
| C-13.AC-3 | partial | the banners; with S-061 complete |
| C-13.AC-8 | partial | "Check" on the page; with S-061 complete |
| C-13.AC-9 | partial | the page; with S-061 complete |
| C-13.AC-10 | partial | the list mark; with S-061 complete |
| C-11.FR-9 | partial | the Broken warning on the Destination and its Routes |
| C-11.FR-10 | partial | Not delivered on the Alert Group page |
| C-11.FR-16 | partial | the page section |
| C-12.FR-8 | partial | the Mention section of the Destination form |
| C-09.FR-13 | partial | the Delivery problem filter |
| C-09.FR-14 | partial | the Delivery section |
| C-08.FR-1 | partial | the Destinations section of the Route editor |
| C-08.FR-11 | partial | the `internal_alerts` card |
