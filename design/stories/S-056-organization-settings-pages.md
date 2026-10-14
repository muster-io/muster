---
id: S-056
title: Organization settings pages (FE)
capability: C-20
kind: fe
layer: L1
depends_on: [S-015, S-055, S-071]
covers: [C-20.FR-1, C-20.FR-3, C-20.FR-6, C-20.AC-2, C-20.AC-4, C-04.FR-8]
files_touched:
  - web/src/routes/admin.organization.tsx
  - web/src/routes/admin.organization.index.tsx
  - web/src/routes/admin.organization.general.tsx
  - web/src/routes/admin.organization.severity-levels.tsx
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/routes/admin.organization.retention.tsx
  - web/src/routes/admin.organization.outbound.tsx
  - web/src/routes/admin.organization.security.tsx
  - web/src/routes/admin.organization.keyring.tsx
  - web/src/routes/admin.system-status.tsx
  - web/src/components/organization-nav.tsx
  - web/src/components/organization-nav.test.tsx
  - web/src/components/organization-form.tsx
  - web/src/components/outgoing-heartbeat-form.tsx
  - web/src/components/time-zone-picker.tsx
  - web/src/components/severity-mapping-editor.tsx
  - web/src/components/severity-mapping-editor.test.tsx
  - web/src/components/severity-values.tsx
  - web/src/components/network-list-editor.tsx
  - web/src/components/network-list-editor.test.tsx
  - web/src/components/keyring-status.tsx
  - web/src/components/keyring-status.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/lib/api.ts
  - web/src/lib/api.test.ts
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/global-setup.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/phone-width.spec.ts
  - web/e2e/link-rules.spec.ts
  - web/e2e/outgoing-heartbeat.spec.ts
  - web/e2e/system-status.spec.ts
  - web/e2e/oidc-settings.spec.ts
  - web/e2e/users.spec.ts
acceptance:
  - "[C-20.FR-1] The main navigation has one \"Organization\" entry in place of the Organization pages; it opens a sub-navigation with General, Severity levels, Alert handling, Security, Outbound addresses, Retention, Keyring, Outgoing heartbeat, Lookup tables, Link rules and System status, each shown only with its Permission, and at 360 px the page does not scroll sideways and every entry of both navigations can be reached."
  - "[C-20.FR-1] General edits the name and the time zone of messages; Alert handling edits \"critical is Urgent\" and the Instance labels; Retention edits the four periods in days and shows the `retention_order` refusal next to the summaries field; each page saves with `If-Match`, shows \"Saved.\" and shows S-015's conflict message over a newer version."
  - "[C-20.FR-1, C-20.AC-4] Severity levels edits the severity label, the mapping of values to critical, warning and info, and each level's emoji and colour, and lists the values seen over the last 14 days with their counts; after Alerts with `severity=\"P5\"` arrive, `P5` is listed, marked \"No mapping — counts as warning\", with the banner \"These values have no mapping and count as warning: P5.\" and a \"Map\" action that adds it to the mapping."
  - "[C-20.FR-3] Outbound addresses edits the outbound address policy (standard or strict) and the allowed and denied lists, shows the rules that always apply — link-local, cloud metadata, unspecified and multicast addresses — and says \"A denied entry always wins.\""
  - "[C-20.FR-6, C-20.AC-2] Keyring shows every key id, which is active, which replicas hold or lack it, \"Not active yet\" for a key that was never active, and, for an older key nothing depends on any more, \"Key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS.\"; it explains that a key is activated with `muster secrets rotate-key`, and it updates without a reload when a key is activated and when the old key becomes removable."
  - "[C-20.FR-1, C-04.FR-8] Security also edits the token grace for OIDC users without an offline token, in days, next to the TOTP policy of S-015."
verify: "make ci e2e"
operator_attention: false
issue: 56
---

# S-056. Organization settings pages (FE)

## Scope

**IN**

- The "Organization" entry of the main navigation and its sub-navigation, which also takes the Organization pages of
  earlier stories (Security, Outgoing heartbeat, Lookup tables, Link rules) and System status.
- Organization → General, Severity levels, Alert handling, Retention, Outbound addresses and Keyring, and the token
  grace on Security.

**OUT**

- The TOTP policy on Security (S-015), the outgoing heartbeat and System status (S-054), Lookup tables and Link rules
  (S-038), whose pages only move into the sub-navigation.
- Activating a key, which is a CLI command (S-071).

## Contracts

- **API used**: `getOrganization`, `updateOrganization`, `listSeverityValues`, `getOutboundPolicy`,
  `updateOutboundPolicy`, `getKeyring` (with `KeyringKey.state` of S-071), the `organization` hint, which
  `web/src/lib/live.ts` now maps to the Organization, outbound policy, values seen and Keyring queries besides the
  System status.
- **Navigation** (`app-shell.tsx`, `organization-nav.tsx`): the main navigation no longer grows with every Organization
  page. `NAVIGATION` loses "Security", "Outgoing heartbeat", "Lookup tables", "Link rules" and "System status" and gains
  one entry "Organization" (`/admin/organization`) after "OIDC"; a `NavEntry` may name several Permissions, any of which
  shows it, and "Organization" is shown when the session holds the Permission of at least one sub-entry and is marked
  active on every page of the sub-navigation. The layout route `admin.organization.tsx` renders the sub-navigation — a
  `navigation` landmark named "Organization" — above every `/admin/organization/*` page; `admin.organization.index.tsx`
  leads to the first sub-entry the session can open; System status keeps its address `/admin/system-status`, to which
  S-053's texts link, and renders the same sub-navigation. The sub-navigation wraps onto several lines on a narrow
  screen instead of scrolling. Its entries, in order, each shown only with its Permission:

  | Entry | Route | Permission | Content |
  |---|---|---|---|
  | General | `/admin/organization/general` | `organization:write` | name, time zone of messages |
  | Severity levels | `/admin/organization/severity-levels` | `organization:write` (`organization:read` for the values seen) | severity label, mapping, emoji and colours, values seen |
  | Alert handling | `/admin/organization/alert-handling` | `organization:write` | "critical is Urgent", Instance labels |
  | Security | `/admin/organization/security` | `organization:write` | TOTP policy (S-015), token grace |
  | Outbound addresses | `/admin/organization/outbound` | `organization:read`, saving with `organization:write` | the outbound address policy |
  | Retention | `/admin/organization/retention` | `organization:write` | the four retention periods |
  | Keyring | `/admin/organization/keyring` | `organization:read` | the Keyring status |
  | Outgoing heartbeat | `/admin/organization/outgoing-heartbeat` | `organization:write` | S-054 |
  | Lookup tables | `/admin/organization/lookup-tables` | `lookup-tables:read` | S-038 |
  | Link rules | `/admin/organization/link-rules` | `link-rules:read` | S-038 |
  | System status | `/admin/system-status` | `system-status:read` | S-054 |

  `app-shell.test.tsx` checks the main entries and the "any of" rule; `organization-nav.test.tsx` checks the order and
  the Permissions of the sub-entries. The end-to-end specs that open these pages from the main navigation
  (`link-rules.spec.ts`, `outgoing-heartbeat.spec.ts`, `system-status.spec.ts`, `oidc-settings.spec.ts`,
  `users.spec.ts`) open "Organization" first and then the sub-entry.
- **One resource, several pages** (`organization-form.tsx`): every page edits its slice of the Organization and sends
  the whole `OrganizationInput` as read, with `If-Match` from `etag`; the one `organizationInput()` of this component
  replaces the copies in `admin.organization.security.tsx` and `outgoing-heartbeat-form.tsx`. A saved page shows
  "Saved."; a `412` shows "Someone else changed these settings. Reload to see them." Field errors of a `422` are shown
  at their pointer: `applyFieldErrors` (`web/src/lib/api.ts`) maps the whole JSON pointer to the form field
  (`/retention/alert_group_summaries_days` → `retention.alert_group_summaries_days`, `/allowed/2` → `allowed.2`), not
  only its first segment; `api.test.ts` covers nested and array pointers.
- **General** (`time-zone-picker.tsx`): IANA zones with search, the current offset shown; the hint "Times in messages use
  this time zone. Each user sees the web UI in their own time zone."
- **Severity levels** (`severity-mapping-editor.tsx`, `severity-values.tsx`): the label name; rows "value → level" with
  add and remove (a duplicate value is refused next to it); per level an emoji and a colour picker; the values-seen
  table — value, count, level, "No mapping — counts as warning" — with "Map" on unmapped rows, and the banner of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) listing the unmapped values; the note "Changes
  apply to Alerts evaluated from now on; open Alert Groups keep their status."
- **Alert handling**: "Critical is Urgent" as a switch; "Instance labels" as a list of label names with the hint from
  C-09 on Replacements.
- **Retention**: Stored Snapshots, alert details, Alert Group summaries and Audit log in days, each with its meaning;
  `retention_order` shows "Keep Alert Group summaries at least as long as alert details." at the summaries field.
- **Outbound addresses** (`network-list-editor.tsx`): "Standard — private addresses allowed, loopback blocked" and
  "Strict — only public addresses and the allowed entries"; the allowed and denied lists of networks, host names and
  domains with per-entry errors at their pointer; the read-only "Always blocked" list from `always_blocked`; "A denied
  entry always wins."
- **Keyring** (`keyring-status.tsx`): a table of keys — id, "Active", "Held by {replicas}", "Missing on {replicas}" in
  red, and by `state`: "Still needed", "Not active yet", or the text "Key {id} is no longer used and can be removed
  from MUSTER_SECRET_KEYS." — and the explanation "To activate a key, add it to MUSTER_SECRET_KEYS on every replica,
  then run `muster secrets rotate-key --activate <id> --actor <your name>`." The page has no form and needs
  `organization:read` only.
- **Security**: "Token grace for OIDC users without an offline token" in days (`oidc_token_grace_seconds`), next to the
  TOTP policy of S-015.

## Steps

1. Write the sub-navigation, move the existing Organization pages into it and update the specs that open them. Check:
   `app-shell.test.tsx` and `organization-nav.test.tsx`; Playwright steps 1 and 8; the moved specs pass.
2. Write the shared form, the pointer mapping, and General, Alert handling and Retention. Check: `api.test.ts`;
   Playwright steps 1 and 2.
3. Write Severity levels with the values seen. Check: `severity-mapping-editor.test.tsx` refuses duplicates; Playwright
   step 3.
4. Write Outbound addresses. Check: `network-list-editor.test.tsx` maps pointers to entries; Playwright step 4.
5. Write Keyring and add the token grace to Security. Check: Playwright steps 5 and 6.

## Verification

Export `MUSTER_SECRET_KEYS` with the development key of S-004 followed by a new key, as in S-071 — the variable replaces
the development default, so it lists both and the Keyring has two keys — and run `make dev`; sign in as
`admin@example.org` / `muster-dev-password`; from a terminal, send Alerts with `severity="P5"` as in S-055. Then in
Playwright:

1. The main navigation shows "Organization" and none of "Security", "Outgoing heartbeat", "Lookup tables", "Link rules"
   and "System status" → "Organization" → the sub-navigation shows, one after the other, "General", "Severity levels",
   "Alert handling", "Security", "Outbound addresses", "Retention", "Keyring", "Outgoing heartbeat", "Lookup tables",
   "Link rules" and "System status" → General → time zone "Europe/Berlin" → "Save" → "Saved."; the Audit log shows
   `organization.updated`.
2. Retention → "Alert Group summaries" 30 while "Alert details" is 90 → "Save" → "Keep Alert Group summaries at least as
   long as alert details." at the field → 90 → "Save" → "Saved."
3. Severity levels → the banner "These values have no mapping and count as warning: P5." and the row "P5 · 1 · warning ·
   No mapping — counts as warning" → "Map" → level "info" → "Save" → the row shows "info" and the banner is gone.
4. Outbound addresses → "Always blocked" lists link-local, cloud metadata, unspecified and multicast addresses → add
   `10.20.0.0/16` to "Denied" → "Save" → "Saved." → remove it → "Save"; an entry "not a network" in "Allowed" is refused
   next to it. `127.0.0.0/8` stays allowed throughout: the fake servers listen on loopback.
5. Keyring → two keys, one "Active" and held by one replica, the other "Not active yet" and without "can be removed";
   terminal, with the same environment: `./bin/muster dev secrets rotate-key --activate <other id> --actor ops` (S-071)
   → without a reload the other key is "Active" and, once the open Root messages are edited, the first shows "Key {id}
   is no longer used and can be removed from MUSTER_SECRET_KEYS."
6. Security → "Token grace for OIDC users without an offline token" 3 days → "Save" → "Saved."; in a second browser
   context, an edit saved meanwhile makes the first save show "Someone else changed these settings. Reload to see
   them."
7. As a Responder → the main navigation shows "Organization", whose sub-navigation shows only "Lookup tables" and "Link
   rules"; `/admin/organization` leads to Lookup tables; `/admin/organization/general` shows "You do not have permission
   to see this page."
8. At 360 px wide, as the Admin (`phone-width.spec.ts`): the page does not scroll sideways; the main navigation scrolls
   inside itself and "Organization" can be scrolled into view and opened; the sub-navigation wraps, and all its eleven
   entries are visible without scrolling.

`make e2e` runs steps 1–7 as `web/e2e/organization-settings.spec.ts` and step 8 in `web/e2e/phone-width.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the organization settings pages under one organization entry`.
- The main navigation had 16 entries after S-054, already cut at 360 px, and this story would have added five more; the
  maintainer chose one "Organization" entry with a sub-navigation (D311). An Admin's main navigation now has 12
  entries.
- Pages that need `organization:read` only (Outbound addresses, Keyring) are read-only for a role without
  `organization:write`; in L1 only Admins hold either Permission.
- S-054 has landed; this story moves its two entries into the sub-navigation along with those of S-015 and S-038.
- Added to `files_touched` while implementing: `web/e2e/global-setup.ts` starts the end-to-end server with
  `MUSTER_SECRET_KEYS` set to the development key followed by a new key (the development key first, so it is active on
  the fresh database) and passes it to the specs as `MUSTER_E2E_SECRET_KEYS`, for the `rotate-key` of step 5; and
  `web/src/components/keyring-status.test.tsx`. On the shared end-to-end server an older key becomes removable only
  once no open Alert Group of an earlier spec still depends on it, so `organization-settings.spec.ts` asserts after the
  activation that the old key is "Still needed" or removable, and the component test covers every state, the
  removable sentence included; the rotation leaves both keys held, so later specs are unaffected.
- About 35 files, most of them small: if the moved specs and the new pages grow past 40, the sub-navigation with the
  moved pages can go first in a pull request of its own.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-20.FR-1 | partial | the pages; with S-055 complete |
| C-20.FR-3 | partial | the editor; with S-055 complete |
| C-20.FR-6 | partial | the Keyring page; with S-071 complete |
| C-20.AC-2 | partial | the Keyring page reports the old key; with S-071 complete |
| C-20.AC-4 | partial | the Severity levels page; with S-055 complete |
| C-04.FR-8 | partial | editing the token grace on the Security page |
