---
id: S-056
title: Organization settings pages (FE)
capability: C-20
kind: fe
layer: L1
depends_on: [S-015, S-055]
covers: [C-20.FR-1, C-20.FR-3, C-20.FR-6, C-20.AC-2, C-20.AC-4, C-04.FR-8]
files_touched:
  - web/src/routes/admin.organization.general.tsx
  - web/src/routes/admin.organization.severity-levels.tsx
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/routes/admin.organization.retention.tsx
  - web/src/routes/admin.organization.network.tsx
  - web/src/routes/admin.organization.security.tsx
  - web/src/components/organization-form.tsx
  - web/src/components/time-zone-picker.tsx
  - web/src/components/severity-mapping-editor.tsx
  - web/src/components/severity-mapping-editor.test.tsx
  - web/src/components/severity-values.tsx
  - web/src/components/network-list-editor.tsx
  - web/src/components/network-list-editor.test.tsx
  - web/src/components/keyring-status.tsx
  - web/src/components/app-shell.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/organization-settings.spec.ts
acceptance:
  - "[C-20.FR-1] Organization → General edits the name and the time zone of messages; Alert handling edits \"critical is Urgent\" and the Instance labels; Retention edits the four periods in days and shows the `retention_order` refusal next to the summaries field; each page saves with `If-Match` and shows S-015's conflict message over a newer version."
  - "[C-20.FR-1, C-20.AC-4] Severity levels edits the severity label, the mapping of values to critical, warning and info, and each level's emoji and colour, and lists the values seen over the last 14 days with their counts; after Alerts with `severity=\"P5\"` arrive, `P5` is listed, marked \"No mapping — counts as warning\", with the banner \"These values have no mapping and count as warning: P5.\" and a \"Map\" action that adds it to the mapping."
  - "[C-20.FR-3] Network edits the outbound address policy (standard or strict) and the allowed and denied lists, shows the rules that always apply — link-local, cloud metadata, unspecified and multicast addresses — and says \"A denied entry always wins.\""
  - "[C-20.FR-6, C-20.AC-2] Security shows the Keyring — every key id, which is active, which replicas hold or lack it — and, for an older key nothing depends on any more, \"Key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS.\"; it explains that a key is activated with `muster secrets rotate-key`."
  - "[C-20.FR-1, C-04.FR-8] Security also edits the token grace for OIDC accounts without an offline token, in days, next to the TOTP policy of S-015."
verify: "make ci e2e"
operator_attention: false
issue: 56
---

# S-056. Organization settings pages (FE)

## Scope

**IN**

- Organization → General, Severity levels, Alert handling, Retention, Network, and the Keyring and token grace on
  Security.

**OUT**

- The TOTP policy on Security (S-015), the outgoing heartbeat (S-054), Lookup tables and Link rules (S-038).
- Activating a key, which is a CLI command (S-055).

## Contracts

- **API used**: `getOrganization`, `updateOrganization`, `listSeverityValues`, `getOutboundPolicy`,
  `updateOutboundPolicy`, `getKeyring`, the `organization` hint.
- **Routes and navigation** (an "Organization" group in the navigation, each entry shown only with its Permission, as
  in S-015):

  | Route | Permission | Content |
  |---|---|---|
  | `/admin/organization/general` | `organization:write` | name, time zone of messages |
  | `/admin/organization/severity-levels` | `organization:write` (`organization:read` for the values seen) | severity label, mapping, emoji and colours, values seen |
  | `/admin/organization/alert-handling` | `organization:write` | "critical is Urgent", Instance labels |
  | `/admin/organization/retention` | `organization:write` | the four retention periods |
  | `/admin/organization/network` | `organization:read`, saving with `organization:write` | the outbound address policy |
  | `/admin/organization/security` | `organization:write`; the Keyring with `organization:read` | TOTP policy (S-015), token grace, Keyring |

- **One resource, several pages** (`organization-form.tsx`): every page edits its slice of the Organization and sends
  the whole `OrganizationInput` as read, with `If-Match` from `etag`; a `412` shows "Someone else changed these
  settings. Reload to see them." Field errors of a `422` are shown at their pointer.
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
- **Network** (`network-list-editor.tsx`): "Standard — private addresses allowed, loopback blocked" and "Strict — only
  public addresses and the allowed entries"; the allowed and denied lists of networks, host names and domains with
  per-entry errors at their pointer; the read-only "Always blocked" list from `always_blocked`; "A denied entry always
  wins."
- **Security** (`keyring-status.tsx`): a table of keys — id, "Active", "Held by {replicas}", "Missing on {replicas}" in
  red, "Still needed" or the text "Key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS." — and the
  explanation "To activate a key, add it to MUSTER_SECRET_KEYS on every replica, then run `muster secrets rotate-key
  --activate <id> --actor <your name>`."; with `organization:read` only, the page shows the Keyring and no form. "Token
  grace for OIDC accounts without an offline token" in days (`oidc_token_grace_seconds`).

## Steps

1. Write the shared form and General, Alert handling and Retention. Check: Playwright steps 1 and 2.
2. Write Severity levels with the values seen. Check: `severity-mapping-editor.test.tsx` refuses duplicates; Playwright
   step 3.
3. Write Network. Check: `network-list-editor.test.tsx` maps pointers to entries; Playwright step 4.
4. Add the Keyring and the token grace to Security. Check: Playwright step 5.

## Verification

Export `MUSTER_SECRET_KEYS` with the development key of S-004 followed by a new key, as in S-055 — the variable replaces
the development default, so it lists both and the Keyring has two keys — and run `make dev`; sign in as
`admin@example.org` / `muster-dev-password`; from a terminal, send Alerts with `severity="P5"` as in S-055. Then in
Playwright:

1. The navigation group "Organization" shows "General", "Severity levels", "Alert handling", "Retention", "Network",
   "Security" → General → time zone "Europe/Berlin" → "Save" → "Saved"; the Audit log shows `organization.updated`.
2. Retention → "Alert Group summaries" 30 while "Alert details" is 90 → "Save" → "Keep Alert Group summaries at least as
   long as alert details." at the field → 90 → "Save" → "Saved".
3. Severity levels → the banner "These values have no mapping and count as warning: P5." and the row "P5 · 1 · warning ·
   No mapping — counts as warning" → "Map" → level "info" → "Save" → the row shows "info" and the banner is gone.
4. Network → "Always blocked" lists link-local, cloud metadata, unspecified and multicast addresses → remove
   `127.0.0.0/8` from "Allowed" → "Save" → add `127.0.0.0/8` back → "Save"; an entry "not a network" is refused next to
   it.
5. Security → the Keyring lists two keys, one "Active" and held by one replica; terminal, with the same environment:
   `./bin/muster dev secrets rotate-key --activate <other id> --actor ops` (S-055) → without a reload the other key is
   "Active" and, once the open Root messages are edited, the first shows "Key {id} is no longer used and can be removed
   from MUSTER_SECRET_KEYS."
6. Security → "Token grace" 3 days → "Save"; in a second browser context, an edit saved meanwhile makes the first save
   show "Someone else changed these settings. Reload to see them."
7. As a Responder → no "Organization" group; `/admin/organization/general` shows "You do not have permission to see this
   page."

`make e2e` runs these steps as `web/e2e/organization-settings.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the organization settings pages`.
- Pages that need `organization:read` only (Network, the Keyring) are read-only for a role without
  `organization:write`; in L1 only Admins hold either Permission.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-20.FR-1 | partial | the pages; with S-055 complete |
| C-20.FR-3 | partial | the editor; with S-055 complete |
| C-20.FR-6 | partial | the Keyring page; with S-055 complete |
| C-20.AC-2 | partial | the Keyring page reports the old key; with S-055 complete |
| C-20.AC-4 | partial | the Severity levels page; with S-055 complete |
| C-04.FR-8 | partial | editing the token grace on the Security page |
