# Messengers

Muster delivers Alert Groups to **Destinations**. A Destination is a place where Alert Groups arrive: a channel of a
messenger, or an outgoing webhook. There are four types:

- **Mattermost**, through a bot account on your Mattermost server;
- **Telegram**, through a bot, to a channel with comments;
- **Bridge**, for any other messenger: a small service that you or someone else writes speaks the Bridge protocol with
  Muster and talks to the messenger;
- **Outgoing webhook**, which sends the lifecycle events of Alert Groups, or requests you build from templates, to any HTTP
  endpoint.

A **Connection** holds the credentials Muster uses to reach one messenger: a bot on a Mattermost server, a Telegram bot, or a
Bridge. Many Destinations can share a Connection. An outgoing webhook has no Connection; the Destination carries its own
address, Secrets and proxy.

To start, create the Connection, check it, create a Destination for a channel, and add the Destination to a Route. A Route
decides which Alert Groups reach which Destinations; see [Routes and Group keys](../concepts/routes-and-group-keys.md).

## What each Destination type can do

Every cell is true for this release. A cell that depends on the bridge says "if the bridge declares it": a Bridge offers
only what its `info` announces, and Muster adapts to the rest. [Write a bridge](write-a-bridge.md) lists the features.

| | Mattermost | Telegram | Bridge | Outgoing webhook, events mode | Outgoing webhook, template mode |
|---|---|---|---|---|---|
| Needs a Connection | yes | yes | yes | no | no |
| One message per Alert Group, kept up to date | yes | yes | yes | no: one request per event | yes, through your requests |
| Edits of the Root message | yes | yes | yes, until the bridge's edit window | no | yes, through the `update` request |
| Buttons | yes | yes | if the bridge declares `buttons` | no | no |
| Without buttons | the web UI and the API | the web UI and the API | [Text commands](../concepts/statuses-and-commands.md#text-commands) in the Thread if the bridge declares `thread_messages`; otherwise the web UI and the API | the web UI and the API | the web UI and the API |
| Thread | replies under the Root message | the comments of the Root message, in the channel's discussion group | replies under the Root message | no: each event is separate | optional `reply_in_thread`, with an optional `open_thread` |
| Loud | a new post with Mentions | a new message | a new message that the bridge posts with `notify` | `notify` is true | `.Notify` is true |
| Quiet | an edit, or a post without a Mention | an edit, or a message sent with `disable_notification` | an edit, or a message without a notification if the bridge declares `quiet_messages` | `notify` is false | `.Notify` is false |
| Mentions | nobody, `@channel` `@all` `@here`, users, Mattermost groups | nobody, users | nobody, users | nobody, everyone, users, groups, as data | the same, as data |
| Account links | yes: a code the bot sends by direct message | yes: a link that opens the bot | yes: a token sent to the bot, and a code if the bridge can find people and send direct messages | not needed: no one presses anything | not needed |
| Rate limits | Connection 5 requests/s; Destination the same by default | Connection 15 messages/s; Destination 10 requests/min, sends and edits together | Connection 10 requests/s, Destination 30 requests/min, or half of what the bridge's info declares | Destination 5 requests/s | Destination 5 requests/s |
| Edit window | none by default; the server setting `PostEditTimeLimit` is unlimited, so keep it | none for the bot's own messages | the bridge declares it, or none; Muster publishes a new Root message past it | not applicable | the chat's own; Muster does not know it |
| Storms | one Storm summary per Route | one Storm summary per Route | one Storm summary per Route | every event is sent, no summary | one Storm summary |
| Proxy | on the Connection | on the Connection | on the Connection | on the Destination | on the Destination |
| Destination check | yes | yes | yes, `checkChannel`, which posts nothing | no | no |
| Test message and preview | yes | yes | yes | yes, sends the event `test` | yes, sends the `create` request |

Notes on the cells:

- **Rate limits.** These are the defaults of the limiters, and you can change them. Mattermost's own rate limit is off by
  default; when an administrator turns it on, it allows 10 requests per second per client address, so keep the limiters of
  all Connections to one server below that together. A Telegram chat takes about 20 messages and edits per minute, which is
  why its Destination limiter is half of that. A bridge says its own limits in its info, and its `rate_limited` answers
  always win. A `429` from a messenger holds the Destinations that share it, for as long as it asks.
- **Account links.** Pressing a button as a Muster user, and being Mentioned, need an Account link. See [Link your
  messenger accounts](account-links.md).
- **Loud and Quiet.** Edits never notify anybody, so every Loud change is a new message. See [Loud and
  Quiet](../concepts/loud-and-quiet.md).
- **Edit windows.** See [Broken Destinations](../concepts/broken-destinations.md#republishing-past-an-edit-window).
- **Destination check.** It asks the messenger whether the bot can still post, without posting. Muster runs it when you save
  a Destination, from **Check**, and as the probe of a [Broken Destination](../concepts/broken-destinations.md) when nothing
  waits. An outgoing webhook has no Destination check, so a Broken one is probed by its next delivery.

## Pages

- [Connect Mattermost](mattermost.md): the bot account, the setting that lets the server call Muster for button presses,
  and who gets notified.
- [Connect Telegram](telegram.md): the bot, a channel with comments, long polling or a webhook;
  [Telegram in restricted networks](telegram-restricted-networks.md) for networks where it is blocked.
- [Connect a messenger through a bridge](bridge.md), and [Write a bridge](write-a-bridge.md) for a messenger Muster does not
  support.
- [Outgoing webhooks](../outgoing-webhooks/events.md): the [events mode](../outgoing-webhooks/events.md), the [template
  mode](../outgoing-webhooks/templates.md) and some [recipes](../outgoing-webhooks/recipes.md).
- [Proxies](proxies.md): the proxy of each client, and what the outbound address policy checks.
- [Link your messenger accounts](account-links.md): how people tie their messenger accounts to their Muster user.
- [Request log](../operations/request-log.md): the recent requests Muster sent to a messenger, a bridge or an outgoing
  webhook, with secrets masked, for troubleshooting.
