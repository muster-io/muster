---
id: S-165
title: Undelivered Mentions in the Timeline and the warnings of a Destination (BE)
capability: C-28
kind: be
layer: L2
depends_on: [S-140]
covers: [C-28.FR-3, C-28.AC-3, C-28.AC-4, C-11.FR-21, C-12.FR-12, C-22.FR-16]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0035_mention_delivery_events.up.sql
  - internal/db/migrations/0035_mention_delivery_events.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/delivery/events.go
  - internal/delivery/mentions.go
  - internal/delivery/mentions_test.go
  - internal/delivery/worker.go
  - internal/delivery/query.sql
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/destinations/warnings.go
  - internal/destinations/warnings_test.go
  - internal/destinations/query.sql
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - test/e2e/mention_warnings_test.go
  - web/src/test/fixtures/destinations.ts
  - docs/concepts/mentions.md
acceptance:
  - "[C-28.FR-3, C-28.AC-4, C-11.FR-21, C-12.FR-12] When the On-call whom an entry names has no Account link in the Destination's Identity space, nothing is posted in the Thread for them, and the Alert Group's Timeline has one delivery event `mention_not_delivered` per person, entry and message, with the person, the entry, the Loud event and the reason `no_account_link` (`mentions_test.go`, `mention_warnings_test.go`)."
  - "[C-28.FR-3, C-28.AC-3, C-22.FR-16] Through a bridge without `native_mentions`, a linked person is sent by the plain name and the Timeline records `mention_not_delivered` with the reason `bridge_cannot_mention_people` (`mentions_test.go`)."
  - "[C-28.FR-3, C-28.AC-4] `getDestination` carries `mention_warnings` — each person and entry whose latest such delivery event is not followed by a delivered Mention of that person through that entry, with the messenger, the reason and since when — computed from the delivery events, not stored; after the person links an account, the next Loud message that mentions them through that entry records `mention_delivered` and the warning is gone (`warnings_test.go`, `mention_warnings_test.go`)."
  - "[C-11.FR-21] `mention_delivered` is recorded only for a person and entry with an open warning on that Destination, is not merged into the Timeline, and both kinds are kept for `retention.alert_details` like every delivery event (`mentions_test.go`, `alertgroups_test.go`)."
  - "[C-28.FR-3] Concepts → Whom a message mentions has \"Undelivered Mentions\": nothing in the Thread, the Timeline entry and the warning on the Destination page, and how it clears; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-165. Undelivered Mentions in the Timeline and the warnings of a Destination (BE)

## Scope

**IN**

- The delivery event "Mention not delivered" for each person an entry names who has no Account link in the
  Destination's Identity space, or whom a bridge without `native_mentions` can only name by the plain name, in the
  Timeline of the Alert Group (C-28.FR-3, C-11.FR-21 revised).
- The Destination's warnings about each person and entry whose latest such event is not followed by a delivered
  Mention, computed from the delivery events (C-28.FR-3).

**OUT**

- The entries and their resolution: S-140. The warnings on the Destination page and the event in the Timeline of the
  web UI: S-143.
- "Page not delivered", which Escalation steps write in the Thread: S-155.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `DeliveryEventKind` gains `mention_not_delivered`; `mention_delivered` is a kind of the database only, never listed
    by the Timeline.
  - `TimelineDeliveryEntry` gains `mention` ({`user` (`UserRef`), `entry` ({`position`, `target`} as on the
    Destination), `event` (`MentionEvent`), `reason` — `no_account_link`, `bridge_cannot_mention_people`}) for
    `mention_not_delivered`.
  - The Destination reads gain `mention_warnings` (`MentionWarning`[] {`user`, `entry`, `messenger` (`ConnectionType`),
    `reason`, `since`}).
- **Schema** (migration `0035_mention_delivery_events`, expand only; `schema.md` §4.11):
  `delivery_events_kind_v3_check` adds `mention_not_delivered` and `mention_delivered`, `NOT VALID` on the partitioned
  table, validated in a second transaction, `delivery_events_kind_v2_check` dropped in a third, as `0010` does; the
  `detail` of these events holds the person's id, the entry's id and position, the Loud event and the reason. An index
  `(org_id, destination_id, kind, occurred_at DESC) WHERE kind IN ('mention_not_delivered', 'mention_delivered')` backs
  the warnings. The down migration restores `_v2`, after deleting the rows of the new kinds.
- **Mentions** (`internal/mentions/mentions.go`): `Resolve` also returns, for each person an entry named and the
  message left out — no Account link — or named by the plain name — a bridge without `native_mentions` — an
  `Undelivered` with the entry and the reason, and for each person it mentioned the entry.
- **Delivery** (`internal/delivery/mentions.go`, `events.go`, `worker.go`): after a Loud message is delivered, the
  worker records one `mention_not_delivered` per `Undelivered`, and one `mention_delivered` for each mentioned person
  and entry that has an open warning on the Destination; a message that ends Not delivered records neither. Nothing is
  posted for them (ADR-0005, journal D567).
- **Groups** (`internal/groups/read.go`): the Timeline merges `mention_not_delivered` with its `mention` fields, and
  leaves `mention_delivered` out.
- **Destinations** (`internal/destinations/warnings.go`): `mention_warnings`, the latest event per person and entry of
  the Destination's delivery events still kept, those whose latest is `mention_not_delivered`.
- **Web test fixtures** (`web/src/test/fixtures/destinations.ts`, S-166): `mention_warnings` empty in the builders of
  Destination reads.
- **Settings**: `retention.alert_details`.
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/mentions.md` — "Undelivered Mentions".

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `_v3` accepts every kind
   of `_v2` and the two new ones.
2. Return the undelivered Mentions from the resolution and record the events in delivery. Check: `mentions_test.go`
   (both packages).
3. Merge the event into the Timeline and compute the warnings; add the spec. Check: `make generate-check`;
   `warnings_test.go`, `alertgroups_test.go`, `destinations_test.go`.
4. Write `test/e2e/mention_warnings_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-140: A, NOTIFY, PUTA, AG; payments-notice (PN) of the Team "payments", whose main Schedule has bob On-call now,
# without a Mattermost Account link; bob's session headers HB; FMM.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
E=$(curl -s "${A[@]}" $API/destinations/$PN | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/destinations/$PN -d "$(curl -s "${A[@]}" $API/destinations/$PN |
  jq -c 'del(.id, .etag, .mentions, .health, .routes, .teams, .created_at, .mention_warnings) | .mention_entries = [{"target":{"kind":"on_call_of_teams_from_label"}}]')" > /dev/null

# C-28.AC-4: not mentioned, nothing in the Thread, the Timeline and the warning
curl -s -X PUT $FAM/groups/w1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"QueueBacklog"}}' > /dev/null
PUTA w1 a '{"labels":{"category":"infra","team":"payments","severity":"warning"}}'; NOTIFY w1 '{"reason":"first notification"}'; sleep 1
G=$(AG 'alertname%3D%22QueueBacklog%22')
curl -s $FMM/posts | jq '[.[] | select(.channel_id == "ch-payments-notice" and .root_id != "")] | length'          # 0
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=20" |
  jq -c '[.items[] | select(.delivery_event == "mention_not_delivered") | {u: .mention.user.login, k: .mention.entry.target.kind, e: .mention.event, r: .mention.reason}]'
# [{"u":"bob","k":"on_call_of_teams_from_label","e":"new_alert_group","r":"no_account_link"}]
curl -s "${A[@]}" $API/destinations/$PN | jq -c '[.mention_warnings[] | {u: .user.login, messenger, reason}]'
# [{"u":"bob","messenger":"mattermost","reason":"no_account_link"}]

# bob links his account as in S-069; the next Loud message mentions him and the warning is gone
LINKMM $CM $FMM; PUTA w1 b '{"labels":{"category":"infra","team":"payments","severity":"warning"}}'; NOTIFY w1 '{"reason":"new alerts"}'; sleep 2
curl -s "${A[@]}" $API/destinations/$PN | jq '.mention_warnings | length'                                          # 0
```

`test/e2e/mention_warnings_test.go` repeats this and adds a Bridge Destination on a fake bridge without
`native_mentions`, where a linked person is sent by the plain name and the reason is `bridge_cannot_mention_people`,
and a Root message that ends Not delivered, which records no event.

## Open questions

None.

## Notes

- Suggested commit: `feat(delivery): record undelivered mentions and warn about them on the destination`.
- Migration `0035` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).
- Split from S-140 at the contract boundary between whom a message mentions and what is recorded when it cannot: S-140
  would have exceeded the size of a story with it. It takes the next free ID, S-165, and keeps its place after S-140 in
  the story map.
- `mention_delivered` exists only so that "is not followed by a delivered Mention" (C-28.FR-3) can be computed from
  delivery events; it is written only to close a warning, so it adds no row for the many Mentions that never failed.
- The warning is computed, never stored, so it clears by itself when retention removes the events behind it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-3 | partial | the Timeline and the warnings in the API; the page is S-143 |
| C-28.AC-3 | partial | the record for a bridge without `native_mentions` |
| C-28.AC-4 | full | |
| C-11.FR-21 | full | revised for L2: "Mention not delivered" |
| C-12.FR-12 | partial | revised for L2: the record and the warning of an unlinked person; the Assignees are S-168 |
| C-22.FR-16 | partial | revised for L2: the record of a linked person named by the plain name |
