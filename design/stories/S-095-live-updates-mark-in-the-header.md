---
id: S-095
title: The mark of the live updates in the header — connecting, live, reconnecting and offline (FE)
capability: C-09
kind: fe
layer: L1
depends_on: [S-030, S-072]
covers: [C-09.FR-26, C-09.AC-26]
files_touched:
  - web/src/lib/live.ts
  - web/src/lib/live-status.ts
  - web/src/lib/live-status.test.ts
  - web/src/components/live-indicator.tsx
  - web/src/components/live-indicator.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/live-indicator.spec.ts
  - docs/concepts/alert-groups.md
acceptance:
  - "[C-09.FR-26] The state follows the live-updates stream: `connecting` from the page load until the first `open`, `live` on every `open`, `reconnecting` once the stream has been closed for `live.reconnecting_after` (5 s), `offline` once it has been closed for `live.offline_after` (30 s) — counted from the page load before the first `open` — while a close shorter than 5 s leaves the state as it was, and any later `open` returns to `live` (`live-status.test.ts`, with a stand-in EventSource and fake timers)."
  - "[C-09.FR-26] Whenever the stream opens again after a loss, every query is invalidated: when the browser reconnected the same EventSource, as before, and also when the shell opened a new one after the old one closed for good (`app-shell.test.tsx`)."
  - "[C-09.FR-26] The mark is `data-testid=\"live-indicator\"` with `data-state` set to the state; its text — \"Connecting…\", \"Live\", \"Reconnecting…\", \"Offline\" («Подключение…», «Онлайн», «Переподключение…», «Нет связи») — sits in a `role=\"status\"` region, so each change is announced politely; the dot is never the only cue; pressing the mark opens a popover with the explanation of reference.md, which in `offline` names the time the stream was lost and has \"Reload page\" («Перезагрузить страницу»), which reloads it (`live-indicator.test.tsx`)."
  - "[C-09.AC-26] With the Alert Group list open, once the stream ends and its reconnects fail, the mark reads \"Reconnecting…\" and, with the page clock run 30 s forward, \"Offline\"; once the stream can be reached again it reads \"Live\" and the Firing tab count includes an Alert Group created through the fake Alertmanager meanwhile, without a reload; a loss that ends within 5 s leaves \"Live\" (`live-indicator.spec.ts`)."
  - "[C-09.AC-26, C-09.FR-26] At 360 CSS pixels in Russian, «Нет связи» shows in the header of the Alert Group list and of an Alert Group page, which do not scroll horizontally; in `live` only the green dot shows there, its text read by screen readers; at 1280 px \"Live\" shows beside it (`live-indicator.spec.ts`)."
  - "[C-09.FR-26] The mark makes no request of its own: the requests the spec records while the state changes are the live-updates stream and the reads of the page (`live-indicator.spec.ts`)."
  - "The documentation page Concepts → Alert Groups says what the mark shows and that the list and the page are read again when the connection is back; every new text exists in English and Russian; `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-095. The mark of the live updates in the header — connecting, live, reconnecting and offline (FE)

## Scope

**IN**

- A small store of the state of the live-updates stream (C-09.FR-26) — `connecting`, `live`, `reconnecting`,
  `offline` — fed by the EventSource of `lib/live.ts` and by the shell's own reopening, with the timings
  `live.reconnecting_after` and `live.offline_after`.
- The mark in the header of the application shell, on every page, with its status region, its popover and "Reload
  page"; its layout at 360 CSS pixels.
- Reading every query again when the stream opens after a loss, whichever path reopened it.
- A paragraph about the mark in the documentation.

**OUT**

- Any change of the stream itself or of the server (C-09.FR-25): none.
- Telemetry, a log or a metric of the state: never (NFR-7).
- A banner about lost live updates: the mark is enough; the banner area keeps the Organization-wide notices of
  C-03.FR-18.

## Contracts

- **Stream** (`GET /api/v1/live-updates`, C-09.FR-25): unchanged; `retry: 3000` keeps the browser's reconnects of the
  same EventSource 3 s apart, and the shell keeps opening a new one 10 s after the server closed the old one for good
  while the session lasts (`RECONNECT_DELAY_MS`).
- **State** (`lib/live-status.ts`): `connectLiveUpdates` reports `open` and `error` with the `readyState` to the store;
  the store keeps the state, the time the stream was last lost and two timers of `live.reconnecting_after` (5 s) and
  `live.offline_after` (30 s) on the page's clock, and exposes it through `useSyncExternalStore`. An `error` while
  `live` starts both timers and leaves `live`; an `open` cancels them and returns to `live`. The shell's new
  EventSource after a close for good continues the same state, so a reopen does not start `connecting` again. The
  first `open` of a new EventSource after a loss invalidates every query, as the browser's reconnect already does.
- **Mark** (`components/live-indicator.tsx`), in the header before the user menu: a button with a dot — green `live`,
  grey `connecting`, amber `reconnecting` (pulsing unless `prefers-reduced-motion`), red `offline` — and the state's
  text, visible in every state but `live` at every width and in `live` from the `sm` breakpoint up (below it a
  screen-reader text); the text sits in a `role="status"` region; the popover (Base UI) shows the explanation of
  reference.md and, in `offline`, the time of the loss in the user's time zone and "Reload page".
  `data-testid="live-indicator"`, `data-state`.
- **Texts** (EN / RU): "Connecting…" / «Подключение…», "Live" / «Онлайн», "Reconnecting…" / «Переподключение…»,
  "Offline" / «Нет связи»; "Live updates are on: lists and pages change as soon as something happens." / «Обновления
  в реальном времени включены: списки и страницы меняются, как только что-то происходит.»; "Connecting to live updates.
  What you see may be out of date." / «Подключаемся к обновлениям в реальном времени. Данные на странице могут быть
  устаревшими.»; "No live updates since {{time}}. What you see may be out of date; it is read again as soon as the
  connection is back." / «Нет обновлений в реальном времени с {{time}}. Данные на странице могут быть устаревшими; они
  перечитаются, как только связь вернётся.»; "Reload page" / «Перезагрузить страницу».
- **Settings**: `live.reconnecting_after`, `live.offline_after` (provisional, P-59), `live.retry`.

## Steps

1. Report the stream's events from `connectLiveUpdates` and write the store. Check: `live-status.test.ts` drives a
   stand-in EventSource with fake timers through every transition, a 4 s loss and a reopen by the shell.
2. Add the mark to the shell and invalidate after any reopen. Check: `live-indicator.test.tsx` covers each state's
   dot, text, status region and popover; `app-shell.test.tsx` covers the invalidation after the shell's reopen.
3. Write `live-indicator.spec.ts`, the texts and the documentation. Check: the Verification below; `make lint` (the
   translations) and `make docs-check`.

## Verification

Run `make dev` and sign in as `admin@example.org` / `muster-dev-password`. By hand first, as an operator would see it:

1. Open the Alert Group list → the header shows a green dot and "Live".
2. Stop `muster dev` (Ctrl-C) → within 5 s "Reconnecting…"; after 30 s "Offline"; press the mark → "No live updates
   since HH:MM. What you see may be out of date; …" and "Reload page".
3. Start `make dev` again; while it starts, fire a new Alert Group from a terminal with the fake Alertmanager → the mark
   turns to "Live" and the Firing tab count grows by one without a reload.

Then in Playwright (`live-indicator.spec.ts`, at 1280 × 800 and 360 × 740, in English and in Russian), with
`page.clock.install()` before the page loads and the live-updates URL routed by the spec:

4. The first stream is answered by the spec with `retry: 3000` and an empty event stream that ends, and every later
   request to it is aborted → `data-state` `live`, then `reconnecting` after `page.clock.runFor(5_000)`, then `offline`
   after `runFor(25_000)`; the status region read "Reconnecting…", then "Offline".
5. A new Alert Group is created through the fake Alertmanager; the spec removes the route → the browser's next
   reconnect reaches `muster dev`, `data-state` turns `live`, and the Firing tab count includes the new Alert Group.
6. A loss of 4 s on the page clock, the next request let through → `data-state` stays `live` throughout.
7. At 360 × 740 in Russian, in `offline`: «Нет связи» shows in the header of the list and of an Alert Group page, and
   `expectNoHorizontalScroll` passes on both; in `live`, only the dot shows and the region still holds «Онлайн».
8. The requests recorded through steps 4 to 6 are the live-updates stream and the reads of the page, none other.

Screenshots of steps 4 and 7 in both languages go into the pull request.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): show whether live updates are connected in the header`.
- The spec routes the live-updates URL instead of cutting the browser's network, because whether Chromium's offline
  emulation ends an open event stream is not something the tests should depend on; stopping `muster dev`, by hand, is
  the real case. `page.clock` moves the SPA's timers only: the browser's own reconnect of an EventSource keeps its 3 s
  on the real clock, so step 5 waits for it with Playwright's retrying assertions, never a fixed sleep.
- Why 5 s and 30 s (P-59): a replica restart or a stream closed for falling behind (`live.max_streams`) reconnects
  within the 3 s of `live.retry`, so 5 s keeps the mark still through it; 30 s covers several reconnects of the browser
  and two reopens by the shell 10 s apart before the mark says the page is not live.
- Before this story, a stream closed for good and reopened by the shell did not invalidate the queries, because a new
  EventSource's first `open` looked like the first of the page; the state now spans the reopen.
- Added after all stories of L1 had merged, with the Snooze presets (D379).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-26 | full | |
| C-09.AC-26 | full | |
