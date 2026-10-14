---
id: S-077
title: "Bridge Connection and Destination pages, Account links through a Bridge in the profile, and labels (FE)"
capability: C-22
kind: fe
layer: L1
depends_on: [S-076, S-070]
covers: [C-22.FR-1, C-22.FR-2, C-22.FR-3, C-22.FR-9, C-22.FR-12, C-22.FR-15, C-22.FR-18, C-22.AC-18, C-22.AC-21]
files_touched:
  - web/src/components/bridge-connection-fields.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/components/bridge-destination-fields.tsx
  - web/src/components/bridge-destination-fields.test.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/destination-health.tsx
  - web/src/components/destination-label.ts
  - web/src/components/destination-label.test.ts
  - web/src/components/link-bridge-dialog.tsx
  - web/src/components/link-bridge-dialog.test.tsx
  - web/src/components/profile-account-links.tsx
  - web/src/components/account-links-table.tsx
  - web/src/components/account-links-table.test.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/destinations.index.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/bridge-connection.spec.ts
  - web/e2e/bridge-destination.spec.ts
  - web/e2e/account-links.spec.ts
acceptance:
  - "[C-22.FR-1, C-22.FR-2, C-22.AC-21] Connections → New → Bridge saves a Connection with a name, the base URL, the token, the event delivery mode, the proxy and the limiter; the push secret field and the callback address with its hint appear only in push mode; \"Check\" shows the steps with latency and path and, on success, the bridge's name and version, the messenger, the protocol version and what it offers (`bridge-connection.spec.ts`)."
  - "[C-22.FR-2] A failing check shows its text: \"The bridge refused the token.\", the protocol version, or the missing event delivery mode (`bridge-connection-fields.test.tsx`)."
  - "[C-22.FR-3, C-22.AC-21] Destinations → New → Bridge saves a channel id through a Bridge Connection, shows the channel's name from its check, refuses a channel the bridge forbids with the bridge's text at the channel field, and offers only nobody and chosen users in the Mention settings (`bridge-destination.spec.ts`)."
  - "[C-22.FR-18] With `limits.send_per_second` and `send_per_second_per_channel` in the Connection's `info`, the Connection and Destination forms propose half of them as limiters; without them, the defaults (`bridge-destination-fields.test.tsx`)."
  - "[C-22.FR-9] The delivery section of the Alert Group page shows \"Past the edit window: updated on the next change of status\" for a delivery whose `past_edit_window` is true, beside its state (`alert-group-deliveries.test.tsx`), and the Timeline names the delivery event `republished_past_edit_window` \"Published again in {destination}: past the edit window\" (`timeline.test.tsx`)."
  - "[C-22.FR-12] The Timeline and the Audit log name the Transport `bridge` \"Bridge\" and «Мост», with an explicit case in both switches, so the locale keys are used and kept by the extraction (`timeline.test.tsx`; the Audit log's switch has no `default`, so the type check proves every Transport has its label)."
  - "[C-22.FR-15, C-22.AC-18, C-22.AC-21] Profile → Messenger accounts → \"Link through {Connection}\" for a Bridge shows the token with a copy button, the instruction to send it to the bot and the bridge's link when it has one; where the bridge offers a code, a handle field labelled by its kind (Email, Login, Username, User id), then the code entry; after linking, the row reads \"{messenger} · {display name} · via {Connection}\" (`account-links.spec.ts`)."
  - "[C-22.FR-1] The labels of S-070 cover the Bridge type: the Connections list shows the messenger's name from `info` and the host of the base URL; a Destination reads \"<name> · <messenger> · via <Connection> · <channel name>\" in the Route editor and its pickers (`destination-label.test.ts`)."
  - "Every new text exists in English and Russian, the pages of the acceptance statements show no horizontal scrolling at 360 px wide in either language, and no Content Security Policy violation is reported (`bridge-connection.spec.ts`, `bridge-destination.spec.ts`)."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-077. Bridge Connection and Destination pages, Account links through a Bridge in the profile, and labels (FE)

## Scope

**IN**

- The Bridge variant of the Connection form and page: base URL, token, event delivery mode, push secret and callback
  address in push mode, the check with the bridge's `info`.
- The Bridge variant of the Destination form: channel id, the check with the channel's name, Mention settings with
  nobody or chosen users, limiters proposed from `info`.
- "Past the edit window" in the delivery section, and the Timeline's text of a republication past the edit window.
- The label of the Transport `bridge` in the Timeline and the Audit log.
- Linking through a Bridge in the profile, by token and by code; Bridge rows of the Account link tables.
- The Bridge type in the labels of S-070.

**OUT**

- The backend (S-073, S-080, S-074, S-081, S-075, S-076); the example bridge (S-078); the documentation (S-074, S-081,
  S-076, S-079).

## Contracts

- **Operations used**: `createConnection`, `updateConnection`, `getConnection`, `listConnections`, `checkConnection`,
  `createDestination`, `updateDestination`, `getDestination`, `checkDestination`, `listLinkableConnections`,
  `startBridgeLink`, `startBridgeCodeLink`, `confirmBridgeLink`, `listMyAccountLinks`, `deleteMyAccountLink`,
  `listUserAccountLinks`, `listAlertGroupDeliveries` through the generated orval client.
- **Pages** (`connections.new.tsx`, `connections.$connectionId.tsx`, `connections.index.tsx`, `destinations.new.tsx`,
  `destinations.$destinationId.tsx`, `destinations.index.tsx`): the type pickers of Connections → New and
  Destinations → New gain Bridge; the Destination page's `isEditable` and `hasCheck` take `bridge` and show the Bridge
  fields; the lists show the Bridge columns of the labels below; the narrowing S-073 and S-080 added becomes the
  labels proper.
- **Connection page** (`bridge-connection-fields.tsx`, `connection-check.tsx`): the fields and secret fields of
  C-22.FR-1 with the write-only pattern of S-040; the event delivery mode as two radio buttons with a line each —
  "Muster polls the bridge; the bridge needs no address of Muster" and "The bridge pushes events to Muster's ingest
  address"; in push mode the push secret and the callback address with the hint of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices); the check's steps, `info` and
  `event_delivery` named in `connection-check.tsx`, and the `info` panel (bridge, messenger, protocol version, features
  as a list of what the bridge offers, limits, lookup kind, delivery modes, when it was read).
- **Destination form** (`bridge-destination-fields.tsx`): a Bridge Connection picker, the channel id with "As the
  messenger names it" as its hint, the check result with the channel's name and its item `channel` named in
  `destination-check.tsx`, the Mention settings component of S-038 limited to nobody and chosen users, and limiters
  proposed by C-22.FR-18.
- **Profile** (`link-bridge-dialog.tsx`, `profile-account-links.tsx`): the token with a copy button and its expiry,
  "Send this token to the bot in a direct message", the link from `link_url`; the code tab only when the Connection's
  `link_methods` has `code`, its field labelled by `handle_kind`; errors `handle_not_found` and `code_not_offered`
  shown at the field; the list refreshes on the live hint of S-052 when the token is used.
- **Labels** (`destination-label.ts`, `destination-health.tsx`): the messenger's name from the Connection's `info`,
  "Bridge" («Мост») while none was read.
- **Delivery state** (`alert-group-deliveries.tsx`): the flag `past_edit_window` of S-081, shown with its text beside
  the delivery's state.
- **Timeline and Audit log** (`timeline.tsx`, `admin.audit-log.tsx`): the Transport `bridge` with an explicit case in
  both `transportLabel` switches — the locale keys `timeline.transport.bridge` and `audit.transport.bridge` are used, so
  the extraction (`removeUnusedKeys`) keeps them — and the Audit log's switch loses its `default`; the delivery event
  `republished_past_edit_window` with its text in the Timeline.
- **Locales**: every text in `en.json` and `ru.json`, the Russian name of the type being «Мост».

## Steps

1. Add the Connection fields and the check panel. Check: `bridge-connection-fields.test.tsx`,
   `connection-form.test.tsx`.
2. Add the Destination fields, limiters and labels. Check: `bridge-destination-fields.test.tsx`,
   `destination-label.test.ts`.
3. Add the profile dialog, the delivery state, and the Transport and delivery event in the Timeline and the Audit log.
   Check: `link-bridge-dialog.test.tsx`, `alert-group-deliveries.test.tsx`, `timeline.test.tsx`, `make lint`.
4. Write the end-to-end specs at 360 px and in Russian. Check: Verification.

## Verification

Playwright against `muster dev` with the fake bridge (`127.0.0.1:18094`), in `web/e2e`:

1. `bridge-connection.spec.ts`: as the Admin, Connections → New → Bridge, "Base URL" `http://127.0.0.1:18094`,
   "Token" `muster-dev-bridge-token-b`, "Polling", Save, Check → visible "Fake chat", "Protocol version 1", "Buttons",
   "Thread messages"; switch to "Push" → visible "Push secret" and "The bridge posts events to http://localhost:8081/
   api/v1/callbacks/bridge/CN…"; with the fake's `protocol_version` set to `2`, Check → visible "The bridge speaks
   protocol version 2; this Muster speaks 1."; at 360 px no horizontal scroll; in Russian the type reads «Мост».
2. `bridge-destination.spec.ts`: Destinations → New → Bridge through "br", channel `ch-nobot`, Save → visible "The bot
   is not a member of this channel." at the channel field; channel `ch-alerts`, Save → the page shows "alerts"; the
   Mention settings offer no "Everyone in the chat"; the Route editor's picker reads "alerts · Fake chat · via br ·
   alerts".
3. `account-links.spec.ts`: as bob, Profile → "Link through br" → the token and "Send this token to the bot in a direct
   message"; the test posts it to `/_fake/link` as `u-bob` → the row "Fake chat · Bob Brown · via br" appears without
   a reload; with the fake's `user_lookup` `email` and a check of the demo Connection "Dev Bridge", the fake's other
   bot, so that Muster has read its `info`, the "Email" field and a code from `/_fake/direct-messages` link the account
   through "Dev Bridge"; the Timeline of an Alert Group bob acknowledged through the fake shows "Bridge".

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add bridge connection and destination pages and linking through bridges`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-1 | partial | the page |
| C-22.FR-2 | partial | the page |
| C-22.FR-3 | partial | the form and "Check" |
| C-22.FR-9 | partial | the delivery state and the Timeline text |
| C-22.FR-12 | partial | the Transport's label in the Timeline and the Audit log |
| C-22.FR-15 | partial | the profile |
| C-22.FR-18 | full | |
| C-22.AC-18 | partial | the profile shows the link |
| C-22.AC-21 | full | |
