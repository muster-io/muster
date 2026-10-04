---
id: S-019
title: Integrations list, form, token dialog, Stored Snapshot viewer and delete dialog (FE)
capability: C-05
kind: fe
layer: L1
depends_on: [S-017, S-018]
covers: [C-05.FR-1, C-05.FR-2, C-05.FR-5, C-05.FR-7, C-05.FR-8, C-05.AC-3, C-05.AC-6]
files_touched:
  - web/src/routes/integrations.index.tsx
  - web/src/routes/integrations.new.tsx
  - web/src/routes/integrations.$integrationId.tsx
  - web/src/routes/integrations.$integrationId.edit.tsx
  - web/src/routes/integrations.$integrationId.snapshots.$storedSnapshotId.tsx
  - web/src/components/integration-form.tsx
  - web/src/components/labels-editor.tsx
  - web/src/components/integration-tokens.tsx
  - web/src/components/integration-token-dialog.tsx
  - web/src/components/integration-token-dialog.test.tsx
  - web/src/components/stored-snapshots.tsx
  - web/src/components/integration-delete-dialog.tsx
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/integrations.spec.ts
  - web/e2e/stored-snapshots.spec.ts
acceptance:
  - "[C-05.FR-1] The Integrations page lists name, Connection mode and the time of the last Snapshot; the create and edit form takes the name, description, Static labels and duplicate window (pre-filled with `integration.duplicate_window`) and shows the Connection mode \"Webhook only\" with its precision text."
  - "[C-05.FR-2, C-05.FR-5, C-05.AC-6] \"Create token\" opens a dialog that shows the token value and the Alertmanager snippet with copy buttons and \"You will not see this token again.\"; after it closes, the token list shows the token's name, creation and last use without a value, and \"Revoke\" removes it after a confirmation."
  - "[C-05.FR-7] The Integration page shows the Connection mode, the time of the last Snapshot and the number received, and lists the recent Stored Snapshots with their state — Pending, Processed or Failed with the error — filtered by state and time range; opening one shows the body exactly as received, its content type and state."
  - "[C-05.FR-8, C-05.AC-3] \"Delete\" asks for confirmation with \"Its tokens stop working at once.\"; after deletion the Integration is gone from the list."
  - "[C-05.FR-7] Without `stored-snapshots:read` the Stored Snapshot section is not shown, and without `integrations:write` no create, edit, token or delete control is shown."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-019. Integrations list, form, token dialog, Stored Snapshot viewer and delete dialog (FE)

## Scope

**IN**

- The Integrations list, the create and edit form with the Static labels editor, and the Integration page.
- The token list, the one-time token dialog with the Alertmanager snippet, and revoking.
- The Stored Snapshot list on the Integration page and the raw viewer.
- The delete dialog.

**OUT**

- The learned Alertmanager routes, the warnings, the built-in Integration and the Alerts view (S-022).
- The Heartbeat section, badges and banners (S-024).
- The count of open Alert Groups in the delete dialog (S-031).

## Contracts

- **API used**: `listIntegrations`, `createIntegration`, `getIntegration`, `updateIntegration`, `deleteIntegration`,
  `listIntegrationTokens`, `createIntegrationToken`, `revokeIntegrationToken`, `listStoredSnapshots`,
  `getStoredSnapshot`.
- **Routes and navigation** (navigation entry "Integrations" with `integrations:read`):

  | Route | Permission | Content |
  |---|---|---|
  | `/integrations` | `integrations:read` | name, Connection mode, last Snapshot ("never" without one); "Create integration" with `integrations:write` |
  | `/integrations/new` | `integrations:write` | the form |
  | `/integrations/$integrationId` | `integrations:read` | details, Static labels, duplicate window, the precision text, last Snapshot and count; Tokens; Stored Snapshots (`stored-snapshots:read`); Edit and Delete (`integrations:write`) |
  | `/integrations/$integrationId/edit` | `integrations:write` | the form with `If-Match` |
  | `/integrations/$integrationId/snapshots/$storedSnapshotId` | `stored-snapshots:read` | the raw viewer |

- **Form** (C-05.FR-1): name, description, Static labels (rows of name and value, "Add label"; `Problem` pointers map
  onto the rows), duplicate window in seconds with the hint "Snapshots of the same Alertmanager group that arrive this
  close together count as one.", the Heartbeat sent as `{"enabled": false}` until S-024. The Connection mode is shown,
  not chosen: "Webhook only" with "An Alert that Alertmanager resolves closes at once. An Alert that Alertmanager stops
  listing is Gone after up to two repeat intervals, never sooner than 5 minutes, and Stale after three learned repeat
  intervals." A stale save (`412`) shows "Someone else changed this integration. Reload to see the changes."
- **Tokens** (C-05.FR-2, FR-5): "Create token" with an optional name; the dialog shows the value and
  `alertmanager_snippet` in monospace blocks with "Copy", and "You will not see this token again."; closing it drops
  both from memory. The list shows the name, creation and last use; "Revoke" confirms with "Alertmanager stops being
  able to send with this token at once."
- **Stored Snapshots** (C-05.FR-7): a data table (S-015) with received time, size, group key, Alert count and state
  ("Pending", "Processed", "Failed: {error}"), filtered by state and time range in the URL. The viewer shows "Received
  {time}", the content type, the state with its error, and the body with "Copy"; a `base64` body is shown as such with
  "The body is not valid UTF-8 and is shown as base64."
- **Delete dialog** (C-05.FR-8): "Delete integration {name}? Its tokens stop working at once. Its Stored Snapshots are
  kept for {days} days." — the days from `retention.stored_snapshots` of `getOrganization`.
- **Live hints**: `integration` hints invalidate the list and the page.

## Steps

1. Write the list, the form with the labels editor, and the page. Check: Playwright creates, edits and lists an
   Integration.
2. Write the token list and dialog. Check: the component test shows the value and snippet once and gone after closing.
3. Write the Stored Snapshot table and viewer. Check: Playwright opens a Snapshot sent by the fake Alertmanager.
4. Write the delete dialog and the permission gating. Check: Playwright runs steps 6 and 7 of Verification.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, then in Playwright:

1. The navigation shows "Integrations"; the list shows "dev-alertmanager" with "Webhook only".
2. "Create integration" → Name "prod-eu", Static label `cluster` = `prod-eu`, the duplicate window shows `45` →
   "Create" → the page "prod-eu" shows "Webhook only" and "An Alert that Alertmanager resolves closes at once.", and
   "Last Snapshot: never".
3. Tokens → "Create token" → Name "rotation-1" → the dialog shows a value starting with `mstr_int_`, a snippet
   containing `send_resolved: true`, `max_alerts: 0` and that value, and "You will not see this token again." → "Copy"
   on the snippet → close → the list shows "rotation-1" without a value.
4. Register the copied value with the fake Alertmanager and send two webhooks, one of them `not json`, as in S-018 →
   reload → "Last Snapshot" shows a moment ago; Stored Snapshots lists two rows "Pending" → open the second → the body
   reads `not json` and the content type `text/plain`.
5. Edit → change the duplicate window to `60` → "Save"; in a second browser context save another change first → back
   in the first, "Save" → "Someone else changed this integration. Reload to see the changes."
6. "Revoke" on "rotation-1" → confirm → the row is gone, and a webhook with the copied value gets `401`.
7. "Delete" → the dialog reads "Its tokens stop working at once." and "kept for 14 days" → "Delete" → the list no
   longer shows "prod-eu".
8. Sign in as a Responder → the list and pages are visible, no "Create integration", "Create token", "Edit" or
   "Delete", and no Stored Snapshots section.

`make e2e` runs these steps as `web/e2e/integrations.spec.ts` and `stored-snapshots.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add integration pages, the token dialog and the stored snapshot viewer`.
- The token dialog differs from the one of S-017 only by the snippet; it may reuse its parts.
- The precision text names the built-in values `processing.gone_min_absence` and `processing.stale_after_factor`; if
  either changes, the text changes with it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-05.FR-1 | partial | the form and the precision text; the Heartbeat section is S-024 |
| C-05.FR-2 | full | together with S-018 |
| C-05.FR-5 | full | together with S-018 |
| C-05.FR-7 | partial | Connection mode, last Snapshot, count and Stored Snapshots; the later sections are S-022 and S-024 |
| C-05.FR-8 | full | together with S-018; the open Alert Group count joins the dialog with S-031 |
| C-05.AC-3 | full | together with S-018 |
| C-05.AC-6 | full | together with S-018 |
