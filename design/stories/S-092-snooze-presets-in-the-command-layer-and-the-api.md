---
id: S-092
title: Snooze presets in the command layer and the API, with the start of the working day and of the week (BE)
capability: C-10
kind: be
layer: L1
depends_on: [S-063, S-055]
covers: [C-10.FR-17, C-10.FR-18, C-10.AC-17, C-10.AC-21, C-10.AC-22, C-20.FR-9, C-20.AC-7]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0015_snooze_presets.up.sql
  - internal/db/migrations/0015_snooze_presets.down.sql
  - design/db/schema.md
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/defaults.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/groups/snoozepreset.go
  - internal/groups/snoozepreset_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/groups/bulk.go
  - internal/groups/bulk_test.go
  - internal/groups/query.sql
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - test/e2e/snooze_presets_test.go
  - docs/concepts/statuses-and-commands.md
acceptance:
  - "[C-10.FR-17, C-10.AC-21] The end of each Snooze preset follows every row of the table of C-10.AC-21 and its two America/New_York cases — a start the clocks skip moves forward by the change, one that happens twice is the first — on a manual clock (`snoozepreset_test.go`, which adds Europe/Berlin across both clock changes of 2026 and a zone with a 45-minute offset)."
  - "[C-10.FR-17, C-10.FR-18, C-10.AC-22] At 02:00 UTC on the development clock, `snoozeAlertGroup` with `{\"preset\":\"tomorrow\",\"time_zone\":\"UTC\"}` sets `snooze_until` to 09:00 UTC the same day; the newest Timeline entry is `snoozed` with that `snooze_until`, and the Audit log entry `alert_group.snoozed` has the details `preset` `tomorrow`, `time_zone` `UTC` and the `until`."
  - "[C-10.FR-17, C-10.AC-22] Without `time_zone`, bob, whose profile time zone is Asia/Tokyo, snoozes through his Personal access token until 09:00 Tokyo time the next day, and a Service account until 09:00 in the Organization's time zone, UTC."
  - "[C-10.FR-18, C-10.AC-22] `listSnoozePresets` answers, in the order `tomorrow`, `week_start`, the ends a Snooze then sets, for the request's `time_zone` or the zone C-10.FR-17 picks for the caller, which it names in `time_zone`; it needs `alert-groups:snooze` (`403` for a Viewer) and answers `422` `invalid_format` at `/query/time_zone` for `Mars/Base`."
  - "[C-10.FR-18, C-10.AC-17, C-10.AC-22] A Snooze with `preset` and `until` gets `422` `one_of_required`, one with `until` and `time_zone` `422` `unsupported` at `/time_zone`, one with `time_zone` `Mars/Base` `422` `invalid_format` at `/time_zone`, and one with none of `until`, `no_end` and `preset` still `422` `one_of_required`; an unknown `preset` is refused by the request validation with `400`."
  - "[C-10.FR-18, C-10.AC-22] A bulk Snooze of two Alert Groups with `preset` `week_start` computes the end once: both outcomes are `done`, and both Alert Groups have the same `snooze_until`."
  - "[C-10.FR-17, C-10.AC-22] A later change of `workday_start` leaves the `snooze_until` of a Snooze already set; once the development clock passes the end of a preset Snooze whose Alerts still fire, the Alert Group is firing without an Owner, and its newest Timeline entry is `snooze_ended`, Loud, mentioning `snooze_ended`."
  - "[C-20.FR-9, C-20.AC-7] `getOrganization` carries `workday_start` `09:00` and `week_start` `monday`, on a new database and on the existing Organization after the migration; an update with `workday_start` `24:00` or `9:00`, or with `week_start` `weekend`, is refused with `invalid_format` at the field and changes nothing; `08:30` with `sunday` is accepted and recorded as `organization.updated` with both fields in its diff, and the next `listSnoozePresets` ends `week_start` at 08:30 on the next Sunday; an update that omits both keeps them."
  - "The documentation page Concepts → Statuses and commands describes the Snooze presets — the rule with its examples, the time zone they use, the two Organization settings and the API's `preset` and `snooze-presets` — and `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 295
---

# S-092. Snooze presets in the command layer and the API, with the start of the working day and of the week (BE)

## Scope

**IN**

- The two Organization settings `organization.workday_start` and `organization.week_start` (C-20.FR-9): the
  migration, the start-up defaults, the read, the update with its validation and the Audit log diff.
- The rule of the Snooze presets `tomorrow` and `week_start` (C-10.FR-17), computed in one place in `internal/groups`
  on the business clock, with the time zone of the request, else of the caller's profile, else of the Organization.
- `preset` and `time_zone` in `SnoozeRequest`, for one Alert Group and for a bulk Snooze, which computes the end once;
  the Audit log details of a preset Snooze.
- The read `listSnoozePresets` (`GET /api/v1/snooze-presets`), which the UI needs to show the end of each preset.
- The documentation of the presets in Concepts → Statuses and commands.

**OUT**

- The presets on the buttons of Mattermost, Telegram and Bridges and in Text commands, with their labels: S-093.
- The presets in the Snooze dialog and the bulk Snooze, the two settings on the General page and the hint in the
  profile: S-094.
- A working-week calendar, holidays, or presets of their own per Route: not in L1 (C-10.FR-17).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated):
  - `SnoozeRequest` gains `preset` (`SnoozePreset`: `tomorrow`, `week_start`) and `time_zone` (string, an IANA time
    zone, only with `preset`). Its description becomes: exactly one of `until`, `no_end` and `preset`; none or more
    than one is `422` `one_of_required`, as today; `time_zone` without `preset` is `422` `unsupported` at `/time_zone`;
    a time zone Muster does not know (or `Local`) is `422` `invalid_format` at `/time_zone`; without `time_zone` the
    end is computed in the caller's profile time zone, else the Organization's (C-10.FR-17). A new example `preset`.
    `BulkCommandRequest.snooze` is the same schema, so its pointers start with `/snooze`; `runBulkCommand` gets an
    example `snooze_preset`. The description of the problem code `one_of_required` names `preset` beside `until` and
    `no_end`.
  - `listSnoozePresets`, `GET /snooze-presets`, tag Alert groups, `x-permission: alert-groups:snooze`,
    `x-listener: app`, session or token. Query `time_zone` (optional). `200` `SnoozePresetList`: `time_zone` (the zone
    used), `workday_start`, `week_start` and `items` — `SnoozePresetEnd` {`preset`, `until`}, in the order
    `tomorrow`, `week_start`; `401`, `403`, `422` (`invalid_format` at `/query/time_zone`), `429`. It reads and
    changes nothing else.
  - `OrganizationBase` gains `workday_start` (string, `pattern: '^([01][0-9]|2[0-3]):[0-5][0-9]$'`, "the start of
    the working day, `organization.workday_start`") and `week_start` (`Weekday`: `monday` … `sunday`,
    `organization.week_start`), both optional there, so that an update that omits one keeps it; `Organization` lists
    both in its `required`, so every read carries them. A value outside the pattern or the enumeration is refused by
    the request validation with `400` `invalid_format` at `/workday_start` or `/week_start`.
- **Schema** (migration `0015_snooze_presets`, expand only, the next free number when the story is implemented;
  `schema.md`, the `organizations` diagram and description): `organizations.workday_start text NOT NULL DEFAULT
  '09:00'` with `CHECK (workday_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$')`, and `organizations.week_start text NOT
  NULL DEFAULT 'monday'` with `CHECK (week_start IN ('monday', 'tuesday', 'wednesday', 'thursday', 'friday',
  'saturday', 'sunday'))`. The defaults give the existing Organization its values; the start-up step of S-007 sets both
  explicitly for a new one from `organization.Defaults`. The down migration drops both columns.
- **Settings**: `organization.workday_start` and `organization.week_start` (provisional, P-58),
  `organization.time_zone`.
- **The rule** (`internal/groups/snoozepreset.go`): a pure function of the preset, `now`, the `*time.Location`, the
  start and the first day of the week, as C-10.FR-17 says: from the date of `now` in the zone, the first wall-clock
  start strictly after `now` — on any day for `tomorrow`, on the first day of the week for `week_start`. A start that a
  clock change skips takes the offset in force before the change, which moves it forward by the change; one that
  happens twice takes the earlier instant. `time.Date` does not promise either side in these cases, so the function
  decides them itself and its tests pin them. Time zones come from the tzdata embedded in the binary (`time/tzdata`,
  imported by `internal/users`).
- **Command layer** (`internal/groups`): `SnoozeEnd` gains `Preset` and `TimeZone`. Its check: exactly one of `Until`,
  `NoEnd` and `Preset`; `TimeZone` only with `Preset`; a `TimeZone` that `organization.ValidTimeZone` refuses. Before
  the dispatcher locks the Alert Group, the command resolves a preset to `Until`: the zone is the request's, else the
  `time_zone` of the User the caller acts as (a new query in `query.sql`, filtered by `org_id`), else the
  Organization's; the start and the first day of the week come with the Organization's time zone in one read
  (`GetListSettings` gains both columns); now is the business clock. The Audit log details of `alert_group.snoozed`
  become `{"no_end", "until", "preset", "time_zone"}`, the last two only for a preset. A bulk Snooze resolves the end
  once before its loop, so every Alert Group gets the same `until`. `Service.SnoozePresets(ctx, caller, timeZone)`
  answers `listSnoozePresets` with the same resolution. Presses and Text commands call `Snooze` with a `Preset` and no
  `TimeZone` from S-093 on.
- **Organization** (`internal/organization`, `internal/api/organization.go`): `Settings` and `Input` gain both fields;
  the update keeps an omitted one, records a change in the diff of `organization.updated` and announces the
  `organization` hint, as for every field.
- **Timeline, lifecycle events, metrics, log events**: unchanged. A preset Snooze is the `snoozed` event of C-10.FR-15
  with its `snooze_until`, counted as `muster_commands_total{command="snooze"}`; its end is the timer `snooze_end`.

## Steps

1. Write the migration and update `schema.md`. Check: `make test-integration` runs the migrations up, down and up on
   PostgreSQL 14 and 17; on the development database, `psql "$MUSTER_DATABASE_URL" -Atc "SELECT workday_start,
   week_start FROM organizations"` prints `09:00|monday`.
2. Add both settings to the Organization's read, update, validation and defaults, and to the spec. Check:
   `make generate-check`; `organization_test.go` covers the read, an accepted update, each refusal, the diff and an
   update that omits both.
3. Write the rule. Check: `snoozepreset_test.go` passes the table of C-10.AC-21, its America/New_York cases, both clock
   changes of 2026 in Europe/Berlin, Asia/Kathmandu and each first day of the week.
4. Resolve presets in the command layer and in bulk Snooze, and record the Audit log details. Check:
   `commands_test.go` and `bulk_test.go` cover every refusal of the check, the three sources of the time zone, the
   details and the single end of a bulk Snooze.
5. Add `preset`, `time_zone` and `listSnoozePresets` to the spec and the handlers. Check: `make generate-check`;
   `internal/api` tests cover the mapping, the problem pointers and the Permission.
6. Write `test/e2e/snooze_presets_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-063: the Admin's Personal access token A, bob's session HB and token B, the Service account token SAT in S, a
# Viewer's token in V, the Integration "cmd" with PUTA, NOTIFY, AG and GET, and ADV. After the first AT, the checks use
# Personal access tokens only: an advance of the development clock may end sessions.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
ORG() { O=$(curl -s "${A[@]}" $API/organization); curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c "del(.id, .etag) | $1" <<<"$O")"; }
NOW() { curl -s localhost:8082/_dev/clock | jq -r '.now | sub("\\.[0-9]+"; "") | fromdateiso8601'; }
AT() { ADV $(( ($1 - $(NOW) % 86400 + 86400) % 86400 )); }                    # on to the next $1 s after 00:00 UTC
T() { jq -rn --argjson s $(( $(NOW) / 86400 * 86400 + $1 )) '$s | todate'; }  # today 00:00 UTC plus $1 s
WHEN() { jq -r '.items[] | "\(.preset) \(.until | fromdate | strftime("%A %H:%M"))"'; }

# C-20.FR-9, C-20.AC-7: the two settings
psql "$MUSTER_DATABASE_URL" -Atc "SELECT workday_start, week_start FROM organizations"     # 09:00|monday
curl -s "${A[@]}" $API/organization | jq -c '{workday_start, week_start}'                  # {"workday_start":"09:00","week_start":"monday"}
ORG '.workday_start = "24:00"' | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'
# {"status":400,"p":"/workday_start","c":"invalid_format"}
ORG '.workday_start = "9:00"' | jq -r '.errors[0].code'                                    # invalid_format
ORG '.week_start = "weekend"' | jq -c '{p: .errors[0].pointer, c: .errors[0].code}'        # {"p":"/week_start","c":"invalid_format"}
ORG '.workday_start = "08:30" | .week_start = "sunday"' | jq -c '{workday_start, week_start}'
# {"workday_start":"08:30","week_start":"sunday"}
curl -s "${A[@]}" "$API/audit-log?action=organization.updated&limit=1" | jq -c '[.items[0].diff[].pointer] | sort'
# ["/week_start","/workday_start"]
ORG 'del(.workday_start, .week_start)' | jq -c '{workday_start, week_start}'               # {"workday_start":"08:30","week_start":"sunday"}
curl -s "${A[@]}" "$API/snooze-presets?time_zone=UTC" | WHEN
# tomorrow <the day of the next 08:30 UTC> 08:30
# week_start Sunday 08:30
ORG '.workday_start = "09:00" | .week_start = "monday"' > /dev/null

# four firing Alert Groups; bob's profile time zone
curl -s "${HB[@]}" -X PUT $API/me -d '{"name":"bob","time_zone":"Asia/Tokyo"}' | jq -r .time_zone   # Asia/Tokyo
curl -s -X PUT $FAM/groups/s1 -d '{"receiver":"cmd","route":"{}","labels":{"alertname":"Nightly"}}' > /dev/null
for c in a b c d; do PUTA s1 $c "{\"labels\":{\"team\":\"ops\",\"cluster\":\"$c\"}}"; done
NOTIFY s1 '{"reason":"first notification"}'
GA=$(AG 'cluster%3D%22a%22'); GB=$(AG 'cluster%3D%22b%22'); GC=$(AG 'cluster%3D%22c%22'); GD=$(AG 'cluster%3D%22d%22')
AT 7200                                                                                    # 02:00 UTC

# C-10.AC-22: the request's time zone, the Timeline and the Audit log
curl -s "${A[@]}" -X POST $API/alert-groups/$GA/snooze -d '{"preset":"tomorrow","time_zone":"UTC"}' | jq -r .alert_group.snooze_until; T 32400
# both lines: today 09:00:00Z
curl -s "${A[@]}" "$API/alert-groups/$GA/timeline?limit=1" | jq -c '.items[0] | {event, s: (.snooze_until == "'"$(T 32400)"'")}'
# {"event":"snoozed","s":true}
curl -s "${A[@]}" "$API/audit-log?action=alert_group.snoozed&limit=1" | jq -c '.items[0].details | {preset, time_zone, no_end}'
# {"preset":"tomorrow","time_zone":"UTC","no_end":false}

# the caller's profile time zone, else the Organization's
curl -s "${B[@]}" -X POST $API/alert-groups/$GB/snooze -d '{"preset":"tomorrow"}' | jq -r .alert_group.snooze_until; T 86400
# both lines: tomorrow 00:00:00Z, which is 09:00 in Tokyo
curl -s "${S[@]}" -X POST $API/alert-groups/$GC/snooze -d '{"preset":"tomorrow"}' | jq -r .alert_group.snooze_until; T 32400
# both lines: today 09:00:00Z
curl -s "${B[@]}" $API/snooze-presets | jq -c '{time_zone, t: (.items[0].until == "'"$(T 86400)"'")}'   # {"time_zone":"Asia/Tokyo","t":true}

# a bulk Snooze computes the end once; the week starts tomorrow (UTC) for this check
ORG ".week_start = \"$(jq -rn --argjson s $(( $(NOW) + 86400 )) '$s | strftime("%A") | ascii_downcase')\"" > /dev/null
curl -s "${A[@]}" $API/alert-groups/bulk-commands \
  -d "{\"command\":\"snooze\",\"alert_group_ids\":[\"$GA\",\"$GD\"],\"snooze\":{\"preset\":\"week_start\",\"time_zone\":\"UTC\"}}" | jq -c '[.results[].outcome]'
# ["done","done"]
GET $GA | jq -r .snooze_until; GET $GD | jq -r .snooze_until; T 118800                     # three lines: tomorrow 09:00:00Z

# C-10.AC-17, C-10.AC-22: refusals
SN() { curl -s "${A[@]}" -X POST $API/alert-groups/$GD/snooze -d "$1" | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'; }
SN "{\"preset\":\"tomorrow\",\"until\":\"$(T 90000)\"}"          # {"status":422,"p":…,"c":"one_of_required"}
SN "{\"until\":\"$(T 90000)\",\"time_zone\":\"UTC\"}"            # {"status":422,"p":"/time_zone","c":"unsupported"}
SN '{"preset":"tomorrow","time_zone":"Mars/Base"}'              # {"status":422,"p":"/time_zone","c":"invalid_format"}
SN '{}'                                                         # {"status":422,"p":…,"c":"one_of_required"}
SN '{"preset":"someday"}' | jq -r .status                       # 400
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" $API/snooze-presets                    # 403
curl -s "${A[@]}" "$API/snooze-presets?time_zone=Mars/Base" | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'
# {"status":422,"p":"/query/time_zone","c":"invalid_format"}

# a change of the settings never moves a Snooze already set; the Snooze ends like any other
ORG '.workday_start = "10:00" | .week_start = "monday"' > /dev/null
GET $GC | jq -r .snooze_until; T 32400                                                     # both lines: today 09:00:00Z
AT 32460                                                                                   # 09:01 UTC
GET $GC | jq -c '{status, owner}'                                                          # {"status":"firing","owner":null}
curl -s "${A[@]}" "$API/alert-groups/$GC/timeline?limit=1" | jq -c '.items[0] | {event, loudness, mentions}'
# {"event":"snooze_ended","loudness":"loud","mentions":["snooze_ended"]}
curl -s "${A[@]}" "$API/snooze-presets?time_zone=UTC" | jq -r '.items[0].until'; T 122400 # both lines: tomorrow 10:00:00Z
ORG '.workday_start = "09:00"' > /dev/null
```

`test/e2e/snooze_presets_test.go` repeats these checks, moving the development clock to 08:59 and to 09:00 sharp as
well (the same day's start, then the next day's), and runs the settings refusals through `muster dev`'s API.

## Open questions

None.

## Notes

- Suggested commit: `feat(commands): snooze until the next working morning or the start of the week`.
- The rule (C-10.FR-17) was decided with the maintainer's two presets (D379): "the first start of the working day
  after now" is one sentence with no extra setting, and it gives what a person woken at 02:00 means by "tomorrow" —
  the morning about to come, seven hours away, not 31. The price is that at 08:59 the preset ends a minute later: it
  means "until the working day starts", which the dialog of S-094 shows as the end before anyone chooses it. The
  defaults and this rule are provisional (P-58) until the team has used them on the test environment.
- `week_start` is the weekend preset: there is no working-week calendar in L1, so `tomorrow` on Friday evening ends on
  Saturday morning.
- The time zone of a person: Muster knows only the profile's (C-03.FR-12), which is empty while it follows the browser;
  the UI therefore always sends `time_zone` (S-094), and messengers fall back to the Organization's (S-093). Mattermost
  knows a user's time zone, but Telegram and the Bridge protocol do not, so one rule for all three uses the profile.
- The presets are computed on the business clock, so the development clock moves them, and a Snooze from the UI at 02:00
  on a laptop whose development clock was advanced uses that clock, unlike the quick durations of S-033, which the
  browser counts from its own clock.
- Added after all stories of L1 had merged, when the maintainer took two items of a comparison with another on-call
  tool into L1 (D379); the inspiration is not named in public documents.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-17 | full | the rule, the time zone and the stored instant |
| C-10.FR-18 | partial | the API, the bulk Snooze, `snooze-presets` and the Audit log; the buttons and Text commands are S-093, the dialog S-094 |
| C-10.AC-17 | partial | the third choice `preset`; the rest was proved by S-032 |
| C-10.AC-21 | full | |
| C-10.AC-22 | full | |
| C-20.FR-9 | partial | the settings in the API; the General page is S-094 |
| C-20.AC-7 | full | |
