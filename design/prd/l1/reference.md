# L1 cross-cutting reference

[L1 index](../L1.md)

Tables that several capabilities share. A capability's requirements point here instead of repeating a table; the
capability named in each row is the one that implements it. Values of the form `area.setting` are defaults from
[defaults.md](defaults.md). The capabilities of L2 (`C-25` onwards, under [`l2/`](../l2/)) add their rows here too:
where a table of L1 is checked against the code, such as the Roles and Permissions and the metrics catalogue, their
rows stand in a table of their own until the story that implements them moves them into it.

Contents: [Loud and Quiet](#loud-and-quiet) · [Lifecycle events added in L2](#lifecycle-events-added-in-l2) ·
[Transports](#transports) · [Buttons and links by status](#buttons-and-links-by-status) ·
[Banners, warnings and notices](#banners-warnings-and-notices) ·
[Documentation links](#documentation-links) · [Roles and Permissions](#roles-and-permissions) ·
[Internal alerts](#internal-alerts) · [Metrics catalogue](#metrics-catalogue) · [Chart rules](#chart-rules) ·
[Metrics added in L2](#metrics-added-in-l2)

## Loud and Quiet

A Loud event is always a new message; an edit never rings anyone (ADR-0005; checked in both messengers,
[F-012](../../facts.md#notifications) and [F-029](../../facts.md#edits-and-notifications)). Loudness is fixed in L1.
Every lifecycle event — its name, loudness, Mentions, Timeline kind and how it appears in a messenger — is a row of the
lifecycle event tables of the capabilities that record them:
[C-09.FR-22](C-09-alert-group-lifecycle.md#lifecycle-events), [C-10.FR-15](C-10-commands.md#functional-requirements) and
[C-17.FR-11](C-17-timers.md#functional-requirements). Delivery turns lifecycle events into messages (C-11.FR-20) and
outgoing webhook events (C-15.FR-2).

Mentions are symbolic: `owner`, `previous_owner`, or the name of a Destination setting of C-12.FR-8 (`new_alert_group`,
`new_alerts`, `reopen`, `ack_timeout`, `snooze_ended`, `rise_to_urgent`), which each Destination turns into real
Mentions. A Reopen into acknowledged mentions only the Owner, whatever the Destination's settings.
_Revised for L2 (2026-10-26):_ from L2 these names are the Loud events of a Destination's Mention entries
(C-28.FR-1), which add `storm_started`, and `paged` names the people that an Escalation step Pages, listed in a field
of its Timeline entry (C-32.FR-9).

Delivery adds the rows below for one Destination (C-11). They are not lifecycle events: those about one Alert Group are
recorded in its Timeline with the kind `delivery`, and none is sent as an outgoing webhook event.

| Delivery event | Loud or Quiet | How it appears | Mentions |
|---|---|---|---|
| Publication into a newly added Destination — added to a Route, from L2 also to a Team, by "Also to the Teams from the label" turned on, by a changed Severity condition (C-26.FR-6) or by a saved Team (C-25.FR-13) | Quiet | new Root message | — |
| Storm summary, first publication | Loud | new message | `new_alert_group`; from L2 `storm_started` (C-28.FR-7) |
| Storm summary updates and final state | Quiet | message update | — |
| Gradual publication after a Storm | Quiet | new Root messages | — |
| Delivered late (resolved while its first Publication waited for a `RetryAfter` or for `Transient` retries within the budget) | Quiet | new Root message with a note | — |
| Publication after a Broken Destination recovers | Loud if the Alert Group is firing, otherwise Quiet | new Root message | `new_alert_group` when Loud |
| Root message update after a Broken Destination recovers | Quiet | one edit to the current state | — |
| Thread replies that came due while a Destination was Broken, from L2 Pages among them (C-32.FR-10) | not sent | — | — |
| Mention not delivered: a Mention entry names a person without an Account link in the Destination's Identity space (C-28.FR-3), from L2 | not sent | nothing in the Thread; the delivery event in the Alert Group's Timeline and a warning on the Destination | — |
| Alert Group that resolved while its Destination was Broken, never published there | not published | Timeline and UI only | — |
| Republication after a deleted Root message | Quiet | new Root message with a note; Thread starts over | — |
| Final edit after the Destination left the Route | Quiet | one edit: "No longer updated here"; for a Bridge Root message past its edit window, a Thread reply with the same text (C-22.FR-9); a Bridge Publication whose answer was lost before the Destination left is not withheld but posted again with its key, carrying the final text (C-22.FR-6): a replayed message is edited to it, otherwise the new message is the final one and a possible duplicate is recorded | — |
| Republication past a bridge's edit window, on a change of status (C-22.FR-9) | Quiet | new Root message; a Thread reply under the old one links to it; the Thread continues under the new one | — |

Telegram makes a Quiet new message with `disable_notification`, which silences the push but does not remove it
(C-14.FR-6). In Telegram a Thread reply, Loud or Quiet and with or without a Mention, reaches only members of the
channel's discussion group, and the automatic copy of a Root message rings them even when the Root message is Quiet
(C-14.FR-9). In Mattermost a Thread reply notifies the Thread's followers by their own settings, so Quiet means "without
a Mention" (C-13.FR-8); a Mention also makes the person a follower of the Thread
([F-027](../../facts.md#threads-and-direct-messages)). A Bridge posts a Quiet new message without a notification
where its bridge declares `quiet_messages`, and otherwise as an ordinary message without Mentions (C-22.FR-6). Slack
cannot send a message without a notification: whom a new message notifies follows each member's settings for the
channel and the Thread, so Quiet means "without a Mention" there too (C-24.FR-6).

## Lifecycle events added in L2

The lifecycle events that the capabilities of L2 record, with the columns and meaning of the table of C-09.FR-22, and
the capability that records each. Outgoing webhooks in events mode receive them one to one (C-15.FR-2), except
`escalation_webhook`, which goes only to the outgoing webhook Destination its step names (C-32.FR-13). The events of L1
keep their rows; L2 adds causes to some of them: `unclaimed` is also recorded once every Escalation chain started on an
Alert Group has run out (C-32.FR-7); an Acknowledge through a Notification gateway records `acknowledged` with the
Transport `gateway` (C-30.FR-6); and a raised Minimum severity of a Route records the Quiet `severity_raised` or
`urgency_raised` with its reason (C-26.FR-9). The Timeline kind `escalation` is new and joins the filter of the Timeline
(C-09.FR-14). Lifecycle events of the later part of L2 — Maintenance windows, Attach and Detach — are added with it.

| Event | When | Loud or Quiet | Mentions | Timeline kind | In a messenger (C-11) | Capability |
|---|---|---|---|---|---|---|
| `team_joined` | a Team joins the Teams of the Alert Group (C-25.FR-6) | Quiet | — | `alerts` | Root message update | C-25 |
| `unknown_team_value` | an Alert's Team label names no Team, once per value (C-25.FR-7) | Quiet | — | `system` | nothing | C-25 |
| `destination_joined` | the Alert Group reaches a Destination after its first Publication elsewhere, by its Alerts and not by a change of configuration, while it is firing (C-26.FR-5) | Loud | `new_alert_group` | `system` | new Root message in that Destination | C-26 |
| `destination_joined` | the same, while it is acknowledged or snoozed | Quiet | — | `system` | new Root message in that Destination | C-26 |
| `paged` | an Escalation step, or an action of a Notification rule, Pages people, a Team or everyone in the chat by Mention (C-32.FR-9) | Loud | `paged` (the people), or the Team mention or token of the step | `escalation` | Thread reply in the Threads of the chain (C-32.FR-8) | C-32 |
| `paged` | the same by a call through a Notification gateway (C-30.FR-4) | Quiet | — | `escalation` | nothing; the gateway calls | C-30, C-32 |
| `page_not_delivered` | a Page reached its person nowhere (C-32.FR-10), or a call ended Not delivered (C-30.FR-7) | Quiet | — | `escalation` | nothing; the Thread reply of the Page already names the person where it could | C-30, C-32 |
| `escalation_step_skipped` | a step is skipped, with its reason (C-32.FR-14) | Quiet | — | `escalation` | nothing | C-32 |
| `assignees_changed` | the Assignees change (C-32.FR-12) | Quiet | — | `escalation` | Root message update | C-32 |
| `escalation_webhook` | a step sends to an outgoing webhook (C-32.FR-13) | Quiet | — | `escalation` | nothing; sent only to the outgoing webhook Destination of the step | C-32 |

## Transports

The way a Command reaches Muster, recorded with every Command in the Timeline and the Audit log (C-10.FR-3) and counted
in `muster_commands_total{transport}`.

| Transport | Commands from | Since |
|---|---|---|
| `ui` | the web UI | L1 |
| `api` | the API, with a Personal access token or a Service account token | L1 |
| `mattermost`, `telegram`, `slack`, `bridge` | presses and, through Bridges, Text commands | L1 |
| `system` | Muster itself: system transitions, from L2 also the steps of Escalation chains | L1 |
| `gateway` | an acknowledgement reported by a Notification gateway, only while nobody owns the Alert Group (C-30.FR-6) | L2, 0.2.0 |
| `mcp` | the MCP server, with a Personal access token (ADR-0016) | L2, specified later |

The Audit log also records `cli` for the CLI commands that take `--actor` (C-03.FR-14); it is not a Transport of
Commands.

## Buttons and links by status

| Root message status | Buttons |
|---|---|
| firing | Ack · Resolve · Snooze (the Route's durations, `route.snooze_durations`, then the Snooze presets "Until tomorrow 09:00" and "Until Monday 09:00", C-10.FR-18) |
| acknowledged | Unack · Resolve · Snooze (the durations, then the presets) |
| snoozed | Ack · Unsnooze · Resolve |
| resolved | none; a link "Open in Muster" |

Other messages: a Reminder carries "Still on it" and "Unack"; a Storm summary and ack timeout notices carry only links;
a test message carries the buttons of its status, answered with "This is a test message; nothing was changed". The UI
and the API offer every command allowed by the status, including Unresolve, which messengers never offer. Snooze
durations appear as separate buttons or a menu, as the messenger allows. The Snooze presets follow them only on a Route
that has Snooze durations; their labels name the Organization's start of the working day and first day of the week in
the Route's language — "Until tomorrow {start}" and "Until {day} {start}", «До завтра {start}» and «До {day} {start}»
with the day in the genitive («До понедельника 09:00») — and a Telegram keyboard puts them on a row of their own, under
the row of the durations. Mattermost and Telegram show Ack and "Still on it" as primary buttons and Resolve as a success
button, and the other buttons plain, without a style ([F-066](../../facts.md#telegram),
[F-068](../../facts.md#mattermost)). Slack has only the styles `primary` and `danger` and asks for one `primary` per set
([F-074](../../facts.md#sending-block-kit-and-limits)): Ack is `primary` on a firing and on a snoozed Root message,
Resolve on an acknowledged one and "Still on it" on a Reminder, every other button is plain, and the Snooze durations
and presets are the options of one menu "Snooze…" (C-24.FR-5). In Mattermost and Slack every link, "Open in Muster"
included, is a link in the text, never a button (C-13.FR-3, C-24.FR-4). A Bridge shows these buttons only when its
bridge declares `buttons`, with the style hints Ack and "Still on it" `primary`, Resolve `success` and the rest
`neutral`, and drops Snooze buttons beyond the bridge's `max_per_message` — the presets first, then the durations from
the longest; without buttons, a Root message that is not resolved names the Text commands of its status in a line "Reply
in the Thread: …" ("Reply in the Thread, mentioning the bot: …" when the bridge forwards only those) when the bridge
forwards Thread messages, and otherwise offers only the link to Muster (C-22.FR-5, C-22.FR-14).

_Revised for L2 (2026-10-26):_ a Destination's button set (C-28.FR-8) — the Organization's, which shows all of them by
default, or its own — chooses which of these buttons its Root messages show: Acknowledge with Unacknowledge, Resolve,
each Snooze duration and each Snooze preset; Unsnooze is not part of the set. The buttons of Reminders, the Text
commands of Bridges, the UI and the API keep every action.

## Banners, warnings and notices

| Where | Shown while | Text (English) | Capability |
|---|---|---|---|
| UI, every page | recovering after downtime | "Muster is recovering after downtime. Data may be incomplete until HH:MM." | C-02, C-03 |
| UI, Admins | no replica is leading | "No replica is leading: Telegram and Bridge polling, Slack's Socket Mode, Heartbeat and Stale checks are paused." | C-02, C-03, C-24 |
| OIDC settings | mapping empty and no Role for unmatched users (`nobody_can_sign_in`) | "Nobody will be able to sign in through OIDC." | C-03 |
| OIDC settings | no sign-in has carried the groups claim, and the latest check found it not advertised (`groups_claim_missing`) | "No sign-in has carried the groups claim ‘{claim}’ yet, so group mappings have not matched anyone yet." | C-03 |
| OIDC settings | the client secret expires within `oidc.secret_expiry_lead` | "The client secret expires on {date}. Issue a new one in the identity provider and enter it here." | C-03 |
| OIDC settings | Role sync would lower the last active Admin | "{user} stays Admin: they are the last active Admin, and the identity provider maps them to {role}. Make another user Admin first." | C-03 |
| Token list | token without expiry | "This token never expires." | C-04 |
| Integration | a `groupKey` is truncated | "Alertmanager truncates Snapshots for N groups. Set `max_alerts: 0` on the Alertmanager receiver." | C-06 |
| Integration | a learned repeat interval exceeds `processing.long_repeat_warning` | "Alertmanager route {route} repeats every {interval}; Muster can resolve its alerts by absence only after {3 × interval}. Use a repeat interval of 5–15 minutes on the route to Muster." | C-06 |
| Integration | Heartbeat not configured | "No Heartbeat: Muster will not notice when Alertmanager goes quiet, and Stale resolution is off." | C-07 |
| Integration | waiting for the first Heartbeat | "Waiting for the first Heartbeat signal. Stale resolution is off until it arrives." | C-07 |
| Integration | Heartbeat lost | "No contact with Alertmanager since HH:MM. Nothing is resolved as Stale until contact returns." | C-07 |
| Alert Group page | a Replacement happened | "Alerts were replaced because `<label>` changed. Consider removing Instance labels from the rule, for example `without(pod, instance)`." | C-09 |
| Alert Group page | its details were removed by retention | "Alerts and Timeline of this Alert Group were removed after {period}; only its summary and Notes are kept." | C-09 |
| Root message and Alert Group page | resolved by a person with Alerts still firing | "⚠ N alerts still firing in Alertmanager" | C-09, C-12 |
| Root message and Alert Group page | Reopen count > 0 | "🔁 Reopened ×N" | C-09, C-12 |
| Snooze dialog | no end chosen | "This Alert Group stays snoozed until someone unsnoozes it." | C-10 |
| Snooze dialog, messenger buttons | the Snooze presets (C-10.FR-18) | "Until tomorrow {start}" · "Until {day} {start}"; in the dialog each with "Ends {date and time}" | C-10 |
| Profile, time zone | always (hint) | "Snooze presets pressed in messengers end in this time zone. While it follows the browser, they use the Organization's time zone, {zone}." | C-10 |
| Header, every page | the state of the live updates (C-09.FR-26) | "Connecting…" · "Live" · "Reconnecting…" · "Offline"; pressed: "Live updates are on: lists and pages change as soon as something happens." · "Connecting to live updates. What you see may be out of date." · "No live updates since {time}. What you see may be out of date; it is read again as soon as the connection is back." with "Reload page" | C-09 |
| Destination and its Routes | Broken | "Broken since HH:MM: {reason}. Muster tries again every {interval}." — the reason is the error, or "unavailable after repeated failures: {last error}" | C-11, C-13 |
| Alert Group page, delivery state | as applicable | "Not delivered: {error}" · "Waiting: {Destination} is Broken" · "Deleted in the messenger" · "Thread not attached to the Root message" · "Possible duplicate" · "Not posted: resolved during a Storm or while the Destination was Broken" · "No longer updated here" | C-11, C-13 |
| Root message | delivered late | "Delivered late: started HH:MM, resolved HH:MM while this Destination was unavailable." | C-11, C-12 |
| Root message | republished after deletion | "The previous message was deleted at HH:MM." | C-11, C-12 |
| Root message | Destination removed from the Route | "No longer updated here; current state in Muster: {link}" | C-11, C-12 |
| Route | Storm active | "Storm since HH:MM: N new Alert Groups. Destinations receive a Storm summary." | C-11 |
| Route or Destination | a template keeps failing | "Template error since HH:MM: {error}." plus "Messages use the fallback template." or "Requests are not sent." | C-12, C-15 |
| Mattermost Connection | always (hint) | "Mattermost calls {callback address} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it." | C-13 |
| Telegram Connection, base URL field | always (hint) | "A self-hosted Bot API server keeps the bot on that server only. Do not call api.telegram.org with this token from anywhere else: even getMe moves the bot back to Telegram's cloud, and presses and comments start to go missing without an error." | C-14 |
| Telegram Connection | the base URL uses `http` | "The bot token travels in clear text over http. Use https unless this path is a private network or a tunnel." | C-14 |
| Telegram Destination check | the channel has no linked discussion group | "Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group." | C-14 |
| Bridge Connection | push mode (hint) | "The bridge posts events to {callback address}, signed with the push secret. Configure the same address and secret in the bridge." | C-22 |
| Bridge Connection check | the token is refused · the protocol version is unknown · the answer does not match the protocol · the event delivery mode is not offered | "The bridge refused the token." · "The bridge speaks protocol version {version}; this Muster speaks 1." · "The bridge's answer does not match the protocol: {detail}" · "The bridge does not offer {mode} event delivery." | C-22 |
| Slack Connection | Socket Mode (hint) | "The Leader holds the Socket Mode connection, so Slack needs no address of Muster. Presses made while the Leader changes are lost; use HTTP if that matters." | C-24 |
| Slack Connection | HTTP mode (hint) | "Slack sends button presses to {callback address}. Enter it as the Interactivity Request URL of the Slack app, and the app's Slack signing secret here. A press on a test message checks both." | C-24 |
| Slack Connection and its check | the last `hello` of Socket Mode counted another connection of the app | "Another program is connected to this Slack app through Socket Mode: presses are shared between them at random. Use one Slack app for each Connection and share it with no other Connection or installation." | C-24 |
| Slack Connection check | the bot token is refused · it is not a bot token · scopes are missing · the tokens belong to two apps | "Slack refused the token." · "This is not a bot token." · "The bot token lacks the scopes {scopes}." · "The app-level token belongs to another Slack app than the bot token." | C-24 |
| Slack Destination check | the bot is not a member of the channel | "Invite the bot to the channel: /invite @{bot}" | C-24 |
| Destination test of a Slack Destination | always | "Slack does not let a bot press its own button, so this test does not check presses. Press a button on the test message: once the press reaches Muster, it answers that nothing was changed." | C-24 |
| Answer to a press in Slack | a press from an account without an Account link | "Your Slack account is not linked to Muster. Link it in your profile: {link}" | C-24 |
| Profile, link Slack | no account has the email or name · several accounts have the name | "No Slack account with this email or name was found." · "Several Slack accounts have this name. Enter the email instead." | C-24 |
| Thread reply | a Bridge Root message was republished past the edit window | "This message is no longer updated. The current one: {link}" | C-22 |
| Alert Group page, delivery state | a Bridge Root message is past its edit window | "Past the edit window: updated on the next change of status" | C-22 |
| Bridge answer to a person | a Text command ran · a Snooze duration is not one of the Route's | "Done: {command} #N" · "Snooze for one of: {durations}", the Route's durations and the words of the Snooze presets | C-22 |
| Bridge answer to a person | a Snooze Text command on an Alert Group whose Route has no Snooze durations | "This Alert Group's Route has no Snooze durations; nothing was changed." | C-22 |
| Bridge answer to a person | a press or Text command from an account without an Account link | "Your account is not linked to Muster. Link it in your profile: {link}" | C-22 |
| Bridge answer to a person, answer to a press in Slack | a press whose button data fails verification or belongs to another message | "This button could not be verified; nothing was changed." | C-22, C-24 |
| Answer to a press in Mattermost, Telegram or Slack, Bridge answer to a person | a press, or a Text command through a Bridge, could not run: its Command failed, or, in Mattermost, Telegram and Slack, Muster could not read the Account link or, in Telegram, the pressed message | "Muster could not run this command; nothing was changed." | C-13, C-14, C-22, C-24 |
| Profile, link through a Bridge | the bridge refuses the direct message with the code · no account has the handle | "The bot cannot send you a direct message. Send it the token instead." · "No account with this {kind} was found." | C-22 |
| Bridge answer to a person | an Account link token was linked · was unknown, used, expired or of another Connection | "Linked to Muster user {name} ({login})" · "This link is invalid or expired. Start again from your Muster profile." | C-22 |
| Telegram Destination check | the bot is not an admin of the discussion group | "The bot is not an admin of the discussion group {group}. Make the bot an admin there, allowed to post messages." | C-14 |
| Outgoing webhook Destination | previous Signing secret not retired | "The previous secret still signs, since {date}." | C-15 |
| Outgoing webhook Destination | a literal credential in a header or the URL | "Store credentials as Secrets: this value is shown to everyone who can read Destinations." | C-15 |
| Root message and Alert Group page | Unclaimed | "⚠ Nobody has taken this" | C-17 |
| Thread reply | its Owner was disabled or deleted (C-03.FR-13) | "The owner was disabled — this Alert Group has no owner now." · "The owner was deleted — this Alert Group has no owner now." | C-03, C-09 |
| Telegram bot, private chat | a `/start` whose token is unknown, used, expired or of another Connection | "This link is invalid or expired. Start again from your Muster profile." | C-18 |
| System status | outgoing heartbeat not set | "Set up an outgoing heartbeat so that a stopped Muster is noticed." | C-19 |
| Severity levels | unmapped values seen | "These values have no mapping and count as warning: {values}." | C-20 |
| Keyring | an older key is no longer needed | "Key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS." | C-20 |
| Keyring | activation refused | "Replicas {names} do not hold key {id} yet." | C-20 |
| Teams page | values of the Team label that name no Team | "Unknown values of {team label}: Alerts arrived with these values, but there is no such Team: no channels of a Team are added and nobody is Paged." | C-25 |
| Team page, Team mention · Destination form, an unavailable Mention target | the Connection cannot mention a group | "Needs the scope usergroups:read" · "The bridge declares no group mentions" · "Telegram cannot mention everyone or groups" | C-25, C-28 |
| Route form | "Also to the Teams from the label" on and the Team label not in the Group key | "Alerts of different Teams with the same key share one Alert Group, shown in the channels of all of them. Add {team label} to the key if each Team needs its own." | C-26 |
| Route form | it names an Escalation chain and no Destination, and does not deliver to Teams | "Only a Notification gateway can reach anyone from this Escalation chain: the Route has no Destination." | C-32 |
| Destination page; Alert Group page, delivery event | a Mention entry names a person without an Account link | "Mention not delivered: @{login}, {entry}, has no linked {messenger} account" | C-28 |
| Thread reply | a Page of a person without an Account link | "Page not delivered: @{login} has no linked {messenger} account" | C-32 |
| Thread reply | a Page by Mention | "Step {n}: {Mentions}, you are needed" and "Chain of the Route {Route} · On-call of {Team} {Schedule} · normal" (or "· important, again in {N} min") | C-32 |
| Root message | not acknowledged while Escalation chains Page | "Assignees: {person} ({Team}), …" | C-29, C-32 |
| Root message | acknowledged while Escalation chains ran | "Acknowledged by {user} ({Team}) · Escalation chains stopped" | C-29, C-32 |
| Schedule page, "On call now" | warnings | "Gap: {from} – {to}" · "Disabled: @{login} in {Rotation layer}" · "@{login} is a Viewer" · "@{login} is not in the Team" · "Unreachable in {messenger}: no linked account." with "A call through the Notification gateway works." where it does | C-27 |
| Schedule page, Overrides | always (hint) | "Any Responder of the Team; no messages." | C-27 |
| Profile, Escalation steps, Notification rules | no Notification gateway exists | "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)" | C-30, C-31, C-32 |
| Answer to a person in a call | an acknowledgement of an Alert Group someone owns · a resolved one | "Already taken by {name}" · "Already resolved" | C-30 |
| Bridge Connection in the role `gateway` | Broken | "Broken since HH:MM: {reason}. Muster tries again every {interval}." | C-30 |

## Documentation links

Where the web UI links to the documentation site (C-21.FR-11). Each link is an entry of the one list of the web UI
(C-21.FR-12), named here by its key; the target is a page of `docs/` and, after `#`, the anchor of its section, under
`https://muster-io.github.io/muster/{version}/`. A page link takes the page's URL, the source path without `.md` and
with a trailing `/` (`concepts/alert-groups/`). The texts are "Guide" next to a page title, "Learn more" next to a
field and "How to fix" next to a problem — «Руководство», «Подробнее» and «Как исправить» in Russian — each with an
accessible name that adds what it is about ("Guide: Routes") and says that it opens in a new tab. A section that a
story adds is named by the heading that story gives it; a story that renames a heading updates its row. Links
appear only in an active, full session (C-21.FR-11): the sign-in and password setup pages, the TOTP prompt and TOTP
enrolment of a limited session, the page about a missing Permission, and empty states have none.

### Next to page titles

| Page | Key | Target | Story |
|---|---|---|---|
| Alert Groups | `page.alert_groups` | `concepts/alert-groups.md` | S-108 |
| Alert Group | `page.alert_group` | `concepts/statuses-and-commands.md` | S-108 |
| Integrations, New Integration, an Integration and its edit form | `page.integrations` | `integrations/alertmanager.md` | S-108 |
| Stored Snapshot | `page.stored_snapshot` | `concepts/snapshot-semantics.md` | S-108 |
| Routes, New Route, a Route | `page.routes` | `concepts/routes-and-group-keys.md` | S-108 |
| Connections, New Connection | `page.connections` | `messengers/index.md` | S-108 |
| A Mattermost, Telegram, Slack or Bridge Connection | `page.connection.<type>` | `messengers/<type>.md` | S-108 |
| Destinations, New Destination | `page.destinations` | `messengers/index.md#what-each-destination-type-can-do` | S-108 |
| A Mattermost, Telegram or Bridge Destination | `page.destination.<type>` | `messengers/<type>.md#add-a-destination` | S-108 |
| A Slack Destination | `page.destination.slack` | `messengers/slack.md#invite-the-bot-and-create-destinations` | S-108 |
| An outgoing webhook Destination, events mode · template mode or `both` | `page.destination.webhook_events` · `page.destination.webhook_template` | `outgoing-webhooks/events.md` · `outgoing-webhooks/templates.md` | S-108 |
| Statistics | `page.statistics` | `concepts/alert-groups.md#statistics` | S-110 |
| Profile | `page.profile` | `sign-in/index.md#your-profile` | S-110 |
| Access → Users, a User | `page.users` | `sign-in/index.md#add-a-user` | S-110 |
| Access → Service accounts, a Service account | `page.service_accounts` | `configure/api-tokens.md#create-a-service-account` | S-110 |
| Access → OIDC | `page.oidc` | `sign-in/oidc-keycloak.md#4-configure-muster` | S-110 |
| Organization → General · Severity levels · Alert handling · Security · Outbound addresses · Retention | `page.organization.<page>` | `configure/organization.md#general` · `#severity-levels` · `#alert-handling` · `#security` · `#outbound-addresses` · `#retention` | S-110 |
| Organization → Keyring | `page.organization.keyring` | `operations/key-rotation.md` | S-110 |
| Organization → Outgoing heartbeat | `page.organization.outgoing_heartbeat` | `operations/outgoing-heartbeat.md` | S-110 |
| Organization → Lookup tables, a Lookup table · Link rules, a Link rule | `page.lookup_tables` · `page.link_rules` | `configure/messages-and-templates.md#add-a-lookup-table` · `#add-a-link-rule` | S-110 |
| Organization → System status | `page.system_status` | `operations/monitoring.md#system-status` | S-110 |
| Organization → Audit log | `page.audit_log` | `configure/organization.md#audit-log` | S-110 |

### Next to fields

| Form, field | Key | Target | Story |
|---|---|---|---|
| Route, Matchers | `field.matchers` | `concepts/routes-and-group-keys.md#matchers` | S-109 |
| Route, Group key and its preview | `field.group_key` | `concepts/routes-and-group-keys.md#group-keys` | S-109 |
| Route, Route profile · Urgent | `field.route_profile` · `field.urgent` | `concepts/routes-and-group-keys.md#route-profiles` · `#urgent` | S-109 |
| Route, message template | `field.template` | `configure/messages-and-templates.md#the-template-language` | S-109 |
| Destination, Mention settings | `field.mentions` | `configure/messages-and-templates.md#whom-a-message-mentions` | S-109 |
| Route, delivery (Loud and Quiet) | `field.loud_quiet` | `concepts/loud-and-quiet.md` | S-109 |
| Route, ack timeout and Reminders | `field.timers` | `concepts/statuses-and-commands.md#ack-timeouts-reminders-and-auto-unacknowledge` | S-109 |
| Route, Snooze durations | `field.snooze` | `concepts/statuses-and-commands.md#snooze-presets` | S-109 |
| Route, lifecycle | `field.lifecycle` | `concepts/alert-groups.md#reopening` | S-109 |
| Integration, Static labels | `field.static_labels` | `integrations/alertmanager.md#static-labels` | S-109 |
| Integration token, the snippet | `field.integration_snippet` | `integrations/alertmanager.md#create-a-token-and-paste-the-snippet` | S-109 |
| Integration, Heartbeat | `field.heartbeat` | `integrations/heartbeat.md#turn-it-on` | S-109 |
| Connection, OIDC, outgoing webhook Destination and Organization → Outgoing heartbeat, proxy | `field.proxy` | `messengers/proxies.md#where-you-set-a-proxy` | S-109 |
| Mattermost Connection, the callback address | `field.mattermost_callback` | `messengers/mattermost.md#let-button-presses-reach-muster` | S-109 |
| Telegram Connection, long polling or webhook · Bot API base URL | `field.telegram_mode` · `field.telegram_base_url` | `messengers/telegram.md#long-polling-or-webhook` · `messengers/telegram-restricted-networks.md` | S-109 |
| Slack Connection, event mode · tokens · the hint "Create the Slack app from the manifest first, then install it and copy its tokens." above the token fields | `field.slack_event_mode` · `field.slack_tokens` · `field.slack_manifest` | `messengers/slack.md#socket-mode-or-http` · `#install-the-app-and-create-the-tokens` · `#create-the-app-from-a-manifest` | S-109 |
| Bridge Connection, event delivery | `field.bridge_events` | `messengers/bridge.md#create-the-connection` | S-109 |
| Outgoing webhook Destination, templates · extraction rules · Secrets · Signing secret | `field.webhook_templates` · `field.webhook_extraction` · `field.webhook_secrets` · `field.webhook_signing_secret` | `outgoing-webhooks/templates.md#what-the-templates-read` · `outgoing-webhooks/templates.md#extraction-rules-and-response` · `outgoing-webhooks/events.md#secrets` · `outgoing-webhooks/events.md#verify-the-signature` | S-109 |
| OIDC, group mapping | `field.oidc_groups` | `sign-in/oidc-keycloak.md#2-add-the-groups-claim` | S-110 |
| Security, TOTP policy | `field.totp_policy` | `sign-in/totp.md#the-policy` | S-110 |
| A Service account, its Role (which gives it its Permissions) | `field.permissions` | `configure/api-tokens.md#two-kinds-of-token` | S-110 |
| Link rule, its template · Lookup table, its rows | `field.link_rule` · `field.lookup_table` | `configure/messages-and-templates.md#links` | S-110 |
| Outbound addresses, the allowed networks | `field.outbound_policy` | `messengers/proxies.md#what-the-outbound-address-policy-checks` | S-110 |

### Next to problems

| Where, problem | Key | Target | Story |
|---|---|---|---|
| Connection and Destination checks, `token_invalid` | `fix.token.<type>` | `messengers/mattermost.md#create-the-bot-account` · `messengers/telegram.md#create-the-bot` · `messengers/slack.md#install-the-app-and-create-the-tokens` · `messengers/bridge.md#create-the-connection` | S-109 |
| Connection and Destination checks, `dns_failed`, `connect_failed`, `tls_failed`, `timeout`, `proxy_refused` | `fix.network` | `messengers/proxies.md#where-you-set-a-proxy` | S-109 |
| Connection and Destination checks, `blocked` | `fix.blocked` | `messengers/proxies.md#what-the-outbound-address-policy-checks` | S-109 |
| Telegram Connection check, `not_bot_api`, `wrong_path_prefix`, `unexpected_status`; the base URL uses `http` | `fix.telegram_base_url` | `messengers/telegram-restricted-networks.md` | S-109 |
| Mattermost Connection check, `not_mattermost`, `wrong_path_prefix`, `unexpected_status` | `fix.mattermost_server` | `messengers/mattermost.md#create-the-connection` | S-109 |
| Slack Connection check, `wrong_token_type`, `app_mismatch` | `fix.slack_tokens` | `messengers/slack.md#install-the-app-and-create-the-tokens` | S-109 |
| Slack Connection check, `not_slack`, `unexpected_status` | `fix.slack_server` | `messengers/slack.md#the-proxy` | S-109 |
| Slack Connection and Destination checks, `missing_scope` | `fix.slack_scopes` | `messengers/slack.md#the-scopes` | S-109 |
| Bridge Connection check, `not_bridge`, `wrong_path_prefix`, `unexpected_status` | `fix.bridge_server` | `messengers/bridge.md#check-before-saving` | S-109 |
| Bridge Connection check, `protocol_version_unknown`, `protocol_mismatch`, `event_delivery_not_offered` | `fix.bridge_check` | `messengers/bridge.md#what-the-check-shows` | S-109 |
| Mattermost Connection check, warning `press_answers_in_thread` | `fix.press_answers_in_thread` | `messengers/mattermost.md#where-answers-to-presses-show` | S-109 |
| Slack Connection page and check, warning `another_client_connected` | `fix.another_client_connected` | `messengers/slack.md#one-slack-app-per-connection` | S-109 |
| Mattermost Destination check, `bot_not_in_channel`, `channel_in_other_team`, `channel_archived` | `fix.mattermost_destination` | `messengers/mattermost.md#add-a-destination` | S-109 |
| Telegram Destination check, `channel_not_found`, `not_a_channel`, `changed_since_saved` | `fix.telegram_destination` | `messengers/telegram.md#add-a-destination` | S-109 |
| Telegram Destination check, `comments_disabled` | `fix.telegram_comments` | `messengers/telegram.md#enable-comments-on-the-channel` | S-109 |
| Telegram Destination check, `bot_not_channel_admin`, `bot_may_not_post`, `bot_may_not_edit`, `bot_not_group_admin` | `fix.telegram_admin` | `messengers/telegram.md#make-the-bot-an-admin-of-the-channel-and-of-the-group` | S-109 |
| Slack Destination check, `bot_not_in_channel`, `channel_not_found`, `channel_archived` | `fix.slack_destination` | `messengers/slack.md#invite-the-bot-and-create-destinations` | S-109 |
| Bridge Destination check, `channel_forbidden` | `fix.bridge_destination` | `messengers/bridge.md#add-a-destination` | S-109 |
| Destination and its Routes, Broken; Alert Group page, delivery state "Waiting: {Destination} is Broken", or "Not delivered" while the Destination is Broken | `fix.broken` | `concepts/broken-destinations.md#recovery` | S-109 |
| Alert Group page, delivery state "Not delivered" while the Destination is healthy | `fix.not_delivered` | `concepts/broken-destinations.md#when-a-destination-becomes-broken` | S-109 |
| Integration, a truncated `groupKey` | `fix.truncated` | `integrations/alertmanager.md#send-every-alert-of-the-group` | S-109 |
| Integration, a long repeat interval | `fix.repeat_interval` | `integrations/alertmanager.md#repeat-intervals` | S-109 |
| Integration, no Heartbeat · waiting for the first Heartbeat signal · Heartbeat lost | `fix.heartbeat` · `fix.heartbeat_waiting` · `fix.heartbeat_lost` | `integrations/heartbeat.md#turn-it-on` · `integrations/heartbeat.md#the-snippet` · `operations/runbooks/MusterHeartbeatLost.md#fix` | S-109 |
| Alert Group page, a Replacement happened | `fix.replacement` | `integrations/alertmanager.md#instance-labels-in-informational-alerts` | S-109 |
| Route or Destination, a template keeps failing | `fix.template_error` | `operations/runbooks/MusterTemplateError.md` | S-109 |
| Outgoing webhook Destination, a literal credential · the previous Signing secret not retired | `fix.webhook_credential` · `fix.webhook_signing_secret` | `outgoing-webhooks/events.md#secrets` · `outgoing-webhooks/events.md#regenerate-and-retire-the-signing-secret` | S-109 |
| Every page, Admins: no replica is leading · recovering after downtime | `fix.no_leader` · `fix.after_downtime` | `operations/runbooks/MusterNoLeader.md` · `operations/high-availability.md#after-downtime` | S-110 |
| OIDC settings, nobody can sign in or the groups claim not carried by sign-ins · the client secret expires | `fix.oidc_mapping` · `fix.oidc_secret` | `sign-in/oidc-keycloak.md#2-add-the-groups-claim` · `operations/runbooks/MusterOIDCSecretExpiring.md` | S-110 |
| System status, outgoing heartbeat not set | `fix.outgoing_heartbeat` | `operations/outgoing-heartbeat.md` | S-110 |
| Keyring, a key that can be removed · a key missing on replicas ("Missing on {replicas}"), which cannot be activated until every replica holds it | `fix.keyring` | `operations/key-rotation.md` | S-110 |

The codes of the checks without a link: `request_failed`, whose `message` names an error no page can foresee, and
`webhook_set`, a step that passed. A code a type never gives, such as `not_slack` of a Telegram check, has no link
there. A new code of `NullableConnectionCheckCode`, `NullableDestinationCheckCode` or the
check's `warnings` gets a row here, or joins this list, in the story that adds it.

The warnings and notices without a link, each because its own text or page already says what to do: the Severity
levels' unmapped values seen (on the page whose Guide link opens `configure/organization.md#severity-levels`), the
OIDC warning `last_admin_kept` ("Make another user Admin first", done in Muster), the token without expiry, the
Snooze dialog's "no end", and the hints that ask for nothing. For now also the error classes of a Destination test
(`DeliveryErrorClass`, such as `blocked` and `template_error`) and the errors of Mattermost button presses.

## Roles and Permissions

Permissions are named `<resource>:<verb>`. The list below is final and is the `Permission` schema of the API
specification; every operation names the Permission it needs. The allocation to Roles is the L1 default, confirmed
with the C-03 sign-in story (P-42).

| Permission | Allows | Admin | Responder | Viewer |
|---|---|---|---|---|
| `alert-groups:read` | Read Alert Groups, Timelines, deliveries, statistics, counts and the user directory | ✓ | ✓ | ✓ |
| `alerts:read` | Read Alerts of Alert Groups and the Alerts view of Integrations | ✓ | ✓ | ✓ |
| `alert-groups:acknowledge` | Acknowledge, Unacknowledge, Takeover and answer Reminders ("Still on it") | ✓ | ✓ | — |
| `alert-groups:resolve` | Resolve and Unresolve | ✓ | ✓ | — |
| `alert-groups:snooze` | Snooze and Unsnooze, and read the ends of the Snooze presets | ✓ | ✓ | — |
| `alert-groups:note` | Add Notes | ✓ | ✓ | — |
| `integrations:read`, `routes:read`, `connections:read`, `destinations:read`, `link-rules:read`, `lookup-tables:read` | Read them, secret values masked (see below); `routes:read` also covers Route profiles, suggestions and dismissing one; picking a Connection to link an account through does not need `connections:read` (see below) | ✓ | ✓ | ✓ |
| `integrations:write`, `routes:write`, `connections:write`, `destinations:write`, `link-rules:write`, `lookup-tables:write` | Create, edit and delete them; `connections:write` also lists a Connection's channels, which calls out with the bot token | ✓ | — | — |
| `destinations:test` | Check, test and preview Destinations | ✓ | — | — |
| `templates:preview` | Dry-run and preview templates | ✓ | — | — |
| `stored-snapshots:read` | Read Stored Snapshots | ✓ | — | — |
| `users:read`, `users:write` | Read and manage Users, their Account links and password setup links; list Roles | ✓ | — | — |
| `oidc:read`, `oidc:write` | Read and change the OIDC settings, run its check | ✓ | — | — |
| `service-accounts:read`, `service-accounts:write` | Read and manage Service accounts and their tokens | ✓ | — | — |
| `teams:read` | Read Teams, the people in them, their Team mentions, Destinations and Working hours, and the Team label values seen without a Team (from 0.2.0, C-25, C-26) | ✓ | ✓ | ✓ |
| `teams:write` | Create, edit and delete Teams, the people in them, their identity provider entries, Team matchers, Team mentions, Working hours, Destinations and Escalation chains (from 0.2.0, C-25, C-26, C-32) | ✓ | — | — |
| `schedules:read` | Read Schedules, Rotation layers, Overrides, the On-call, gaps and "On call now" (from 0.2.0, C-27) | ✓ | ✓ | ✓ |
| `schedules:write` | Create, edit and delete Schedules and Rotation layers, and choose a Team's main Schedule (from 0.2.0, C-27) | ✓ | — | — |
| `schedule-overrides:write` | Create, change and delete Overrides, when the caller is in the Team or is an Admin (from 0.2.0, C-27.FR-6) | ✓ | ✓ | — |
| `organization:read` | Read the outbound address policy, the Keyring status and the severity values seen | ✓ | — | — |
| `organization:write` | Change Organization settings, the outbound address policy and the outgoing heartbeat | ✓ | — | — |
| `audit-log:read`, `system-status:read` | Read the Audit log and the System status | ✓ | — | — |

Without a Permission, for every signed-in identity: own profile, TOTP, Account links and Personal access tokens, and
from L2 own Notification rules, contacts of Notification gateways and iCal feed (the operations that change them need
the web session, C-03.FR-27), the profile's short list of Connections to link an
account through (only the id, the name and the messenger of each, C-18.FR-10), the basic read of the `organization`
resource (the outgoing heartbeat URL masked), system notices and live updates. A bulk command needs the Permission of
the command it runs. A Personal access token can only narrow its owner's Permissions; a Service account has its Role's
Permissions but cannot become an Owner (C-04.FR-2). A token may only assign a Role whose Permissions it holds itself,
when it creates or updates a User or a Service account (`422` `permission_not_held` at `/role`, C-04.FR-7).

**Secret masking.** Secret values are write-only for everyone, Admins included (C-03.FR-21): bot tokens, the OIDC
client secret, proxy passwords, outgoing webhook Secrets and Signing secrets, the outgoing heartbeat URL and master
keys. Reads return only whether a value is set and when it last changed; Audit log diffs show only that it changed;
test results, previews, Timeline errors and logs show it masked. Outgoing webhook URL and header templates are readable
by everyone who can read Destinations because they contain only references (`{{ .Secrets.<name> }}`), never the values
(C-15.FR-10). A Personal access token can only narrow its owner's Permissions.

### Permissions added in L2

The capabilities of L2 add these Permissions, with the same rules. Each joins the table above, the `Permission` schema
and the allocation that the migrations seed in the story that implements it; until then this table holds them.
Attaching an Escalation chain to a Route needs `routes:write`, and to a Team `teams:write`; seeing and changing another
User's Notification rules and removing their gateway contacts need `users:read` and `users:write`.

| L2 Permission | Allows | Admin | Responder | Viewer | Capability |
|---|---|---|---|---|---|
| `escalation-chains:read` | Read Escalation chains | ✓ | ✓ | ✓ | C-32 |
| `escalation-chains:write` | Create, edit and delete Escalation chains | ✓ | — | — | C-32 |

## Internal alerts

The closed L1 registry (C-06.FR-14). Each is an Alert of the built-in "Muster" Integration, routed by ordinary Routes,
deduplicated by fingerprint, resolved explicitly and never Gone or Stale. A configuration entity appears by its immutable
id, with its current name in a separate `*_name` label; the fingerprint is computed from `alertname` and the id labels
only, so renaming the entity updates the name label of the open Internal alert instead of starting a new one. An
Internal alert and a chart rule for the same condition share one name and one runbook page. From L2, an Internal alert
about a Team's object also carries the Team's slug under the Organization's Team label, outside the fingerprint, so
that it belongs to the Team (C-25.FR-5); the Team label may not be the name of a label an Internal alert sets itself,
such as `schedule` or `team_id`, and setting it to one is refused with `422` (C-25.FR-5).

| Internal alert | Severity | Raised while | Labels besides `alertname` and `severity` | Raised by |
|---|---|---|---|---|
| `MusterSnapshotTruncated` | warning | an Integration has a truncated `groupKey` | `integration`, `integration_name` | C-06 |
| `MusterHeartbeatLost` | critical | an Integration is Heartbeat lost | `integration`, `integration_name`, the Integration's Static labels | C-07 |
| `MusterDestinationBroken` | critical | a Destination is Broken | `destination`, `destination_name` | C-11 |
| `MusterTemplateError` | warning | a template of a Route or Destination keeps failing | `route` and `route_name`, or `destination` and `destination_name`; `template` | C-12, C-15 |
| `MusterOIDCSecretExpiring` | warning | the OIDC client secret expires within `oidc.secret_expiry_lead` | — | C-19 |

### Internal alerts added in L2

Each joins the registry, with a chart rule of the same name ([below](#chart-rules-added-in-l2)) and a runbook page, in
the story that implements it.

| Internal alert | Severity | Raised while | Labels besides `alertname` and `severity` | Raised by |
|---|---|---|---|---|
| `MusterScheduleGap` | warning | a Schedule with `schedule.gap_alert` on has a gap now or one that starts within `schedule.gap_alert_horizon`; an annotation says when the nearest gap starts | `schedule`, `schedule_name`, `team_id`, `team_name`, and the Team's slug under the Organization's Team label, outside the fingerprint, so that the alert belongs to its Team (C-25.FR-5) | C-27 |
| `MusterNotificationGatewayBroken` | critical | a Notification gateway is Broken | `connection`, `connection_name` | C-30 |

## Metrics catalogue

The L1 catalogue. The generated reference page (C-02.FR-19) is built from the registry in code; CI checks it against
this table (C-19.FR-2). Entity labels carry immutable ids; names come from the `*_info` metrics. Every other label takes
its values from a closed set defined in code (ADR-0014); where this table lists the values, the list is the whole set.
Histograms use `le` buckets; time to acknowledge and time to resolve use buckets from 1 minute to 24 hours.
`muster_alert_group_time_to_ack_seconds` is observed once per Alert Group, at its first acknowledgement: Takeover,
Unacknowledge and Reopen do not observe it again, and an Alert Group closed without an acknowledgement is not observed.

| Metric | Type | Labels | Exported from |
|---|---|---|---|
| `muster_build_info` | gauge | `version`, `commit` | C-02 |
| `muster_leader` | gauge | — | C-02 |
| `muster_clock_skew_seconds` | gauge | — | C-02 |
| `muster_client_requests_total` | counter | `client` (`delivery`, `interactive`, `background`, `heartbeat` — the client classes of ADR-0015), `outcome` (`ok`, `retry_after`, `transient`, `fatal`, `unknown`, `blocked`, `redirect`) | C-02 |
| `muster_client_request_duration_seconds` | histogram | `client`, `outcome` — the same sets | C-02 |
| `muster_db_pool_connections` | gauge | `state` (`acquired`, `idle`, `constructing`) — connections of the replica's main database pool | C-02 |
| `muster_db_pool_max_connections` | gauge | — | C-02 |
| `muster_db_pool_acquires_total` | counter | — | C-02 |
| `muster_db_pool_acquire_wait_seconds_total` | counter | — (the total time spent waiting for a free connection) | C-02 |
| `muster_short_lived_rows_pruned_total` | counter | `table` — the short-lived table the Leader deleted rows from (`short_lived_pruning`) | C-02 |
| process and Go runtime metrics (`process_*`, `go_*`) | — | — | C-02 |
| `muster_api_requests_total` | counter | `route_pattern`, `method`, `code` | C-03 |
| `muster_api_request_duration_seconds` | histogram | `route_pattern`, `method` | C-03 |
| `muster_login_failures_total` | counter | `method` (`local` — a wrong login or password; `oidc` — a refused OIDC sign-in; `totp` — a wrong TOTP or recovery code) | C-03 |
| `muster_oidc_checks_total` | counter | `outcome` (`ok`, `refused`, `unavailable`, `skipped`) — background re-checks of OIDC users at the IdP; `skipped` when the user has no live session and no usable Personal access token (C-03.FR-30) | C-03 |
| `muster_ingest_requests_total` | counter | `integration` (the token's Integration, a revoked token's included; `unknown` when the token matches no Integration or only a deleted one), `outcome` (`accepted`, `unauthorized`, `too_large`) | C-05 |
| `muster_ingest_request_duration_seconds` | histogram | — | C-05 |
| `muster_integration_info` | gauge | `integration`, `name` | C-05 |
| `muster_ingest_processing_delay_seconds` | histogram | `integration` | C-06 |
| `muster_ingest_backlog` | gauge (Leader) | — | C-06 |
| `muster_ingest_truncated_snapshots_total` | counter | `integration` | C-06 |
| `muster_ingest_failed_snapshots_total` | counter | `integration` — a Stored Snapshot that could not be processed (C-06.FR-20) | C-06 |
| `muster_alerts_resolved_total` | counter | `integration`, `reason` (`resolved`, `gone`, `stale`, `integration_deleted`) | C-06 |
| `muster_ingest_resolved_dropped_total` | counter | `integration` — a `resolved` for a fingerprint that fires nowhere (C-06.FR-4) | C-06 |
| `muster_heartbeat_lost` | gauge (Leader) | `integration` | C-07 |
| `muster_route_info` | gauge | `route`, `name` | C-08 |
| `muster_alert_groups` | gauge (Leader) | `route`, `status` | C-09 |
| `muster_alert_groups_created_total` | counter | `route` | C-09 |
| `muster_alert_groups_reopened_total` | counter | `route` | C-09 |
| `muster_alert_groups_resolved_total` | counter | `route`, `by` (`user`, `system`) | C-09 |
| `muster_alert_group_time_to_resolve_seconds` | histogram | `route` | C-09 |
| `muster_alert_group_time_to_ack_seconds` | histogram | `route` | C-10 |
| `muster_commands_total` | counter | `command` (`acknowledge`, `unacknowledge`, `resolve`, `unresolve`, `snooze`, `unsnooze`, `add_note`, `still_on_it`), `transport` (`ui`, `api`, `mattermost`, `telegram`, `bridge`, `slack`, `system`), `outcome` (`done`, `unchanged`, `refused`, `skipped`, `failed`) | C-10 |
| `muster_delivery_attempts_total` | counter | `destination`; `kind` (`publication`, `update`, `thread_reply`, `storm_summary`, `final_edit`, `webhook_event`); `outcome` (`delivered`, `markup_rejected`, `retry_after`, `transient`, `fatal`, `unknown`, `template_error`) | C-11 |
| `muster_delivery_latency_seconds` | histogram | `destination` | C-11 |
| `muster_delivery_queue` | gauge (Leader) | `destination` | C-11 |
| `muster_destination_broken` | gauge (Leader) | `destination` | C-11 |
| `muster_storm_active` | gauge (Leader) | `route` | C-11 |
| `muster_destination_info` | gauge | `destination`, `name` | C-11 |
| `muster_template_errors_total` | counter | `route`, `destination` (one of them empty), `template` (`root_message`, `line`, `ack_timeout_notice`, `link_rule`, `webhook_request`) | C-12 |
| `muster_template_render_duration_seconds` | histogram | `template` (as above) | C-12 |
| `muster_request_log_records_total` | counter | `direction` (`outbound`, `inbound`) — records written to the Request log | C-23 |
| `muster_request_log_write_failures_total` | counter | `direction` (`outbound`, `inbound`) — records that could not be written and were dropped | C-23 |
| `muster_request_log_pruned_total` | counter | — (records the Leader deleted past `request_log.max_records` or `request_log.max_age`, `request_log_pruning`) | C-23 |
| `muster_team_info` | gauge | `team`, `name` | C-25 |
| `muster_schedule_info` | gauge | `schedule`, `name` | C-27 |

## Chart rules

Rendered by the chart when the Prometheus or VictoriaMetrics CRDs are enabled (C-19.FR-3). Each rule has a
`description` and a `runbook_url`; thresholds, windows and the job selector (`job="muster"` below, part of every
series selector) are chart values.
The default expressions are covered by rule unit tests (C-19.AC-8). The documentation tells
users to send the critical rules through an Alertmanager route that bypasses Muster.

| Rule | Severity | Default expression | For | Meaning |
|---|---|---|---|---|
| `MusterDown` | critical | `up{job="muster"} == 0` | 2m | the scrape target is down |
| `MusterNoLeader` | critical | `max(muster_leader{job="muster"}) == 0` | 2m | no replica leads: Telegram and Bridge polling, Slack's Socket Mode, Heartbeat lost and Stale checks, partitions and the outgoing heartbeat have stopped; ingestion, delivery and timers keep working |
| `MusterDeliveryFailing` | critical | `max by (destination) (muster_delivery_queue{job="muster"}) > 0 unless on (destination) sum by (destination) (increase(muster_delivery_attempts_total{job="muster",outcome="delivered"}[10m])) > 0` | 5m | a Destination has queued deliveries and no successful delivery for 10 minutes |
| `MusterDestinationBroken` | critical | `max by (destination) (muster_destination_broken{job="muster"}) == 1` | 5m | a Destination is Broken |
| `MusterHeartbeatLost` | critical | `max by (integration) (muster_heartbeat_lost{job="muster"}) == 1` | 1m | an Integration is Heartbeat lost; the backup for the Internal alert |
| `MusterDeliverySlow` | warning | `histogram_quantile(0.95, sum by (le) (rate(muster_delivery_latency_seconds_bucket{job="muster"}[15m]))) > 5` | 15m | delivery p95 above the objective of NFR-2 |
| `MusterDeliveryQueueGrowing` | warning | `max by (destination) (muster_delivery_queue{job="muster"}) > 100 and max by (destination) (deriv(muster_delivery_queue{job="muster"}[15m])) > 0` | 15m | a delivery queue keeps growing |
| `MusterIngestBacklog` | warning | `max(muster_ingest_backlog{job="muster"}) > 1000 or histogram_quantile(0.95, sum by (le) (rate(muster_ingest_processing_delay_seconds_bucket{job="muster"}[5m]))) > 60` | 5m | processing falls behind |
| `MusterIngestRejected` | warning | `sum by (integration, outcome) (increase(muster_ingest_requests_total{job="muster",outcome="too_large"}[15m])) > 0 or sum by (integration, outcome) (increase(muster_ingest_requests_total{job="muster",outcome="unauthorized"}[15m])) > 10` | 0m | an oversized ingestion request, or more than 10 unauthorized ones, arrived in the last 15 minutes |
| `MusterTemplateError` | warning | `sum by (route, destination, template) (increase(muster_template_errors_total{job="muster"}[15m])) > 0` | 0m | templates failed in the last 15 minutes |
| `MusterClockSkew` | warning | `max by (instance) (abs(muster_clock_skew_seconds{job="muster"})) > 2` | 5m | a replica's clock is off |
| `MusterLoginFailures` | warning | `sum by (method) (increase(muster_login_failures_total{job="muster"}[10m])) > 20` | 0m | a burst of failed sign-ins |

### Chart rules added in L2

Each joins the table above, the chart and the rule unit tests in the story that implements it; their thresholds and
windows are _provisional_ (P-88), as those of L1 were.

| Rule | Severity | Default expression | For | Meaning |
|---|---|---|---|---|
| `MusterScheduleGap` | warning | `max by (schedule) (muster_schedule_gap{job="muster"}) == 1` | 0m | a Schedule has a gap now or within the horizon; the backup for the Internal alert |
| `MusterNotificationGatewayBroken` | critical | `max by (connection) (muster_notification_gateway_broken{job="muster"}) == 1` | 5m | a Notification gateway is Broken |

## Metrics added in L2

The metrics that the capabilities of L2 add. Each joins the [metrics catalogue](#metrics-catalogue) and the registry,
with the entity labels it needs in the label policy of ADR-0014, in the story that implements it; until then this table
holds them, outside the catalogue that the code checks. The value `gateway` joins the `transport` values of
`muster_commands_total` (C-30.FR-10).

| Metric | Type | Labels | Exported from |
|---|---|---|---|
| `muster_schedule_gap` | gauge (Leader) | `schedule` — 1 while there is a gap now or one that starts within `schedule.gap_alert_horizon` | C-27 |
| `muster_notification_gateway_broken` | gauge (Leader) | `connection` | C-30 |
| `muster_gateway_calls_total` | counter | `connection`, `outcome` (`accepted`, `retry_after`, `transient`, `fatal`, `unknown`) | C-30 |
| `muster_escalation_chain_info` | gauge | `chain`, `name` | C-32 |
| `muster_pages_total` | counter | `chain`, `kind` (`mention`, `call`), `outcome` (`delivered`, `not_delivered`) | C-32 |
