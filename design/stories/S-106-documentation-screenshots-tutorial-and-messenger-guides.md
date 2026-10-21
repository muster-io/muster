---
id: S-106
title: "Screenshots for the documentation, the image check, the tutorial \"First alert in a messenger\" and step-by-step sections on the messenger pages"
capability: C-21
kind: docs
layer: L1
depends_on: [S-104]
covers: [C-21.FR-9, C-21.FR-10, C-21.AC-5, C-21.AC-6]
files_touched:
  - Makefile
  - AGENTS.md
  - web/playwright.config.ts
  - web/playwright.screenshots.config.ts
  - web/tsconfig.json
  - web/e2e/support.ts
  - web/e2e/screenshots/capture.ts
  - web/e2e/screenshots/first-alert.spec.ts
  - web/e2e/screenshots/mattermost.spec.ts
  - web/e2e/screenshots/telegram.spec.ts
  - web/e2e/screenshots/slack.spec.ts
  - web/e2e/screenshots/bridge.spec.ts
  - internal/tools/imagecheck/main.go
  - internal/tools/imagecheck/imagecheck_test.go
  - docs/first-alert.md
  - docs/messengers/mattermost.md
  - docs/messengers/telegram.md
  - docs/messengers/slack.md
  - docs/messengers/bridge.md
  - docs/quick-start.md
  - docs/index.md
  - docs/assets/stylesheets/screenshots.css
  - mkdocs.yml
acceptance:
  - "[C-21.FR-10, C-21.AC-6] `make docs-screenshots` starts a fresh `muster dev` with the fake servers, runs the Playwright suite under `web/e2e/screenshots/` with `web/playwright.screenshots.config.ts` — a 1280 × 800 viewport at device scale 1, `en-US`, the light colour scheme, animations and the caret off — and, only when every spec passed, moves the screenshots it took under `bin/docs-screenshots/images/` to `docs/assets/screenshots/<page path>/<NN>-<name>.png`, replacing the whole directory; `make docs-screenshots GUIDES=telegram` runs one spec and replaces only the page directories that run wrote; a failed run changes no image; `make e2e` and CI never run the suite (`web/playwright.config.ts` ignores `screenshots/**`)."
  - "[C-21.FR-10, C-21.AC-6] `make docs-check` runs `internal/tools/imagecheck` over `docs/`: it fails, naming the page, the line and the image, for an image reference (Markdown or `<img>`) whose file does not exist, for a PNG under `docs/assets/screenshots/` that no page references, for an image without alternative text, and for an image whose source is another site (`http:`, `https:` or `//`); it passes on the committed site (`imagecheck_test.go` covers each failure)."
  - "[C-21.FR-9, C-21.AC-5] The page \"First alert in a messenger\" (`docs/first-alert.md`), in the navigation after Quick start and linked from Quick start's Next steps and the home page, leads through seven numbered steps — create an Integration and its token, create a Telegram Connection with its check, add a Destination with its check and a test message, add a Route that sends to it, link the Admin's Telegram account, send a test alert, acknowledge the Alert Group from Telegram — each with the screenshot of the screen it describes and a link to the page that explains it in full."
  - "[C-21.AC-5] `web/e2e/screenshots/first-alert.spec.ts` follows those steps in order through the UI against the fake Alertmanager and the fake Telegram server, taking the tutorial's screenshots as it goes, and ends asserting that the Alert Group is Acknowledged by the Admin and that its Timeline names the Transport Telegram; it fails, and no image changes, when a step no longer works."
  - "[C-21.FR-9] The Mattermost and Telegram pages gain the step-by-step sections \"Create the Connection\" and \"Add a Destination\", and the Slack and Bridge pages get screenshots inside their sections \"Create the Connection\" and \"Invite the bot and create Destinations\" or \"Add a Destination\": the form filled in, the result of \"Check connection\" or \"Check\", the saved page and the test message's result, each image with alternative text; the headings these sections give are the anchors of reference.md's Documentation links."
  - "[C-21.FR-9, C-21.FR-10] `make docs-check` passes: the site builds strictly, every page is in the navigation, no link, anchor or image is broken, and `llms.txt` and the full text are current."
verify: "make docs-check"
operator_attention: false
issue: null
---

# S-106. Screenshots for the documentation, the image check, the tutorial "First alert in a messenger" and step-by-step sections on the messenger pages

## Scope

**IN**

- The screenshot suite: its Playwright configuration, the capture helper and `make docs-screenshots`.
- The image check in `make docs-check`.
- The tutorial "First alert in a messenger" with its screenshots.
- Step-by-step sections with screenshots on the four messenger pages: Mattermost, Telegram, Slack and Bridge.

**OUT**

- The step-by-step sections of every other page (S-107).
- The links from the web UI to the documentation (S-108, S-109, S-110).

## Contracts

- **Make**: `make docs-screenshots` (new; `## Take the documentation screenshots on muster dev (run by hand, never in
  CI)`), with the prerequisites of `make demo` (`dev-db build web-browsers`); it runs
  `pnpm --dir web exec playwright test --config playwright.screenshots.config.ts` (with `GUIDES` as the spec names to
  run), into an emptied `bin/docs-screenshots/images/`; only when Playwright exits `0` does it replace, in
  `docs/assets/screenshots/`, each page directory the run wrote — and, on a run of every spec, remove the page
  directories the run did not write. `make docs-check` gains `$(GO) run ./internal/tools/imagecheck docs` beside
  `tablecheck`. AGENTS.md's table of Make targets gains the row of `make docs-screenshots`, its row of
  `make docs-check` names the image check, and its list of generated paths under Quality bar adds
  `docs/assets/screenshots/`.
- **Playwright**: `web/playwright.screenshots.config.ts` extends `playwright.config.ts` as the demo's does, with
  `testDir: "e2e/screenshots"`, `testIgnore: []`, output under `bin/docs-screenshots`, the viewport 1280 × 800,
  `deviceScaleFactor: 1`, `colorScheme: "light"`, `locale: "en-US"`, no trace, no video; `web/playwright.config.ts`
  ignores `screenshots/**` as it ignores `demo.spec.ts`; `web/tsconfig.json` includes the new configuration.
- **Capture helper** (`web/e2e/screenshots/capture.ts`): `capture(page, docsPage, name, options)` writes
  `bin/docs-screenshots/images/<docsPage>/<name>.png`, which `make docs-screenshots` moves into
  `docs/assets/screenshots/`, where `docsPage` is the page's source path without `.md` (`messengers/telegram`) and
  `name` is `NN-<slug>` in the order the page shows them; it waits for the text the step names to be visible
  (`options.waitFor`; the live-updates stream keeps the network busy, so it never waits for the network to be idle) and
  for `document.fonts.ready`, masks what would change on every run or must not be read — tokens shown once, `public_id`s
  in addresses (`options.mask`, Playwright's `mask`) — takes the screenshot with `animations: "disabled"` and `caret:
  "hide"`, of the whole viewport or of one element (`options.locator`), and can outline the element the step asks the
  reader to use (`options.highlight`, a 2 px outline in the brand indigo, removed after the shot). Nothing it draws
  stays on the page.
- **Data**: demo data only. The specs make what they show through the UI they document, and the rest through the API
  and the fake servers' control endpoints (`support.ts` gains the helpers they share): the fake Telegram server's
  second bot (`234567:dev-telegram-token-b`, for which `muster dev` creates no Connection), its chats through
  `PUT /_fake/chats/{chat}`, the `/start` of the Account link through `POST /_fake/updates` and the press of Ack
  through `POST /_fake/press`; the fake Mattermost server's second bot (`muster-dev-mattermost-token-b`); the fake
  Slack server's second app (`xoxb-muster-dev-slack-token-b`, `xapp-muster-dev-slack-token-b`); the fake bridge's second
  bot (`muster-dev-bridge-token-b`). Names on the screens are neutral and realistic ("Production Alertmanager",
  "checkout", "#alerts"); no screenshot shows a real workspace, server, person or token.
- **Independence**: a spec's images do not depend on which other specs ran before it in the same `muster dev`. Each
  spec removes in `afterAll` what it created (Connections, Destinations, Routes, Account links — as `deleteRoute` and
  `removeSlackConnection` of `support.ts` do), signs in at the start of each test, and takes a list as the element of
  the rows it made where other rows could show; a spec that advances the development clock signs in again afterwards.
- **Images**: `docs/assets/screenshots/<page path>/*.png`, committed; they are the output of `make docs-screenshots`,
  generated files that `files_touched` does not list ([README §2](README.md#2-repository-layout)). Pages reference them
  with relative paths and alternative text that says what the screen shows, and the class `screenshot` through
  `attr_list` (`![…](…){ .screenshot }`), which `docs/assets/stylesheets/screenshots.css` (in `extra_css` of
  `mkdocs.yml`) gives a thin border and a full-width maximum.
- **Image check** (`internal/tools/imagecheck`, Go, no dependencies): reads every Markdown page under `docs/` except
  the excluded files of `mkdocs.yml`, collects image references outside code blocks — `![alt](path)` and
  `<img src alt>` — and fails, naming `page:line` and the image, for a relative path that does not resolve to a file
  under `docs/`, for an empty alternative text, and, after reading every page, for each PNG under
  `docs/assets/screenshots/` that no page references; it also fails for an image whose source is another site
  (`http:`, `https:` or `//`), which `make docs` does not refuse today (`REMOTE_ASSET_RE` covers scripts and
  stylesheets) and which a reader's browser would load from that site.
- **Pages**:
  - `docs/first-alert.md` (new), "First alert in a messenger", in the `nav` of `mkdocs.yml` right after Quick start. It
    starts where Quick start ends — Muster running, the Admin signed in — and has the sections "1. Create the
    Integration", "2. Connect Telegram", "3. Add a Destination", "4. Send Alert Groups to the Destination", "5. Link
    your Telegram account", "6. Send a test alert", "7. Acknowledge it in Telegram" and "Next steps" (Mattermost, Slack
    and Bridges, which differ only in steps 2, 3 and 5, with links to their sections). Step 4 creates the Route
    "checkout" with the Matcher `service="checkout"`, the Group key `alertname`, `service` and the Destination; step 6
    shows the Quick start's `curl` with `service="checkout"`, and the spec sends the same alert through the fake
    Alertmanager. The pictures of Telegram itself — the message and its buttons — are not screenshots of Telegram: the
    tutorial shows the Destination's preview and the Alert Group page afterwards.
  - `docs/messengers/mattermost.md` and `docs/messengers/telegram.md`: new sections "Create the Connection" (after
    creating the bot) and "Add a Destination" (after the messenger's own preparation), each as numbered steps with
    screenshots; the existing section "Check before saving" stays, shortened where the new section now shows it.
  - `docs/messengers/slack.md` and `docs/messengers/bridge.md`: screenshots and numbered steps inside the existing
    sections "Create the Connection" and "Invite the bot and create Destinations" or "Add a Destination"; the
    headings stay, since `llms.txt.tmpl` quotes `bridge.md` and reference.md links to them.
  - `docs/quick-start.md` (Next steps) and `docs/index.md` link to the tutorial.
- **Names**: settings by `area.setting`, the UI's texts as the UI writes them, in bold for buttons and fields, and the
  terms of `CONTEXT.md`.

## Steps

1. Write the Playwright configuration, the capture helper and `make docs-screenshots`; take one screenshot of the
   Connections page. Check: `make docs-screenshots GUIDES=telegram` writes it, and `make e2e` does not list the suite.
2. Write `imagecheck` with its tests. Check: `go test ./internal/tools/imagecheck`, then `make docs-check` passes.
3. Write the tutorial's spec and page. Check: `make docs-screenshots GUIDES=first-alert` passes and the page shows its
   images in `make docs-serve`.
4. Write the four messenger specs and their sections. Check: `make docs-screenshots`, then `make docs-check`.

## Verification

```sh
make docs-screenshots                                                      # all five specs pass
ls docs/assets/screenshots/first-alert docs/assets/screenshots/messengers   # first-alert/ and the four messengers
python3 -c 'import struct,glob,sys; w=[struct.unpack(">I",open(f,"rb").read()[16:20])[0] for f in glob.glob("docs/assets/screenshots/**/*.png",recursive=True)]; print(len(w), max(w)); sys.exit(max(w)>1280)'
                                                                           # N images, none wider than 1280 px
make docs-check                                                            # passes
pnpm --dir web exec playwright test --list | grep -c 'screenshots/'         # 0: the suite is not part of make e2e
# the image check's three failures, each undone afterwards:
sed -i.bak 's|01-new-connection.png|01-missing.png|' docs/messengers/telegram.md && make docs-check  # fails: docs/messengers/telegram.md:<line>: ../assets/screenshots/messengers/telegram/01-missing.png does not exist
mv docs/messengers/telegram.md.bak docs/messengers/telegram.md
cp "$(ls docs/assets/screenshots/first-alert/*.png | head -n1)" docs/assets/screenshots/first-alert/99-orphan.png
make docs-check      # fails: docs/assets/screenshots/first-alert/99-orphan.png is referenced by no page
rm docs/assets/screenshots/first-alert/99-orphan.png
make docs-serve &   # open http://localhost:8000/first-alert/ and the four messenger pages; every image loads
```

The pull request carries the output of `make docs-screenshots`, a few of the images inline, and the result of the
tutorial's spec, whose last assertion is the Alert Group acknowledged from Telegram.

## Open questions

None.

## Notes

- Suggested commit: `docs: add the tutorial first alert in a messenger and screenshots of the messenger guides`.
- Telegram is the tutorial's messenger because it needs no address of Muster that the messenger reaches (long
  polling) and a bot takes a minute to create; the maintainer may choose another in review, which changes steps 2, 3
  and 5 only.
- CI never takes the screenshots: fonts, relative times and the dev clock make pixels differ between machines. The
  images change only when someone runs `make docs-screenshots` and commits them, and `make docs-check` keeps the
  references and the files in step.
- `llms.txt.tmpl` quotes `docs/messengers/bridge.md` and `docs/messengers/slack.md` at length; a quote that the new
  steps break makes `make docs-check` fail and is fixed in the page, not in the template.
- The screenshots show the UI before the documentation links of S-108 to S-110; S-110 takes them all again once every
  link is in place.
- `make docs-screenshots` needs the same machine as `make demo` (Docker for PostgreSQL, the Playwright browsers); on the
  test VM it runs like `make e2e`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-9 | partial | the tutorial and the messenger pages; the other pages are S-107 |
| C-21.FR-10 | full | |
| C-21.AC-5 | full | |
| C-21.AC-6 | partial | the suite, the check and the images of these guides; S-107 completes the images of the other guides |
