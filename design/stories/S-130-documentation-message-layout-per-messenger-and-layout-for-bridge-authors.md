---
id: S-130
title: "Documentation: the message layout in each messenger, \"Labels in the message\", and layout for bridge authors"
capability: C-29
kind: docs
layer: L2
depends_on: [S-129, S-127, S-128]
covers: [C-29.FR-9]
files_touched:
  - docs/configure/messages-and-templates.md
  - docs/messengers/write-a-bridge.md
  - docs/messengers/bridge.md
  - docs/messengers/mattermost.md
  - docs/messengers/slack.md
  - docs/messengers/telegram.md
  - docs/llms.txt.tmpl
  - web/e2e/screenshots/routes-and-templates.spec.ts
acceptance:
  - "[C-29.FR-9] Messages and templates shows the default Root message in Mattermost, Slack, Telegram and through a Bridge — the title with the status as a word, the fields, the summary and the description, the key labels, the Alerts by their distinguishing label, the people, the buttons and the links — with a screenshot of each tab of the Route's preview, explains \"Labels in the message\" with its default and how a value shows (`name=value`, `name: N values`), what a Route template replaces, and what is cut first when a message is too long; the Mattermost, Slack and Telegram pages link to it from what they post."
  - "[C-29.FR-9] Write a bridge has the section \"Render the structured layout\": `layout` beside `body`, plain-text values that the bridge escapes, the number apart from the title, the Alerts' `total` for \"and K more\", people by the `ref` of a Mention, and an example of rendering it natively with the Go library's types and with the Python library's models; Connect a messenger through a bridge links to it."
  - "`make docs-screenshots GUIDES=routes-and-templates` takes the screenshots of the four tabs, every image has its alternative text, and `make docs-check` passes; `llms.txt` points bridge authors to the new section."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-130. Documentation: the message layout in each messenger, "Labels in the message", and layout for bridge authors

## Scope

**IN**

- C-29.FR-9: the documentation's page on messages shows the layout in each messenger and "Labels in the message"; the
  guide for bridge authors describes `layout` with an example of rendering it natively.
- The screenshots of the Route's preview per messenger, and the scenario of `llms.txt` for bridge authors.

**OUT**

- Whom a message mentions and the Mention entries: S-144. Assignees in the layout: S-161, with the chains.

## Contracts

- **Pages**: `docs/configure/messages-and-templates.md` (S-125 started its sections; this story adds the per-messenger
  views and the screenshots), `docs/messengers/write-a-bridge.md` (a new section under "Rendering the envelope", with
  code for `muster-bridge-go` 0.x and `muster-bridge` 0.x as released by S-127 and S-128), `docs/messengers/bridge.md`
  ("What Muster posts"), and links from the Mattermost, Slack and Telegram pages.
- **Screenshots** (`web/e2e/screenshots/routes-and-templates.spec.ts`): the preview's four tabs on a Route with three
  `KubernetesContainerOOMKilled` Alerts; the images are never listed.
- **llms.txt** (`docs/llms.txt.tmpl`): the scenarios "Write a bridge in Go" and "in Python" name the section.
- **Examples**: generic only (`payments`, `checkout`, `example.org`).

## Steps

1. Write the sections and the bridge example. Check: `make docs-serve`.
2. Take the screenshots. Check: `make docs-screenshots GUIDES=routes-and-templates`.
3. Check the site. Check: `make docs-check`.

## Verification

1. `make docs-check` passes: pages, images, links, anchors, the anchors of `docs-links.json` and `llms.txt.tmpl`.
2. The Go example of "Render the structured layout" compiles against the released library: copied into a scratch
   module with `go get github.com/muster-io/muster-bridge-go@latest`, `go vet` passes; the Python example runs against
   `muster-bridge` from PyPI with the envelope of the page as input and prints the lines the page shows.
3. On `muster dev`, "Learn more" next to "Labels in the message" opens the section.

## Open questions

None.

## Notes

- Suggested commit: `docs: show the message layout per messenger and how bridges render it`.
- Depends on the library stories so that the examples use released types; if their release waits, the examples name
  the version the maintainer releases, and the pull request says so.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-29.FR-9 | partial | completes it with S-125's sections |
