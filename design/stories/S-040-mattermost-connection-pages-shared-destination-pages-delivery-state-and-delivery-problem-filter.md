---
id: S-040
title: Mattermost Connection pages with the check, the callback address and its hint (FE)
capability: C-13
kind: fe
layer: L1
depends_on: [S-038, S-061]
covers: [C-13.FR-1, C-13.FR-2, C-13.FR-13, C-13.AC-16]
files_touched:
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/callback-hint.tsx
  - web/src/components/limiter-field.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/mattermost-connection.spec.ts
  - docs/messengers/mattermost.md
acceptance:
  - "[C-13.FR-1, C-13.FR-13] Connections lists the Connections with type, name and the number of Destinations; the Mattermost form takes the name, server URL, bot token (write-only: \"Set\" with the time of the last change, \"Replace\"), the proxy form and the limiter, and the page shows the callback address with the hint \"Mattermost calls {callback address} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it.\""
  - "[C-13.FR-2] \"Check connection\" shows \"Connected as muster-dev-bot\" with the latency and the path (direct or through the proxy), or the failing step's message; deleting a Connection that one Destination uses shows \"This Connection is used by 1 Destination. Delete it first.\""
  - "[C-13.AC-16, C-13.FR-2] When the check returns the warning `press_answers_in_thread` (the fake's bot is not a system admin by default), \"Check connection\" shows, besides the result, the hint \"The bot may not make ephemeral messages, so answers to button presses show in the Thread of the Root message. Give the bot the `create_post_ephemeral` permission, for example the system admin role, to show them in the channel.\" (the permission shown as code); with `bot_system_admin` on the fake there is no hint."
  - "[C-13.FR-1] The navigation lists \"Connections\" with `connections:read`; the Audit log names the changed fields of a Connection and shows a replaced bot token as a changed secret, never its value."
  - "[C-13.FR-1] Without `connections:write` the Connection pages are read-only: no \"Create connection\", \"Save\", \"Check connection\" or \"Delete\"."
verify: "make ci e2e"
operator_attention: false
issue: 40
---

# S-040. Mattermost Connection pages with the check, the callback address and its hint (FE)

## Scope

**IN**

- Connections: the list and the Mattermost Connection page with its check, proxy form, limiter, callback address and
  hint, and deleting a Connection.
- The navigation entry "Connections" and the Audit log fields of a Connection.

**OUT**

- The Destination pages, the Mention section, the Route editor's Destinations and delivery sections, the Internal
  alerts suggestion card, the Delivery section of the Alert Group page and the "Delivery problem" filter (S-064).
- The Telegram Connection form (S-043); the Test and Preview panels (S-048).

## Contracts

- **API used**: `listConnections`, `createConnection`, `getConnection`, `updateConnection`, `deleteConnection`,
  `checkConnection`, `streamLiveUpdates` (hint `connection`).
- **Routes and navigation** (`app-shell.tsx` and its test gain the entry "Connections" with `connections:read`):

  | Route | Permission | Content |
  |---|---|---|
  | `/connections` | `connections:read` | list: type, name, Destinations; "Create connection" with `:write` |
  | `/connections/new` | `connections:write` | type choice (Telegram from S-043), then the form |
  | `/connections/$connectionId` | `connections:read` | the form, read-only without `:write`; "Check connection"; the callback address and hint for Mattermost; "Delete" |

- **Connection form** (`connection-form.tsx`): name, server URL, the secret field of S-015 for the bot token, the proxy
  form of S-015, the limiter (`limiter-field.tsx`: `limit` per `per_seconds`, reused by the Destination form of
  S-064). The callback address comes from the read-only `MattermostConnection.callback_url` and is shown with a copy
  button and the hint of [reference.md](../prd/l1/reference.md#banners-warnings-and-notices). `409 in_use` shows "This
  Connection is used by N Destinations. Delete them first." in its plural forms ("… by 1 Destination. Delete it
  first.").
- **Connection check** (`connection-check.tsx`): each step with "ok" or its message, `latency_ms` and `via`; for
  Mattermost "Connected as {bot_name}"; `503` shows "The messenger is busy; try again in N s."
- **Audit log** (`audit-diff.tsx` and its test): the resource type `connection` names its fields — name, server URL,
  proxy, limiter — and the bot token as "secret changed".
- **Live updates** (`live.ts`): the `connection` hint refreshes the list and the page.

## Steps

1. Add the Connection pages, the navigation entry and the form with the limiter. Check: `connection-form.test.tsx`
   and step 1 of Verification.
2. Add the check, the callback address with its hint, the delete dialog and the Audit log fields. Check: steps 2 to 5.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; the fake Mattermost server of S-039 runs with
its team `dev` and channels. Then in Playwright:

1. Connections → "Create connection" → Mattermost → name "mm", server URL `http://127.0.0.1:18065`, bot token
   `mm-dev-token` → "Save" → the page shows the bot token as "Set", the callback address
   `http://localhost:8081/api/v1/callbacks/mattermost/CN…` and the hint starting "Mattermost calls".
2. "Check connection" → "Connected as muster-dev-bot", "direct" and a latency in ms.
3. From a terminal create the Destination "alerts" on `ch-alerts` of "mm" through the API, as in S-039 → Connections →
   "mm" → "Delete" → "This Connection is used by 1 Destination. Delete it first."
4. "mm" → replace the bot token → "Save" → Audit log → the `connection.updated` entry shows the bot token as a changed
   secret.
5. As a Viewer → Connections shows no "Create connection", and "mm" no "Save", "Check connection" or "Delete".

`make e2e` runs these steps as `web/e2e/mattermost-connection.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the mattermost connection pages`.
- Split before implementation, because it touched about 39 files: S-040 keeps the Connection pages; S-064 takes the
  Destination pages, the Route editor's Destinations and delivery sections, the delivery state and the "Delivery
  problem" filter. The file name keeps its slug; the ID does not change.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-1 | partial | the Connection pages; with S-039 complete |
| C-13.FR-2 | partial | the Connection check on the page; the Destination form's check is S-064; with S-039 complete |
| C-13.FR-13 | partial | the address and the hint; the test press is S-047 and S-048 |
