---
id: S-209
title: "Spike: how many fields Slack takes in one section, and where a Mattermost attachment draws its fields"
capability: C-38
kind: docs
layer: L2
depends_on: []
covers: [C-38.FR-2, C-38.FR-6]
files_touched:
  - design/facts.md
  - design/prd/L2.md
  - design/prd/l1/defaults.md
  - design/stories/S-210-the-message-layout-of-a-route-with-its-checks-migration-api-audit-log-and-the-rerender-be.md
  - design/stories/S-211-the-root-message-follows-the-message-layout-in-mattermost-slack-and-telegram-with-the-shortening-and-templates-be.md
acceptance:
  - "[C-38.FR-2, C-38.FR-6] `design/facts.md` has a new entry F-NNN that says how many `fields` Slack accepts in one `section` block of `chat.postMessage` and of `chat.update`, with the error and its text for one more than that, the date, the method and the workspace's plan; if the answer is not 10 the value of `message.layout_fields` is corrected in `defaults.md` and in P-94 of L2 §5.3, and the change is named in S-210."
  - "[C-38.FR-6] `design/facts.md` has a new entry F-NNN that says, for the Mattermost version of the test stand, whether an attachment draws its short `fields` under its `text` whatever their order among the properties of the request, and whether that holds when the post is edited with `PUT /posts/{id}/patch`, with the three attachments that were posted and how each was drawn."
  - "The item \"Message layout\" leaves Pending in `design/facts.md`, and the contracts of S-210 and S-211 cite the new entries: any assumption the facts contradict is corrected in them, or proposed as a change of the PRD in the pull request."
verify: "make lint"
operator_attention: true
issue: null
---

# S-209. Spike: how many fields Slack takes in one section, and where a Mattermost attachment draws its fields

## Scope

**IN**

- The two checks of the item "Message layout" in Pending of `facts.md` (L2 §5.1), on the maintainer's test stands,
  recorded as facts.
- The corrections of S-210's and S-211's contracts that the facts call for, and of the provisional value P-94 if the
  Slack answer is not 10.

**OUT**

- The layout itself and the adapters that follow it: S-210, S-211. This story changes no code.

## Contracts

- **Facts** (`design/facts.md`): two entries numbered after the highest `F-NNN` when the spike merges, each with the
  date, the method and the versions; the item "Message layout (C-38)" under Pending is replaced by "Used in" lines that
  cite S-210 and S-211, as the spike of S-116 does.
- **Provisional value**: P-94 (`message.layout_fields`, 10 fields, "the fact of Pending") is confirmed by the Slack
  entry, or changed in `defaults.md` and L2 §5.3 in this pull request, with the reason; S-210 validates against the
  value that stands when it is dispatched.
- **Method**, from the test stands of the maintainer (their Slack workspace and Mattermost server): `curl` against the
  messengers' APIs with a bot token the maintainer provides; nothing goes through Muster.

## Steps

1. Slack: post a message with one `section` block holding 10, then 11, then 12 `fields` with `chat.postMessage`, and
   the same with `chat.update`; note the response and the error of each. Check: the entry says the accepted count and
   quotes the error.
2. Mattermost: post three messages with one attachment each — `fields` before `text`, `fields` after `text`, and
   `fields` with `short` true and false mixed — and edit one with `PUT /posts/{id}/patch`; look at each in the web
   client at 360 px and 1280 px. Check: the entry says where the fields are drawn and whether the order of properties
   or the edit changes it.
3. Record the entries, move the item out of Pending, correct P-94 if needed and the two story contracts. Check:
   `make lint`.

## Verification

`make lint` passes. `git diff` shows `design/facts.md` with the two entries and the item gone from Pending; the entries
name the versions, the dates and the exact requests, with no token in them (a token in a fact is a secret in a log
line: `[redacted]`).

## Open questions

None; the spike answers them.

## Notes

- Suggested commit: `docs(facts): record how many fields Slack takes in one section and where Mattermost draws them`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The checks themselves
  may run any time the maintainer has the stands; S-210 and S-211 wait for the merge.
- **Maintainer** (operator attention): provides the Slack bot token with `chat:write` and a channel, and the
  Mattermost server and its bot, for the length of the checks, and revokes them after; an agent does not create
  accounts. If the Mattermost answer is that the order of fields in the request does matter in some version, S-211's
  contract changes and the editor's hint "In Mattermost the fields always show under the text" goes (C-38 UI): say so
  in the pull request for the maintainer to decide.
- If P-94 changes, L2 §5.3 and `defaults.md` change in this pull request, and the coverage table of C-38 does not.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-2 | partial | the limit of fields that P-94 assumes; the layout itself is S-210 and S-211 |
| C-38.FR-6 | partial | the two facts the adapters rely on; the adapters are S-211, S-212 |
