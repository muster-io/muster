---
id: S-134
title: The personal iCal feed of On-call times with its token (BE)
capability: C-27
kind: be
layer: L2
depends_on: [S-131]
covers: [C-27.FR-10, C-27.FR-14, C-27.AC-7]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0032_ical_feeds.up.sql
  - internal/db/migrations/0032_ical_feeds.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/schedules/feeds.go
  - internal/schedules/feeds_test.go
  - internal/schedules/ical.go
  - internal/schedules/ical_test.go
  - internal/schedules/query.sql
  - internal/schedules/feeds_integration_test.go
  - internal/tokens/tokens.go
  - internal/api/profile.go
  - internal/api/ical.go
  - internal/api/ical_test.go
  - internal/server/server.go
  - internal/server/server_test.go
  - internal/runtime/runtime.go
  - internal/users/admin.go
  - internal/users/admin_integration_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - test/e2e/ical_test.go
  - docs/concepts/schedules.md
acceptance:
  - "[C-27.FR-10, C-27.AC-7] `issueMyIcalFeed` from the web session returns, once, `url` — `MUSTER_PUBLIC_URL/ical/{token}.ics` — whose token starts with the prefix of `ical.token` and carries 32 random bytes; the database holds only its SHA-256 hash; `getMyIcalFeed` says that a feed exists and when it was issued, never the token; a Personal access token gets `403` `session_required` on issuing and revoking, and a Service account `403` `service_account_not_allowed` on all three (`feeds_test.go`, `ical_test.go`)."
  - "[C-27.FR-10, C-27.AC-7] `GET /ical/{token}.ics` on the app listener, without a session, answers `200` `text/calendar` with one `VEVENT` per segment of a Schedule's Result in `ical.window` in which the User is On-call, named \"{Team} · {Schedule}\", adjacent segments of one Schedule joined; with the two Rotation layers of scenario 1, in a week when petr has the lower Rotation layer and olga the upper one, petr's feed shows that week as its nights and weekend only, and an Override of petr shows as an event of its own span; a Rotation layer limited to Friday 19:00–09:00 shows as one event from Friday 19:00 to Saturday 09:00 (`ical_test.go`, `feeds_integration_test.go`)."
  - "[C-27.FR-10, C-27.AC-7] After `issueMyIcalFeed` again the old URL answers `404` and the new one `200`; after `revokeMyIcalFeed` the new one answers `404` too; a token of a disabled User answers `404`, and deleting a User deletes their feed (`feeds_integration_test.go`, `admin_integration_test.go`, `test/e2e/ical_test.go`)."
  - "[C-27.FR-10, C-27.AC-7] No log line, error or metric carries the token: a request for an unknown, revoked or disabled User's token is logged as `ical_feed_not_found` and a failure as `ical_feed_failed`, each with `path` `/ical/[redacted].ics` and, where known, the User's `public_id`; the probe `ical_feed` of lint 5 passes the token through issuing, fetching with a good and a bad token, and revoking without it reaching a log line or an error (`make lint-arch`)."
  - "[C-27.FR-10] Issuing and revoking are Audit log entries `ical_feed.issued` and `ical_feed.revoked` of the User, without the token (`feeds_test.go`)."
  - "[C-27.FR-14] Concepts → Schedules has \"iCal feed\": what it shows, that the link is shown once, that a new link revokes the old one, and that Schedules are not imported from iCal; `make docs-check` passes."
  - "P-82, the token's prefix `mstr_cal_`, 32 random bytes and one feed per User, is confirmed in defaults.md and L2.md §5.3 (Notes); P-81, `ical.window`, stays provisional."
verify: "make ci test-integration lint-arch e2e-go docs-check"
operator_attention: true
issue: null
---

# S-134. The personal iCal feed of On-call times with its token (BE)

## Scope

**IN**

- A personal iCal feed per User: issued from the web session, shown once, revocable, hashed in the database, served
  without a session at `MUSTER_PUBLIC_URL/ical/{token}.ics` and masked wherever its path would be logged (C-27.FR-10,
  L2.md NFR-6).
- The feed's events: the segments of the Result of every Schedule in `ical.window` in which the User is On-call, joined
  per Schedule, Overrides included.
- The feed of a deleted User removed with them.

**OUT**

- The iCal section of the Schedule page: S-136.
- Importing Schedules from iCal: out of scope for L2 (L2.md §4).

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; tag "Profile"):
  - `getMyIcalFeed` (`GET /me/ical-feed`, `authenticated`; session or a User's token; `403`
    `service_account_not_allowed` for a Service account): `IcalFeed` {`exists` (boolean), `issued_at` (or null)}.
  - `issueMyIcalFeed` (`POST /me/ical-feed`, session and CSRF only, as the other operations that change the caller's
    own account): `IcalFeedIssued` {`url` (shown once), `issued_at`}; it replaces the previous token.
  - `revokeMyIcalFeed` (`DELETE /me/ical-feed`, session and CSRF only): `204`, also when none exists.
  - `getIcalCalendar` (`GET /ical/{token}.ics`, tag "Schedules", `security: []`, `x-permission: none`,
    `x-listener: app`, its own `servers` entry `{public_url}` since it is outside `/api/v1`): `200` with
    `text/calendar; charset=utf-8`, `404` (`not-found`) for an unknown, revoked or disabled User's token.
  - `api/README.md`: the iCal path among the paths outside `/api/v1`, and `mstr_cal_` among the token prefixes.
- **Schema** (migration `0032_ical_feeds`, expand only; `schema.md` §4.3 and the list of lookups by hash):
  `ical_feeds`: `org_id`, `user_id` (primary key, FK `users`), `token_hash bytea NOT NULL UNIQUE`, `issued_at`. The
  lookup by hash is `-- archlint:org-exempt` — the token names the Organization — as the other lookups by hash
  `schema.md` names. The down migration drops it.
- **Feeds** (`internal/schedules/feeds.go`): `Issue` makes the token with `tokens.Generate(tokens.PrefixIcal)`
  (`mstr_cal_`, 32 random bytes, P-82) and replaces the User's row in one statement, so issuing again revokes the old
  token; `Revoke` deletes it; both record the Audit log without the token. `Lookup(token)` hashes it and finds an
  active User's feed, else not found.
- **iCal document** (`internal/schedules/ical.go`): RFC 5545 written by hand — no new dependency — with `VCALENDAR`
  (`PRODID:-//Muster//Schedules//EN`, `VERSION:2.0`, `X-WR-CALNAME:Muster On-call`) and one `VEVENT` per joined
  segment: `UID` `{schedule public_id}-{start}@muster`, `DTSTAMP` of the business clock, `DTSTART` and `DTEND` in UTC,
  `SUMMARY` "{Team} · {Schedule}"; lines folded at 75 octets, text escaped. The segments come from the Result (S-131)
  of each Schedule of the Organization from `ical.window` back to its end ahead, keeping those of the User.
- **Server** (`internal/server/server.go`): `IsIcalPath` routes `/ical/` on the app listener to the feed handler
  (`internal/api/ical.go`) before the SPA; the handler logs nothing on success, `ical_feed_not_found` (info, C-27) for a
  `404`, and `ical_feed_failed` (error, C-27) for a failure, both in `internal/logging/events.go`, with `path`
  `/ical/[redacted].ics` — the path with its token masked as `logging.Secret` does (L2.md NFR-6) — and the User's
  `public_id` where it is known; the real path never reaches a log line.
- **Users** (`internal/users/admin.go`): deleting a User deletes their `ical_feeds` row in the same transaction;
  disabling keeps it, and the lookup refuses it.
- **Lint 5** (`internal/archlint/secretleak.go`): the probe `ical_feed`, as the Heartbeat's path token has one.
- **Shared tables**: defaults.md marks `ical.token` decided (P-82); L2.md §5.3 moves P-82 to "Decided since they were
  first listed".
- **Settings**: `ical.window` (P-81), `ical.token` (P-82), `MUSTER_PUBLIC_URL`.
- **Documentation**: `docs/concepts/schedules.md` — "iCal feed".

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the feeds, the iCal document and the operations; add the spec. Check: `make generate-check`; `feeds_test.go`,
   `ical_test.go` covers folding, escaping, joined segments, an Override and the window.
3. Route the path, add the log event, the probe and the deletion with the User. Check: `server_test.go`,
   `admin_integration_test.go`, `make lint-arch`.
4. Confirm P-82, write the documentation and `test/e2e/ical_test.go`. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-131: A and the Schedule "Main" (SC) of "payments" with the two Rotation layers from MON; petr's session
# headers HP (cookie and X-CSRF-Token, as in S-051) and his token P.
API=localhost:8080/api/v1
F=$(curl -s "${HP[@]}" -X POST $API/me/ical-feed); URL=$(jq -r .url <<<"$F"); echo "$URL" | grep -c '/ical/mstr_cal_'   # 1
curl -s "${P[@]}" -o /dev/null -w '%{http_code}\n' -X POST $API/me/ical-feed                                       # 403
curl -s "${HP[@]}" $API/me/ical-feed | jq -c '{exists}'                                                           # {"exists":true}
psql "$MUSTER_DATABASE_URL" -Atc "SELECT length(token_hash), position('mstr_cal_' in encode(token_hash, 'escape')) FROM ical_feeds"   # 32|0

# C-27.AC-7: week 1 — lower petr, upper olga: petr's events of that week are its nights and the weekend
curl -s "$URL" -D - -o feed.ics | grep -i '^content-type'                                                          # Content-Type: text/calendar; charset=utf-8
W1=$(date -u -d "$MON" +%Y%m%d); W2=$(date -u -d "$MON +7 days" +%Y%m%d)
grep '^DTSTART:' feed.ics | cut -d: -f2 | awk -v a=$W1 -v b=$W2 '$0 >= a && $0 < b' | wc -l                        # 6
grep -m1 '^SUMMARY' feed.ics                                                                                        # SUMMARY:payments · Main

# the old link stops working after a new one, the new one after revoking
URL2=$(curl -s "${HP[@]}" -X POST $API/me/ical-feed | jq -r .url)
curl -s -o /dev/null -w '%{http_code} ' "$URL"; curl -s -o /dev/null -w '%{http_code}\n' "$URL2"                  # 404 200
curl -s "${HP[@]}" -X DELETE $API/me/ical-feed; curl -s -o /dev/null -w '%{http_code}\n' "$URL2"                  # 404
grep -c 'mstr_cal_' dev.log                                                                                         # 0
grep -c '/ical/\[redacted\]\.ics' dev.log                                                                           # 2 or more (the 404s above)
```

Petr's six events of week 1 are Monday 10:00–11:00, the four nights from 19:00 to 11:00 and Friday 19:00 to Monday
10:00, Berlin time: the lower Rotation layer's week without the upper one's five spans 11:00–19:00.
`test/e2e/ical_test.go` repeats this with exact times and adds an Override of petr, a disabled User's link answering
`404`, and a deleted User's feed gone.

## Open questions

None.

## Notes

- Suggested commit: `feat(schedules): add a personal ical feed of on-call times`.
- Migration `0032` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).
- The maintainer confirms one provisional value with this story (operator attention): P-82, the prefix `mstr_cal_`, 32
  random bytes, stored as a hash, shown once, one per User. P-81, the window of 7 days back and 60 ahead, is confirmed
  by the test environment and stays provisional.
- The token is a credential in a URL that calendar applications fetch without a session, so it is treated like the
  Heartbeat's path token: hashed, never logged, and refused for a disabled User.
- The feed is the User's across every Schedule of every Team, not only their Teams' main Schedules (C-27.FR-10).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-10 | partial | the API and the feed; the section of the page is S-136 |
| C-27.FR-14 | partial | "iCal feed" |
| C-27.AC-7 | full | |
