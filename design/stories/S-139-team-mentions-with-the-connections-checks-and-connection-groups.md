---
id: S-139
title: Team mentions per Connection, with the groups a Connection reads and the group mentions a bridge declares (BE)
capability: C-25
kind: be
layer: L2
depends_on: [S-115, S-119, S-126, S-138]
covers: [C-25.FR-1, C-25.FR-8, C-25.FR-11, C-25.FR-12, C-25.AC-7, C-28.FR-2, C-22.FR-2, C-24.FR-1, C-24.FR-2]
files_touched:
  - api/openapi.yaml
  - api/bridge.yaml
  - internal/db/migrations/0033_team_mentions.up.sql
  - internal/db/migrations/0033_team_mentions.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/teams/mentions.go
  - internal/teams/mentions_test.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/api/teams.go
  - internal/api/teams_test.go
  - internal/connections/groups.go
  - internal/connections/groups_test.go
  - internal/connections/connections.go
  - internal/connections/slack.go
  - internal/connections/query.sql
  - internal/api/connections.go
  - internal/slack/check.go
  - internal/slack/check_test.go
  - internal/slack/client.go
  - internal/slack/client_test.go
  - internal/mattermost/check.go
  - internal/mattermost/check_test.go
  - internal/mattermost/client.go
  - internal/bridge/info.go
  - internal/bridge/info_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/usergroups.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - test/e2e/team_mentions_test.go
  - web/src/test/fixtures/connections.ts
  - web/src/test/fixtures/teams.ts
  - docs/concepts/teams.md
  - docs/messengers/slack.md
  - docs/messengers/mattermost.md
  - docs/install/versions.md
acceptance:
  - "[C-25.FR-8, C-25.AC-7] A Team takes `team_mentions` — one per Connection, each a group's name and, for Slack, the user group's id — saved with `If-Match` and recorded in the diff of `team.updated`; one for a Mattermost Connection is saved; one for a Telegram Connection, for a Slack Connection whose last check found no `usergroups:read`, or for a Bridge Connection whose bridge does not declare group mentions is refused with `422` `group_mentions_unsupported` at `/team_mentions/{i}/connection_id` (`mentions_test.go`, `teams_test.go`)."
  - "[C-25.FR-8] `getTeam` lists under `mention_connections` every Connection that may have a Destination, with its Team mention or none, `available`, and for the unavailable ones the `MentionUnavailableReason` `telegram_no_groups`, `usergroups_read_missing` or `bridge_no_group_mentions`; a Team mention whose Connection later loses the ability stays, reads `available` false with the reason, and mentions nobody (`mentions_test.go`)."
  - "[C-24.FR-1, C-24.FR-2] The Slack Connection check's step `scopes` reports `usergroups:read` as present or missing without failing, `getConnection` of a Slack Connection carries `usergroups_read` from its last successful check, and a check that no longer finds the scope sets it false; the bot needs no other new scope (`check_test.go`)."
  - "[C-25.FR-8] The Mattermost Connection check gains the step `groups` (`ConnectionCheckStep.name`), which reads the groups as F-NNN of S-138 says and passes with a warning when the bot may not read them; `getConnection` of a Mattermost Connection carries `groups_read` (`check_test.go`)."
  - "[C-22.FR-2, C-28.FR-2] `api/bridge.yaml` gives `Capabilities` the optional `group_mentions` (boolean) and `everyone_mentions` (an array of `channel`, `here`, `all`, `everyone`, unique), additively in protocol version 1; the Bridge Connection check shows them with the other features of `info`, and Muster reads them from the stored `info`; `make lint` passes Redocly and oasdiff reports no breaking change (`info_test.go`)."
  - "[C-25.FR-8] `listConnectionGroups` lists the Mattermost groups of a Mattermost Connection and the Slack user groups of a Slack Connection whose last check found `usergroups:read` — `id`, `name`, `display_name` and `member_count` — through the interactive path, `503` with `Retry-After` when no limiter token is free in time, and answers `409` `groups_not_listed` for a Telegram or Bridge Connection and a Slack Connection without the scope (`groups_test.go`)."
  - "[C-25.FR-11] Team mentions use the Team's optimistic locking and are in its Audit log diff; deleting a Team or a Connection deletes their Team mentions in the same transaction (`teams_test.go`, `groups_test.go`)."
  - "[C-25.FR-12, C-24.FR-1] Concepts → Teams has \"Team mentions\" — per messenger, that a Team without one for a Connection is mentioned as nobody and never by its On-call; the Slack manifest of Connect Slack adds `usergroups:read`, Connect Mattermost says what the bot needs to read groups, and the upgrade notes of 0.2.0 say that an installed Slack app is reinstalled to get the scope; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-139. Team mentions per Connection, with the groups a Connection reads and the group mentions a bridge declares (BE)

## Scope

**IN**

- A Team mention per Connection that can mention a group: a Mattermost group by name, a Slack user group chosen from
  those the Connection reads once its check found `usergroups:read`, a group name on a Bridge Connection whose bridge
  declares group mentions; none on Telegram; refused with `group_mentions_unsupported` otherwise (C-25.FR-8, after the
  spike S-138).
- The Connections' checks that find these abilities: the optional scope `usergroups:read` of a Slack Connection
  (C-24.FR-1, FR-2 revised), the groups a Mattermost bot may read, and the group mentions and mentions of everyone in the
  chat that a bridge declares in `info` (C-22.FR-2 revised).
- `connections/{id}/groups` for the Team mention and the group entries of S-140.
- The fake Mattermost server's groups, the fake Slack server's `usergroups.list`, the fake bridge's new `info` fields.

**OUT**

- Mentioning the Team in messages — Team entries, "Team from the label", the rendering of `@group`, `<!subteam^ID>` and
  the bridge's group entries — and the entry "Team {Team}" of a Team's Destinations: S-140.
- "How to mention the Team" on the Team page and the Team mentions on the Teams list: S-143.
- The roles of `info`: S-145. The libraries' types for the new `info` fields: S-141, S-142.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `TeamInput` gains `team_mentions` (`TeamMentionInput`[] {`connection_id`, `group` (the Mattermost group's name, the
    Slack user group's handle, or the bridge's group name; 1 to 64 characters of the form of `mentions` group names),
    `group_id` (the Slack user group's id, `^S[0-9A-Z]+$`, required for Slack and refused for the others)}).
  - `Team` gains `team_mentions` (`TeamMention`[] = the input with `connection` (`EntityRef` and `type`), `available`,
    `unavailable_reason`) and `mention_connections` (`TeamMentionConnection`[] {`connection` (`EntityRef`), `type`,
    `available`, `unavailable_reason` (`MentionUnavailableReason` or null), `team_mention` (the Team's for it, or
    null)}), for every Connection that is not deleted.
  - `MentionUnavailableReason`, one enum for every target a Connection cannot mention, shared by the Team mentions
    here and the Mention entries of S-140 and S-143: `telegram_no_groups` (Telegram has no group mentions and no
    mentions of everyone), `usergroups_read_missing` (a Slack Connection whose last check found no
    `usergroups:read`), `bridge_no_group_mentions` (a bridge that declares no group mentions), `bridge_not_declared`
    (a mention of everyone that the bridge does not declare in `everyone_mentions`), `messenger_has_no_token` (a mention
    of everyone that the messenger lacks: `@everyone` in Mattermost, `@all` in Slack).
  - `listConnectionGroups` (`GET /connections/{connection_id}/groups`, `connections:write` because it calls out with
    the bot token, as `listConnectionChannels`; `q`): `ConnectionGroupList` {`items` ({`id`, `name`,
    `display_name`, `member_count` or null})[], `truncated`}; `409` `groups_not_listed` (`conflict`, a new code) for
    Telegram, Bridge and a Slack Connection without the scope.
  - The Slack Connection read gains `usergroups_read` (boolean or null until checked); the Mattermost Connection read
    gains `groups_read` (boolean or null); `ConnectionCheckStep.name` gains `groups` (the Mattermost check's new step),
    and `ConnectionCheckStep` gains `optional_scopes` ({`scope`, `present`}[]), set on the Slack step `scopes`.
  - Problem code `group_mentions_unsupported` (`validation-failed`, in `errors[].code`).
- **Protocol** (`api/bridge.yaml`, MIT): `Capabilities` gains `group_mentions` ("The bridge renders a group mention
  that Muster sends as a `mentions` entry of the kind `group`; the Team mentions and group entries of a Destination need
  it") and `everyone_mentions` (the mentions of everyone in the chat its messenger has, by their symbolic names); both
  optional, additive in version 1 (ADR-0017). The `kind` of a `mentions` entry is S-140's.
- **Schema** (migration `0033_team_mentions`, expand only; `schema.md` §4.5, §4.15):
  - `team_mentions`: `org_id`, `team_id` (FK), `connection_id` (FK),
    `group_name`, `slack_usergroup_id` (set for a Slack Connection only, which `internal/teams` checks), `created_at`;
    primary key `(team_id, connection_id)`; index `(org_id, connection_id)`.
  - `connections.slack_usergroups_read boolean` and `connections.mattermost_groups_read boolean`, null until a check
    sets them. The down migration drops all of it.
- **Teams** (`internal/teams/mentions.go`): validates each Team mention against the Connection's current abilities —
  Mattermost always, Slack with `slack_usergroups_read`, a Bridge with `group_mentions` in its stored `info`, Telegram
  never — writes them replaced as a whole, in the Team's diff; computes `available` and `mention_connections`;
  `ForConnection(team, connection)` answers the Team mention that delivery uses from S-140. A Slack Team mention is
  saved with the id and handle the picker took from `listConnectionGroups`; saving does not call Slack. Teams and
  Connections are deleted softly, so no foreign key cascades: `teams.Delete` deletes the Team's `team_mentions` in its
  transaction, and the Connection delete in `internal/connections/connections.go` deletes those of the Connection in
  its own, each recorded in the Audit log diff of the Team it changes.
- **Connections** (`internal/connections/groups.go`, `slack.go`, `connections.go`): `listConnectionGroups` on the
  interactive path; the Slack check stores `slack_usergroups_read` from `x-oauth-scopes`; the Mattermost check stores
  `mattermost_groups_read`.
- **Slack** (`internal/slack/check.go`, `client.go`): `usergroups:read` is an optional scope — present or missing,
  never `missing_scope` — and `usergroups.list` (`include_count=true`), with the user groups whose `date_delete` is 0.
- **Mattermost** (`internal/mattermost/check.go`, `client.go`): the step `groups` and the read of the groups, as the
  facts of S-138 say (assumed until then: `GET /api/v4/groups?filter_allow_reference=true&per_page=200`, `403` for a
  bot without the permission, which the step reports as a warning).
- **Bridge** (`internal/bridge/info.go`): `Info` gains `GroupMentions` and `EveryoneMentions`, read from the stored
  `info`; `Features()` names them, so the check shows them.
- **Web test fixtures**: the builders of S-166 (`connections.ts`: `usergroups_read` and `groups_read` null) and of
  S-119 (`teams.ts`: `team_mentions` and `mention_connections` empty) set the neutral values of the new required read
  fields, so the TypeScript check of the web tests stays green after `make generate`.
- **Fake servers**: the fake Mattermost server answers `GET /api/v4/groups` with the groups `payments_team` (alice) and
  `devops_team` (bob), and `PUT /_fake/config` with `groups_forbidden` makes it answer `403`; the fake Slack server
  answers `usergroups.list` with the user group `S0FAKEPAYTEAM` (`payments-team`, with `U0FAKEALICE`) to a token whose
  scopes include `usergroups:read`, and `missing_scope` otherwise, and `/_fake/config` takes `usergroups` by app key; the fake bridge's `/_fake/config` takes
  `capabilities.group_mentions` and `capabilities.everyone_mentions` through the generated types, and its test checks
  them.
- **Settings**: none new; `connection.slack.*` unchanged.
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/teams.md` — "Team mentions"; `docs/messengers/slack.md` — the manifest with
  `usergroups:read`, what it is for, and reinstalling an installed app; `docs/messengers/mattermost.md` — what the bot
  needs to read groups; `docs/install/versions.md` — the Slack scope in the upgrade notes of 0.2.0.

## Steps

1. Add the two fields to `api/bridge.yaml`. Check: `make generate-check` (the models of `internal/bridge/gen/`); `make
   lint`.
2. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
3. Extend the checks of Slack and Mattermost, and `info`; write `listConnectionGroups`; extend the fakes. Check:
   `check_test.go` (both), `client_test.go`, `info_test.go`, `groups_test.go`, the fakes' tests.
4. Write the Team mentions with their validation and availability; add the spec. Check: `make generate-check`;
   `mentions_test.go`, `teams_test.go`.
5. Write `test/e2e/team_mentions_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-115: A, the Team "payments" (TP); the demo Connections of muster dev: Mattermost (CM), Telegram (CT),
# "Example Slack" (CS) and "Example Bridge" (CB).
API=localhost:8080/api/v1; FSL=127.0.0.1:18097/_fake; FB=127.0.0.1:18094/_fake
TM() { E=$(curl -s "${A[@]}" $API/teams/$TP | jq -r .etag)
  curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/teams/$TP -d "$(curl -s "${A[@]}" $API/teams/$TP |
    jq -c "{name, slug, description, own_working_hours, working_hours, matchers, idp_entries, destination_entries} | .team_mentions = $1")"; }
CHECK() { curl -s "${A[@]}" -X POST $API/connections/$1/checks > /dev/null; }

# C-25.AC-7: Mattermost saved; Telegram refused
TM "[{\"connection_id\":\"$CM\",\"group\":\"payments_team\"}]" | jq -c '[.team_mentions[] | {g: .group, available}]'   # [{"g":"payments_team","available":true}]
TM "[{\"connection_id\":\"$CT\",\"group\":\"x\"}]" | jq -c '{status, c: .errors[0].code, p: .errors[0].pointer}'
# {"status":422,"c":"group_mentions_unsupported","p":"/team_mentions/0/connection_id"}

# Slack: refused until the check finds usergroups:read
CHECK $CS; curl -s "${A[@]}" $API/connections/$CS | jq .usergroups_read                                            # false
TM "[{\"connection_id\":\"$CS\",\"group\":\"payments-team\",\"group_id\":\"S0FAKEPAYTEAM\"}]" | jq -r '.errors[0].code'   # group_mentions_unsupported
curl -s -X PUT $FSL/config -d '{"scopes":{"a":"chat:write,channels:read,groups:read,im:write,users:read,users:read.email,usergroups:read"}}' > /dev/null
CHECK $CS; curl -s "${A[@]}" $API/connections/$CS | jq .usergroups_read                                            # true
curl -s "${A[@]}" $API/connections/$CS/groups | jq -c '[.items[] | {id, name}]'                                     # [{"id":"S0FAKEPAYTEAM","name":"payments-team"}]
TM "[{\"connection_id\":\"$CM\",\"group\":\"payments_team\"},{\"connection_id\":\"$CS\",\"group\":\"payments-team\",\"group_id\":\"S0FAKEPAYTEAM\"}]" |
  jq -c '[.team_mentions[].connection.type]'                                                                        # ["mattermost","slack"]

# Mattermost groups; the bridge declares group mentions
curl -s "${A[@]}" $API/connections/$CM/groups | jq -c '[.items[].name]'                                              # ["devops_team","payments_team"]
curl -s "${A[@]}" $API/teams/$TP | jq -c '[.mention_connections[] | select(.connection.id == "'$CB'") | {available, unavailable_reason}]'
# [{"available":false,"unavailable_reason":"bridge_no_group_mentions"}]
curl -s -X PUT $FB/config -d '{"capabilities":{"native_mentions":true,"group_mentions":true,"everyone_mentions":["channel","here"]}}' > /dev/null
CHECK $CB; curl -s "${A[@]}" $API/teams/$TP | jq -c '[.mention_connections[] | select(.connection.id == "'$CB'") | .available]'   # [true]
curl -s "${A[@]}" $API/connections/$CB | jq -c '.info.capabilities | {group_mentions, everyone_mentions}'
# {"group_mentions":true,"everyone_mentions":["channel","here"]}

# a lost ability keeps the Team mention, unavailable
curl -s -X PUT $FSL/config -d '{"scopes":{"a":"chat:write,channels:read,groups:read,im:write,users:read,users:read.email"}}' > /dev/null
CHECK $CS; curl -s "${A[@]}" $API/teams/$TP | jq -c '[.team_mentions[] | select(.connection.type == "slack") | {available, unavailable_reason}]'
# [{"available":false,"unavailable_reason":"usergroups_read_missing"}]
```

`test/e2e/team_mentions_test.go` repeats these checks and adds a Mattermost bot that may not read groups (the step
`groups` warns, `groups_read` false, a typed Team mention is still saved), a deleted Connection whose Team mentions go,
and `listConnectionGroups` of a Telegram Connection answering `409`. The PUTs of `/_fake/config` merge into the fake's
configuration as in S-097; "a" is the key of the demo Slack app.

## Open questions

1. **Corrections after the spike S-138.** The reading of Mattermost groups — the endpoint, the permission and whether a
   Team mention is saved when the bot may not read groups — is assumed here as above. Recommended: S-138 corrects this
   contract before it is implemented, as its acceptance says; if Mattermost groups cannot be read by a bot at all, the
   Team mention stays a typed name with no picker.

## Notes

- Suggested commit: `feat(teams): add team mentions per connection with the groups each connection can read`.
- Migration `0033` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).
- Depends on S-119, which creates the builder of Team reads that this story extends.
- `everyone_mentions` is read here and offered by the Mention entries of S-140; Muster never sends a mention of
  everyone in the chat that the bridge did not declare (ADR-0017).
- A Slack Connection keeps working without `usergroups:read`: only Team mentions and user group entries need it
  (C-24.FR-1 revised), so the step `scopes` never fails on it.
- `api/bridge.yaml` changes here and in S-140; the libraries take both in S-141 and S-142, which follow S-140.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-1 | partial | the Team mentions of a Team |
| C-25.FR-8 | partial | the API; the field of each Connection on the Team page is S-143, Mentions in messages S-140 |
| C-25.FR-11 | partial | Team mentions in the locking and the Audit log |
| C-25.FR-12 | partial | "Team mentions"; S-144 completes the page |
| C-25.AC-7 | full | |
| C-28.FR-2 | partial | what each Connection can mention; the entries are S-140 |
| C-22.FR-2 | partial | revised for L2: group mentions and mentions of everyone in `info`; the roles are S-145 |
| C-24.FR-1 | full | revised for L2: the optional scope `usergroups:read` and the manifest |
| C-24.FR-2 | full | revised for L2: the step `scopes` reports it and the Connection keeps it |
