---
id: S-104
title: "Connect Slack on the documentation site, with the app manifests, and Slack on the Messengers page"
capability: C-24
kind: docs
layer: L1
depends_on: [S-103]
covers: [C-24.FR-16, C-24.AC-20]
files_touched:
  - docs/messengers/slack.md
  - docs/messengers/index.md
  - docs/messengers/account-links.md
  - docs/messengers/proxies.md
  - docs/concepts/loud-and-quiet.md
  - docs/concepts/broken-destinations.md
  - docs/concepts/statuses-and-commands.md
  - docs/quick-start.md
  - docs/sign-in/index.md
  - docs/install/helm.md
  - docs/operations/runbooks/MusterDestinationBroken.md
  - docs/operations/high-availability.md
  - docs/operations/request-log.md
  - docs/index.md
  - docs/llms.txt.tmpl
  - README.md
  - mkdocs.yml
acceptance:
  - "[C-24.FR-16, C-24.AC-20] The page \"Connect Slack\" (`docs/messengers/slack.md`), in the navigation under Messengers after Telegram, gives the app manifest for Socket Mode — the app and its bot named Muster, the Messages tab on and read-only, the Home tab off, interactivity on, Socket Mode on, token rotation off, the six bot scopes — and the changes for HTTP, whose Interactivity Request URL is the Connection's callback address; and says what each scope is for, that the bot posts under the name in the app's settings (F-073), how to install the app, create the app-level token with `connections:write`, invite the bot with `/invite @Muster` and create the Connection and its Destinations."
  - "[C-24.FR-16] The page explains Socket Mode against HTTP — no address of Muster that Slack reaches against no press lost while the Leader changes — one Slack app per Muster installation and the warning about another program on the app, the proxy (CONNECT to `slack.com`, to the socket's hosts under `slack.com` and to `hooks.slack.com`), Loud and Quiet in Slack (Quiet means without a Mention), Mentions (`@channel`, `@here`, linked users), the limiters and Slack's rate limits, edits, deleted Root messages and the reply that appears in the channel for a moment when a Root message was deleted before any reply, the test message that cannot press its own button, and the Request log; every setting is named by its `area.setting`."
  - "[C-24.FR-16] The Messengers page names five Destination types and its table has a Slack column with every row filled; Link your messenger accounts has a Slack section (email or name, the code in the Messages tab); Proxies names the hosts Slack needs; Loud and Quiet, Broken Destinations, High availability (Slack's Socket Mode on the Leader) and the Request log page name Slack where they name the other messengers; Statuses and commands has Slack in its table of Transports, with the Snooze durations and presets as one menu; the Quick start offers Slack beside Mattermost and Telegram; Sign-in names Slack's callback and Slack buttons; the Helm page names Slack's presses in HTTP mode among what reaches `ingestURL`; the runbook of `MusterDestinationBroken` names a bot removed from a Slack channel and Slack's `429`; the home page, `llms.txt` and the README list Slack among the messengers."
  - "[C-24.AC-20] `make docs-check` passes: the site builds strictly, no link or anchor is broken, and `llms.txt` and the full text are current."
verify: "make docs-check"
operator_attention: false
issue: 313
---

# S-104. Connect Slack on the documentation site, with the app manifests, and Slack on the Messengers page

## Scope

**IN**

- The page "Connect Slack" with both manifests, and Slack on the pages that name the messengers.

**OUT**

- Everything Muster does with Slack (S-097 to S-103).

## Contracts

- **Pages**: `docs/messengers/slack.md` (new, the outline of C-24.FR-16); `docs/messengers/index.md` (five types, the
  Slack column: needs a Connection — yes; edits — yes, no window known; buttons — yes, with the Snooze menu; Thread —
  replies under the Root message; Loud — a new message with Mentions; Quiet — an edit, or a message without a Mention;
  Mentions — nobody, `@channel`, `@here`, users; Account links — a code by direct message; rate limits — Connection 50
  requests/min, Destination 30 requests/min; edit window — none known for a bot's messages; Storms — one Storm summary
  per Route; proxy — on the Connection; Destination check — yes, `conversations.info`; test message and preview — yes,
  the test cannot press), and the notes and Pages list; `account-links.md`, `proxies.md`, `loud-and-quiet.md`,
  `broken-destinations.md`, `statuses-and-commands.md`, `quick-start.md`, `sign-in/index.md`, `install/helm.md`, the
  runbook `MusterDestinationBroken.md`, `high-availability.md`, `request-log.md`, `docs/index.md` and `README.md` as the
  acceptance says; `mkdocs.yml` nav; `llms.txt.tmpl` gains the scenario "Connect Slack", built from quotes of the page
  like the others.
- **Manifests**: the YAML of the Socket Mode app (`display_information.name` Muster, `features.app_home` with
  `messages_tab_enabled` and `messages_tab_read_only_enabled` true and `home_tab_enabled` false, `features.bot_user`
  `display_name` Muster, `oauth_config.scopes.bot` the six scopes, `settings.interactivity.is_enabled` true,
  `settings.socket_mode_enabled` true, `settings.token_rotation_enabled` false, `settings.org_deploy_enabled` false),
  and for HTTP `settings.interactivity.request_url` set to the callback address and `socket_mode_enabled` false; no
  `event_subscriptions`.
- **Names**: settings by `area.setting` (`connection.slack.event_mode`, `connection.slack.limiter`,
  `destination.slack.limiter`, `slack.request_tolerance`, `slack.hello_timeout`, `account_link.slack_code`), facts not
  cited by id on the site.

## Steps

1. Write the page with the manifests, checked against the maintainer's test app's settings. Check: `make docs`.
2. Add Slack to the other pages, the navigation, `llms.txt` and the README. Check: `make docs-check`.

## Verification

```sh
make docs-check                                                           # passes
grep -c 'messengers/slack.md' mkdocs.yml                                  # 1
grep -c 'socket_mode_enabled' docs/messengers/slack.md                    # 2: both manifests
grep -c '| Slack |\|Slack |' docs/messengers/index.md                     # the table's header names Slack
make docs-serve &   # then open http://localhost:8000/messengers/slack/ and follow every link on it
```

The manifest is checked by pasting it into a new app's "Create from an app manifest" on the maintainer's test
workspace, which Slack accepts without a warning — or, without access, by comparing it field by field with the test
app's manifest export; the pull request says which.

## Open questions

None.

## Notes

- Suggested commit: `docs(messengers): add connect slack with the app manifests`.
- The manifest is the one of the spike, with the bot named Muster (D385); the scopes are those the test app has, which
  the spike found sufficient.
- The documentation names no other product than Slack itself, and shows no workspace, channel or user of the test
  workspace.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-16 | full | |
| C-24.AC-20 | full | |
