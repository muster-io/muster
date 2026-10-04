---
id: S-054
title: System status page and outgoing heartbeat settings (FE)
capability: C-19
kind: fe
layer: L1
depends_on: [S-015, S-053]
covers: [C-19.FR-7, C-19.FR-10, C-19.AC-6]
files_touched:
  - web/src/routes/admin.system-status.tsx
  - web/src/routes/admin.organization.outgoing-heartbeat.tsx
  - web/src/components/system-status-sections.tsx
  - web/src/components/system-status-sections.test.tsx
  - web/src/components/outgoing-heartbeat-form.tsx
  - web/src/components/app-shell.tsx
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/system-status.spec.ts
  - web/e2e/outgoing-heartbeat.spec.ts
acceptance:
  - "[C-19.FR-10, C-19.AC-6] The System status page shows the replicas with the Leader marked and their key ids, the recovery state, delivery queues, Broken Destinations with their reason, Integrations with Heartbeat lost or truncated Snapshots, template errors, active Storms and the last outgoing heartbeat; each entity links to its page, and the page has no button that changes anything."
  - "[C-19.AC-6] With a Broken Destination and a Heartbeat-lost Integration, both appear on the page; a Responder sees no \"System status\" entry and \"You do not have permission to see this page.\" at its address."
  - "[C-19.FR-7] Without an outgoing heartbeat URL the page shows \"Set up an outgoing heartbeat so that a stopped Muster is noticed.\" with a link to Organization → Outgoing heartbeat, whose form takes the URL as a write-only secret field and the proxy form, and shows the last result."
verify: "make ci e2e"
operator_attention: false
issue: 54
---

# S-054. System status page and outgoing heartbeat settings (FE)

## Scope

**IN**

- The System status page for Admins: read-only, with links to what it lists.
- Organization → Outgoing heartbeat: the URL, the proxy form and the last result.

**OUT**

- The status read and the sender (S-053); the other Organization pages (S-056).

## Contracts

- **API used**: `getSystemStatus`, `getOrganization`, `updateOrganization` (only `outgoing_heartbeat`, sent with the
  other fields as read and `If-Match`), the hints `system-status`, `destination`, `integration` and `organization`.
- **Routes and navigation** (shown only with the Permission, as in S-015):

  | Route | Permission | Content |
  |---|---|---|
  | `/admin/system-status` | `system-status:read` | the sections below |
  | `/admin/organization/outgoing-heartbeat` | `organization:write` | the URL, the proxy form, the last result |

- **System status** (C-19.FR-10; `system-status-sections.tsx`): one section per field of `SystemStatus`, in this order:
  "Muster {version} ({commit})"; "Replicas" — id, "Leader" badge, key ids, last refresh; "Recovery after downtime" —
  "Not recovering" or "Recovering until {time} after downtime from {from} to {to}"; "Delivery queues" — Destination
  and queued count; "Broken Destinations" — Destination, "since {time}", reason; "Integrations needing attention" —
  "Heartbeat lost since {time}" or "Snapshots truncated"; "Template errors" — Route or Destination, template, since,
  error; "Storms" — Route, since, count; "Outgoing heartbeat" — "Last sent {time}: OK" or "Last sent {time}: {error}",
  or the suggestion "Set up an outgoing heartbeat so that a stopped Muster is noticed." with a link. Empty sections say
  "None". Every entity is a link to its page; there are no other controls. The page refreshes on its hints and every
  30 seconds.
- **Outgoing heartbeat** (C-19.FR-7; `outgoing-heartbeat-form.tsx`): "Heartbeat URL" through the `secret-field` of
  S-015 ("Set, changed {time}" / "Not set", "Replace", "Clear"), the `proxy-form` of S-015, the explanation "Muster
  calls this URL every minute while a replica leads. Use a dead man's switch that alerts when the calls stop.", and
  "Last result" from `getSystemStatus` when the user may read it; a stale save shows S-015's conflict message.

## Steps

1. Write the sections and the page. Check: `system-status-sections.test.tsx` renders every section from a fixture,
   with no control other than links.
2. Write the outgoing heartbeat page. Check: Playwright steps 4 and 5.
3. Add the navigation entries and the hints. Check: Playwright steps 1 to 3 and 6.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal, as in S-053, create the
Mattermost Destination "alerts" and remove the bot from its channel, and create an Integration "hb" with the Heartbeat
on whose signal never arrives after the first one (`ADV 360`). Then in Playwright:

1. The navigation shows "System status" → the page shows "Replicas" with one row marked "Leader", "Broken
   Destinations" with "alerts" and a reason containing "403", "Integrations needing attention" with "hb — Heartbeat lost
   since {time}", and "Outgoing heartbeat" with "Set up an outgoing heartbeat so that a stopped Muster is noticed.".
2. The page has no button besides the navigation; "alerts" links to `/destinations/…`.
3. Terminal: add the bot back and `ADV 300` → without a reload "Broken Destinations" says "None".
4. "Set up an outgoing heartbeat" → Organization → Outgoing heartbeat → "Replace" → `http://127.0.0.1:18093/ping/dev`
   → "Save" → "Set, changed {time}".
5. Terminal: `ADV 60` → the page shows "Last result: OK"; System status shows "Last sent {time}: OK".
6. As a Responder → no "System status" entry; `/admin/system-status` shows "You do not have permission to see this
   page."
7. At 360 × 740 pixels both pages stay usable and `document.documentElement.scrollWidth` equals the viewport width.

`make e2e` runs these steps as `web/e2e/system-status.spec.ts` and `outgoing-heartbeat.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the system status page and the outgoing heartbeat settings`.
- The page only reads; every fix happens on the page of the entity it links to.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-19.FR-7 | partial | the settings page; with S-053 complete |
| C-19.FR-10 | partial | the page; with S-053 complete |
| C-19.AC-6 | partial | the page; with S-053 complete |
