---
id: S-138
title: "Spike: Mattermost groups and Slack user groups — reading them, mentioning them, and everyone from a bot"
capability: C-25
kind: docs
layer: L2
depends_on: []
covers: [C-25.FR-8, C-28.FR-2]
files_touched:
  - design/facts.md
  - design/stories/S-139-team-mentions-with-the-connections-checks-and-connection-groups.md
  - design/stories/S-140-mention-entries-their-resolution-the-migration-of-the-l1-settings-and-storm-started.md
  - design/stories/S-143-mentions-grid-of-the-destination-form-and-how-to-mention-the-team.md
  - design/stories/S-144-documentation-whom-a-message-mentions-and-team-mentions.md
acceptance:
  - "[C-25.FR-8, C-28.FR-2] `design/facts.md` has a new entry F-NNN that says whether `@<group>` in a bot's post notifies the members of a Mattermost custom group, and of an LDAP-synced group, in a Thread reply's `message` and inside the attachment of a Root message, as `@channel` does (F-113), and whether the group's `allow_reference` matters, with the Mattermost version and edition."
  - "[C-25.FR-8] `design/facts.md` has a new entry F-NNN that says which Mattermost editions and licences have custom groups and LDAP-synced groups, and how a bot reads them — `GET /api/v4/groups` with which parameters and which permission or role of the bot — and what the call answers to a bot without it."
  - "[C-25.FR-8, C-28.FR-2] `design/facts.md` has a new entry F-NNN that says what `usergroups.list` answers with the scope `usergroups:read` (the fields Muster needs: `id`, `handle`, `name`, `date_delete`, `user_count`) and without it, and whether an app that gained the scope in its manifest lacks it in `x-oauth-scopes` until it is reinstalled."
  - "[C-28.FR-2] `design/facts.md` has a new entry F-NNN that says whether `<!subteam^ID>` from a bot notifies the members of a Slack user group in a Thread reply and in the blocks of a Root message, and whether `<!everyone>` from a bot notifies anyone outside the workspace's general channel, and how it renders elsewhere."
  - "[C-25.FR-8, C-28.FR-2] The items \"Mattermost groups\" and \"Slack user groups\" leave Pending in `design/facts.md`, and S-139 and S-140 cite the new entries: the fake Mattermost and Slack servers, the Connections' checks and the rendering of group mentions follow the facts, any assumption the facts contradict is corrected in those contracts or proposed as a change of C-25.FR-8 or C-28.FR-2 in the pull request; S-143 gains the reasons and texts the facts call for, such as Mattermost groups that need a licence, and S-144 cites what the documentation must say."
verify: "make lint"
operator_attention: true
issue: null
---

# S-138. Spike: Mattermost groups and Slack user groups — reading them, mentioning them, and everyone from a bot

## Scope

**IN**

- The checks of L2.md §5.1 and of Pending in facts.md on Mattermost groups and Slack user groups, and on `<!everyone>`
  from a bot, on the maintainer's test stands, recorded as facts.
- The corrections of S-139's and S-140's contracts, and the notes of S-144, that the facts call for.

**OUT**

- Team mentions and the Connections' checks: S-139. The Mention entries and how delivery writes group mentions: S-140.
- Telegram: S-132. Push of Mentions in Thread replies: S-160. GitLab's claims: S-116.

## Contracts

- **Facts** (`design/facts.md`): new entries in the sections Mattermost and Slack, numbered after the highest `F-NNN`
  when the spike merges, each with the date, the method and the version or plan, and "Used in C-25.FR-8, C-28.FR-2";
  the items "Mattermost groups" and "Slack user groups" leave Pending.
- **Story contracts**: S-139 assumes that a Mattermost bot reads groups with `GET /api/v4/groups` (and reports in the
  check whether it may), that a Slack bot with `usergroups:read` reads user groups with `usergroups.list`, and that
  `x-oauth-scopes` reports the scope only after a reinstall; S-140 assumes that `@<group>` in a Mattermost message and
  `<!subteam^ID>` in a Slack one notify the group's members, and `<!everyone>` everyone of the workspace in its general
  channel. Each differing fact corrects the contract, or the pull request proposes the change of the requirement.

## Steps

1. The maintainer runs the checks of Verification on the test stands, with scripts an agent may prepare outside the
   repository. Check: the observations of each step are written down with the raw answers, tokens removed.
2. Write the facts and move the items out of Pending. Check: `make lint`.
3. Correct S-139's, S-140's and S-143's contracts and add the citations to S-144. Check: `scripts/stories-sync --dry-run`
   parses the files.

## Verification

On the maintainer's Mattermost test server (record the version, the edition and the licence) and Slack test workspace
(record the plan), with test accounts `alice` and `bob` on a phone and a desktop each:

1. Mattermost: create a custom group `payments_team` with `alice`; if the server has LDAP, note whether an LDAP-synced
   group exists. As the Muster bot (role Member, then with the permission the server asks for), call `GET
   /api/v4/groups?per_page=10` and `GET /api/v4/groups?filter_allow_reference=true`; record the status and fields of
   each answer.
2. Mattermost: the bot posts a Root message whose attachment text starts with `@payments_team`, and a Thread reply
   whose `message` is `@payments_team`; record whether `alice` gets a notification (desktop and phone) and `bob` none;
   repeat with `allow_reference` off.
3. Slack: an app whose manifest has `usergroups:read` added after installation — call `auth.test` and record
   `x-oauth-scopes`; reinstall and record it again. Create the user group `payments-team` with `alice`; call
   `usergroups.list` with and without the scope; record the answer and the error.
4. Slack: the bot posts a Root message whose blocks start with `<!subteam^S…>` and a Thread reply with it; record whether
   `alice` gets a notification and `bob` none.
5. Slack: the bot posts `<!everyone>` in the general channel and in another channel; record who is notified and how the
   message renders in each.

The record of each step goes into the facts; tokens and real names never go into the repository.

## Open questions

None; the spike answers them.

## Notes

- The maintainer performs the checks (operator attention): they need the test servers, an admin account on each and two
  test accounts with clients. An agent may write the scripts outside the repository and run them on the maintainer's
  instructions.
- `covers` names C-25.FR-8 and C-28.FR-2 in part: the spike settles how the Connections read and mention groups; S-139
  and S-140 implement them.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-8 | partial | the facts of reading and mentioning groups; the requirement is S-139 |
| C-28.FR-2 | partial | the facts of group and everyone mentions; the requirement is S-139 and S-140 |
