# C-43. Paging by direct messages

[L2 index](../L2.md) · Release: a 0.2.x patch release after 0.2.0 · UI: yes · Depends on: C-09, C-10, C-11, C-13,
C-14, C-18, C-20, C-22, C-23, C-24, C-27, C-30, C-31, C-32, C-41

This capability reverses what L2's core decided in ADR-0018 ("Pages, not direct messages" and the rejected alternative
"Paging by direct messages"), which ADR-0005 (who calls messenger APIs), ADR-0017, C-11.FR-2, the goal of C-31 and
C-32 repeated. The questions of the draft were decided by the maintainer in its review (#711; journal D764), who
accepted every recommendation and amended ADR-0018; ADR-0005, ADR-0017, C-11.FR-2 and FR-3, C-31, C-32, CONTEXT.md and
L2.md changed with it, and "What changes elsewhere" lists what the stories change. The facts at the end are checked by
a spike before the stories that rely on them.

**Goal.** A person can choose, in their Notification rules, to be Paged by a direct message from the bot in every
messenger where they have an Account link — Telegram, Mattermost, Slack, and Bridges that declare the capability —
instead of, or besides, a Mention in the Thread and a call through a Notification gateway. A Mention in a Thread reaches
a person only if the messenger pushes Thread replies and only in the chats of the Alert Group's Destinations; a direct
message needs neither a Root message nor a Destination, and a direct message notifies by default in the messengers' own
settings (to be verified on phones, see the facts below). The message carries what the person needs to decide and,
where the messenger allows it, the buttons Acknowledge and Snooze, so that a person who acknowledges from the lock
screen acknowledges the Alert Group as themselves. Muster
still ships no gateway and calls or texts nobody by itself; a direct message is one more way to Page, in the same rules.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the settings of this capability are
under "Settings", and its provisional value is P-99 of
[L2 §5.3](../L2.md#53-provisional-values-and-behaviours); P-98 was confirmed by the spike.

## Scenarios

1. Ivan links his Telegram account and, in "Edit sequences" (C-41), makes his important rule "Direct message in
   Telegram, wait 3 minutes, direct message again". An Escalation step Pages him importantly at 03:20: his phone shows
   a message from the Muster bot, "Page · critical · #123 PaymentsAPIDown", with the buttons Acknowledge and Snooze. He
   presses Acknowledge; the Alert Group is acknowledged by Ivan with the Transport `telegram`, the chain stops, and the
   message is edited to "Acknowledged by Ivan at 03:21".
2. Petr's Mattermost account is linked, but the Route's Destination is a Telegram channel: the Thread of its Root
   message mentions only Telegram accounts. His rule "Direct message in Mattermost" reaches him anyway, with no Root
   message in any Mattermost channel.
3. Anna blocked the Telegram bot. Her direct message ends Not delivered with the reason "The bot is blocked in
   Telegram"; the Timeline shows it, the Alert Group page says why she was not reached, her Schedule warns "Unreachable by
   direct message in Telegram: the bot is blocked", and her Mattermost direct message in the same rule is delivered.
4. Sergey linked his Slack account, and later the app's Messages tab was turned off. His Page ends Not delivered with
   the reason "{messenger} does not accept direct messages from the bot", and the Account links section of his profile
   warns him.
5. A Telegram Page is queued for Dmitry and, before it is sent, someone acknowledges the Alert Group: the direct message
   is never sent, and, as with a call, nothing is counted. Another message, already sent, is edited once to say
   "Acknowledged by Olga at 03:24".
6. Maria presses Acknowledge in a forwarded copy of a Page that Ivan received; the press comes from an account not
   linked to Ivan, so nothing changes and she is told privately that the message was not sent to her.
7. An Admin who runs a workspace that forbids alert content in personal chats turns the Organization's setting off; the
   direct-message actions of every person's rules are skipped with their reason, and the profile shows them as
   unavailable. An Admin who tries to put the action into the Organization's default rules is refused.
8. A bridge for a messenger without native support declares `direct_messages`; its users pick "Direct message in {the
   bridge}" in their rules and the fake bridge shows the message with its layout and buttons.

## Functional requirements

### The action in Notification rules

- **C-43.FR-1** A Notification rule gains the action "Direct message" (C-31.FR-1), beside "Mention in the Thread",
  "Call through a Notification gateway" and "Wait N minutes": it Pages the person by a direct message from the bot of a
  Connection in which they have an Account link (C-18). The action counts as one that Pages (C-31.FR-1: a rule has at
  least one), and rules stay within `notification_rule.limits`. An action in the sequence may be placed anywhere among
  the Mentions, the calls and the waits: they run in order and a wait delays what follows, as for the others (C-31.FR-3).
  Each direct-message action that runs, a repeat of the same action included, sends a new message and never edits or
  deletes an earlier one, since an edit notifies nobody in Telegram and Mattermost (FR-8); only overdue actions of one
  run through one Account link collapse into one message (FR-5).
- **C-43.FR-2** A direct message goes through an Account link: the action names the link — the Connection of a
  Telegram, Mattermost, Slack or Bridge account of the person — when the person has more than one Account link in a
  messenger that can receive Pages by direct message, as the call names its gateway when there is more than one
  (C-31.FR-1); with one, the action uses it, and a link added later never changes what a stored rule does until the
  person names it. A person who wants two messengers adds two actions with no wait between them, which run at the same
  moment. Account links of a Bridge Connection count only when its `info` declares `direct_messages` with the Page
  support of FR-13; a Bridge Connection whose only role is `gateway` has none.
- **C-43.FR-3** The Organization's default rules stay as C-31.FR-2 sets them: normal — a Mention in the Thread;
  important — a Mention, a wait of 5 minutes and a Mention. No state of the switch "Also call through the Notification
  gateway" changes, and C-31's switch gains no direct-message variant. The Organization's rules cannot hold the action,
  since it names the person's own Account link, which the Organization has none of: `updateOrganization` answers `422`
  at the action with the code `direct_message_not_allowed`, and the editor of C-41.FR-9 does not offer it. A person
  adds it to their own rules with the editor of C-41.FR-1, and an Admin to a User's with that of C-41.FR-7.
- **C-43.FR-4** The action is offered only where it can work. The editors list it for the Account links that qualify
  (FR-2) and show the others as unavailable with the reason: "Link a Telegram, Mattermost or Slack account first" when
  the person has none, "The bridge does not declare direct messages", "Turned off by an Admin" when the Organization's
  setting is off (FR-23), and, for a link the messenger could not reach, "The bot is blocked in {messenger}" or the
  reason of FR-19. A stored action that stopped qualifying stays stored, as a call to a deleted gateway does
  (C-41.FR-3), and the API accepts it, with the reasons of FR-17 in the run.
- **C-43.FR-5** When an Escalation step Pages a person by a rule that has a direct-message action, the action makes one
  direct message per Alert Group (FR-9, FR-10), a Page of the step with the kind `direct_message`. The run stops, as the
  run of a rule always does (C-31.FR-3), when the Alert Group is acknowledged, snoozed or resolved, or its chain stops
  or starts over; a direct message that was queued but not yet sent then ends Not delivered with no reason and is not
  counted, as a call does (C-30.FR-4). An action whose Account link, Connection or the Organization's setting is gone is
  skipped and recorded with its reason (C-32.FR-14): `account_link_deleted`, `connection_deleted`,
  `direct_messages_disabled`. When actions come due late, such as after Muster was down, the overdue direct-message
  actions of one run through the same Account link collapse into one message, as overdue Mentions do (C-31.FR-3); those
  through different links are each sent.

### What a direct message says

- **C-43.FR-6** A direct message is plain, short text in the language of the person's profile, with, in this order:
  how urgent it is and why the person is Paged — "Page · critical", then "Step {n} of the chain {chain} · On-call of
  {Team} {Schedule} · important" or, for a rule's later action, "· important, again"; the Alert Group's title; its
  number and a link to its page in Muster; and its Route and Team — "Route {Route} · Team {Team}", without the Team where
  the Alert Group has none. Beyond the title it names no label, no Alert, no annotation and no Instance label, and shows no
  description or summary; the title is the `alertname` or the values of the Group key (C-09.FR-23), so it can carry
  label values (FR-22). The text uses only the built-in texts, in English and Russian (NFR-8).
- **C-43.FR-7** Where the messenger supports buttons in a private chat and the Connection's bot is set up for presses —
  Telegram's inline keyboard, Mattermost's interactive message, Slack's Block Kit buttons, a bridge that declares
  `buttons` — the message has the buttons Acknowledge and Snooze, the latter with the snooze durations of the Alert
  Group's Route (C-08), and a link to the Alert Group page. Where it does not, the message is plain text with that link
  and says how to act in the web UI. Resolve, Takeover, Unacknowledge, Notes and the other buttons of a Root message are
  never offered in a direct message.
- **C-43.FR-8** A direct message is edited at most once. When the Alert Group stops being firing for it — Acknowledge,
  Snooze or Resolve, by anyone, from any Transport — delivery edits the message to its state at that moment, removing
  the buttons: "Acknowledged by {name} at {time}", "Snoozed by {name} until {time}" or "Resolved at {time}", under the
  first line of the message. An edit notifies nobody in Telegram ([F-012](../../facts.md#notifications)) and
  Mattermost ([F-029](../../facts.md#edits-and-notifications)); Slack and bridges are to verify. The edit counts against
  the Connection's limiter but not against `direct_message.person_spacing`, which paces sends only. Later changes — a Reopen, Unacknowledge, Unsnooze, the end of a Snooze, Unresolve, a new Owner, a Takeover —
  edit nothing: a Reopen that Pages again makes a new direct message. An edit that the messenger refuses, such as a
  message deleted by the person, is recorded in the Page's outcome and not retried beyond the usual classification
  (C-11.FR-8).

### Sending

- **C-43.FR-9** A direct message is a row of a table of its own, `direct_messages` (one per Page, person and Account
  link), written in the dispatcher's transaction with its Page and sent by the delivery worker, as ADR-0005 as amended
  says of who calls messenger APIs. The row has a `public_id` of its own, which the button data names (FR-14). The row
  keeps what is needed to send, edit and bind a press — the Connection, the messenger's chat and message ids once sent,
  the Alert Group, the person and the Page — and never the text, which is built at the send from the person's language
  and the Alert Group, and again at the edit. The delivery worker claims due rows with a lease on any replica, takes a
  token of the Connection's limiter, sends in the delivery client class and classifies the answer as it does for Thread
  replies (C-11.FR-8): Transient answers are retried within `delivery.transient_backoff` and `delivery.transient_budget`,
  Fatal ones end the message Not delivered. Critical Alert Groups go first within the rank of direct messages. This adds
  a rank to the order of C-11.FR-3, recorded there as a revision: the sends of direct messages come after the first
  rank (the Root-message work of Urgent Alert Groups) and before the configuration edits of Urgent Alert Groups, so
  that a Page is not held behind non-urgent work; their final edits (FR-8) share the rank of Thread replies. A direct
  message is never reconciled to a Desired state: a Root message is, a direct message has one send and one edit (FR-8).
- **C-43.FR-10** Direct messages are not combined: each Alert Group has its own message with its own buttons, as every
  Alert Group has its own Thread. To keep a burst of Pages — many Alert Groups Paging one person — within
  what the messenger accepts, direct messages to one person through one Connection go at most one per
  `direct_message.person_spacing`, in the order of FR-9, and they are not dropped for waiting. A direct message that waits is still stopped by an acknowledgement
  (FR-5).
- **C-43.FR-11** A direct message goes only through the Connection's limiter (`connection.telegram.limiter`,
  `connection.mattermost.limiter`, `connection.slack.limiter`, `connection.bridge.limiter`): it names no Destination,
  so it takes no token of a Destination's limiter. A RetryAfter holds what each messenger's rule holds today: Slack's
  and Mattermost's the Connection (C-24, C-13), a bridge's `rate_limited` the Connection (C-22), Telegram's the person's
  chat, and the Connection only after two chats get one within 60 seconds (C-14.FR-7); the stories confirm each. The budget of Slack's Connection, 50 requests a minute shared with every Root message edit, is the one
  most likely to be reached by Pages; the stories measure it (Facts to verify) before proposing a limiter of its own.
- **C-43.FR-12** A direct message never makes anything Broken: only a Destination and a Notification gateway can be
  (CONTEXT.md), and a direct message has neither. Answers that name the person — the bot is blocked, the account is
  unavailable, the messenger or workspace does not accept direct messages — end it with their reason (FR-17); other
  Fatal answers end it `direct_refused`, unknown ones `direct_unknown`, and Transient answers past the budget
  `direct_budget_used`. A Broken Destination of the same Connection withholds nothing here. A Connection-wide Fatal
  answer, such as a revoked token or a deleted bot, adds nothing new: each direct message through that Connection ends
  `direct_refused`, and the Connection's Destinations become Broken through their own deliveries, as they do today, and
  raise `MusterDestinationBroken`. A Connection-level Broken state is not added; it waits until installations show
  Connections that only Page by direct message.
- **C-43.FR-13** How each messenger is reached, as far as the facts say (the rest is under "Facts to verify"):
  - **Telegram:** `sendMessage` to the private chat whose id is the Account link's Telegram user id, with an inline
    keyboard, and no `disable_notification`, since a Page is meant to ring ([F-012](../../facts.md#notifications)). A person can
    be reached only after they pressed Start in the bot, which linking requires (C-18.FR-2). A person's private chat
    with the bot that an Admin makes a Destination (L2 §1.5, #371) stays a Destination, apart from this: it receives
    Root messages and Thread replies, while a direct message is a Page of one person; the two share Telegram's budget
    of that chat under their own limiters, which the spike measures, and the story of that Destination says that its
    person can also have the rule.
  - **Mattermost:** `POST /api/v4/channels/direct` with the bot's and the person's ids, then a post with an interactive
    attachment; a bot can write to a person who never wrote to it ([F-028](../../facts.md#threads-and-direct-messages)).
    Mattermost accepts the channel and the post for a deactivated person, answering `201` to both
    ([F-145](../../facts.md#threads-and-direct-messages)), so before it sends a direct message — not before its one
    edit — Muster reads the person with `GET /api/v4/users/{user_id}`, and a `delete_at` above 0 ends the message
    `account_unavailable` without sending it (journal D765).
  - **Slack:** `chat.postMessage` with `channel` set to the person's `U…` id or to the `D…` channel of
    `conversations.open`, with Block Kit buttons ([F-096](../../facts.md#users-and-direct-messages)); the scope
    `im:write` is the one Account links by code already need. A `U…` id that names no user answers `chat.postMessage`
    with `channel_not_found` ([F-152](../../facts.md#users-and-direct-messages)), which ends a direct message
    `account_unavailable` (journal D765).
  - **Bridge:** the operation of `api/bridge.yaml` that sends a direct message — extended, see "What changes elsewhere"
    — takes, beyond the `Idempotency-Key` and the `message_id` and `replayed` answer it already has, the structured
    `layout` of C-29 and the buttons, answers a problem code that tells a blocked bot from a refusal, and lets Muster
    edit the message. The Page support is an addition to the capability `direct_messages` that bridges already
    declare for Account link codes; a bridge without it receives no Page.

### Presses in a direct message

- **C-43.FR-14** A press on a button of a direct message runs the Command through the dispatcher in `internal/groups`
  like any press (ADR-0016): Acknowledge as an Acknowledge, Snooze with its duration as a Snooze, with the Transport of
  the messenger — `telegram`, `mattermost`, `slack` or `bridge` — and the actor the User linked to the account that
  pressed (C-18.FR-8). The button data names the Alert Group and the direct message, a subject of its own kind with the
  message's `public_id` within the 64 bytes of a button's data (C-22.FR-5, as the action data of a call names the call,
  C-30.FR-2); the press is bound to the message in `direct_messages` by the Connection, the chat and the message id,
  where a press on a Root message is bound to a delivery.
- **C-43.FR-15** A press is refused, changes nothing, is logged as a refusal and is answered privately when its account
  is linked to another User than the one the message was sent to: "This message was not sent to you". A press by an
  unlinked account gets only the private answer of C-18.FR-8 with a link to the profile; one whose User lacks the
  Permission or is disabled gets the texts of C-18.FR-8. A press on a message whose Alert Group is no longer firing, or
  that someone owns, follows the precondition of the Command (ADR-0004): the answer names who has it, "Already taken by
  {name}", and, as for a gateway, a Takeover is never made from a direct message (C-30.FR-6).
- **C-43.FR-16** Answers to a press go through the interactive path (C-11.FR-2): Telegram's `answerCallbackQuery`
  within about 15 seconds ([F-010](../../facts.md#buttons)), Slack's `response_url` or ephemeral answer, a Mattermost
  ephemeral answer, a bridge's `answerEvent`; after a press that changes the Alert Group, the delivery worker's edit of
  FR-8 shows the new state.

### Pages and reachability

- **C-43.FR-17** A direct message is recorded as a Page with the kind `direct_message`, next to `mention` and `call`
  (C-32.FR-3, C-30.FR-4): a row of `pages` with the Account link's Connection (`direct_connection_id`, set exactly for
  this kind, as `gateway_connection_id` is for a call; the kind joins the CHECKs next to `mention` and `call`), the `direct_messages` row it settles, its person in
  `page_users`, and the outcome Delivered or Not delivered, settled when the message is sent or ends. The reasons of
  Not delivered, which the Timeline and the Alert Group page show in words, are: `no_account_link` (the person has none
  in a messenger that qualifies), `account_link_deleted`, `connection_deleted`, `direct_messages_disabled` (the
  Organization's setting, FR-23), `bot_blocked` (the person blocked the bot), `account_unavailable` (the account is
  deactivated or its chat not found), `direct_messages_not_accepted` (the messenger or the workspace does not accept
  direct messages from the bot — Slack's disabled Messages tab, a Mattermost permission, Telegram's "can't initiate
  conversation", a bridge's `forbidden`), `direct_refused` (other Fatal answers), `direct_unknown` (unknown answers)
  and `direct_budget_used` (the retry budget used up, as `call_budget_used`). A message that Muster did not send because its run stopped has no reason and
  is not counted (FR-5). The new reasons join the CHECKs of `pages` and `page_users` in an expand step (ADR-0006).
- **C-43.FR-18** A delivered direct message counts as reaching the person, so the person becomes an Assignee as they do
  for a delivered Thread reply that mentions them and a call the gateway accepted (C-32.FR-12); one that ends Not
  delivered makes none. The lifecycle events of C-32 and C-30 are used as they are: `paged` with the kind `direct_message`
  — Quiet, with no Mention and nothing in any Thread, since the direct message is the Page — and `page_not_delivered`
  with the person and the reason (C-32.FR-10), both Timeline kind `escalation`; the Thread of the Alert Group shows
  nothing of a direct message, and the Timeline and "How it went" do. `muster_pages_total{chain,kind,outcome}` gains
  `kind` `direct_message`; `muster_commands_total{transport}` needs no new value; a Page not counted is not counted
  here either (C-32.FR-10). The stories register the log events.
- **C-43.FR-19** When a direct message ends `bot_blocked`, `account_unavailable` or `direct_messages_not_accepted`, the
  Account link records the time and the reason (`direct_unreachable_at`, `direct_unreachable_reason`); both are cleared
  by the next direct message of that link that is delivered, by a press from it, and by linking again. While they are
  set, the profile shows the link as "The bot is blocked: direct messages do not reach you" or with the text of its
  reason, the editor offers no action for it (FR-4), the Schedule warns about the On-call's link in the way of
  C-27.FR-8 — "Unreachable by direct message in {messenger}: {reason}." — and Muster still tries the next direct
  message, since a person may have unblocked the bot. The answer is recorded once per Page, not per retry.
- **C-43.FR-20** Reachability warnings count direct messages. C-27.FR-8's warning that an On-call is unreachable where
  the Destinations cannot mention them adds, where the person's rules for the Schedule's Pages include a direct message
  through a qualifying link that is not blocked, "A direct message works." beside the text about calls, and the warning
  of C-32.FR-17 for a Route that names a chain and no Destination reads "Only a Notification gateway or a direct message
  can reach anyone from this Escalation chain: the Route has no Destination." — a direct message needs no Root message,
  so a chain with such rules Pages without one. A step's behaviour does not change: it Pages people by their rules.

### Escalation chains

- **C-43.FR-21** An Escalation step stays unaware of direct messages: it Pages the On-call, chosen people or a Team, and
  a person is Paged by their normal or important rule (C-32.FR-3). No step names a direct message, and a step "Page a
  whole Team or everyone in the chat" never makes one — a Team mention is a Mention of the Thread, and a Team has no
  private chat. A "Page by Mention" in the Thread is a reply every member of the chat can read, mentions the person,
  names the step and the chain, and depends on a Root message in a Destination of the chain (C-32.FR-8, FR-9); a direct
  message is private to the person, depends on neither, says the same thing about the step and carries the buttons. A
  repeat of a step, the stop at Acknowledge, Snooze and Resolve, the start over of a chain and the ack timeout work as
  they do for the other actions (C-32.FR-5, FR-6).

### Privacy and the Organization

- **C-43.FR-22** The content of a direct message is limited by FR-6, so that a personal chat, which is stored by the
  messenger under its own retention and by the person's devices, holds the title, the Severity level, the number, the
  Route and the Team, and nothing from the Alerts. The title is the `alertname` or the values of the Group key (C-09.FR-23), so it can carry label values,
  such as `cluster=prod, namespace=payments`; the documentation says so, and an Admin who cannot allow that turns the
  action off (FR-23).
- **C-43.FR-23** The Organization has a setting, `organization.direct_messages`, on by default and changed by Admins
  (`organization:write`) on Organization → Alert handling, recorded in the Audit log with a before/after diff
  (C-20). While it is off, no direct message is sent, the editors show the action unavailable (FR-4), the stored actions
  are skipped with `direct_messages_disabled` (FR-5) and a Page needs another action to reach anyone. The person's own
  choice is their rule: no one is messaged unless they or an Admin put the action into their own rules; the profile shows it in words and lets the person remove it (C-31.FR-4); the default rules do not include it
  (FR-3).
- **C-43.FR-24** `direct_messages` rows and their Pages stay with their Alert Group and go with it (`pages.alert_group_id`
  `ON DELETE CASCADE`, so with the summary row under `retention.alert_group_summaries`); a deleted User shows
  pseudonymized there, as in every Page, and no new deletion happens at the User's deletion. Unlinking an account — by
  the person, an Admin or the User's deletion — stops the editing of its messages and marks the rows, and Muster does
  not delete messages from the person's chat: they are the person's, as the messages of Account links are. Rows hold no
  text (FR-9). The Audit log records no direct message; the Request log (C-23) records the calls to a messenger as for
  every Connection, with the person's id masked in the way of a gateway's contact.

### Documentation

- **C-43.FR-25** The documentation describes the action, how it differs from a Mention in the Thread and from a call, what
  a direct message says and never says, the buttons and the single edit, what each messenger needs (Telegram: Start;
  Slack: the scope `im:write` and the app's Messages tab; Mattermost: nothing beyond the bot), what a blocked bot does,
  that Do-not-disturb and the messenger's notification settings of the person apply to a direct message as to any
  other, and the Organization's setting; its page on Notification rules and the page "How an alert reaches people" gain
  the new action and its row in the table of C-32.FR-11. The statement of C-30.FR-11 and C-32.FR-20 that only a
  Notification gateway is reliable at night is revised to name direct messages once the facts below are verified.

## UI

Profile → "Linked accounts": a link whose bot is blocked shows "The bot is blocked: direct messages do not reach you",
and a link the messenger refuses shows "{messenger} does not accept direct messages from the bot." Profile →
"Personal notification rules" and the editor of C-41: the action "Direct message" with "in {messenger}" (the Account
link, where the person has more than one), listed beside "Mention in the Thread" and "Call through a Notification
gateway"; the preview reads "At once: a direct message in Telegram · After 3 min: the direct message again"; the
unavailable texts of FR-4; and the words of the rules, "a direct message in {messenger}". Organization → Alert handling:
"Direct messages" with "Muster may Page people by direct message from its bot. Alert titles then appear in personal
chats." and its switch, and the editor of the default rules (C-41.FR-9), which does not offer the action. Alert Group page: the Pages of
the Timeline and "How it went" show "Direct message to {person} in {messenger}: delivered" or "not delivered:
{reason}". Schedule page: the warnings of FR-19 and FR-20. The messages themselves are built from texts that exist in
English and Russian (FR-6).

## API surface

`NotificationRuleSet` gains the action kind `direct_message` with the Account link it names (`account_link_id`, optional
with one link) — validated as the call action is, with `422` for a link that is not the person's or does not qualify
(`account_link_required`, `direct_messages_disabled`, as `contact_required` is for calls) and, in
`updateOrganization`, `direct_message_not_allowed`; the profile's and a User's rules
(`me/notification-rules`, `users/{id}/notification-rules`) carry it, and the reasons of an unavailable action are part of
their read, as those of calls are (C-41.FR-3). `organization` gains `direct_messages` (boolean). The read of an Account
link (`me/account-links`, `users/{id}/account-links`) gains `direct_messages`: `available`, `unreachable` (with the time and the reason of FR-19) or
`unsupported` with its reason. The Timeline entry and the Page reads gain the kind `direct_message` and the reasons of
FR-17. In `api/bridge.yaml`: the Page support of `capabilities.direct_messages`, the extension of `sendDirectMessage` and
an `editDirectMessage` operation (prose only in "What changes elsewhere"). The Transport enum is unchanged.

## Acceptance

Checked with the fake Telegram, Mattermost, Slack and bridge servers, which record direct messages with their layout and
buttons and take scripted faults, and a virtual clock.

- **C-43.AC-1** With the rule "Direct message", wait 3 minutes, "Direct message", an important Page of an Escalation step
  sends one direct message at once, a second 3 minutes later (the development clock advanced), and none after an
  Acknowledge in between; a message queued but unsent when the Alert Group is acknowledged ends Not delivered with no
  reason and is not counted in `muster_pages_total`.
- **C-43.AC-2** The message has the lines of FR-6 in the person's language, no label and no annotation of the Alert Group
  — a fixture with labels and a description shows neither — and the buttons Acknowledge and Snooze where the fake
  messenger supports them, plain text with the link where it does not; Resolve is never offered.
- **C-43.AC-3** Pressing Acknowledge in each fake messenger acknowledges the Alert Group as the linked User with that
  messenger's Transport and edits the message once to "Acknowledged by {name} at {time}" without buttons; a later
  Reopen edits nothing; a Page of the reopened Alert Group makes a new message.
- **C-43.AC-4** A press from an unlinked account, from an account linked to another User, from a Viewer and from a
  disabled User changes nothing and gets the private answers of FR-15; a press on a message of an Alert Group another
  User owns answers "Already taken by {name}" and makes no Takeover.
- **C-43.AC-5** The fake faults — the bot blocked, the chat not found, direct messages refused, a Fatal answer, a
  RetryAfter and Transient failures until the budget is used — give the Not delivered reasons of FR-17, Transient ones
  are retried under the same `Idempotency-Key` for a bridge, a RetryAfter holds the Connection's limiter, and the person
  is an Assignee only after a delivered message.
- **C-43.AC-6** After `bot_blocked` (and likewise `account_unavailable` and `direct_messages_not_accepted`), the Account link is shown as unreachable in the profile, the Schedule warns about the
  On-call, the editor offers no action for it, the next message is still tried, and a delivered one clears the mark.
- **C-43.AC-7** A Broken Destination of the same Connection withholds no direct message, and no answer to a direct
  message makes a Destination, a Connection or a gateway Broken or raises an Internal alert.
- **C-43.AC-8** Of ten Pages queued for one person in one second, the fake messenger receives them at most one per
  `direct_message.person_spacing`, critical Alert Groups first, none dropped for waiting.
- **C-43.AC-9** With `organization.direct_messages` off, nothing is sent, stored actions are skipped with
  `direct_messages_disabled` in the Timeline, the editor shows the action unavailable, and the Audit log shows the change
  with its diff; turned on again, the next Page sends.
- **C-43.AC-10** A bridge that declares `direct_messages` without the Page support receives no Page and its links are
  offered no action; one that declares it receives the `layout`, the buttons and an `Idempotency-Key`, and `muster
  bridge check` checks the support.
- **C-43.AC-11** A step "Page a whole Team" and a Team mention make no direct message; a Route that names a chain and no
  Destination, whose people have direct-message rules, Pages them, and its warning reads as in FR-20.
- **C-43.AC-12** Rows and Pages stay with their Alert Group and show a deleted User pseudonymized; an unlinked
  account's messages are no longer edited; no message is deleted in the messenger; the rows hold no text; the Request
  log shows the person's id masked.
- **C-43.AC-13** In a real browser, 360 CSS pixels wide, the person adds the action in the editor, sees it in the
  preview and in words in the profile, and sees a blocked link's warning; an Admin turns the Organization's setting off
  and the editor shows the action unavailable, in English and in Russian.
- **C-43.AC-14** A person with two qualifying Account links is offered the link in the action; one action with one link
  uses it, and a link added later changes nothing; two actions with no wait between them send two messages at the same
  moment; a stored action whose link was deleted is skipped with `account_link_deleted`.
- **C-43.AC-15** `updateOrganization` with a direct-message action in the default rules gets `422` at the action with
  `direct_message_not_allowed`, and the editor of the default rules does not offer it; the action in a person's rules
  saves.
- **C-43.AC-16** Overdue direct-message actions of one run through one link, due together after a pause of Muster, send
  one message; through two links, two.
- **C-43.AC-17** An On-call whose rules include a direct message through a link that is not unreachable shows "A direct
  message works." on the Schedule page beside the text about calls.

## Settings

Rows of [defaults.md](../l1/defaults.md):

| Status | Setting | Value | Where |
|---|---|---|---|
| decided (P-97) | `organization.direct_messages` | on: the action "Direct message" is offered and sent; off: never sent, stored actions skipped | Organization |
| decided (P-98) | `direct_message.person_spacing` | 1 s between direct messages to one person through one Connection (Telegram's documented limit for one chat is about one message a second; Slack's for one channel is one a second); one private chat or direct chat took faster sends with no `429` in Telegram ([F-135](../../facts.md#private-chats)), Mattermost ([F-147](../../facts.md#rate-limit-and-server-settings)) and Slack ([F-154](../../facts.md#rate-limits)) | built in |
| P-99 | `direct_message.title` | the Alert Group's title, cut to 200 characters | built in |

P-99 is in the table of [L2 §5.3](../L2.md#53-provisional-values-and-behaviours), confirmed by the test environment;
P-98 was confirmed by the spike of this capability (F-135, F-147, F-154; journal D765), and P-97, the default of the
switch, was decided by the maintainer with this capability. The limiters of the
Connections stay as they are (`connection.telegram.limiter` 15 messages/s, `connection.mattermost.limiter`
5 requests/s, [F-030](../../facts.md#rate-limit-and-server-settings), `connection.slack.limiter` 50 requests/min,
[F-099](../../facts.md#rate-limits), `connection.bridge.limiter`); Telegram's documented 30 messages a second across
chats is above the Connection's 15 and needs no setting.

## What changes elsewhere

Made with this capability (journal D764): ADR-0018, ADR-0005 and ADR-0017 as below, each with an "Amended" line, and
their summaries in the ADR index; C-11.FR-2 and C-11.FR-3; the texts of C-31 and C-32 that said that Muster sends no
direct messages; CONTEXT.md's Page and Notification rules; L2.md §1.4, §1.5, §4, §5.1, §5.3 and §6; and the rows of
"Settings" in defaults.md. The rest
of this list is changed by the stories of this capability, each in the pull request that needs it, as for every
story: the specification first, the schema by a new migration, the fake servers, the lints and the other texts.

- **ADR-0018.** "Pages, not direct messages" becomes "Pages by Mention, by call and by direct message": "Muster Pages a
  person by a Mention in the Alert Group's Thread, by a call through a Notification gateway, or by a direct message from
  the bot, as the person's Notification rules say; the bot's direct messages also serve Account links and similar
  system messages." The paragraph on warnings about an unreachable On-call keeps its text and adds that a direct message
  through a qualifying Account link reaches a person that a Mention does not. The paragraph "Notification rules" lists
  direct messages among the actions. The rejected alternative "Paging by direct messages" is replaced by one that
  records the reasons then given and why they no longer hold: a direct message is neither a Root message nor a Thread
  reply, but the delivery worker sends it from a table of its own (ADR-0005 changes, below), the choice of Connection
  is the person's rule (the action names an Account link), and a person who blocked the bot is a reason of the Page. The
  Context bullet "A direct message that wakes a person is neither, and nobody waits for it" is revised in the same way,
  and an Amended line is added. The rejected "Direct messages about Shifts and Overrides, and a bot command that says
  who is on call" stays rejected.
- **ADR-0005**, four places: "Who calls messenger APIs" says that the delivery worker also sends and edits the direct
  messages of Pages, from `direct_messages`; the last sentence of "Notification gateways (L2)" (which keeps calls
  outside reconciliation) names direct messages as the other non-reconciled send; the Amended line of 2026-10-26 gets a
  new line; and the rejected alternative "Direct messages for Pages" is revised as in ADR-0018.
- **ADR-0017**, the line that says direct messages go through the interactive path: they still do for Account link codes
  and answers, and a Page by direct message through a bridge is sent by the delivery worker.
- **C-11.FR-2** (made) gained "_Revised for L2 (2026-11-22):_": a Page by direct message is sent and edited by the
  delivery worker from the table `direct_messages` (C-43.FR-9); the interactive path still serves only what a person
  waits for. The Account link messages, answers and system messages stay on the interactive path.
- **C-11.FR-3** (made) gained "_Revised for L2 (2026-11-22):_" with the rank of C-43.FR-9: the sends of direct
  messages after the Root-message work of Urgent Alert Groups and before the configuration edits of Urgent Alert
  Groups; their final edits with the Thread replies.
- **C-13, C-14, C-22, C-24, response mappings.** Each adds the rows for a direct message: Telegram's `403` "bot was
  blocked by the user" to `bot_blocked`, "user is deactivated" and a missing chat to `account_unavailable`, "can't
  initiate conversation" to `direct_messages_not_accepted`; Slack's `messages_tab_disabled` and `cannot_dm_bot` to
  `direct_messages_not_accepted`, `user_not_found`, `channel_not_found` of a direct message (F-152) and deactivated
  users to `account_unavailable`; Mattermost's refused channel creation to `direct_messages_not_accepted`, and a person
  whose `delete_at`, read before the send, is above 0 to `account_unavailable` (F-145); a bridge's `forbidden` and its
  new problem codes likewise. The
  existing mapping of Telegram's "bot was kicked" to Fatal stays for chats of Destinations. C-22.FR-12, which binds a
  press to a Root message or a Thread reply of a Destination, also binds it to a direct message; C-14.FR-7's holds name
  the person's chat.
- **C-31:** made — the goal and the UI got `_Revised for L2 (2026-11-22):_` markers: a rule may hold direct messages,
  only the person's own rules hold them, and the UI drops "Muster sends no direct messages."; by the stories — FR-1 adds
  "Direct message" to the actions and says the Organization's rules cannot hold it; FR-3 adds it to the run; FR-6 says
  that a Mention reaches a person only through an Account link in the Identity space of the Destination's Connection
  and a direct message through any qualifying Account link; scenarios and AC-1 keep their defaults.
- **C-32:** the goal and FR-3's list of steps are unchanged; the table of FR-11 gains a row, "Direct message — a message
  from the bot in a private chat, with buttons — the person has an Account link in a messenger that qualifies and the
  messenger can reach them"; FR-10 adds that a direct message that ends Not delivered records `page_not_delivered` with
  the reasons of C-43.FR-17 and writes nothing in the Thread; FR-12 counts a delivered direct message as reaching a
  person; FR-17's warning, FR-19's `kind` and FR-20's statement that only a gateway is reliable at night are revised
  (C-43.FR-18, FR-20, FR-25).
- **C-41:** FR-2 adds the action to the list; FR-3 the availability of C-43.FR-4; FR-4's check text "A rule needs a
  Mention or a call" gains the direct message; FR-5 the preview line "a direct message in {messenger}"; AC-1 and AC-3 a
  case each; FR-8 is unaffected, and FR-9's Organization editor does not offer the action.
- **C-40:** FR-6's "Who was Paged" counts direct messages too — a count of its own beside "Paged" and "Calls", and
  "Pages of both kinds" becomes "Pages of every kind" — through S-223 and S-224 or a story of C-43.
- **C-42:** FR-8's accelerated run names a direct message as it names the other actions of a rule: "would send a
  direct message to @{login} in {messenger}", or why it would not.
- **C-27.FR-8 / S-164:** the warnings of FR-19 and FR-20.
- **C-18:** FR-4 notes that an Account link is also where a direct message goes, FR-6 that unlinking stops the editing of
  its messages, and the profile lists an unreachable link; `direct_unreachable_at` and `direct_unreachable_reason` are
  columns of the Account link.
- **C-20:** `organization.direct_messages` joins the Organization's settings.
- **L2.md:** made — §1.4's table and §1.5's list gained C-43; §4 "Removed from the plans of L2" no longer removes
  "Paging by direct messages" and keeps "messages about Shifts and Overrides"; §5.1 gained the spike; §5.3 gained P-98
  and P-99, and P-97 is among the decided ones; §6 names C-11.FR-2 and C-11.FR-3. By the stories: §3 NFR-12 names the
  additive bridge capability and NFR-7 the messages to people's private chats, which carry alert titles, and §6 the
  other revisions above.
- **reference.md:** the rows of `paged` and `page_not_delivered` of the L2 lifecycle events name the direct message;
  `muster_pages_total`'s `kind` gains `direct_message`; the Route form's warning for a chain and no Destination reads as
  in FR-20.
- **`api/openapi.yaml`:** the `EscalationSkipReason` enum gains the skip reasons of FR-5; the Page kinds and reasons;
  the action of `NotificationRuleSet`; `organization.direct_messages`; the account link read.
- **CONTEXT.md:** see "Glossary changes". **AGENTS.md:** the lint-3 row names the direct message as a send that only the
  delivery worker makes.
- **`api/bridge.yaml`** (prose only). `capabilities.direct_messages` is declared today by a bridge that can send direct
  messages for Account link codes, with `bot_can_start`. The capability stays and gains an optional boolean `pages`: the
  bridge accepts a direct message with the structured `layout` of C-29, with `buttons` where it declares them, and
  answers the id of the message. `sendDirectMessage` already takes an `Idempotency-Key` and answers `message_id` and
  `replayed`; it gains the optional `layout`, `buttons` and signed action data (within 64 bytes, as the buttons of a Root
  message) and a problem code that separates a blocked bot from a refusal; a new operation `editDirectMessage` edits the
  message; `press` events carry the direct chat and the message id of a press made in a direct message. A bridge with
  `bot_can_start` false is offered no Page for a person who never wrote to it. All additive in version 1 (ADR-0017); the
  bridge libraries support the Page in releases of their own; `muster bridge check` checks it.
- **Fake servers.** The fake Telegram, Mattermost, Slack and bridge servers record direct messages with their text,
  buttons and edits, take presses on them, and take scripted faults at `/_fake/faults` for the bot blocked, the chat
  not found, direct messages refused and rate limits; the fake Slack and Mattermost servers have the people of the
  development data with a direct channel; the fake Telegram server gains a way to mark a person as having blocked the
  bot.
- **Schema.** `direct_messages` with a `public_id` prefix (expand migration), named in the table of schema.md §2 under
  the owner that writes it (delivery); the kind `direct_message` and the reasons of FR-17 in `pages` and `page_users`;
  `direct_connection_id` on `pages`; the two columns on account links; `organization.direct_messages`; the action kind
  in the stored Notification rules; schema.md §4.18 and the table's claims for `pages`.
- **Architecture lints.** Lint 3 allows `delivery.Adapter` sends of a direct message in the delivery worker only, and
  the interactive path's answers as today.

### Glossary changes

For `CONTEXT.md`: the extensions of Page and Notification rules, naming direct messages, are made with this
capability; the term Direct message joins it with the first story of C-43, which also writes it as a term where the
documents name it.

- **Direct message**: A message that Muster's bot sends to one person in a private chat of a messenger — to Page them,
  as their Notification rules say, or to answer an Account link; never a Root message or a Thread reply. _Avoid_: DM,
  Private message, Notification (on its own).
- **Page**, extended: "A call for attention made by an Escalation step: a Mention in the Alert Group's Thread, a
  direct message from the bot, or a call through a Notification gateway."
- **Notification rules**, extended: "such as a Mention in the Thread together with a direct message or a call through a
  Notification gateway".
- No word under an existing _Avoid_ is used; "Notification" alone stays reserved for the gateway's.

## Related ADRs

ADR-0004, ADR-0005, ADR-0013, ADR-0016, ADR-0017, ADR-0018.

## Depends on

C-09 — the title and the Timeline; C-10 — the dispatcher and the Commands of presses; C-11 — classification, limiters
and the delivery worker; C-13, C-14, C-24 — the bots of Mattermost, Telegram and Slack and their presses; C-18 —
Account links; C-20 — the Organization's settings; C-22 — the Bridge protocol; C-23 — the Request log and its masking;
C-27 — the warnings about unreachable On-calls; C-30 — the analogy and the Pages of calls; C-31 — Notification rules;
C-32 — Escalation chains, Pages and Assignees; C-41 — the editor, without which a person can set the action only
through the API.

## Suggested story split

- **Spike** — the facts below on the test stands, with a person's phone; comes before the stories that rely on them.
- **BE** — the action in Notification rules and the API, the Page kind, the reasons, `organization.direct_messages`, the
  run semantics of FR-5, with a fake sender.
- **BE** — `direct_messages`, the delivery worker's sending, the rank, the spacing, the single edit, Telegram.
- **BE** — Mattermost and Slack senders and their presses, with the bound presses and the refusals of FR-15.
- **BE** — the Bridge protocol extension, the fake bridge and `muster bridge check`; **in the bridge libraries** — the Page.
- **BE** — the unreachable state of Account links, the warnings of Schedules and chains.
- **FE** — the action in the editors and the profile, the Account link states, the Organization's switch, the Timeline.
- **docs** — the pages and the revisions above.

## Facts to verify

Verified facts are cited; the rest are checked by the spike that comes first in the story split, on the test stands
with a person's phone where it matters, before the stories that rely on them, and recorded in
[facts.md](../../facts.md) (the Pending list gains them).

**Telegram.**
- Verified: `disable_notification` still pushes without sound, an edit never pushes
  ([F-012](../../facts.md#notifications)); an edit without a keyboard removes the buttons
  ([F-011](../../facts.md#buttons)); a press must be answered within about 15 seconds
  ([F-010](../../facts.md#buttons)); inline buttons take a style ([F-066](../../facts.md#buttons)); a bot edits its own
  messages at any age in a channel and a group ([F-065](../../facts.md#limits)); a group's budget is about 20 sends and
  edits a minute ([F-016](../../facts.md#limits)).
- To verify: `sendMessage` to a person who pressed Start and then blocked the bot answers `403 Forbidden: bot was blocked
  by the user` (and what a deactivated account and a deleted chat answer), and to one who never started `403 Forbidden:
  bot can't initiate conversation with a user` — with the test bot and two test accounts; whether an inline keyboard and
  `callback_query` work in a private chat as in a group, with `message.chat.type` `private`; whether a forwarded copy of
  a message keeps its buttons (scenario 6); whether a bot edits its own private messages at any age (F-065 covers a
  channel and a group); the budget of one private chat (about one message a second, bursts), whether edits count in it
  as in F-016, and the global budget (about 30 a second), by bursts against two accounts; whether the push of a private
  message rings a phone with the chat muted and in Do-not-disturb.

**Mattermost.**
- Verified: `POST /api/v4/channels/direct` with the bot's and a user's ids returns their direct channel, and a bot post
  there reaches a user the bot knows only by id ([F-028](../../facts.md#threads-and-direct-messages)); an edit never
  notifies ([F-029](../../facts.md#edits-and-notifications)); the rate limit is off by default, counted per client address
  ([F-030](../../facts.md#rate-limit-and-server-settings)); `PostEditTimeLimit` is `-1` by default
  ([F-032](../../facts.md#rate-limit-and-server-settings)).
- To verify: a bot's interactive attachment with buttons in a direct channel is shown, and a press calls the integration
  URL with the person's id and the post in a `D`-type channel (F-054 and F-055 are presses through the API, not a
  person's, and on root posts and Thread replies; Muster's press binding today looks for a Root message); whether the
  post notifies the person's phone under `PushNotificationContents` `full` and `generic`; what the direct channel
  answers when the bot lacks the permission to create it or the person is deactivated; whether a bot with the role
  Member may edit its own direct post.

**Slack.**
- Verified: `conversations.open {users}` with the scope `im:write`, `chat.postMessage` to the `U…` id or the `D…`
  channel, the Messages tab read-only turning input off, `chat.delete` in the direct chat
  ([F-096](../../facts.md#users-and-direct-messages)); `chat.postMessage` allows about one message a second per channel
  and `chat.update` is tier 3 ([F-099](../../facts.md#rate-limits)).
- To verify: the real `block_actions` payload of a press (F-093 is from the documentation only, no press was seen live,
  and the Pending list holds it); what `chat.postMessage` answers to a user id with the Messages tab off
  (`messages_tab_disabled`, already Pending); Block Kit buttons in the app's direct chat with the tab read-only, a press
  arriving with `channel.id` `D…` and a `response_url` that answers in that chat; whether `chat.update` of a direct
  message notifies, and the workspace's edit window (F-084, Pending); whether a message of the bot to a person in
  Do-not-disturb pushes; the budget of `chat.postMessage` to many different direct chats at once, since the
  Connection's 50 requests a minute is the documented floor of `chat.update`, not of `chat.postMessage`.

**Bridges.**
- Verified: the capability `direct_messages` with `bot_can_start` and `sendDirectMessage` (with an `Idempotency-Key`,
  `message_id` and `replayed`) exist for Account link codes (`api/bridge.yaml`), and `forbidden` is the answer for an
  account that cannot receive a message from the bot.
- To verify: the fake bridge and the bridge libraries carry the extension; a reference bridge for one messenger takes a
  layout, buttons and an edit in a direct chat; whether an edit of a bridge's message notifies.

**All messengers.**
- Whether a direct message pushes to a phone reliably, which the Pending spike on Mentions in Threads (S-160) does not
  cover: the spike is extended to direct messages in the three messengers, to settle whether the documentation may say
  that a direct message is reliable at night.
