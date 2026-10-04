---
id: S-048
title: Test and Preview panels on the Destination page (FE)
capability: C-16
kind: fe
layer: L1
depends_on: [S-043, S-046, S-047]
covers: [C-16.FR-1, C-16.FR-2, C-16.FR-3, C-16.FR-4, C-16.AC-3, C-16.AC-7, C-13.FR-13]
files_touched:
  - web/src/components/destination-test-panel.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/destination-preview-panel.tsx
  - web/src/components/test-source-picker.tsx
  - web/src/components/rendered-request.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/destination-test.spec.ts
  - web/e2e/destination-preview.spec.ts
acceptance:
  - "[C-16.FR-1, C-16.FR-4] Every Destination page has \"Test\" and \"Preview\" (with `destinations:test`), each with a source selector: \"Example\" (default) or a recent Alert Group of the Destination's Routes by `#N` and title."
  - "[C-16.FR-2] \"Send test message\" shows one block per step — \"Message\", \"Button press\", \"Event\" or \"Create\" — with the request (Secrets, the Signing secret and tokens shown as `[redacted]`), the response status, the response body as plain text, the extracted values, the duration and the outcome; `limited` reads \"The messenger is busy; nothing was sent. Try again in a few seconds.\""
  - "[C-16.FR-3, C-16.AC-7, C-13.FR-13] For a Mattermost Destination the \"Button press\" block reads \"Button presses reach Muster.\" or \"The button press did not reach Muster. Add the host of {MUSTER_INGEST_URL} to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.\""
  - "[C-16.AC-3] A test of an outgoing webhook in template mode shows the extracted values and shows every Secret value as `[redacted]`."
  - "[C-16.FR-4] \"Preview\" renders the Root message — Markdown for Mattermost, HTML for Telegram, both inside a sandboxed frame with links not followed — or each webhook request with method, URL, headers and body, without sending anything."
  - "[C-16.FR-2] After a successful test of a Broken Destination the page shows it healthy without a reload."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-048. Test and Preview panels on the Destination page (FE)

## Scope

**IN**

- The "Test" panel with the source selector and the result per step, including the Mattermost button press result.
- The "Preview" panel for every Destination type.

**OUT**

- Everything else on the Destination page (S-040, S-043, S-046).

## Contracts

- **API used**: `testDestination`, `previewDestination`, `listAlertGroups` (recent Alert Groups of the Destination's
  Routes, `route` filter), `getDestination`; the `destination` hint.
- **Source selector** (`test-source-picker.tsx`): "Example" (`{kind: "example"}`) or "Alert Group" with a search over
  the last 7 days of the Destination's Routes (`{kind: "alert_group", alert_group_id}`).
- **Test panel** (`destination-test-panel.tsx`): "Send test message" (disabled while a test runs); one block per
  `TestStep` with the step's name — `message` "Message", `press` "Button press", `event` "Event", `create` "Create" —
  the outcome ("Sent", or the class's text: `limited` "The messenger is busy; nothing was sent. Try again in a few
  seconds.", `transient` "No answer or a temporary error", `fatal` "Refused", `unknown` "Unexpected answer",
  `template_error` "Template error", `blocked` "Blocked by the outbound address policy"), `error` as plain text,
  `duration_ms`, the request (`rendered-request.tsx`: method, URL, headers, body as code), `response_status`,
  `response_body` as plain text and `extracted` as a table. The `press` block shows "Button presses reach Muster." when
  it has no error, otherwise its `error`. `health` from the result updates the page's health banner.
- **Preview panel** (`destination-preview-panel.tsx`): items of `previewDestination`; `markdown` rendered without raw
  HTML, `html` rendered in a sandboxed `iframe` (`sandbox=""`) with the Telegram subset, `json` and requests as code;
  links are shown, never followed automatically.

## Steps

1. Add the source selector and the test panel with its blocks. Check: `destination-test-panel.test.tsx` renders every
   class and the press block.
2. Add the preview panel. Check: Playwright steps of Verification.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; create from a terminal, as in S-047, the
Mattermost Destination "alerts", the Telegram Destination "tg-alerts" and the webhooks "auto" (events, Secret `token`)
and "chat" (template). Then in Playwright:

1. Destinations → "alerts" → "Test" → "Example" → "Send test message" → "Message: Sent" and "Button press: Button
   presses reach Muster."
2. From a terminal clear the fake server's `AllowedUntrustedInternalConnections` → "Send test message" → "Button press"
   shows "The button press did not reach Muster. Add the host of http://localhost:8081 to
   ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server."
3. "chat" → "Test" → "Create: Sent" with "Extracted values: id = m…" and the header `Authorization: Bearer [redacted]`.
4. "auto" → "Test" → "Event: Sent", response status 200.
5. "tg-alerts" → "Preview" → "Example" → the rendered message shows the example Alert Group's heading without the
   "🧪 Test message" mark, the expandable label section and the buttons; "chat" → "Preview" shows "Create", "Update", "Open thread", "Reply in
   thread" with their URLs and bodies; the fake servers recorded no new message.
6. With the limiter of "auto" set to 1 per 60 s and spent by a delivery from a terminal → "Test" → "Event: The messenger
   is busy; nothing was sent. Try again in a few seconds."
7. Make "alerts" Broken from a terminal (as in S-047), add the bot back → "Test" → the page shows "Healthy" without a
   reload.

`make e2e` runs these steps as `web/e2e/destination-test.spec.ts` and `destination-preview.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the test and preview panels to destination pages`.
- Response bodies come from outside: they are shown as text only, never as HTML.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-16.FR-1 | partial | the panel; with S-047 complete |
| C-16.FR-2 | partial | the result; with S-047 complete |
| C-16.FR-3 | partial | the press result; with S-047 complete |
| C-16.FR-4 | partial | the preview panel; with S-047 complete |
| C-16.AC-3 | partial | the panel; with S-047 complete |
| C-16.AC-7 | partial | the panel; with S-047 complete |
| C-13.FR-13 | partial | the press result on the Destination page; with S-040 and S-047 complete |
