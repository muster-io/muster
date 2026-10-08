---
id: S-038
title: Route message editors, Lookup tables and Link rules pages, links block (FE)
capability: C-12
kind: fe
layer: L1
depends_on: [S-027, S-030, S-033, S-037]
covers: [C-12.FR-2, C-12.FR-3, C-12.FR-5, C-12.FR-6, C-12.FR-9, C-12.AC-2, C-12.AC-6, C-09.FR-14, C-08.FR-1]
files_touched:
  - web/src/components/route-policy-message.tsx
  - web/src/components/template-editor.tsx
  - web/src/components/template-editor.test.tsx
  - web/src/components/template-preview.tsx
  - web/src/components/template-preview.test.tsx
  - web/src/components/sample-picker.tsx
  - web/src/components/route-form.tsx
  - web/src/components/template-error-banner.tsx
  - web/src/components/route-list.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/routes/admin.organization.lookup-tables.index.tsx
  - web/src/routes/admin.organization.lookup-tables.$lookupTableId.tsx
  - web/src/routes/admin.organization.lookup-tables.new.tsx
  - web/src/components/lookup-table-editor.tsx
  - web/src/components/lookup-table-editor.test.tsx
  - web/src/routes/admin.organization.link-rules.index.tsx
  - web/src/routes/admin.organization.link-rules.$linkRuleId.tsx
  - web/src/routes/admin.organization.link-rules.new.tsx
  - web/src/components/link-rule-form.tsx
  - web/src/components/alert-group-links.tsx
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/route-message.spec.ts
  - web/e2e/link-rules.spec.ts
  - web/e2e/alert-group-links.spec.ts
acceptance:
  - "[C-12.FR-3, C-08.FR-1] The Route editor has a Message section with \"Language\" (English or Русский) and three editors — \"Root message\", \"Alert line\" and \"Ack timeout notice\" — each either \"Built-in\" or a custom template."
  - "[C-12.FR-5, C-12.FR-2] Each editor previews the template against a chosen recent Stored Snapshot or Alert Group, by default the Route's most recent ones, and shows the rendered message under it, refreshed as the text changes; \"Custom\" starts from the built-in template's source, and the preview switches between Mattermost and Telegram markup."
  - "[C-12.AC-2] Typing `{{ env \"HOME\" }}` marks line 1, column 4 with \"Unknown function: env\", the preview shows the error instead of a message, and \"Save\" shows the same error at that editor without saving the Route."
  - "[C-12.FR-9] Organization → Lookup tables lists the tables; a table is edited as a grid of a key column and its named columns, rows are added and removed, and deleting a table a Link rule uses shows \"This table is used by a Link rule and cannot be deleted.\""
  - "[C-12.FR-9, C-12.AC-6] Organization → Link rules lists the rules, the built-in \"Explore\" marked \"Built-in\" without \"Delete\"; the form takes the name, Matchers with the Matcher builder, the scope (Alert Group, or each value of a label) and the URL template, whose preview against a Stored Snapshot shows the rendered link, for example `https://grafana.example.org/d/latency?var-ns=api`."
  - "[C-12.FR-9, C-09.FR-14] The Alert Group page shows a Links block — Link rules, \"Runbook\", \"Dashboard\" and \"Source\" — each opening in a new tab; at 360 CSS pixels it fits without horizontal scrolling."
  - "[C-12.FR-9] Without `link-rules:write` or `lookup-tables:write` the pages are read-only: no create, edit or delete."
  - "[C-12.FR-6] A Route whose template keeps failing shows \"Template error since HH:MM: {error}. Messages use the fallback template.\" in the Route editor and a mark in the Routes list, from `Route.template_error`; the Alert Group's Timeline shows the `fallback_template_used` entry as \"Fallback template used: {template} failed — {error}\"."
  - "[C-12.FR-9] The Audit log names the changed fields of Lookup tables and Link rules, and the navigation lists \"Lookup tables\" and \"Link rules\" under Organization."
verify: "make ci e2e"
operator_attention: false
issue: 38
---

# S-038. Route message editors, Lookup tables and Link rules pages, links block (FE)

## Scope

**IN**

- The Message section of the Route editor: language and the three template editors with live previews.
- The Organization pages for Lookup tables and Link rules, with a preview of a Link rule's URL.
- The Links block on the Alert Group page.

**OUT**

- The Destinations section of the Route editor and the Mention settings of a Destination (S-064); the Destination
  preview (S-048); the ack timeout and Reminder fields (S-050).

## Contracts

- **API used**: `getRoute`, `updateRoute`, `createRoute`, `previewTemplate`, `listIntegrations` (a Stored Snapshot
  belongs to an Integration, and `listStoredSnapshots` requires one), `listStoredSnapshots`, `listAlertGroups`,
  `listLookupTables`, `createLookupTable`, `getLookupTable`, `updateLookupTable`, `deleteLookupTable`, `listLinkRules`,
  `createLinkRule`, `getLinkRule`, `updateLinkRule`, `deleteLinkRule`, `getAlertGroup`.
- **Message section** (`route-policy-message.tsx`, in `route-form.tsx`): "Language" (`policy.language`: English,
  Русский); three editors bound to `policy.templates.root_message`, `.line`, `.ack_timeout_notice` with a "Built-in" /
  "Custom" switch (`null` / a string). "Custom" pre-fills the editor with the built-in template's source — `source` of
  `previewTemplate` with an empty `template` in the Route's language. The ack timeout notice is stored now and used
  from S-050 on.
- **Template editor** (`template-editor.tsx`): no third-party code editor — a plain `<textarea>` over a layer that draws
  the same text with Go-template highlighting and line numbers, so that it works under the Content Security Policy of
  the app listener, `style-src 'self'` (`internal/server/spa.go`): it injects no `<style>` element and sets no `style`
  attribute (the textarea and the layer scroll together through `scrollTop` and `scrollLeft`), and adds no dependency;
  `route-message.spec.ts` collects violations with `watchCsp` (`web/e2e/support.ts`) and expects none; it marks
  `errors[]` of `previewTemplate` and of a `422` at their `line` and `column` with the error text (`unknown_function` →
  "Unknown function: {name}", the name read from the template at that position; `template_syntax` → the `detail`).
- **Preview** (`template-preview.tsx`, `sample-picker.tsx`): debounced `previewTemplate` with the editor's kind,
  `route_id`, `language` and the chosen sample, picked in a native `<select>` (D250) — "Recent snapshots of this route"
  (default; a Link rule, which has no Route, starts from "Built-in example"), one Stored Snapshot by its time (with
  `stored-snapshots:read`), or one Alert Group by `#N` — and the "Mattermost" / "Telegram" switch as `format`
  (`markdown`, `html`); a Mattermost result is shown as rendered Markdown inside a frame — the subset Muster lays out
  (lines, `- ` lists, bold, italics, strike-through, code and links) drawn into elements by the page itself, with no
  dependency — and a Telegram result as its HTML text, untrusted text never interpreted as HTML; links open only when
  they are `http(s)`, in a new tab with `rel="noopener noreferrer"`; "Shortened to fit" appears when `truncated`.
- **Lookup tables** (navigation entry "Lookup tables" under Organization with `lookup-tables:read`):

  | Route | Permission | Content |
  |---|---|---|
  | `/admin/organization/lookup-tables` (`admin.organization.lookup-tables.index.tsx`) | `lookup-tables:read` | list: name, description, columns, rows; "Create table" with `:write` |
  | `/admin/organization/lookup-tables/$lookupTableId` | `lookup-tables:read` | name, description, columns, the grid of rows; save with `If-Match` |

  "Create table" opens `/admin/organization/lookup-tables/new` (`admin.organization.lookup-tables.new.tsx`) with the same
  editor. A `422 column_mismatch` marks the row; `409 in_use` shows "This table is used by a Link rule and cannot be
  deleted." (or "… cannot be renamed." for a new name) followed by the Link rules that read it — the Problem names them
  only in its `detail`, which clients never parse, so the page reads `listLinkRules` and names the rules whose URL
  templates call `lookup` with the table's name; `412` and `428` show the conflict message of S-015 with "Reload".
- **Link rules** (navigation entry "Link rules" under Organization with `link-rules:read`):

  | Route | Permission | Content |
  |---|---|---|
  | `/admin/organization/link-rules` (`admin.organization.link-rules.index.tsx`) | `link-rules:read` | list: name, scope, Matchers, "Built-in" badge; "Create rule" with `:write` |
  | `/admin/organization/link-rules/$linkRuleId` | `link-rules:read` | the form; "Delete" except for the built-in rule |

  "Create rule" opens `/admin/organization/link-rules/new` (`admin.organization.link-rules.new.tsx`) with the same form.
  The form reuses the Matcher builder of S-027; the scope is "Alert Group" or "Each value of label" with a label field;
  the URL template editor previews the link through `previewTemplate` (kind `link_rule`, with `templates:preview`)
  against the built-in example or a chosen Stored Snapshot or Alert Group. The built-in rule keeps its name and scope
  (read-only fields), as `builtin_immutable` requires.
- **List routes as `index` files**: the list pages are `….index.tsx`, so that TanStack Router makes the detail routes
  `$lookupTableId` and `$linkRuleId` siblings of the list, not children rendered inside a list layout without an
  `<Outlet>`.
- **Navigation and the Audit log** (`app-shell.tsx`, `audit-diff.tsx` and their tests): the entries "Lookup tables"
  (`lookup-tables:read`) and "Link rules" (`link-rules:read`) join the Organization entries of the shell;
  `audit-diff.tsx` names the fields of the resource types `lookup_table` (name, description, columns, rows) and
  `link_rule` (name, Matchers, scope, URL template), as it does for `route`.
- **Template errors** (C-12.FR-6; `template-error-banner.tsx`, `route-form.tsx`, `route-list.tsx`, `timeline.tsx`):
  while `Route.template_error` is set, the Route editor shows the banner of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) for a Route, "Template error since HH:MM:
  {error}." with "Messages use the fallback template.", the time in the user's time zone and the error as untrusted
  text, and the Routes list marks the Route; the Timeline of S-030 gains the text of the `system` entry
  `fallback_template_used` (S-036): "Fallback template used: {template} failed — {error}".
- **Links block** (`alert-group-links.tsx`): `AlertGroup.links` as a list of named links with `rel="noopener
  noreferrer"` and `target="_blank"`, under the header on the Alert Group page; hidden when empty.

## Steps

1. Add the template editor and the preview with its sample picker. Check: `template-editor.test.tsx` marks an error at
   its line and column.
2. Add the Message section to the Route editor. Check: Playwright steps 1 to 3 of Verification.
3. Add the Lookup tables pages. Check: `lookup-table-editor.test.tsx` and step 4.
4. Add the Link rules pages and the links block. Check: steps 5 to 7.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal create the Lookup table
`grafana` with the row `prod` and send the Alerts of S-037 (Alert Group "HighLatency" with a runbook and a
`generatorURL`). Then in Playwright:

1. Routes → "Create route" → On-call → Message → "Language" offers "English" and "Русский"; "Root message" shows
   "Built-in".
2. "Alert line" → "Custom" → the editor holds the built-in line template → replace it with
   `{{ .Labels.pod }} on {{ .Labels.cluster }}` → the preview under it shows a line such
   as "b on prod" from the Route's recent snapshots; sample "Alert Group" → `#N` of HighLatency → the preview changes.
3. "Root message" → "Custom" → "Telegram" → the preview shows the built-in body as Telegram HTML → replace the text with
   `{{ env "HOME" }}` → line 1, column 4 is marked with "Unknown function: env" and the preview shows the error → "Save" → the same error at "Root message", and the Routes list has no new Route.
4. Organization → Lookup tables → "grafana" → add a row `stage` with `https://grafana-stage.example.org` and `PROM2` →
   "Save" → the list shows 2 rows.
5. Organization → Link rules → "Create rule" → name "Dashboard", Matcher `cluster =~ .+`, scope "Alert Group", URL
   template `{{ lookup "grafana" .Labels.cluster "address" }}/d/latency?var-ns={{ .Labels.namespace }}` → the preview
   shows `https://grafana.example.org/d/latency?var-ns=api` → "Save".
6. Link rules → "Explore" shows "Built-in" and no "Delete"; Lookup tables → "grafana" → "Delete" → "This table is used
   by a Link rule and cannot be deleted."
7. Alert Groups → HighLatency → the Links block shows "Dashboard", "Runbook" and "Source"; "Dashboard" opens
   `https://grafana.example.org/d/latency?var-ns=api` in a new tab; at 360 × 740 pixels
   `document.documentElement.scrollWidth` equals the viewport width.
8. As a Viewer → Link rules and Lookup tables show no "Create", "Save" or "Delete".
9. No Destination type exists yet to make a template fail at delivery, so from a terminal set the error state of the
   Route "pods" directly: `psql "$MUSTER_DATABASE_URL" -c "UPDATE routes SET template_error_since = now(),
   template_error = 'map has no entry for key \"pod\"', template_error_template = 'line' WHERE name = 'pods'"` → Routes
   marks "pods" → open it → "Template error since HH:MM: map has no entry for key \"pod\". Messages use the fallback
   template."
10. Audit log → the entries of step 4 and step 5 name "Rows" and "URL template"; the navigation shows "Lookup tables"
    and "Link rules" under Organization.

The Timeline text of `fallback_template_used` is checked by `timeline.test.tsx`; the entry itself comes from S-036.

`make e2e` runs these steps as `web/e2e/route-message.spec.ts`, `link-rules.spec.ts` and `alert-group-links.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add route message editors, lookup tables and link rules pages`.
- Previews are untrusted output: rendered Markdown without raw HTML, links not followed automatically.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-12.FR-2 | partial | the line template editor; with S-036 complete |
| C-12.FR-3 | partial | the language field; with S-036 complete |
| C-12.FR-5 | partial | the editor preview; with S-036 complete |
| C-12.FR-6 | partial | the template error banner on the Route and the Timeline text; with S-036 and S-037 complete |
| C-12.FR-9 | partial | the pages and the links block; with S-037 complete |
| C-12.AC-2 | partial | the error position in the editor; with S-036 complete |
| C-12.AC-6 | partial | the page preview; with S-037 complete |
| C-09.FR-14 | partial | the Links block |
| C-08.FR-1 | partial | the Message section of the Route editor |
