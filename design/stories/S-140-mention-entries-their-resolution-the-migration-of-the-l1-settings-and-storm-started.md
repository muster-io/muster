---
id: S-140
title: Mention entries with their Loud events and their resolution, the migration of the L1 Mention settings, and storm_started (BE)
capability: C-28
kind: be
layer: L2
depends_on: [S-121, S-131, S-139]
covers: [C-28.FR-1, C-28.FR-2, C-28.FR-3, C-28.FR-4, C-28.FR-5, C-28.FR-6, C-28.FR-7, C-28.FR-10, C-28.AC-1, C-28.AC-2, C-28.AC-3, C-28.AC-5, C-28.AC-6, C-28.AC-8, C-25.FR-8, C-26.FR-1, C-26.FR-5, C-11.FR-6, C-11.FR-20, C-12.FR-8, C-12.FR-12, C-13.FR-8, C-15.FR-11, C-17.FR-2, C-22.FR-3]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0034_mention_entries.up.sql
  - internal/db/migrations/0034_mention_entries.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/mentions/entries.go
  - internal/mentions/entries_test.go
  - internal/mentions/entries_integration_test.go
  - internal/mentions/query.sql
  - internal/destinations/destinations.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/destinations/query.sql
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/teams/destinations.go
  - internal/teams/teams_test.go
  - internal/delivery/delivery.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/storm.go
  - internal/delivery/storm_test.go
  - internal/delivery/deliverytest/recorder.go
  - internal/webhooks/mentions.go
  - internal/webhooks/body_test.go
  - internal/runtime/runtime.go
  - test/e2e/mention_entries_test.go
  - web/src/test/fixtures/destinations.ts
  - docs/concepts/mentions.md
  - mkdocs.yml
  - docs/install/versions.md
acceptance:
  - "[C-28.FR-1, C-28.FR-10, C-12.FR-8] Every Destination type takes `mention_entries` — each a target (`everyone` with `channel`, `here`, `all` or `everyone`; `user`; `group`; `team`; `teams_from_label`; `on_call_of_team`; `on_call_of_teams_from_label`) and the Loud events it applies to (`new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`, `snooze_ended`, `rise_to_urgent`, `storm_started`), all seven when omitted — saved in order with `If-Match` and in the diff of `destination.updated`; a Destination without entries mentions nobody (`entries_test.go`, `write_test.go`)."
  - "[C-28.FR-2, C-28.AC-3, C-22.FR-3] A Telegram Destination refuses `channel`, a group, a Team and \"Team from the label\" with `422` `mention_unsupported` at `/mention_entries/{i}/target` and takes a person and \"On-call of a Team\"; a Slack Destination takes `everyone` and refuses `all`, and takes a user group or a Team only while its Connection's last check found `usergroups:read`; a Bridge Destination takes `channel` only when its bridge declares it in `everyone_mentions`, a group only with `group_mentions`, and a person on any bridge; an outgoing webhook takes every target; an entry whose Connection later lost the ability stays, reads `available` false with its `MentionUnavailableReason` and mentions nobody (`entries_test.go`)."
  - "[C-28.FR-3, C-28.AC-1, C-28.AC-2, C-25.FR-8, C-13.FR-8] A Mattermost Destination with \"On-call of the Team from the label\" for every event and `here` for `new_alert_group` only mentions, on a new Alert Group of \"payments\", the On-call of payments' main Schedule at that moment and `@here`, and on its Reopen only the On-call; \"Team payments\" mentions `@payments_team` when the Team has that Team mention for the Connection, and nobody — not the On-call — when it has none; each person and group is mentioned once per message (`mentions_test.go`, `mention_entries_test.go`)."
  - "[C-28.FR-3, C-12.FR-12] A person whom an entry names without an Account link in the Destination's Identity space is left out of the message's Mentions and nothing is written in the Thread for them (`mentions_test.go`)."
  - "[C-28.FR-4] Reminders and auto-unacknowledge still mention the Owner and a Takeover the previous Owner whatever the entries, a Reopen into acknowledged only the Owner, an edit never adds Mentions, and a Quiet new message carries none (`worker_test.go`)."
  - "[C-28.FR-5, C-28.AC-5] The migration turns the L1 settings \"`@channel` for `new_alert_group`, Alice for `ack_timeout`\" into two entries — `channel` for `new_alert_group` and `storm_started`, Alice for `ack_timeout` — and a Destination that mentioned nobody into none; until 0.3.0 `mentions` is read-only, computed from the entries L1 can express, reads the same as before, and is ignored in requests; the column keeps what the migration found (`entries_integration_test.go`, `db_test.go`)."
  - "[C-28.FR-6, C-28.AC-8, C-26.FR-1] Naming a Destination of the Team \"payments\" that has no entry naming that Team adds the entry \"Team payments\" for every Loud event, recorded in the Audit log, on every Destination type but Telegram, which gets none; an Admin may remove it, and naming it again later adds it again only when it has none (`teams_test.go`)."
  - "[C-28.FR-7, C-28.AC-6, C-11.FR-6] The first publication of a Storm summary carries the Mentions of the Destination's entries for `storm_started`, with \"Team from the label\" and \"On-call of the Team from the label\" taken from the Teams of the Alert Groups it counts; its later edits carry none, and it starts nothing else (`storm_test.go`)."
  - "[C-26.FR-5, C-11.FR-20, C-17.FR-2] `destination_joined` mentions the Destination's entries for `new_alert_group`, and an ack timeout notice those for `ack_timeout` (`worker_test.go`)."
  - "[C-15.FR-11] In outgoing webhook data a Team is a target of `WebhookMentionTarget` with the type `team`, its `slug` and `name`, \"Team from the label\" one such target per Team of the Alert Group, and the On-call of a Team a `user` target (`body_test.go`)."
  - "[C-28.FR-1, C-28.FR-2] The documentation has the page Concepts → Whom a message mentions with \"Mention entries\", \"What each messenger supports\", \"Teams and the On-call\" and \"Upgrading from 0.1\", and the upgrade notes of 0.2.0 describe the migration; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-140. Mention entries with their Loud events and their resolution, the migration of the L1 Mention settings, and storm_started (BE)

## Scope

**IN**

- A Destination's Mentions as one ordered list of entries, each a target and the Loud events it applies to, limited to
  what the Destination's Connection can mention (C-28.FR-1, FR-2; C-12.FR-8, C-22.FR-3 revised).
- Their resolution for each Loud event: people by Account link, groups, Teams by their Team mention and nobody without
  one, "Team from the label", the On-call of a Team and of the Teams from the label at the moment of the event; each
  person and group once (C-28.FR-3, C-25.FR-8, C-11.FR-20, C-17.FR-2 revised). The rules of L1 for the Owner stay
  (C-28.FR-4). Mattermost writes a group or a Team mention as `@name`, as L1 already writes groups (C-13.FR-8 revised).
- Teams and the On-call as targets of the data of outgoing webhooks (C-15.FR-11 revised).
- The 0.2.0 migration of the L1 settings into entries, with the L1 form read-only until 0.3.0 (C-28.FR-5).
- The entry "Team {Team}" added to a Destination that becomes a Destination of the Team, Telegram excepted (C-28.FR-6,
  C-26.FR-1).
- `storm_started`: the first publication of a Storm summary mentions its entries (C-28.FR-7, C-11.FR-6 revised).

**OUT**

- Writing group mentions and mentions of everyone in Slack (`<!subteam^ID>`, `<!everyone>`) and through bridges (the
  `kind` of a Mention in `api/bridge.yaml`): S-171, which follows this story.
- "Mention not delivered" in the Timeline and the Destination's warnings about it: S-165.
- The Mentions grid of the Destination form, "What each messenger supports" and "How to mention the Team": S-143. The
  documentation's completion: S-144.
- The libraries' types: S-141, S-142.
- The `paged` people of Escalation steps in webhook data: S-155; the Assignees named in the layout: S-168.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - New schemas: `MentionEvent` (the seven Loud events above); `MentionTargetKind` (the seven kinds above);
    `MentionTargetInput` {`kind`, `everyone` (`channel`, `here`, `all`, `everyone`; with `everyone` only), `user_id`
    (with `user`), `group` and `group_id` (with `group`; `group_id` the Slack user group's id, required on Slack),
    `team_id` (with `team` and `on_call_of_team`)}; `MentionEntryInput` {`target`, `events` (`MentionEvent`[], unique,
    at least one; all seven when omitted)}; `MentionEntry` = input + `target.user` (`UserRef`), `target.team`
    (`TeamRef`, its `deleted` set for a deleted Team), `available`, `unavailable_reason` (`MentionUnavailableReason` of
    S-139, or null) and `resolves_to` ({`team` (`TeamRef` or null), `mentions` ({`kind` — `user`, `group`, `everyone`
    —, `name`, `linked`})[]}[]: what the entry mentions now — for "from the label" targets one item per Team that is
    not deleted).
  - Every `*DestinationInput` gains `mention_entries` (`MentionEntryInput`[], optional: omitted on create is none,
    omitted on update keeps); the reads require `mention_entries` (`MentionEntry`[]). `mentions` (`MentionSettings`)
    becomes `deprecated` and `readOnly` in the reads — computed from the entries that L1 can express — and is optional
    and ignored in the inputs; "removed in 0.3.0".
  - Problem code `mention_unsupported` (`validation-failed`, in `errors[].code`) at `/mention_entries/{i}/target`.
  - `WebhookMentionTarget`, the existing schema of the targets in outgoing webhook data (C-15.FR-11), gains the type
    `team` with `slug` and `name`; `MentionName`, the enum of a delivery event's `mentions`, gains `storm_started`.
- **Schema** (migration `0034_mention_entries`, expand only; `schema.md` §4.5 and §4.11):
  - `mention_entries`: `id`, `org_id`, `destination_id` (FK, `ON DELETE CASCADE`), `position`, `target_kind`
    (`CHECK` of the seven kinds), `everyone` (`CHECK IN ('channel', 'here', 'all', 'everyone')`), `user_id` (FK
    `users`), `group_name`, `slack_usergroup_id`, `team_id` (FK `teams`), `events text[] NOT NULL` (`CHECK (events <@
    ARRAY[…the seven…] AND cardinality(events) > 0)`), `created_at`; a CHECK that each kind has exactly its fields;
    unique `(destination_id, position)`; indexes `(org_id, team_id)` and `(org_id, user_id)`.
  - The conversion (C-28.FR-5), in the up migration: for every Destination, each target chosen in `mentions` — the
    everyone choice other than `none`, each chosen User, each group — becomes one entry for the kinds it was chosen
    for, with `storm_started` added where `new_alert_group` is among them; positions: everyone, then Users, then groups,
    in the order L1 listed them. `destinations.mentions` keeps what the migration found and is not written afterwards,
    except that a Destination created from 0.2.0 on stores the L1 default (nobody), so that 0.1 replicas of a rolling
    update mention as before. Destinations of a Team (`team_destinations`, S-121) without an entry naming that Team get
    "Team {Team}", as C-28.FR-6 would have added (only builds of `master` between S-121 and this story have them), by
    the same rule as settled 1: every type but Telegram.
  - `delivery_events_mentions_v2_check` with `storm_started`, `NOT VALID`, validated in a second transaction, the old
    one dropped in a third, as `0010` does for the kind CHECK.
  - The down migration drops the table and restores the old CHECK.
- **Mentions** (`internal/mentions`): `entries.go` — the entries, their validation against the Connection's abilities
  (Mattermost: `channel`, `here`, `all`, groups, Teams; Slack: `channel`, `here`, `everyone`, and groups and Teams with
  `slack_usergroups_read`; Telegram: people and the On-call only; Bridge: the declared `everyone_mentions`, and groups
  and Teams with `group_mentions`; outgoing webhook: all), `available` and `resolves_to`, the L1 form computed from
  them, and `AddTeamEntry` for C-28.FR-6; `mentions.go` — `Resolve` takes, for a Loud event of an Alert Group in a
  Destination at time t, the entries whose events include it: a person by their Account link in the Identity space
  (left out without one; S-165 records it); a group by its name or Slack id; a Team by its Team mention for the
  Connection, nobody without one; "Team from the label" by the Team mention of each Team of the Alert Group
  (`alert_group_teams`); "On-call of a Team" by the On-call of the Team's main Schedule at t, nobody without one or in
  a gap; "On-call of the Team from the label" the same for each Team; then the Owner rules of L1; each person and
  group once. The Storm summary's `storm_started` resolution takes the Teams of the Alert Groups it counts. An entry
  that names a deleted Team or User stays, reads it marked deleted and mentions nobody (settled 2).
- **Imports** (ADR-0016): `mentions` imports neither `schedules` nor `teams`. It declares the interfaces it consumes —
  `OnCalls` (`OnCallAt(team, t)`, the On-call of a Team's main Schedule, which `schedules` satisfies with S-131's
  `OnCallAt`) and `TeamMentions` (`ForConnection(team, connection)`, which `teams` satisfies, S-139) — and
  `internal/runtime` wires them, as S-169 does for the Paging packages. `schedules` (S-164's `IdentitySpace`) and
  `teams` (`AddTeamEntry`) may import `mentions`; `delivery` and `webhooks` import it as in L1.
- **Destinations** (`internal/destinations`): the entries in the inputs, reads, diffs and validation of every type; the
  L1 `mentions` computed for the read.
- **Teams** (`internal/teams/destinations.go`): naming a Destination of the Team adds "Team {Team}" for every event
  through `mentions.AddTeamEntry` when it has no entry of the kind `team` with that Team, in the same transaction, as
  `destination.updated` with the Team's actor; on a Telegram Destination it adds nothing (settled 1).
- **Delivery** (`internal/delivery`): the worker resolves the Mentions of a Loud message through `mentions.Resolve`,
  `destination_joined` with `new_alert_group` and an ack timeout notice with `ack_timeout`; the first publication of a
  Storm summary with `storm_started` instead of L1's `new_alert_group`; the recording test adapter keeps the targets.
  Mattermost writes group targets as L1 does (`@{name}`, F-113 and the facts of S-138); until S-171 merges, the Slack
  and Bridge adapters leave out the group and everyone targets they cannot write yet.
- **Outgoing webhooks** (`internal/webhooks/mentions.go`): the data of C-15.FR-11 revised.
- **Web test fixtures** (`web/src/test/fixtures/destinations.ts`, S-166): `mention_entries` empty in the builders of
  Destination reads. The new enums meet no exhaustive switch of the SPA, and `MentionName` none either.
- **Settings**: `destination.mentions`, `route.storm_threshold`.
- **Log events, metrics**: none new.
- **Documentation**: the new page `docs/concepts/mentions.md` ("Whom a message mentions") — "Mention entries", "What
  each messenger supports" (the table of C-28.FR-2), "Teams and the On-call" (no fallback from a Team to its On-call),
  "Start of a Storm" and "Upgrading from 0.1" — under Concepts; `docs/install/versions.md` — the migration of the Mention
  settings and the read-only `mentions` until 0.3.0.

## Steps

1. Write the migration with its conversion and `schema.md`. Check: `make test-integration` runs up, down and up;
   `entries_integration_test.go` migrates the settings of C-28.AC-5, a Destination of each type and one that mentioned
   nobody, and reads back the same L1 form.
2. Write the entries, their validation, availability and `resolves_to`; add the spec. Check: `make generate-check`;
   `entries_test.go` covers the table of C-28.FR-2 cell by cell.
3. Write the resolution with its interfaces, wire them, and use it in delivery, Storm summaries and outgoing webhooks.
   Check: `mentions_test.go`, `worker_test.go`, `storm_test.go`, `body_test.go`; `go vet ./...` finds no import cycle.
4. Add the entry of a Team's Destination. Check: `teams_test.go`.
5. Write `test/e2e/mention_entries_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-121 and S-139: A, NOTIFY, PUTA, AG, TPUT; the Route "Infrastructure" (R) with infra-critical (IC) and infra-notice
# (IN); the Team "payments" (TP) with its Team mention payments_team for the demo Mattermost Connection (CM) and its
# Destination payments-notice (PN); the Team "search" (TS) without a Team mention; the Schedule "Main" of payments
# whose On-call now is alice, linked to u-alice of the fake Mattermost server as in S-069; the demo Telegram Destination
# (DT); CLK.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
DPUT() { E=$(curl -s "${A[@]}" $API/destinations/$1 | jq -r .etag)
  curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/destinations/$1 -d "$(curl -s "${A[@]}" $API/destinations/$1 |
    jq -c "del(.id, .etag, .mentions, .health, .routes, .teams, .created_at) | .mention_entries = $2")"; }
FIRSTLINE() { curl -s $FMM/posts | jq -r --arg c "$1" '[.[] | select(.channel_id == $c)] | last |
  (if (.message // "") != "" then .message else .props.attachments[0].text end) | split("\n")[0]'; }

# C-28.AC-3: Telegram refuses everyone, groups and Teams, takes the On-call of a Team
DPUT $DT '[{"target":{"kind":"everyone","everyone":"channel"}}]' | jq -c '[.status, .errors[0].code, .errors[0].pointer]'
# [422,"mention_unsupported","/mention_entries/0/target"]
DPUT $DT "[{\"target\":{\"kind\":\"on_call_of_team\",\"team_id\":\"$TP\"}}]" | jq -c '[.mention_entries[].events | length]'   # [7]

# C-28.AC-1: On-call of the Team from the label (every event) and @here (new Alert Group only)
DPUT $IN '[{"target":{"kind":"on_call_of_teams_from_label"}},{"target":{"kind":"everyone","everyone":"here"},"events":["new_alert_group"]}]' |
  jq -c '[.mention_entries[] | .resolves_to[] | select(.team.slug == "payments") | .mentions[].name]'                 # ["alice"]
curl -s -X PUT $FAM/groups/m1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"OOMKilled"}}' > /dev/null
PUTA m1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY m1 '{"reason":"first notification"}'; sleep 1
FIRSTLINE ch-infra-notice                                                                                          # @alice @here
G=$(AG 'team%3D%22payments%22')
curl -s -X DELETE $FAM/groups/m1/alerts/a > /dev/null; NOTIFY m1 '{"reason":"resolved"}'; sleep 1
PUTA m1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY m1 '{"reason":"new alerts"}'; sleep 1
FIRSTLINE ch-infra-notice                                                                                          # @alice   (the Reopen)

# C-28.AC-2: "Team payments" mentions the Team mention; "Team search" mentions nobody, not its On-call
DPUT $PN "[{\"target\":{\"kind\":\"team\",\"team_id\":\"$TP\"}},{\"target\":{\"kind\":\"team\",\"team_id\":\"$TS\"}}]" |
  jq -c '[.mention_entries[] | [.resolves_to[].mentions[].name]]'                                                  # [["payments_team"],[]]

# C-28.AC-8: a Destination named as one of the Team's gets "Team {Team}"; a Telegram one does not
D2=$(curl -s "${A[@]}" $API/destinations -d "{\"type\":\"mattermost\",\"name\":\"payments-critical\",\"connection_id\":\"$CM\",
  \"team_id\":\"team-dev\",\"channel_id\":\"ch-payments-critical\",\"limiter\":{\"limit\":30,\"per_seconds\":60}}" | jq -r .id)
TPUT $TP "[{\"destination_id\":\"$PN\",\"severity_condition\":\"below_critical\"},{\"destination_id\":\"$D2\",\"severity_condition\":\"critical\"},
  {\"destination_id\":\"$DT\",\"severity_condition\":\"any\"}]"
curl -s "${A[@]}" $API/destinations/$D2 | jq -c '[.mention_entries[] | {k: .target.kind, t: .target.team.slug, n: (.events | length)}]'
# [{"k":"team","t":"payments","n":7}]
curl -s "${A[@]}" $API/destinations/$DT | jq -c '[.mention_entries[].target.kind]'                                  # ["on_call_of_team"]

# C-28.FR-5: the L1 form is read-only and derived
curl -s "${A[@]}" $API/destinations/$IN | jq -c '.mentions.new_alert_group'                                         # {"everyone":"here","user_ids":[],"groups":[]}
```

`test/e2e/mention_entries_test.go` repeats these checks and adds a Slack Destination that takes `everyone` and takes a
user group only after its Connection's check found the scope, a bridge that takes `channel` only when it declares it
(C-28.AC-3; their messages are S-171's), a Storm on the Route whose summary's first publication mentions the entries
for `storm_started` (C-28.AC-6), an outgoing webhook with a Team target, and the Owner rules of C-28.FR-4. The migration
of C-28.AC-5 is checked by `make test-integration`, which migrates a database from `0033` with the L1 settings.

## Open questions

None open. Settled:

1. **Settled: no entry "Team {Team}" on a Telegram Destination** (maintainer's decision; C-28.FR-6 and ADR-0018
   amended): Telegram cannot mention Teams. Every other type gets it, also when its Connection lacks the ability now —
   a Slack Connection without `usergroups:read`, a bridge without group mentions — where it shows unavailable and
   mentions nobody, like any entry that lost its ability.
2. **Settled: entries that name a deleted Team or User stay**, read with the Team or User marked deleted, and mention
   nobody, as L1 settings kept deleted Users; configuration changes stay explicit.

## Notes

- Suggested commit: `feat(mentions): mention people, groups, teams and on-calls through entries per loud event`.
- The L1 column `destinations.mentions` and the field `mentions` stay until 0.3.0 (L2.md §2 "Expand and contract");
  0.3.0 removes both.
- The On-call is resolved at the moment of the event on the business clock, so the development clock moves it.
- Mentions are still never added by an edit (ADR-0005): a Team mention that is set later, or an On-call who links an
  account later, is mentioned from the next Loud event on.
- Split from the first contract of this story by the review: the writing of group mentions and mentions of everyone in
  Slack and through bridges, with `Mention.kind` in `api/bridge.yaml`, is S-171, which follows it; until then those
  targets are saved and resolved but left out of Slack and bridge messages.
- Between this story's merge and S-143's, the Destination form still shows the editor of the L1 settings, whose saves
  the server ignores (`mentions` is read-only); entries are set through the API until S-143 replaces the editor.
- Migration `0034` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-1 | partial | the API and delivery; the grid is S-143 |
| C-28.FR-2 | partial | the validation and the unavailable entries; writing them in Slack and bridges S-171, the table on the form S-143 |
| C-28.FR-3 | partial | the resolution; "Mention not delivered" and the warnings are S-165 |
| C-28.FR-4 | full | |
| C-28.FR-5 | full | |
| C-28.FR-6 | full | |
| C-28.FR-7 | full | |
| C-28.FR-10 | partial | the entries in the API, the locking and the Audit log; the form is S-143 |
| C-28.AC-1 | full | |
| C-28.AC-2 | full | |
| C-28.AC-3 | partial | the refusals; the Timeline record for a bridge without `native_mentions` is S-165 |
| C-28.AC-5 | full | |
| C-28.AC-6 | full | |
| C-28.AC-8 | full | |
| C-25.FR-8 | partial | mentioning a Team by its Team mention, nobody without one; in Slack and bridges S-171 |
| C-26.FR-1 | partial | the Mention entry that naming a Destination of the Team adds |
| C-26.FR-5 | partial | the Mentions of the entries for `new_alert_group` in `destination_joined` |
| C-11.FR-6 | partial | revised for L2: `storm_started` |
| C-11.FR-20 | partial | revised for L2: the Mentions of the entries; the lifecycle events of C-32 come with S-155, S-156, S-168 and S-169 |
| C-12.FR-8 | partial | revised for L2: the entries; the form is S-143 |
| C-12.FR-12 | partial | revised for L2: an unlinked person left out; the record is S-165, the Assignees S-168 |
| C-13.FR-8 | full | revised for L2: Mattermost groups and Team mentions |
| C-15.FR-11 | partial | revised for L2: Teams and the On-call as targets; the people of `paged` are S-155 |
| C-17.FR-2 | full | revised for L2 |
| C-22.FR-3 | partial | revised for L2: the entries of a Bridge Destination; gateway-only Connections are S-145 |
