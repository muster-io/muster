---
id: S-109
title: "\"Learn more\" links next to complex fields and \"How to fix\" links next to checks, warnings and Broken states on the Integration, Route, Connection, Destination and Alert Group pages (FE)"
capability: C-21
kind: fe
layer: L1
depends_on: [S-108, S-107]
covers: [C-21.FR-11, C-21.AC-8]
files_touched:
  - web/src/lib/docs-links.json
  - web/src/lib/check-fixes.ts
  - web/src/lib/check-fixes.test.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/docs-links-forms.spec.ts
  - web/src/components/matcher-builder.tsx
  - web/src/components/group-key-editor.tsx
  - web/src/components/route-form.tsx
  - web/src/components/route-policy-message.tsx
  - web/src/components/mention-settings.tsx
  - web/src/components/route-policy-delivery.tsx
  - web/src/components/route-policy-timers.tsx
  - web/src/components/route-policy-snooze.tsx
  - web/src/components/route-policy-lifecycle.tsx
  - web/src/components/integration-form.tsx
  - web/src/components/integration-token-dialog.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/components/proxy-form.tsx
  - web/src/components/callback-hint.tsx
  - web/src/components/telegram-connection-fields.tsx
  - web/src/components/slack-connection-fields.tsx
  - web/src/components/bridge-connection-fields.tsx
  - web/src/components/webhook-destination-fields.tsx
  - web/src/components/extraction-rules.tsx
  - web/src/components/destination-secrets.tsx
  - web/src/components/header-editor.tsx
  - web/src/components/signing-secret.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/destination-health.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/integration-warnings.tsx
  - web/src/components/alert-group-notices.tsx
  - web/src/components/template-error-banner.tsx
acceptance:
  - "[C-21.FR-11, C-21.AC-8] Every field of the rows of reference.md's \"Next to fields\" marked S-109 — the Route's Matchers, Group key, Route profile, Urgent, message template, Mention settings, delivery, ack timeout and Reminders, Snooze durations and lifecycle; the Integration's Static labels, snippet and Heartbeat; the Connection's proxy, the Mattermost callback address, Telegram's mode and Bot API base URL, Slack's event mode, tokens and manifest, the Bridge's event delivery; the outgoing webhook's templates, extraction rules, Secrets and Signing secret — shows \"Learn more\" beside its label, linking to the row's target of the running version."
  - "[C-21.FR-11, C-21.AC-8] `check-fixes.ts` maps each code of a Connection or Destination check and each check warning to its key of \"Next to problems\" by the Connection's type, and a code without a row to none (`check-fixes.test.ts`, every code of `NullableConnectionCheckCode`, `NullableDestinationCheckCode` and the warnings of the spec); the check panels show \"How to fix\" beside a failed step or check and beside a warning that has a key — in a create form, an edit form and on a saved page."
  - "[C-21.FR-11, C-21.AC-8] \"How to fix\" also shows beside the Broken state of a Destination and of its Routes, the delivery states \"Not delivered\" and \"Waiting: {Destination} is Broken\" of the Alert Group page, the Integration warnings (truncated `groupKey`, a long repeat interval, no Heartbeat, waiting for the first Heartbeat signal, Heartbeat lost), the Replacement notice of the Alert Group page, the template error banner of a Route or Destination, the Telegram warning about `http`, and the outgoing webhook's warnings about a literal credential and a previous Signing secret; it never shows beside a passed step, an empty state or a hint that asks for nothing."
  - "[C-21.AC-8] The texts are \"Learn more\" / «Подробнее» and \"How to fix\" / «Как исправить» with their accessible names in both languages, `make lint` finds no missing key, `make docs-check` passes with every new key of `docs-links.json`, and the Route editor, a Slack Connection's create form and a Telegram Destination with a failed check keep no horizontal scrollbar at 360 px wide (`web/e2e/docs-links-forms.spec.ts`)."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-109. "Learn more" links next to complex fields and "How to fix" links next to checks, warnings and Broken states on the Integration, Route, Connection, Destination and Alert Group pages (FE)

## Scope

**IN**

- "Learn more" beside the complex fields of the forms of Routes, Integrations, Connections and outgoing webhook
  Destinations.
- "How to fix" beside the results of the Connection and Destination checks, their warnings, Broken states, the
  delivery states of the Alert Group page, the Integration warnings, the Replacement notice, template errors and the
  warnings of Telegram and outgoing webhook forms.

**OUT**

- The version, the components, the anchor check and the page titles (S-108).
- The Statistics, profile, Access and Organization pages (S-110).

## Contracts

- **The list**: `docs-links.json` gains the keys of reference.md's
  [Documentation links](../prd/l1/reference.md#documentation-links) marked S-109, in "Next to fields" and "Next to
  problems"; each target is the row's, its anchor the heading that S-106, S-107 or an earlier story gave.
- **Check codes** (`web/src/lib/check-fixes.ts`): `connectionCheckFix(type, code)`, `destinationCheckFix(type, code)`
  and `checkWarningFix(warning)`, each a key or none, written as one table per function from the rows of "Next to
  problems"; the codes are the enums of `NullableConnectionCheckCode`, `NullableDestinationCheckCode` and the
  Connection check's `warnings` in `api/openapi.yaml`, and the test fails when the generated client has a code the
  table does not name, as mapped or as having no link.
- **Placement** (`DocsLink` of S-108, `kind` `learn_more` or `how_to_fix`): "Learn more" sits beside the field's label,
  or in the hint under it where the field has one (the callback address, the Telegram base URL); "How to fix" sits at
  the end of the line of the problem's text. A field shared by several forms (`proxy-form.tsx`, `mention-settings.tsx`)
  shows the link in each of them.
- **Texts**: those of S-108, with accessible names that name the field or the problem ("Learn more: Group key (opens in
  a new tab)").
- **End-to-end** (`web/e2e/docs-links-forms.spec.ts`): the fields and problems of the acceptance on `muster dev`, the
  failed checks made through the fake servers' faults and states as their own specs make them.

## Steps

1. Add the keys and `check-fixes.ts` with its test. Check: `make test-web`, `make docs-check`.
2. Place "Learn more" in the Route, Integration, Connection and outgoing webhook forms. Check: `make lint`,
   `make test-web`.
3. Place "How to fix" in the check panels and beside the warnings and states. Check: `make test-web`.
4. Write the end-to-end spec. Check: `make e2e-web`, then Verification.

## Verification

Playwright against `muster dev` at 1280 and 360 px, signed in as the Admin:

1. Routes → New: "Learn more" beside "Matchers" links to
   `https://muster-io.github.io/muster/latest/concepts/routes-and-group-keys/#matchers`, beside "Group key" to
   `…#group-keys`, beside the message template to `…/configure/messages-and-templates/#the-template-language`.
2. Connections → New → Slack: "Learn more" beside the event mode links to `…/messengers/slack/#socket-mode-or-http`;
   with the bot token `xoxb-wrong` "Check connection" fails the step `token` and shows "How to fix" to
   `…/messengers/slack/#install-the-app-and-create-the-tokens`.
3. A Telegram Destination on a channel whose comments the fake server turned off (`PUT /_fake/chats/{chat}`): "Check"
   fails `discussion_group` with "Comments are not enabled for this channel." and "How to fix" to
   `…/messengers/telegram/#enable-comments-on-the-channel`.
4. A Destination made Broken through the fake server's faults: "How to fix" beside "Broken since …" on its page and on
   its Route, to `…/concepts/broken-destinations/#recovery`.
5. An Integration whose Snapshot the fake Alertmanager truncated: "How to fix" beside the warning, to
   `…/integrations/alertmanager/#send-every-alert-of-the-group`.
6. A Connection check that passes shows no "How to fix"; the empty Routes list shows no link but the title's.
7. With the language switched to Russian, the links read «Подробнее» and «Как исправить».
8. At 360 px the Route editor, the Slack create form and the failed Telegram check have
   `document.documentElement.scrollWidth <= 360`.

```sh
make docs-check            # passes; docslinkcheck counts the keys of S-108 and S-109
```

## Open questions

None.

## Notes

- Suggested commit: `feat(web): link complex fields and problems to the documentation`.
- With 35 files the story stays under the size signal; the forms and problems of the admin pages are S-110.
- A component test that asserts a warning's whole text with `toHaveTextContent` may need the link's text excluded;
  such a test file is added to `files_touched` in the pull request, as AGENTS.md allows.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-11 | partial | fields and problems of these pages; S-108 and S-110 cover the rest |
| C-21.AC-8 | partial | these places; S-108 and S-110 complete the table |
