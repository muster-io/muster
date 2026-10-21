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
  - CONTRIBUTING.md
  - .github/workflows/nightly.yml
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
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/telegram/client.go
  - internal/telegram/client_test.go
  - internal/mattermost/client.go
  - internal/mattermost/check_test.go
  - internal/bridge/client.go
  - internal/bridge/client_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/runtime/runtime.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_test.go
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
  - "[C-21.FR-10, C-21.AC-6] `make docs-screenshots` runs only on Linux with Docker, inside the official Playwright image `mcr.microsoft.com/playwright` at the version of `@playwright/test` in `web/package.json`, pinned by digest; anywhere else it refuses with a message that names that environment and the VM muster-lab, and the screenshot configuration refuses to run outside `make docs-screenshots`. In that image it runs the Playwright suite under `web/e2e/screenshots/` with `web/playwright.screenshots.config.ts` — a 1280 × 800 viewport at device scale 1, `en-US`, the light colour scheme, animations and the caret off — on a fresh `muster dev` with the fake servers."
  - "[C-21.FR-10, C-21.AC-6] Only when every spec passed does `make docs-screenshots` move the screenshots it took under `bin/docs-screenshots/images/` to `docs/assets/screenshots/<page path>/<NN>-<name>.png`: a run of every spec replaces the whole directory, `make docs-screenshots GUIDES=telegram` replaces only the page directories that run wrote, and a failed run changes no image; the specs that advance the development clock (`*.clock.spec.ts`) run after the others, each on a fresh `muster dev` of its own; `make e2e` and CI never run the suite (`web/playwright.config.ts` ignores `screenshots/**`), and the nightly workflow's job `docs-screenshots` runs it without committing anything."
  - "[C-21.FR-10, C-21.AC-6] Under `muster dev` a Telegram Connection with the default Bot API address `https://api.telegram.org`, a Mattermost Connection on `https://chat.example.org` and a Bridge Connection on `https://bridge.example.org` reach the fake servers when their token is one of the fakes' published tokens, as a Slack Connection does through `SlackRoute`; any other token goes to the address the Connection names; outside `muster dev` nothing is routed; and a routed request still passes the outbound address policy, which refuses it with `blocked` once the loopback network is removed from the allowed networks (`devmode_test.go`, the clients' and `connections_test.go`'s tests, `doctor_test.go`)."
  - "[C-21.FR-10, C-21.AC-6] `make docs-check` runs `internal/tools/imagecheck` over `docs/`: it fails, naming the page, the line and the image, for an image reference (Markdown or `<img>`) whose file does not exist, for a PNG under `docs/assets/screenshots/` that no page references, for an image without alternative text, and for an image whose source is another site (`http:`, `https:` or `//`); it passes on the committed site (`imagecheck_test.go` covers each failure)."
  - "[C-21.FR-9, C-21.AC-5] The page \"First alert in a messenger\" (`docs/first-alert.md`), in the navigation after Quick start and linked from Quick start's Next steps and the home page, leads through seven numbered steps — create an Integration and its token, create a Telegram Connection with its check, add a Destination with its check and a test message, add a Route that sends to it, link the Admin's Telegram account, send a test alert, acknowledge the Alert Group from Telegram — each with the screenshot of the screen it describes and a link to the page that explains it in full; no screenshot shows a loopback address, a fake server's address or the warning about a bot token over `http`."
  - "[C-21.AC-5] `web/e2e/screenshots/first-alert.spec.ts` follows those steps in order through the UI against the fake Alertmanager and the fake Telegram server, taking the tutorial's screenshots as it goes, and ends asserting that the Alert Group it created — found by the number the spec read when it appeared, not a fixed one — is Acknowledged by the Admin and that its Timeline names the Transport Telegram; it fails, and no image changes, when a step no longer works."
  - "[C-21.FR-9] The Mattermost and Telegram pages gain the step-by-step sections \"Create the Connection\" and \"Add a Destination\", and the Slack and Bridge pages get screenshots inside their sections \"Create the Connection\" and \"Invite the bot and create Destinations\" or \"Add a Destination\": the form filled in, the result of \"Check connection\" or \"Check\", the saved page and the test message's result, each image with alternative text; the headings these sections give are the anchors of reference.md's Documentation links."
  - "[C-21.FR-9, C-21.FR-10] `make docs-check` passes: the site builds strictly, every page is in the navigation, no link, anchor or image is broken, and `llms.txt` and the full text are current."
verify: "make ci docs-check"
operator_attention: false
issue: 335
---

# S-106. Screenshots for the documentation, the image check, the tutorial "First alert in a messenger" and step-by-step sections on the messenger pages

## Scope

**IN**

- The screenshot suite: its Playwright configuration, the capture helper, `make docs-screenshots` in its pinned
  environment, and the nightly job that runs it.
- Routes of `muster dev` that send realistic addresses — the default Bot API, `https://chat.example.org` and
  `https://bridge.example.org` — to the fake servers, so that no screenshot shows a loopback address.
- The image check in `make docs-check`.
- The tutorial "First alert in a messenger" with its screenshots.
- Step-by-step sections with screenshots on the four messenger pages: Mattermost, Telegram, Slack and Bridge.

**OUT**

- The step-by-step sections of every other page (S-107).
- The links from the web UI to the documentation (S-108, S-109, S-110), and the screenshots taken again once they are
  in place (S-110).

## Contracts

- **Make**: `make docs-screenshots` (new; `## Take the documentation screenshots on muster dev in the Playwright image
  (Linux with Docker; run by hand, never in CI)`), with the prerequisites of `make demo` (`dev-db build`).
  - **Environment**: the system fonts that the SPA uses differ between machines, so the target runs only where the
    fonts are the same: on Linux with Docker, inside `PLAYWRIGHT_IMAGE`, `mcr.microsoft.com/playwright:v<version>-noble`
    pinned by digest, where `<version>` is that of `@playwright/test` in `web/package.json` — the target fails when
    they differ. It runs the container with `--network host` (for `muster dev` and the development PostgreSQL), the
    checkout mounted at its own path, the caller's user id, and the image's browsers. On another system, or without
    Docker, it stops before anything runs with: "make docs-screenshots runs only on Linux with Docker, in
    $(PLAYWRIGHT_IMAGE), so that fonts match; run it on the VM muster-lab or another Linux machine." The
    configuration refuses to load unless `make docs-screenshots` set `MUSTER_DOCS_SCREENSHOTS=1`.
  - **Runs**: it empties `bin/docs-screenshots/images/`, then runs every spec but the `*.clock.spec.ts` ones in one
    Playwright run, then each `*.clock.spec.ts` in a run of its own — each run starts its own `muster dev` on a fresh
    database through `e2e/global-setup.ts`. `GUIDES` names the specs to run. Only when every run exits `0` does it
    replace, in `docs/assets/screenshots/`, each page directory the runs wrote, and, when `GUIDES` is empty, remove the
    page directories they did not write.
  - `make docs-check` gains `$(GO) run ./internal/tools/imagecheck docs` beside `tablecheck`.
  - AGENTS.md's table of Make targets gains the row of `make docs-screenshots`, its row of `make docs-check` names the
    image check, and its list of generated paths under Quality bar adds `docs/assets/screenshots/`. CONTRIBUTING.md's
    development setup says where and how the screenshots are taken, and that they are committed by hand.
- **Nightly**: `.github/workflows/nightly.yml` gains the job `docs-screenshots` on `ubuntu-latest`, which runs
  `make docs-screenshots`, uploads `bin/docs-screenshots/` as an artifact and writes `git status --short
  docs/assets/screenshots` to the step summary; it commits nothing and is not a check of pull requests, so a later
  story that breaks a screenshot spec is noticed the next night without blocking anyone. Actions are pinned by commit
  SHA.
- **Playwright**: `web/playwright.screenshots.config.ts` extends `playwright.config.ts` as the demo's does, with
  `testDir: "e2e/screenshots"`, `testIgnore: []`, output under `bin/docs-screenshots`, the viewport 1280 × 800,
  `deviceScaleFactor: 1`, `colorScheme: "light"`, `locale: "en-US"`, no trace, no video; `web/playwright.config.ts`
  ignores `screenshots/**` as it ignores `demo.spec.ts`; `web/tsconfig.json` includes the new configuration.
- **Capture helper** (`web/e2e/screenshots/capture.ts`): `capture(page, docsPage, name, options)` writes
  `bin/docs-screenshots/images/<docsPage>/<name>.png`, where `docsPage` is the page's source path without `.md`
  (`messengers/telegram`) and `name` is `NN-<slug>` in the order the page shows them. It waits for the text the step
  names to be visible (`options.waitFor`; the live-updates stream keeps the network busy, so it never waits for the
  network to be idle) and for `document.fonts.ready`; masks what must not be read — a token shown once — with
  Playwright's `mask` (`options.mask`); takes the screenshot with `animations: "disabled"` and `caret: "hide"`, of the
  whole viewport or of one element (`options.locator`); and can outline the element the step asks the reader to use
  (`options.highlight`, a 2 px outline in the brand indigo, removed after the shot). Nothing it draws stays on the
  page.
- **Routes of `muster dev`** (`internal/devmode`), beside `SlackRoute` and in the same form — a function of the
  Connection's address and its token that answers the fake server's address or nothing:
  - `TelegramRoute`: the Bot API base URL `https://api.telegram.org`, the default, with one of the fake Telegram
    server's published bot tokens, goes to the fake Telegram server;
  - `MattermostRoute`: the server URL `https://chat.example.org` with one of the fake Mattermost server's published
    bot tokens goes to the first fake Mattermost server;
  - `BridgeRoute`: the base URL `https://bridge.example.org` with one of the fake bridge's published tokens goes to
    the fake bridge.
  Any other address or token is not routed, so a real token always goes where its Connection points. The clients
  (`internal/telegram`, `internal/mattermost`, `internal/bridge`) take the route in their settings as the Slack client
  does, `internal/connections` passes it to every client it builds (checks, the check before saving, delivery,
  polling), `internal/runtime` sets it only under `muster dev`, and `muster doctor` uses it under development as it
  uses `SlackRoute`. A routed request is an ordinary request to the fake's loopback address: it goes through
  `internal/outbound`, the outbound address policy checks it after name resolution — the development database allows
  `127.0.0.0/8` (`LoopbackNetwork`), and without it the request is `blocked` — and the Request log records it as
  other requests of `muster dev`. The demo Connections keep their addresses, so the existing end-to-end specs do not
  change. The OIDC issuer is not routed: the browser follows the issuer's addresses itself, so the guides show the
  OIDC settings as a form filled with example values and taken before saving (S-107).
- **Data**: demo data only. The specs make what they show through the UI they document, and the rest through the API
  and the fake servers' control endpoints (`support.ts` gains the helpers they share): the fake Telegram server's
  second bot (`234567:dev-telegram-token-b`, for which `muster dev` creates no Connection) on the default Bot API
  address, its chats through `PUT /_fake/chats/{chat}`, the `/start` of the Account link through
  `POST /_fake/updates` and the press of Ack through `POST /_fake/press`; the fake Mattermost server's second bot
  (`muster-dev-mattermost-token-b`) on `https://chat.example.org`; the fake Slack server's second app
  (`xoxb-muster-dev-slack-token-b`, `xapp-muster-dev-slack-token-b`); the fake bridge's second bot
  (`muster-dev-bridge-token-b`) on `https://bridge.example.org`. Names on the screens are neutral and realistic
  ("Production Alertmanager", "checkout", "#alerts"); no screenshot shows a real workspace, server, person or token,
  or a fake server's address. The callback addresses that Muster shows for Mattermost and Bridges are Muster's own
  development address, `http://localhost:8081`, which the guides tell the reader to read as their `MUSTER_INGEST_URL`.
- **Independence**: a spec's images do not depend on which other specs ran before it. Each spec:
  - removes in `afterAll` what it created — Integrations, Connections, Destinations, Routes and Account links, as
    `deleteRoute` and `removeSlackConnection` of `support.ts` do — and reverts every Organization setting it saved,
    through the API with the `ETag` it read first; the specs of this story save none;
  - reads the numbers and ids it shows or asserts — Alert Group numbers, `public_id`s — from what it created, never as
    fixed values;
  - signs in at the start of each test, and takes a list as the element of the rows it made where other rows could
    show.
  A spec that advances the development clock is named `*.clock.spec.ts` and runs on its own `muster dev` (above); it
  first turns off the Heartbeat of the demo Integration `dev-alertmanager` through the API, so that the advance raises
  no `MusterHeartbeatLost`. In the other runs the demo Heartbeat, signalled every 60 real seconds by the fake
  Alertmanager, stays live for the whole run and changes nothing a screenshot shows. The specs of this story advance
  no clock.
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
    and Bridges, which differ only in steps 2, 3 and 5, with links to their sections). Step 2 keeps the default Bot API
    address. Step 4 creates the Route "checkout" with the Matcher `service="checkout"`, the Group key `alertname`,
    `service` and the Destination; step 6 shows the Quick start's `curl` with `service="checkout"`, and the spec sends
    the same alert through the fake Alertmanager. The pictures of Telegram itself — the message and its buttons — are
    not screenshots of Telegram: the tutorial shows the Destination's preview and the Alert Group page afterwards.
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

1. Add the routes of `muster dev` and pass them through the clients, `internal/connections`, the runtime and
   `muster doctor`. Check: `go test ./internal/devmode ./internal/telegram ./internal/mattermost ./internal/bridge
   ./internal/connections ./internal/doctor`, then a Telegram Connection on the default address with
   `234567:dev-telegram-token-b` passes "Check connection" on `muster dev`.
2. Write the Playwright configuration, the capture helper and `make docs-screenshots` with its environment; take one
   screenshot of the Connections page. Check: on muster-lab `make docs-screenshots GUIDES=telegram` writes it; on
   macOS the target refuses; `make e2e` does not list the suite.
3. Write `imagecheck` with its tests. Check: `go test ./internal/tools/imagecheck`, then `make docs-check` passes.
4. Write the tutorial's spec and page. Check: `make docs-screenshots GUIDES=first-alert` passes and the page shows its
   images in `make docs-serve`.
5. Write the four messenger specs and their sections, and the nightly job. Check: `make docs-screenshots`, then
   `make docs-check`; the nightly workflow started by hand (`gh workflow run nightly.yml`) passes its new job.

## Verification

On the VM muster-lab (Linux with Docker):

```sh
make docs-screenshots                                                      # all five specs pass
ls docs/assets/screenshots/first-alert docs/assets/screenshots/messengers   # first-alert/ and the four messengers
python3 -c 'import struct,glob,sys; w=[struct.unpack(">I",open(f,"rb").read()[16:20])[0] for f in glob.glob("docs/assets/screenshots/**/*.png",recursive=True)]; print(len(w), max(w)); sys.exit(max(w)>1280)'
                                                                           # N images, none wider than 1280 px
make docs-check                                                            # passes
pnpm --dir web exec playwright test --list | grep -c 'screenshots/'         # 0: the suite is not part of make e2e
# a failed run changes nothing: break one locator of telegram.spec.ts, then
make docs-screenshots GUIDES=telegram; git status --short docs/assets/screenshots   # the run fails; no change
# the image check's failures, each undone afterwards:
sed -i.bak 's|01-new-connection.png|01-missing.png|' docs/messengers/telegram.md && make docs-check
                     # fails: docs/messengers/telegram.md:<line>: ../assets/screenshots/messengers/telegram/01-missing.png does not exist
mv docs/messengers/telegram.md.bak docs/messengers/telegram.md
cp "$(ls docs/assets/screenshots/first-alert/*.png | head -n1)" docs/assets/screenshots/first-alert/99-orphan.png
make docs-check      # fails: docs/assets/screenshots/first-alert/99-orphan.png is referenced by no page
rm docs/assets/screenshots/first-alert/99-orphan.png
```

On a Mac: `make docs-screenshots` stops at once with the message of the environment, and
`pnpm --dir web exec playwright test --config playwright.screenshots.config.ts` refuses to load.

Against `muster dev`, the routes: Connections → New → Telegram with the default Bot API address and
`234567:dev-telegram-token-b` — "Check connection" passes, the form shows no warning about `http`, and the fake
records `getMe`; with the token `999:not-published` the check goes to `api.telegram.org` (refused without network or
`token_invalid`), never to the fake. The same for Mattermost on `https://chat.example.org` and a Bridge on
`https://bridge.example.org`. `grep -rE '127\.0\.0\.1|localhost:1[89]' docs/first-alert.md docs/messengers/*.md`
finds no fake server's address.

`make docs-serve`: open `http://localhost:8000/first-alert/` and the four messenger pages; every image loads.

The pull request carries the output of `make docs-screenshots`, a few of the images inline, the result of the
tutorial's spec, whose last assertion is the Alert Group acknowledged from Telegram, and the run of the nightly job.

## Open questions

None.

## Notes

- Suggested commit: `docs: add the tutorial first alert in a messenger and screenshots of the messenger guides`.
- Telegram is the tutorial's messenger because it needs no address of Muster that the messenger reaches (long
  polling) and a bot takes a minute to create; the maintainer may choose another in review, which changes steps 2, 3
  and 5 only.
- CI never takes the screenshots for a pull request: relative times and the development clock make pixels differ
  between runs. The images change only when someone runs `make docs-screenshots` in its pinned environment and
  commits them, and `make docs-check` keeps the references and the files in step. The nightly job proves only that
  the suite still runs.
- The routes follow `SlackRoute` (D426): published tokens only, so the routes cannot send a real token to a fake, and
  nothing outside `muster dev` is routed.
- `llms.txt.tmpl` quotes `docs/messengers/bridge.md` and `docs/messengers/slack.md` at length; a quote that the new
  steps break makes `make docs-check` fail and is fixed in the page, not in the template.
- The screenshots show the UI before the documentation links of S-108 to S-110; S-110 takes them all again once every
  link is in place.
- With 38 files the story stays under the size signal; the routes of `muster dev` belong here because no screenshot
  of a Connection can be taken without them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-9 | partial | the tutorial and the messenger pages; the other pages are S-107 |
| C-21.FR-10 | full | |
| C-21.AC-5 | full | |
| C-21.AC-6 | partial | the suite, the check and the images of these guides; S-107 completes the images of the other guides |
