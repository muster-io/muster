---
id: S-219
title: "Documentation: the message layout editor, the blocks that always show, templates and the preview"
capability: C-38
kind: docs
layer: L2
depends_on: [S-217, S-218, S-213, S-214, S-130]
covers: [C-38.FR-16]
files_touched:
  - docs/configure/messages-and-templates.md
  - docs/messengers/write-a-bridge.md
  - docs/messengers/bridge.md
  - web/src/lib/docs-links.json
  - web/src/components/message-layout-editor.tsx
  - web/src/components/message-layout-editor.test.tsx
  - web/e2e/screenshots/routes-and-templates.spec.ts
acceptance:
  - "[C-38.FR-16] Messages and templates has the section \"Edit the layout of a Route's message\" (anchor `#message-layout`): the four blocks and their order, the fields of the catalogue — Severity, Route, Teams, Started, \"Firing / total\" and a label by its name, shown as its value, \"N values\" when the firing Alerts differ, left out when none has it — the switches of the summary, description, key labels, Alert list and other links, the limit of 10 fields and of 1 to 20 values in the Alert list, \"Reset to default\" and the Route that follows Muster's default, which later releases may improve."
  - "[C-38.FR-16] The section says which blocks always show — the title with the status, the notices, the people and the footer, the buttons and \"Open in Muster\" — and why, that in Mattermost the fields always show under the text, that shortening cuts the description first, then the Alert list, the summary and the key labels, and that a hidden block is not counted against a limit."
  - "[C-38.FR-16] The section says how the layout meets a Root message template: the template renders the summary, description, key labels and Alert list as one body at the place of the first of them, the fields, their order and the other links still follow the layout, the Fallback template ignores it, and \"Remove the template\" removes it with the layout in one save; and that a change of the layout edits the open Root messages Quietly, once, without Mentions."
  - "[C-38.FR-16] The section describes the preview on Alert Groups that arrived — the picker, \"Find by number\", the language, the buttons of a Destination, the four tabs with the payload and the length against each limit, the blocks that shortening cuts — and \"Send test message\", with screenshots from `make docs-screenshots GUIDES=routes-and-templates` of the editor and of the preview on a long message; every image has its alternative text."
  - "[C-38.FR-16] Write a bridge describes `order` in `layout` — the four block names in the layout's order, a hidden block empty and in place, a bridge that renders natively follows it and one that ignores it keeps its own order — with the example of rendering by `order` for `muster-bridge-go` and `muster-bridge` as released by S-213 and S-214; Bridge says that the body is in the layout's order."
  - "[C-38.FR-16] The editor's \"Learn more\" links resolve: `web/src/lib/docs-links.json` has the entry for the editor's page, `docslinkcheck` finds its page and anchor, and `make docs-check` passes."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-219. Documentation: the message layout editor, the blocks that always show, templates and the preview

## Scope

**IN**

- C-38.FR-16: the documentation's page on messages describes the editor, the blocks that always show, the relation to
  templates and the preview on real Alert Groups, with screenshots.
- `order` for bridge authors, and the documentation link of the editor's page.

**OUT**

- The editor and the preview themselves: S-217, S-218. The layout of C-29 in each messenger, "Labels in the message"
  and the guide for the structured `layout`: S-130, which this story extends and does not rewrite.

## Contracts

- **Pages**: `docs/configure/messages-and-templates.md` (the new section `## Edit the layout of a Route's message`
  with the anchor `#message-layout`, after "Labels in the message", linking to "Edit a Route's message" and "The
  template language"), `docs/messengers/write-a-bridge.md` (under the section "Render the structured layout" that S-130
  wrote, `order`, with code for the two libraries), `docs/messengers/bridge.md` ("What Muster posts": the body in the
  layout's order). Navigation in `mkdocs.yml` is unchanged: the pages exist.
- **Documentation links** (`web/src/lib/docs-links.json`): `field.message_layout` →
  `configure/messages-and-templates.md#message-layout`; `message-layout-editor.tsx` shows a "Learn more" beside the
  page's header through the existing `DocsLink`, with its test; `docslinkcheck` checks the entry against the built
  site (`make docs-check`).
- **Screenshots** (`web/e2e/screenshots/routes-and-templates.spec.ts`): the editor on the Route "Infrastructure" with
  the fields added, and the preview of the long message with "Shortened: …"; taken in the light theme as every
  documentation screenshot is (C-39.FR-7); the images are never listed.
- **Examples**: generic only (`payments`, `checkout`, `example.org`).

## Steps

1. Write the section and the `order` text with the code for both libraries. Check: `make docs-serve`.
2. Add the link entry and the "Learn more", then the screenshots. Check: `make docs-screenshots
   GUIDES=routes-and-templates`.
3. Check the site. Check: `make docs-check`.

## Verification

1. `make docs-check` passes: pages, images and their alternative text, links, anchors, the anchors of `docs-links.json`.
2. `make docs-serve`, open Messages and templates → "Edit the layout of a Route's message": the section reads in the
   order of the acceptance statements, with two screenshots.
3. `make dev`, open the editor of a Route, press "Learn more": the page opens at that section.
4. Write a bridge → "Render the structured layout": `order` is described and the example renders the blocks by it,
   checked against the Go library's `Example_layout` of S-213.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe the message layout editor, the blocks that always show and the preview on real
  alert groups`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The page says that "Reset to default" returns the Route to Muster's default, "which later releases may improve":
  the same words as C-38.FR-5, so that an Admin who wants a layout to stay as it is saves it as their own.
- The screenshots stay in the light theme whatever the User's theme (C-39.FR-7), so that this story does not depend on
  S-221 but follows the rule of C-39.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-16 | full | |
