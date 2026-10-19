# Loud and Quiet

Every change to an Alert Group is either **Loud** or **Quiet**. A Loud change should make people's devices ring. A Quiet
change should only update what they see when they look. Muster fixes the loudness of every change; you decide only whom
a Loud change **Mentions**.

The rule behind it is simple, and it is the same in Mattermost and Telegram: **an edit never notifies anybody.** Editing
a message, even to add a Mention, does not ring a phone. So a Loud change is always a **new message**, a new Root message
or a reply in the Thread, and a Quiet change is an edit of the Root message or a new message without a Mention.

## Which changes are Loud

| Change | Loud or Quiet | Mentions |
|---|---|---|
| A new Alert Group starts | Loud, as a new Root message | the Destination's `new_alert_group` setting |
| New Alerts join a **firing** Alert Group | Loud, as a Thread reply; batched | `new_alerts` |
| New Alerts join an acknowledged or snoozed Alert Group | Quiet | nobody |
| A Reopen into firing | Loud | `reopen` |
| A Reopen into acknowledged | Loud | the Owner only |
| A Reopen into snoozed | Quiet | nobody |
| A Snooze ends while Alerts still fire | Loud, listing what accumulated | `snooze_ended` |
| A rise to Urgent that ends a Snooze or removes an acknowledgement | Loud, with the reason | the Owner it removed, and `rise_to_urgent` |
| A rise in Severity level that changes nothing else | Quiet | nobody |
| A Takeover | Loud | the previous Owner |
| An ack timeout notice | Loud | `ack_timeout` |
| A Reminder | Loud | the Owner |
| Auto-unacknowledge | Loud, with the reason | the Owner it removed |
| The Owner is disabled or deleted | Loud | nobody |
| Several notices missed during downtime | Loud, as one reply | as the notices it stands for |
| Acknowledge, Unacknowledge, Snooze, Unsnooze, Resolve, Unresolve | Quiet | nobody |
| An Alert resolves, is replaced or continues, or its annotations change | Quiet | nobody |
| The system resolves the Alert Group | Quiet, with a Thread reply that gives the reason | nobody |
| A Note | Quiet, and not sent to messengers | nobody |

The pattern: Muster rings when something needs a human who may not be looking, such as a new problem, one that returned,
one that nobody took, or one that someone took and went quiet. It does not ring for what a person just did, because that
person is looking. The Takeover is the only result of a person's command that is Loud, because the previous Owner has
just lost the Alert Group.

Delivery adds a few cases of its own:

| Delivery event | Loud or Quiet |
|---|---|
| A Destination added to a Route receives its open Alert Groups | Quiet |
| A Destination recovers from [Broken](broken-destinations.md): an Alert Group still firing is published | Loud |
| The same recovery, for an Alert Group that is not firing, or an edit of a message already there | Quiet |
| A message published late, after the Alert Group already resolved | Quiet, with a note |
| A Root message posted again because it was deleted | Quiet, with a note |
| A Storm summary, when it first appears | Loud |
| A Storm summary's updates, and the Alert Groups posted after the Storm | Quiet |

You cannot change the loudness of an event. You can change whom it Mentions.

## What Loud means in each Destination

| Destination | A Loud change is | A Quiet change is |
|---|---|---|
| Mattermost | a new post, with its Mentions in the text of the post | an edit, or a new post without a Mention |
| Telegram | a new message, in the channel for a Root message and in the discussion group for a Thread reply | an edit, or a new message sent with `disable_notification` |
| Bridge | a new message that the bridge posts with `notify` on, with its Mentions | an edit, or a new message that the bridge posts without a notification where it declares `quiet_messages` |
| Outgoing webhook | an event with `notify` true (events mode), or `.Notify` true in a template (template mode) | `notify` false |

A few details:

- **Mattermost.** A reply in a Thread notifies the Thread's followers by their own settings, and a Mention makes the
  person a follower. So Quiet means "without a Mention", not "nobody is notified". Mentions are in the text of the
  post and never in the attachment, because a Mention in an attachment notifies with an empty notification. See [Who
  gets notified](../messengers/mattermost.md#who-gets-notified).
- **Telegram.** `disable_notification` silences the sound but does not remove the push. Only members of the channel's
  discussion group get Thread replies, and the automatic copy of every Root message rings them, even for a Quiet one.
  Tell the people on call to join the group and mute the channel itself. See [Known limitation: Quiet Root messages ring
  the group](../messengers/telegram.md#known-limitation-quiet-root-messages-ring-the-group).
- **Bridge.** Whether the bridge can post without a notification depends on the bridge. A bridge that does not declare
  `quiet_messages` posts a Quiet message as an ordinary one, without Mentions.
- **Outgoing webhook.** Muster rings nothing itself. In events mode every event is sent, Quiet ones too, with `notify`
  telling what a person-facing system should do with it. Your receiver decides, for example, to call a phone only when
  `notify` is true. See [events mode](../outgoing-webhooks/events.md#event-names) and [Recipes](../outgoing-webhooks/recipes.md).

## Mentions

A **Mention** is a deliberate @-reference that Muster adds to a message so that a person is notified. Muster adds a
Mention only where this page says it does. Alert data never mention anyone: an `@` in a label or an annotation is
neutralized with a zero-width space, so `@channel` in a label notifies nobody.

### Whom a Loud change Mentions

Each Destination has a **Mention setting** for each of six kinds of Loud change:

- `new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`, `snooze_ended` and `rise_to_urgent`.

For each, the Destination chooses **nobody** (the default for all six), **everyone in the chat**, **chosen Muster users**,
or **messenger groups**. What a Destination type offers:

| Destination | Nobody | Everyone in the chat | Chosen users | Messenger groups |
|---|---|---|---|---|
| Mattermost | yes | `@channel`, `@all` or `@here` | yes | Mattermost group names |
| Telegram | yes | no | yes | no |
| Bridge | yes | no | yes | no |
| Outgoing webhook | yes | yes, as data | yes, as data | yes, as data |

Some Mentions are not a setting. A **Reminder** and an auto-unacknowledge always Mention the Owner, a **Takeover**
Mentions the previous Owner, and a Reopen into acknowledged Mentions only the Owner.

### Account links

A Mention of a user needs an **Account link**: the tie between a Muster user and their account in the messenger. The
people who are Mentioned, and the Owner above all, link their accounts from their profile; see [Link your messenger
accounts](../messengers/account-links.md). Muster renders a Mention in the messenger's own form:

- Mattermost: `@username`;
- Telegram: a link to the person, whose text is the display name;
- Bridge: a marker that a bridge with `native_mentions` renders as the messenger's mention and any other bridge as the
  person's name.

**A user without an Account link is named by their display name as plain text, which notifies nobody.** So choosing a
user for a Mention setting, or being an Owner, rings a phone only once that person has linked their account. The footer
of a Root message names a person by their messenger username without `@`, which does not notify them either.

For an outgoing webhook, Muster does not know the receiver's markup, so the Mention is data: the event's `mentions`
list, and `.Mentions` in a template, with the target's type, id, name and login. Your receiver or your template turns
it into whatever the receiving system understands.

## Storm summaries

When many Alert Groups start at once, one Route can flood a Destination. A **Storm** is a Route creating more new Alert
Groups in one minute than its Storm threshold (`route.storm_threshold`, 20 by default). Then each of the Route's
Destinations gets one **Storm summary** instead of a message per Alert Group:

- The summary is Loud only when it first appears, and Mentions as a new Alert Group does (`new_alert_group`).
- **Urgent** Alert Groups are still posted on their own, first.
- Its updates and its final state are Quiet. When the rate has stayed below the threshold for 5 minutes
  (`delivery.storm_calm_period`), the summary shows its final state, and the Alert Groups that are still open are
  posted gradually and Quietly. Those that already resolved are not posted.
- Messengers and the template mode of outgoing webhooks follow Storms. The events mode does not: it receives every event.

## Why the rule is fixed

A rule of thumb for on-call: the phones should ring for problems that need a human, and nothing else. Because an edit is
always silent, a person can acknowledge, snooze and resolve all day, and the message changes under their eyes without a
sound; and because the loudness of every change is fixed, a team never has to wonder whether a change will ring. The
Mention settings are the one place to tune who rings.
