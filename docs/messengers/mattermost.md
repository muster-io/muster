# Connect Mattermost

Muster posts to Mattermost through the REST API with a **bot account**: one post per Alert Group, the **Root
message**, which Muster edits as the Alert Group changes, and replies in its **Thread** for what happens later. People
act on the Alert Group with the buttons of the post. Muster uses no incoming webhook and no plugin.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org`, `MUSTER_INGEST_URL` is
`https://ingest.muster.example.org` and the Mattermost server is `https://chat.example.org`.

## Create the bot account

1. In the System Console, under **Integrations → Bot Accounts**, turn on **Enable Bot Account Creation**.
2. In **Integrations → Bot Accounts**, add a bot, for example `muster`. Give it the role **Member**: Muster needs no
   system admin role and no **post:all** or **post:channels** permission, because it posts only to channels the bot
   is a member of. With the role Member, the answers to button presses show in the Thread of the Root message; see
   [Where answers to presses show](#where-answers-to-presses-show).
3. Copy the bot's access token. Mattermost shows it once.
4. Add the bot to the team, and to every channel a Destination posts to. A public channel is enough; a private channel
   works the same once the bot is a member.

The bot token works even when **Enable Personal Access Tokens** is off, its default.

In Muster, create a **Connection** of type Mattermost with the server's address and the bot token, then **Check** it:
the check reads the bot's own user with the token and shows the bot's name. Then create a **Destination** for each
channel; saving it checks that the bot is a member of the channel and refuses the Destination otherwise, naming the
failing check.

## Direct messages

Account links send a person a direct message from the bot: the bot opens the direct channel with the person's user id
and posts there. Make sure your server lets the bot open direct messages with the people who link their accounts — for
example, if **Enable users to open Direct Message channels with** is restricted to members of a team, add the bot to
the teams of those people.

## Let button presses reach Muster

A press on a button makes the Mattermost server call Muster at the Connection's callback address, which the Connection
page shows:

```text
https://ingest.muster.example.org/api/v1/callbacks/mattermost/CN…
```

When that address resolves to a private or otherwise reserved address — the usual case inside one network — the
Mattermost server refuses to call it unless the host is listed in **ServiceSettings.AllowedUntrustedInternalConnections**
(**Environment → Developer → Allow untrusted internal connections to** in the System Console). Add the host of
`MUSTER_INGEST_URL`, for example:

```json
"ServiceSettings": {
  "AllowedUntrustedInternalConnections": "ingest.muster.example.org"
}
```

Without it, a person who presses a button sees only a generic **Action integration error**; only the Mattermost server
log says why ("address forbidden … in a reserved range and not in AllowedUntrustedInternalConnections"). The test
message of a Destination presses its own button through the bot and tells you whether the press reached Muster.

Whatever the person who pressed must read — that the press was refused, that their Mattermost account is not linked
to Muster yet, or that the button could not be verified — arrives as a message only they can see. A press from an
account without an Account link changes nothing. A request that does not come from a button of a Muster post in one of
the Connection's channels gets no answer at all.

### Where answers to presses show

Muster first sends the answer as an ephemeral message, which shows in the channel, marked "(Only visible to you)".
Mattermost allows that only with the `create_post_ephemeral` permission, which by default only the system admin role
has. Without it, Mattermost refuses the ephemeral message, and Muster returns the same text in its answer to the press:
Mattermost then shows it from **System** in the **Thread** of the Root message. With collapsed reply threads,
Mattermost's default, it is not shown in the channel view; the person opens the Thread to read it. Either way the answer
is never lost.

**Check connection** tells you which applies: when the bot's roles do not grant `create_post_ephemeral`, the check
passes with a warning that answers will show in the Thread of the Root message, and `muster doctor` prints the
Connection as a `WARN` line. To show answers in the channel, give the bot that permission, for example the system admin
role. Otherwise keep the role Member and let people read the answers in the Thread.

## Rate limit

Mattermost's rate limit is off by default. When an admin turns it on (`RateLimitSettings.Enable`, which takes a server
restart), it allows 10 requests per second, with a burst of 100, **per client address**. All Connections of one Muster
installation to the same Mattermost server usually come from one address and share that bucket, so keep their limiters
together below it. The default limiter of a Connection is 5 requests per second. A `429` from Mattermost holds every
request of the Connection, to any of its Destinations, for the seconds of its `Retry-After`.

## Keep posts editable

Muster edits a Root message for as long as its Alert Group is open, which can be days. Keep
**ServiceSettings.PostEditTimeLimit** at `-1`, its default, so that posts can be edited at any age.

## Who gets notified

- **Edits notify nobody**, even an edit that adds a Mention. That is why every Loud event is a new post: a new Root
  message, or a reply in the Thread.
- **Thread replies notify the Thread's followers** by their own notification settings, and a Mention makes the person
  mentioned a follower of the Thread. "Quiet" therefore means "without a Mention", not "nobody notified".
- A Loud post carries its Mentions — `@channel`, `@here`, `@all`, a Mattermost group, or a person's `@username` through
  their Account link — in the post's text, never in the attachment, so that the notification shows what happened. A
  `{{ mention "channel" }}` in a Route's template shows in the attachment as text and, on a Loud post, adds its Mention
  to the post's text.
- Alert data never mention anyone: an `@` in a label or annotation is shown as text and notifies nobody. The footer of
  a Root message names a person by their username without `@`, which does not notify them either.

## Check from the command line

`muster doctor` prints one line per Mattermost Connection and per Mattermost Destination with the same checks, without
sending anything.
