---
id: S-177
title: Pages by Mention in Telegram, through Bridges and to outgoing webhooks (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-155]
covers: [C-32.FR-8, C-32.FR-9, C-32.FR-11, C-15.FR-2, C-15.FR-11, C-18.FR-4]
files_touched:
  - api/openapi.yaml
  - internal/delivery/where.go
  - internal/delivery/where_test.go
  - internal/delivery/pages.go
  - internal/delivery/pages_test.go
  - internal/delivery/query.sql
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/groups/pages.go
  - internal/groups/pages_test.go
  - internal/groups/query.sql
  - design/db/schema.md
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/webhooks/adapter.go
  - internal/webhooks/adapter_test.go
  - test/e2e/pages_test.go
  - docs/concepts/escalation-chains.md
  - docs/outgoing-webhooks/events.md
acceptance:
  - "[C-32.FR-8, C-32.FR-9, C-18.FR-4] A chain Pages in the Threads of its own Telegram and Bridge Destinations as it does in Mattermost and Slack (S-155): `delivery.PageTargets` no longer leaves them out, and a Telegram reply goes to the discussion group as every Thread reply does (C-14.FR-3) (`where_test.go`, `pages_test.go`)."
  - "[C-32.FR-9, C-32.FR-11] The Telegram adapter writes the Lead of a Page around its Mention line — \"Step 1: <a href=\\\"tg://user?id=…\\\">Ivan</a>, you are needed\" — and the bridge envelope its Mention markers — \"Step 1: [@Ivan](muster-mention:m1), you are needed\" — with the entries of `mentions`; a person linked through a bridge that does not declare `native_mentions` is named by the plain name and the Page reaches nobody there, so it is not delivered to them through that Destination (`layout_test.go`, `envelope_test.go`, `pages_test.go`)."
  - "[C-32.FR-9] A step \"Page the whole Team\" Pages a Telegram Destination never, as Telegram mentions no group and no one for everyone in the chat, and a Bridge Destination only when its bridge declares group mentions or the token in `everyone_mentions` (`where_test.go`)."
  - "[C-15.FR-2, C-15.FR-11] Outgoing webhooks in events mode receive `paged` with its people as `mentions` data (`WebhookMentionTarget`, `user` targets, a Team or the token of a `page_team` step), and `page` gains `chain`, `step` and `importance` (`body_test.go`, `webhookevents_test.go`)."
  - "[C-32.FR-11] An outgoing webhook in the template mode among a chain's Destinations gets the Page through its \"reply in thread\" request when it has one, with the people as data of the event it carries; one without that request gets no Page (`adapter_test.go`, `pages_test.go`)."
  - "[C-32.FR-11] Concepts → Escalation chains has the rows of Telegram, Bridge and outgoing webhooks of the table of C-32.FR-11, and Outgoing webhooks → Events describes `paged` with its `mentions`; `make docs-check` passes."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: 606
---

# S-177. Pages by Mention in Telegram, through Bridges and to outgoing webhooks (BE)

## Scope

**IN**

- Page replies in Telegram Destinations, in the discussion group, with the Mention line inside the text, and through
  Bridges with the Mention markers of C-22.FR-16 (C-32.FR-8, FR-9, FR-11).
- No Mention through a bridge without `native_mentions`: the plain name reaches nobody, so the Page is not delivered to
  that person through that Destination (C-32.FR-11).
- Outgoing webhooks: `paged` in events mode with its people as `mentions` data (C-15.FR-11), and Pages in the "reply in
  thread" request of a template-mode webhook (C-32.FR-11).

**OUT**

- Pages in Mattermost and Slack, the runs of Notification rules, undelivered Pages and the metric: S-155.
- Calls through a Notification gateway: S-157. The read side of Pages: S-178. The push facts: S-160.

## Contracts

- **Spec** (`api/openapi.yaml`): `OutgoingWebhookEvent` carries the people of `paged` in `mentions`
  (`WebhookMentionTarget`[], as `user` targets, or the Team or token of a `page_team` step); `WebhookPage` gains
  `chain`, `step` and `importance` and is carried on `paged` too.
- **Delivery** (`internal/delivery/where.go`, `pages.go`, `webhookevents.go`): `pageTypes` gains Telegram, Bridge and
  the outgoing webhooks with a Thread — template mode with "reply in thread"; an events-mode webhook takes `paged` as an
  event, not a reply. A target's people with an Account link through a bridge whose stored info does not declare
  `native_mentions` — or has none yet, as mentions.Resolve and the reach of S-171 read it — are not `Linked` for that
  Destination (C-22.FR-16). This is decided once, when the Page is queued: the reply renders from its row and the
  Page's people (`paged_user_ids`, and the `no_account_link` the Page recorded), never from the Connection's info at
  send time, so the bridge's first info read racing the Page cannot turn a Named person into "Page not delivered".
  `page_team` checks the bridge's group
  mentions and `everyone_mentions` through `mentions.Can` with the Connection's capabilities, which `ListPageRoots`
  reads with whether an outgoing webhook has a "reply in thread" request (`query.sql`). The events of `paged`
  resolve its people and its Team or token as `mentions` data instead of the Destination's Mention entries.
- **Dispatcher** (`internal/groups/pages.go`, `query.sql`): `groups.PageTarget` gains `Named`, the linked people its
  reply names by the plain name; a person only Named, or only in withheld replies, is not delivered at once
  (`not_delivered`), not `no_account_link`, since they have an Account link. The `page_not_delivered` of a Page by
  Mention carries its importance, which `LockPage` reads for the one recorded once the replies ended, so that `page`
  has it on both events.
- **Telegram** (`internal/telegram/layout.go`) and **Bridge** (`internal/bridge/envelope.go`): the Mention line is
  `messages.Message.MentionLine`, so a Page's Lead stands around it, as S-155 does in Mattermost and Slack.
- **Webhooks** (`internal/webhooks/body.go`, `adapter.go`): the body of `paged`, and the event a "reply in thread"
  request carries for a Page.
- **Documentation**: the remaining rows of the table of C-32.FR-11 in Concepts → Escalation chains; `paged` in
  Outgoing webhooks → Events.

## Steps

1. Let `PageTargets` take Telegram, Bridge and template-mode webhook Destinations, with `native_mentions` and the group
   capabilities of a bridge. Check: `where_test.go`.
2. Write the Lead in the Telegram layout and the bridge envelope. Check: `layout_test.go`, `envelope_test.go`.
3. Add the people of `paged` to events mode and the reply in thread of the template mode. Check: `body_test.go`,
   `adapter_test.go`, `webhookevents_test.go`.
4. Extend `test/e2e/pages_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-155, with the chain devops also on a Route whose Destinations are the demo Telegram channel, the demo Bridge
# Destination of "Example Bridge" and an events-mode outgoing webhook to the fake receiver; ivan linked through both.
FTG=127.0.0.1:18081/_fake; FBR=127.0.0.1:18094/_fake
curl -s $FTG/messages | jq -r '[.[] | select(.text | test("you are needed"))][0].text'
# Step 1: <a href="tg://user?id=…">Ivan</a>, you are needed
curl -s $FBR/messages | jq -c '[.[] | select(.body | test("you are needed"))][0] | {body, mentions}'
# {"body":"Step 1: [@Ivan](muster-mention:m1), you are needed\n…","mentions":[{"ref":"m1","external_id":"u-ivan",…}]}
# (an entry for a person has no kind: only groups and everyone carry one, C-22.FR-16)
# FWH: the recorded requests of the outgoing webhook's receiver (internal/fakes/fakewebhook, as webhook_events_test.go)
curl -s $FWH/requests | jq -c '[.[] | select(.body.event == "paged")][0].body.mentions'
# [{"type":"user","id":"SR…","name":"Ivan","login":"ivan"}]
```

`test/e2e/pages_test.go` repeats these checks and adds a Bridge Destination without `native_mentions` — its reply names
Ivan by the plain name, and the Page records `page_not_delivered` `not_delivered` when no other Destination reached him —
and a template-mode webhook with and without "reply in thread".

## Open questions

None.

## Notes

- Suggested commit: `feat(escalation): page people in telegram, through bridges and to outgoing webhooks`.
- Split from S-155 before its implementation, which with these Destinations would have touched about 80 files; S-155
  leaves Telegram, Bridge and webhook Destinations out of `PageTargets`, so a chain whose Destinations are all of those
  records `page_not_delivered` `no_root_message` until this story.
- In Telegram a Mention reaches only members of the discussion group (F-014, F-129): S-172 warns about the On-calls who
  are not.
- A Bridge Connection's info is stored only by a check of the Connection, a probe of a Notification gateway, or the
  first read of a send through it. A chain starts with the first Publication in any Destination, so a Page can be
  queued before the first send through a bridge that was never checked: its linked people are then Named there and
  counted as not reached through it, though the bridge, if it declares `native_mentions`, renders the marker as a real
  Mention. This race only under-reports; question for the maintainer: read the info when a Bridge
  Connection is saved, so that it is never unknown.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-8 | partial | Telegram, Bridges and outgoing webhooks; Mattermost and Slack are S-155 |
| C-32.FR-9 | partial | Telegram and Bridges; Mattermost and Slack are S-155 |
| C-32.FR-11 | partial | the rows of Telegram, Bridge and outgoing webhooks; Mattermost and Slack S-155, the gateway S-157, the push facts S-160, the page S-161 |
| C-15.FR-2 | partial | revised for L2: the people of `paged` in events mode; the event itself is S-155 |
| C-15.FR-11 | full | revised for L2: the people of `paged` as `mentions` data |
| C-18.FR-4 | partial | revised for L2: in Telegram and through Bridges; Mattermost and Slack are S-155 |
