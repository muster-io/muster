---
id: S-166
title: Shared fixtures of Destination and Connection reads in the web tests
capability: C-28
kind: infra
layer: L2
depends_on: []
covers: []
files_touched:
  - web/src/test/fixtures/destinations.ts
  - web/src/test/fixtures/connections.ts
  - web/src/components/bridge-destination-fields.test.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/destination-label.test.ts
  - web/src/components/destination-preview-panel.test.tsx
  - web/src/components/destination-secrets.test.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/route-destinations.test.tsx
  - web/src/components/slack-destination-fields.test.tsx
  - web/src/components/telegram-destination-fields.test.tsx
  - web/src/components/webhook-destination-fields.test.tsx
  - web/src/components/account-links-table.test.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/slack-connection-fields.test.tsx
  - web/src/components/telegram-connection-fields.test.tsx
acceptance:
  - "`web/src/test/fixtures/destinations.ts` exports one builder per Destination type — `mattermostDestination`, `telegramDestination`, `slackDestination`, `bridgeDestination`, `webhookDestination` — each returning a complete read of the generated type with every required field set to a neutral value and taking the fields a test cares about as overrides; `web/src/test/fixtures/connections.ts` does the same for `mattermostConnection`, `telegramConnection`, `slackConnection` and `bridgeConnection`."
  - "The ten test files that built Destination reads by hand and the five that built Connection reads by hand build them through the builders only, with the same assertions as before; no other web test builds a Destination or Connection read by hand (`grep` of the read types' required fields outside `web/src/test/fixtures/` finds none); `make ci` passes without a change of behaviour."
verify: "make ci"
operator_attention: false
issue: null
---

# S-166. Shared fixtures of Destination and Connection reads in the web tests

## Scope

**IN**

- One builder per Destination type and per Connection type for the web tests, under `web/src/test/fixtures/`, and the
  conversion of every test that builds such a read by hand.

**OUT**

- Any change of the API, the pages or their behaviour. The builder of Team reads: S-119, which has the first tests that
  build them.

## Contracts

- **Builders** (`web/src/test/fixtures/destinations.ts`, `connections.ts`): typed with the generated types of
  `web/src/api/gen/`, each returning a complete read whose required fields hold neutral values — `health` healthy, no
  Routes, `mentions` from `defaultMentions` — overridable field by field. A story that adds a required field to a
  Destination or Connection read sets its neutral value here, in the same pull request, and lists the builder in its
  `files_touched`: S-113 (`buttons`), S-115 (`owning_team`), S-121 (`teams`), S-140 (`mention_entries`), S-165
  (`mention_warnings`), S-139 (`usergroups_read`, `groups_read`). Tests written from this story on build these reads
  only through the builders.
- **Tests converted**: the Destination reads of `bridge-destination-fields`, `destination-form`, `destination-label`,
  `destination-preview-panel`, `destination-secrets`, `destination-test-panel`, `route-destinations`,
  `slack-destination-fields`, `telegram-destination-fields` and `webhook-destination-fields`; the Connection reads of
  those of them that build one and of `account-links-table`, `bridge-connection-fields`, `connection-form`,
  `slack-connection-fields` and `telegram-connection-fields`.

## Steps

1. Write the builders. Check: `make lint` (the TypeScript check).
2. Convert the tests one by one. Check: `make test-web` after each; the grep of the acceptance.

## Verification

`make ci` passes; `grep -rlE "bot_token_status|destination_count|health: \{ state" web/src --include='*.test.ts*'`
lists no file outside `web/src/test/fixtures/` that builds a Destination or Connection read. The story changes no page,
so it has no browser check; `make e2e` is unaffected.

## Open questions

None.

## Notes

- Suggested commit: `test(web): build destination and connection reads in tests through shared fixtures`.
- `covers` is empty: no requirement speaks about test fixtures. The story exists so that the backend stories of L2 that
  add a required field to these reads — after which `make generate` rewrites `web/src/api/gen/` and the TypeScript
  check fails on every hand-built read — change one builder instead of a dozen tests. S-113 and S-115, the first such
  stories, depend on it, and it comes first in the story map of L2.
- `kind: infra`: it changes only the test tooling of the web tests and no behaviour, so it depends on no backend story.

## Coverage

| ID | Covered | Note |
|---|---|---|
| — | — | no requirement; see Notes |
