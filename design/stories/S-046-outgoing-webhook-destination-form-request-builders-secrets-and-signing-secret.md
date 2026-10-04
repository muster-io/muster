---
id: S-046
title: Outgoing webhook Destination form, request builders, Secrets and Signing secret actions (FE)
capability: C-15
kind: fe
layer: L1
depends_on: [S-040, S-043, S-045]
covers: [C-15.FR-1, C-15.FR-3, C-15.FR-5, C-15.FR-10, C-15.FR-12]
files_touched:
  - web/src/components/webhook-destination-fields.tsx
  - web/src/components/webhook-destination-fields.test.tsx
  - web/src/components/request-builder.tsx
  - web/src/components/header-editor.tsx
  - web/src/components/extraction-rules.tsx
  - web/src/components/destination-secrets.tsx
  - web/src/components/destination-secrets.test.tsx
  - web/src/components/signing-secret.tsx
  - web/src/components/signing-secret-dialog.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-delete-dialog.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/webhook-destination.spec.ts
  - web/e2e/webhook-secrets.spec.ts
acceptance:
  - "[C-15.FR-1] The outgoing webhook Destination form has the mode tabs \"Events\", \"Template\" and \"Both\", the proxy form, the Mention section and the limiter; it has no Connection and no \"Check\"."
  - "[C-15.FR-1, C-15.FR-3] In \"Events\", the form takes the URL template and the header editor; in \"Template\", one request builder each for \"Create\", \"Update\", \"Open thread\" and \"Reply in thread\" — method, URL, headers, body — with extraction rules (name and JSONPath) for \"Create\" and \"Open thread\"; a template error from saving is shown at its field with its line and column."
  - "[C-15.FR-5] Creating the Destination opens a dialog with the Signing secret, a copy button and \"You will not see this secret again.\"; the page then shows \"Signing secret: set\" with its date, \"Regenerate\" shows a new secret once, the page shows \"The previous secret still signs, since {date}.\" with \"Retire previous secret\", and retiring it removes the warning."
  - "[C-15.FR-10] The Secrets section lists Secret names with \"Set\" and the date of the last change, adds, replaces and removes Secrets with a value field that is never filled from the server, and shows how to reference one: `{{ .Secrets.token }}`; a literal value in an `Authorization` header or in the URL's query shows \"Store credentials as Secrets: this value is shown to everyone who can read Destinations.\" at that field."
  - "[C-15.FR-10] A Viewer opening the Destination sees the URL and header templates with their `.Secrets` references, the Secret names only, and no actions."
  - "[C-15.FR-12] Deleting an outgoing webhook Destination says what happens: in events mode \"Queued events will not be sent. The Signing secret and the Secrets are deleted now.\", in template mode \"Open messages get a last update, then the secrets are deleted.\""
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-046. Outgoing webhook Destination form, request builders, Secrets and Signing secret actions (FE)

## Scope

**IN**

- The outgoing webhook variant of the Destination form of S-040: mode tabs, the events URL and headers, the four request
  builders with extraction rules, the proxy, Mentions and limiter.
- The Secrets section, the Signing secret section and its dialogs, the literal-credential warning, the delete dialog.

**OUT**

- Test and Preview (S-048); the shared Destination pages (S-040).

## Contracts

- **API used**: `createDestination` (`DestinationCreated.signing_secret`), `updateDestination`, `getDestination`,
  `deleteDestination`, `listDestinationSecrets`, `setDestinationSecret`, `deleteDestinationSecret`, `getSigningSecret`,
  `generateSigningSecret`, `retirePreviousSigningSecret`.
- **Webhook fields** (`webhook-destination-fields.tsx`, in the type slot of `destination-form.tsx`): tabs "Events",
  "Template", "Both" bound to `mode`; "Events" — URL template and `header-editor.tsx`; "Template" — four
  `request-builder.tsx` instances ("Create" and "Update" required, "Open thread" and "Reply in thread" optional with an
  "Add" switch), each with method, URL, headers, body (the template editor of S-038) and, for "Create" and "Open
  thread", `extraction-rules.tsx` (name, JSONPath, help "Read as `{{ .Response.<name> }}`"); "Both" shows both parts.
  The proxy form of S-015 and the Mention section and limiter of S-040 follow. `422` errors appear at their pointers
  (`/events/url`, `/template/update/url` …) with `line` and `column`.
- **Literal credentials**: `warnings` of kind `literal_credential` mark the field named by `field` with "Store
  credentials as Secrets: this value is shown to everyone who can read Destinations."
- **Secrets** (`destination-secrets.tsx`): names from `listDestinationSecrets` with "Set" and `updated_at`; "Add
  secret" (name, value), "Replace", "Remove" with confirmation; `If-Match` from the list's `ETag`; the value field is
  write-only and cleared after saving; a hint shows `{{ .Secrets.<name> }}` for the selected name.
- **Signing secret** (`signing-secret.tsx`, `signing-secret-dialog.tsx`): after `createDestination`, the dialog shows
  `signing_secret` once with a copy button and "You will not see this secret again."; the section shows "Signing secret:
  set" with `updated_at`, "Regenerate" (confirmation, then the same dialog with the new secret), and while
  `previous_active_since` is set the warning "The previous secret still signs, since {date}." with "Retire previous
  secret".
- **Delete dialog** (`destination-delete-dialog.tsx`): the text depends on `mode` as in the acceptance; for messenger
  Destinations it says "Open messages get a last update \"No longer updated here\", then the Destination is removed."

## Steps

1. Add the webhook fields with the request builders and extraction rules. Check: `webhook-destination-fields.test.tsx`
   and Playwright steps 1 and 2 of Verification.
2. Add the Secrets section and the literal-credential warning. Check: `destination-secrets.test.tsx` and steps 3 and 4.
3. Add the Signing secret section and dialogs and the delete dialog. Check: steps 5 to 7.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; the fake receiving endpoint of S-044 runs on
`127.0.0.1:18093`. Then in Playwright:

1. Destinations → "Create destination" → "Outgoing webhook" → tabs "Events", "Template", "Both"; no Connection field
   and no "Check".
2. "Template" → "Create": POST `http://127.0.0.1:18093/chat/ops/messages`, body `{"text": {{ .AlertGroup.Title | toJson }}}`,
   extraction rule `id` = `$.data.id`; "Update": PUT `http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}` with
   `{{ .Nope }` as body → "Save" → the body of "Update" is marked with the template error and its line and column →
   fix the body → "Save" → the dialog "You will not see this secret again." with a secret starting `whsec_` → "Copy" →
   "Close".
3. Secrets → "Add secret" → name `token`, value `s3cr3t` → "Save" → the list shows "token" "Set" and a date, and no
   value anywhere on the page.
4. "Events" → header `Authorization` = `Bearer abc` → "Save" → at that header: "Store credentials as Secrets: this
   value is shown to everyone who can read Destinations." → change it to `Bearer {{ .Secrets.token }}` → "Save" → the
   warning is gone.
5. Signing secret → "Regenerate" → confirm → a new secret once → the page shows "The previous secret still signs,
   since {date}." → "Retire previous secret" → the warning is gone.
6. As a Viewer → the Destination shows `Bearer {{ .Secrets.token }}`, the Secret name "token" and no "Regenerate",
   "Add secret" or "Delete".
7. As the Admin → "Delete" → "Queued events will not be sent. The Signing secret and the Secrets are deleted now." for
   an events-mode Destination → "Delete" → the Destinations list no longer shows it.

`make e2e` runs these steps as `web/e2e/webhook-destination.spec.ts` and `webhook-secrets.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the outgoing webhook destination form with secrets and signing secret actions`.
- The Signing secret dialog uses the pattern of the token dialogs of S-017.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-15.FR-1 | partial | the form; with S-044 and S-045 complete |
| C-15.FR-3 | partial | the request builders; with S-045 complete |
| C-15.FR-5 | partial | the Signing secret actions; with S-044 complete |
| C-15.FR-10 | partial | the Secrets section and the warning; with S-044 complete |
| C-15.FR-12 | partial | the delete dialog; with S-044 and S-045 complete |
