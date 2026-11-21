---
id: S-238
title: The Escalation chains timeline of the Destination page with the chains offered, the start and "Play" (FE)
capability: C-42
kind: fe
layer: L2
depends_on: [S-236, S-235, S-237, S-158, S-135]
covers: [C-42.FR-7, C-42.FR-8, C-42.FR-9, C-42.FR-11, C-42.AC-6, C-42.AC-7, C-42.AC-9]
files_touched:
  - web/src/components/destination-test-panel.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/destination-chain-play.tsx
  - web/src/components/destination-chain-play.test.tsx
  - web/src/components/chain-play-timeline.tsx
  - web/src/components/chain-play-timeline.test.tsx
  - web/src/components/test-source-picker.tsx
  - web/src/lib/chain-play.ts
  - web/src/lib/chain-play.test.ts
  - web/src/test/fixtures/chain-play.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/destination-chain-play.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-42.FR-7] Under \"Escalation chains\" in the Test panel, beside the step-by-step test and whether or not a run is going on, the page takes a source and a \"Severity level\" of its own, as the step-by-step form does, and lists the offered chains of `listDestinationChainPlayOffers` — each with its name, its origin (the Route \"Infrastructure\" or the Team payments) and the Destinations its Pages would reach — re-read when the source or the Severity level changes; with none, it says \"No Escalation chain Pages in this Destination.\" with a link to Escalation chains for those who may change them (`destination-chain-play.test.tsx`, `destination-chain-play.spec.ts`)."
  - "[C-42.FR-8, C-42.AC-6] \"Start from\" is \"Now\" or a chosen date and time in the person's time zone, and \"Play\" plays one chain or, with \"All chains\", every offered one with `createDestinationChainPlay`, showing the answer at once as a timeline \"+0 min\", \"+10 min\" … from the start: each entry with its chain, step number and kind, whom it \"would Page\" — On-call with its Schedule, chosen people, a Team or everyone in the chat — normally or importantly and in which Destinations' Threads, \"would call @{login} through {gateway}\", each action of a person's Notification rule with its own wait, \"would send `escalation_webhook` to {Destination}\", and at the end \"Unclaimed\"; playing makes no request but the operation's own (`chain-play-timeline.test.tsx`, `destination-chain-play.spec.ts`)."
  - "[C-42.FR-8, C-42.AC-7] A step the play skips shows \"skipped\" and its reason — only during Working hours, Severity level, a gap in the Schedule, a gateway or contact gone, a Viewer or disabled person — and a person a Page would not reach shows \"Page not delivered\" and the reason, such as \"@petr has no linked Mattermost account\"; played from a chosen Saturday 03:00 a step limited to Working hours and a Schedule with a gap show them; passes after the first of a repeating chain are folded into one line with their count, and a chain that repeats without end says so (`chain-play-timeline.test.tsx`, `destination-chain-play.spec.ts`)."
  - "[C-42.FR-9] The page calls the computation \"Play\" and never a run of a chain: it says that nothing is sent to anyone and nothing is recorded, that the moments are computed and the clock does not move, and that it reads the chains, Schedules, Notification rules, Account links and contacts as they are now; the timeline is shown only on the page and posts nothing in any Test message's Thread; in a real browser the fake Mattermost server and the fake gateway receive nothing while the timeline is played (`destination-chain-play.test.tsx`, `destination-chain-play.spec.ts`)."
  - "[C-42.FR-11, C-42.AC-9] At 360 CSS pixels wide the timeline is a list, entries one under the other, with no horizontal scrolling of the page, in English and in Russian, and the controls — source, Severity level, \"Start from\", \"All chains\", \"Play\" — are reachable; every new text exists in English and Russian with Russian plurals, and a missing key fails `make ci` (`phone-width.spec.ts`, `destination-chain-play.spec.ts`, the missing-key check of `make ci`)."
  - "[C-42.FR-7] The section is absent for a User without `destinations:test`; a `403` or `422` of the operation shows its problem at the form and keeps the last timeline (`destination-chain-play.test.tsx`, `destination-chain-play.spec.ts`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-238. The Escalation chains timeline of the Destination page with the chains offered, the start and "Play" (FE)

## Scope

**IN**

- The part "Escalation chains" of the Test panel: the source and Severity level, the offered chains, "All chains",
  "Start
  from", "Play" and the timeline with its words, skips, undelivered Pages, folded repeats and Unclaimed (C-42.FR-7,
  FR-8, FR-9, FR-11, UI).

**OUT**

- The step-by-step test: S-237. The API: S-235, S-236. The documentation and its link: S-239.
- Any Play from the Escalation chain page or the Route form: C-42 offers the accelerated run on the Destination page
  only.

## Contracts

- **API used**: `listDestinationChainPlayOffers` and `createDestinationChainPlay` of the generated client;
  `listAlertGroups` of the source picker as S-048 and S-237 use it. The page never starts anything but these two reads
  and computations.
- **Components**: `destination-chain-play.tsx` — the part: the form (source, Severity level, "Start from", the offers as
  rows with a check each, "All chains"), "Play" and the explanation of what playing is; `chain-play-timeline.tsx` — the
  timeline, built from `ChainPlay.moments` with entries of each kind, folded repeats and the stop line; `web/src/lib/
  chain-play.ts` — the formatting of "+N min", the words of entries and skip reasons in both languages, the Start
  conversion from the person's time zone (C-09.FR-17) to a date-time; `destination-test-panel.tsx` mounts the part in
  the
  slot S-237 left; `test-source-picker.tsx` is reused.
- **Permissions**: `destinations:test`, which only Admins have; absent without it.
- **Texts** (EN / RU; a missing key fails CI): "Escalation chains" / «Цепочки эскалации»; "All chains" / «Все цепочки»;
  "Start from" / «Начиная с»; "Now" / «Сейчас»; "Play" / «Проиграть»; "+{N} min" / «+{N} мин»; "would Page" / «вызвал
  бы»; "would call" / «позвонил бы»; "skipped" / «пропущен»; "Page not delivered" / «Вызов не доставлен»; "would send" /
  «отправил бы»; "Unclaimed" / «Никто не взял»; "No Escalation chain Pages in this Destination." / «Ни одна цепочка
  эскалации не вызывает в этом месте доставки.»; the explanation of what playing is and is not; the reasons' words;
  Russian plurals.
- **Fixtures**: `web/src/test/fixtures/chain-play.ts` — builders of offers and timelines, with S-237's builders.

## Steps

1. Write `web/src/lib/chain-play.ts`. Check: `chain-play.test.ts` (the moments, the reasons' words, the Start
   conversion, folded repeats).
2. Write the timeline. Check: `chain-play-timeline.test.tsx` (each kind, skipped, not delivered, Unclaimed, the
   repeating chain).
3. Write the part and mount it in the panel with the texts. Check: `destination-chain-play.test.tsx` (the offers, "All
   chains", the errors keeping the last timeline), `destination-test-panel.test.tsx`; `make ci` (missing keys).
4. Write `destination-chain-play.spec.ts` and extend `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev`, in English and then Russian, with the setup of S-169 and S-236: the Route
"Infrastructure" naming `devops`, the Team payments naming `payments` with Working hours Monday to Friday, the
Destination "infra-critical", ivan On-call with a confirmed contact at the fake gateway, petr without a Mattermost
Account link.

1. Sign in as the Admin, open Destinations → infra-critical → "Test" → "Escalation chains", choose "Example" and
   "critical". Expect "devops · Route Infrastructure" and "payments · Team payments" offered, each with its
   Destinations.
2. Choose "All chains", "Start from" a Saturday at 03:00, press "Play". Expect the timeline "+0 min · devops · Step 1 ·
   would Page @ivan (On-call of devops, Main) normally in the Threads of infra-critical and infra-notice", "+0 min ·
   payments · Step 1 · skipped: only during Working hours", "+10 min · devops · Step 3 · would call @ivan through Phone
   gateway", "+10 min · payments · Step 3 · would Page @petr importantly: Page not delivered, @petr has no linked
   Mattermost account", "+25 min · the chains have run out: Unclaimed". Compare with `curl -s -X POST "${A[@]}"
   $API/destinations/$D/chain-plays …`.
3. Remove the Schedule's member for the Saturday: expect "skipped: a gap in the Schedule". Set "Start from" to "Now":
   the
   timeline changes with the real moment.
4. While the timeline is played, check `curl -s $FMM/posts | jq length` and `curl -s $FBR/notifications | jq length`
   before and after: unchanged; `curl -s localhost:8082/_dev/clock | jq -r .now` unchanged.
5. Start a step-by-step run in the same panel and play the chains: expect the run's Test message to get no new Thread
   reply. Pick "Warning" as the Severity level: expect only the chains whose condition a warning meets offered.
6. Open a Destination no chain Pages in: expect "No Escalation chain Pages in this Destination." and the link for an
   Admin. Sign in as a Responder: expect no "Escalation chains" part.
7. At 360 px: the timeline is a list, no horizontal scrolling, the controls reachable. Switch to Russian: the Russian
   texts and plurals.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the escalation chains timeline to the destination test`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The word "Play" is chosen on purpose: a run is a run of a chain in the database, and this computes only (C-42.FR-9);
  "accelerated run" stays a description in the documentation and is no term of the glossary.
- The design pass of the UI belongs to the later layers (journal D315); this part uses the existing components.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-7 | partial | the part, the form and the offers in the browser; the offers' selection is S-236 |
| C-42.FR-8 | partial | the timeline in the browser; its computation is S-235, S-236 |
| C-42.FR-9 | partial | the page's words and the no-effects check in the browser; the guarantee is S-235, S-236 |
| C-42.FR-11 | partial | the chains' part at 360 px and in Russian; the step-by-step part is S-237 |
| C-42.AC-6 | partial | the timeline of the three-step chain in the browser; the API's is S-235, S-236 |
| C-42.AC-7 | partial | the skips from the Saturday 03:00 in the browser; the API's is S-235 |
| C-42.AC-9 | partial | the timeline at 360 px; the step-by-step part is S-237 |
