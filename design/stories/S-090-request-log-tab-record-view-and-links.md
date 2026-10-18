---
id: S-090
title: "Request log tab, the record view, and the links from the Alert Group page and the check panels (FE)"
capability: C-23
kind: fe
layer: L1
depends_on: [S-089]
covers: [C-23.FR-6, C-23.AC-1, C-23.AC-6]
files_touched:
  - web/src/components/request-log.tsx
  - web/src/components/request-log.test.tsx
  - web/src/components/request-record.tsx
  - web/src/components/request-record.test.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/connection-check.test.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/destination-test-panel.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/request-log.spec.ts
  - docs/operations/request-log.md
  - mkdocs.yml
acceptance:
  - "[C-23.FR-6, C-23.AC-6] The page of every Connection type and of an outgoing webhook Destination has the tab \"Request log\" («Журнал запросов»), opened by `?tab=request-log`: a list, newest first, of the time in the user's time zone, a direction arrow with its accessible name (\"Sent\" / \"Received\"), the operation, the status or the error code, and the latency, loaded page by page with \"Load more\"; filters by direction, status class and operation change the list and the URL; an empty list says \"No requests in the last 7 days.\" (`request-log.test.tsx`, `request-log.spec.ts`)."
  - "[C-23.FR-6, C-23.AC-6] A record opens in a dialog with the request — method, URL, headers, body — and the answer — status, headers, body — each body formatted when it is JSON and shown as plain text otherwise, a truncated body marked \"Cut at 16 KB of {size}\", the kind of check, the latency, the path and the attempt, links to the Destination, the Alert Group and \"Requests of this check\", and copy buttons for the URL, the headers and each body; `[redacted]` values are shown as they are; nothing in a header or body is rendered as HTML or Markdown (`request-record.test.tsx`)."
  - "[C-23.FR-6, C-23.AC-1] In the delivery section of the Alert Group page, each Destination's row has \"Requests\", which opens the records of that Destination for that Alert Group in the same dialog list; the page shows no horizontal scrolling at 360 px wide with it, in either language (`alert-group-deliveries.test.tsx`, `request-log.spec.ts`)."
  - "[C-23.FR-6] The Connection check panel — also before saving — and the Destination's check and test panels show \"Requests of this check\" once a result names its `request_log_run`, which opens the records of that run (`connection-check.test.tsx`, `destination-test-panel.test.tsx`, `request-log.spec.ts`)."
  - "[C-23.FR-6] The documentation page Operations → Request log says what is recorded and what is not, the bounds, the masking and where the tab and the links are, and `make docs-check` passes."
  - "Every new text exists in English and Russian, the pages of the acceptance statements stay usable at 360 px wide, and no Content Security Policy violation is reported (`request-log.spec.ts`)."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 268
---

# S-090. Request log tab, the record view, and the links from the Alert Group page and the check panels (FE)

## Scope

**IN**

- The "Request log" tab of the Connection page (every type) and of the outgoing webhook Destination page, with its
  filters (C-23.FR-6, D354).
- The record view: the request and the answer with their headers and bodies, copy buttons.
- "Requests" for each Destination in the Alert Group page's delivery section: what was sent and what came back.
- "Requests of this check" in the Connection check panel, the check before saving included, and in the Destination's
  check and test panels.
- The documentation page.

**OUT**

- Recording, masking, pruning and the API (S-088, S-089).
- Design: the pages are functional; their visual design comes with the design work of L2 or L3 (D315).

## Contracts

- **Operations used**: `listRequestLog` (with `connection`, `destination`, `run`, `direction`, `status_class`,
  `operation`, `alert_group`), `getRequestRecord`, and `request_log_run` of `checkConnection`,
  `checkConnectionDraft`, `checkDestination` and `testDestination`, through the generated orval hooks.
- **List** (`request-log.tsx`): one component for the tab and the dialog lists, taking the owner, the Alert Group or
  the run; an infinite query over the cursor; the columns of C-23.FR-6, with the operation in a monospace font; a
  failed request's status cell shows its error code, translated like the codes of the checks (`checkCodeText`); the
  filters are search parameters of the route, so a link to a filtered list can be shared. Times use the user's time
  zone (NFR-9).
- **Record** (`request-record.tsx`): reads `getRequestRecord` when opened; the request and the answer side by side
  from 1024 px and one under the other below; headers as a two-column list; bodies in a scrollable `pre`, a JSON body
  formatted with `JSON.stringify(…, null, 2)` after `JSON.parse` succeeds, and the raw text otherwise; the copy buttons
  use the clipboard API and say "Copied". No body, header or URL is ever set as HTML.
- **Tabs** (`connections.$connectionId.tsx`, `destinations.$destinationId.tsx`): the tab beside the page's form, as
  `StatusTabs` draws tabs, shown to everyone with `connections:read` or `destinations:read`; on the Destination page
  only for an outgoing webhook — a messenger Destination's requests are its Connection's, and the Alert Group page and
  the check panels link to them.
- **Alert Group page** (`alert-group-deliveries.tsx`): "Requests" («Запросы») on each row opens a dialog with the
  list for that Destination and that Alert Group; the row keeps its layout at 360 px (NFR-15).
- **Check panels** (`connection-check.tsx`, `destination-check.tsx`, `destination-test-panel.tsx`): "Requests of this
  check" («Запросы этой проверки») under a result that names its run.
- **Texts** (`en.json`, `ru.json`): the tab, the columns, the arrows' names, the filters and their values, "Load
  more", the empty list, the record's sections, "Cut at 16 KB of {size}" with the size in KB, the kinds of checks
  (`connection_check` "Connection check", `draft_check` "Check before saving", `destination_check` "Destination check",
  `test` "Test message", `probe` "Broken probe"), the links and "Copied", with Russian plurals where a count is shown.
- **Documentation** (`docs/operations/request-log.md`, `mkdocs.yml`): what the Request log records — outbound
  requests of Connections and outgoing webhooks, inbound requests at the callbacks with Muster's answers, the checks —
  and what it leaves out (empty long polls, OIDC, the outgoing heartbeat, `muster doctor`, requests for unknown
  Connections); the bounds `request_log.max_records`, `request_log.max_age`, `request_log.body_max`; the masking; who
  may read it; where the tab and the links are. Linked from Operations in the navigation.

## Steps

1. Write the list and the record with their component tests. Check: `make test-web`.
2. Add the tabs, "Requests" on the Alert Group page and the links of the check panels, with the texts. Check:
   `make lint` (types and translations), `make test-web`.
3. Write the Playwright test and the documentation page. Check: `make e2e-web`, `make docs-check`, then Verification.

## Verification

Against `muster dev` with the data of S-088's Verification (a Publication to "alerts" through "Dev Mattermost") and a
test message to an outgoing webhook Destination of the fake webhook server whose endpoint answers `500`
(`request-log.spec.ts`, at 1280 px and 360 px, English and Russian):

1. Connections → "Dev Mattermost" → "Request log": the first row reads "→ createPost 201 … ms"; filtering by direction
   "Received" empties the list or shows only `press`; filtering by status class `2xx` keeps `createPost`.
2. The row opens the record: the request body formatted as JSON with `"channel_id"`, `Authorization: [redacted]`,
   and the answer's status `201`; "Copy body" puts the body on the clipboard (read back in the test).
3. The Alert Group page of the published Alert Group → delivery section → "Requests" on "alerts" lists the same
   `createPost`; at 360 px the page has no horizontal scroll (`document.documentElement.scrollWidth <= 360`).
4. Destinations → the outgoing webhook → "Send test message" → "Requests of this check" lists the `event` with `500`
   and its body; the tab "Request log" of that Destination lists it too.
5. Connections → New → Bridge, the fake bridge's address and its second token → "Check connection" → "Requests of
   this check" lists `getInfo` with "Check before saving".

Screenshots of steps 1 to 3 at both widths go into the pull request.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the request log tab and the record view`.
- The tab is not on messenger Destination pages: their requests are their Connection's, and the Request log is bounded
  per Connection; the Alert Group page and the check panels lead to the requests of one Destination.
- Bodies can be large and untrusted: they are shown as text in a scrollable block, never interpreted.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-23.FR-6 | full | |
| C-23.AC-1 | partial | "Requests" on the Alert Group page; the record and the API in S-088 |
| C-23.AC-6 | full | |
