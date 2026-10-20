# Connect Slack

Muster posts to Slack through the Web API with the **bot** of a Slack app that you create from a manifest and install in
your workspace: one message per Alert Group, the **Root message**, which Muster edits as the Alert Group changes, and
replies in its **Thread** for what happens later. People act on the Alert Group with the buttons of the message and its
**Snooze…** menu. Muster uses no incoming webhook, no Events API subscription and no user token.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org` and `MUSTER_INGEST_URL` is
`https://ingest.muster.example.org`.

## Socket Mode or HTTP

A button press reaches Muster in one of two **event modes**, set on the Connection (`connection.slack.event_mode`).
Choose before you create the app, because the app's manifest differs.

Socket Mode
:   The Leader holds a WebSocket connection to Slack, so Slack needs no address of Muster: Muster only calls out. It is
    the way when Muster has no address that Slack can reach. It needs the app-level token. Only the Leader holds the
    socket, so a press made while the Leader changes is lost: the person sees Slack's error, and Muster does not count
    on Slack to send it again. A handover takes well under a minute; see
    [High availability](../operations/high-availability.md).

HTTP
:   Slack posts every press to the Connection's callback address, which the Connection page shows:

    ```text
    https://ingest.muster.example.org/api/v1/callbacks/slack/CN…
    ```

    Slack must reach this address over HTTPS. Any replica receives the press, so no press is lost while the Leader
    changes. It needs the app's Slack signing secret: Muster verifies the signature of every press with it, and
    refuses a request whose timestamp is further than `slack.request_tolerance` (5 minutes) from its own clock, so keep
    the clocks of Muster's nodes right. A request that fails any check is answered `401` with the same body, which
    reveals nothing about which Connections exist.

    Expose only `/api/v1/callbacks/` on the address that Slack reaches, and nothing else of the ingest listener, for
    example with a reverse proxy that forwards that path alone to the ingest port. See
    [The two public addresses](../install/helm.md#the-two-public-addresses).

A Slack app sends its presses to the sockets alone while its Socket Mode is on, and over HTTP alone while it is off, so
the app and the Connection must agree: a Connection receives presses only in its own mode.

### One Slack app per Muster installation

Slack hands each press to any one of the app's open socket connections. Two Muster installations in Socket Mode on one
Slack app would therefore share its presses at random, and a press made in one would reach the other. Create a Slack
app for each installation, a test copy included.

Muster notices it. When Slack tells the Leader that the app has more socket connections than the Leader holds, another
program is connected to the same app, usually a second installation or a script: the **Check connection** of a
Connection in Socket Mode passes with the warning `another_client_connected`, the Connection page shows the same
warning, `muster doctor` prints a `WARN` line, and the Leader logs
[`slack_socket_shared`](../reference/log-events.md#slack_socket_shared). Presses go on working, but some of them are
lost to the other program. Right after a Leader crashed, Slack may count its old connection for a while; the count
is taken again at every connection, so the warning goes away by itself.

## Create the app from a manifest

1. Open [Your Apps](https://api.slack.com/apps) in Slack, select **Create New App → From a manifest**, choose the
   workspace and paste one of the manifests below, in YAML.
2. Check the summary Slack shows and create the app.

### The manifest for Socket Mode

```yaml
display_information:
  name: Muster
  description: Muster alert groups
features:
  app_home:
    home_tab_enabled: false
    messages_tab_enabled: true
    messages_tab_read_only_enabled: true
  bot_user:
    display_name: Muster
oauth_config:
  scopes:
    bot:
      - chat:write
      - channels:read
      - groups:read
      - im:write
      - users:read
      - users:read.email
settings:
  interactivity:
    is_enabled: true
  org_deploy_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
```

### The manifest for HTTP

It differs from the first in the Interactivity Request URL, the callback address of the Connection, and in Socket Mode,
which is off:

```yaml
display_information:
  name: Muster
  description: Muster alert groups
features:
  app_home:
    home_tab_enabled: false
    messages_tab_enabled: true
    messages_tab_read_only_enabled: true
  bot_user:
    display_name: Muster
oauth_config:
  scopes:
    bot:
      - chat:write
      - channels:read
      - groups:read
      - im:write
      - users:read
      - users:read.email
settings:
  interactivity:
    is_enabled: true
    request_url: https://ingest.muster.example.org/api/v1/callbacks/slack/CNAGK7M3QX9P2R
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
```

The Connection page shows its callback address only once the Connection is saved, and saving it needs the bot token of
the installed app. Create the app with the address above, create the Connection, then copy its callback address into
**Interactivity & Shortcuts → Request URL** of the app.

### What the manifest sets

The app and its bot are named **Muster**. The bot posts under the name in the app's settings: Muster does not ask for
`chat:write.customize`, so a name that a message carries would be ignored. To use another name, change both names in
the manifest.

The Messages tab is on and read-only
:   Account links send a person a code by direct message from the bot, and the direct messages of an app show in its
    Messages tab. Read-only means that the person reads the code and cannot write to the bot. Keep the tab on: with it
    off, Slack refuses the bot's direct message, and the request for a code fails with the problem
    `messages_tab_disabled` (`409`). A refused request still counts toward the limits of Account links, 5 requests an
    hour for a person and 3 for one Slack account (`account_link.slack_code_requests`), so after you turn the tab on
    again, a person who tried several times may have to wait up to an hour. Turn it on under **App Home → Show Tabs →
    Messages Tab**. See [Link Slack](account-links.md#link-slack).

The Home tab is off
:   Muster publishes no Home tab.

Interactivity is on
:   Buttons and menus send nothing to Muster without it. Over HTTP it holds the Request URL.

Socket Mode is on, or off for HTTP
:   See [Socket Mode or HTTP](#socket-mode-or-http).

Token rotation is off
:   With rotation, the bot token expires and Slack issues a new one. Muster stores the token you enter and has no way to
    renew it, so a rotating token ends as `token_expired` and every Destination of the Connection becomes
    [Broken](../concepts/broken-destinations.md).

The app is not deployed to an organization
:   One Connection serves one app in one workspace.

There is no event subscription
:   Muster does not use the Events API: it hears only the presses of buttons and menus.

### The scopes

Muster needs these six bot scopes and no other. **Check connection** reads the scopes Slack grants the token and fails
with `missing_scope`, naming the missing ones.

`chat:write`
:   Posts, edits and deletes the bot's own messages, and answers the people who press its buttons.

`channels:read`
:   Reads a public channel to check a Destination, and lists the public channels the bot is a member of.

`groups:read`
:   The same for private channels.

`im:write`
:   Sends the codes of Account links by direct message.

`users:read`
:   Finds a person by name for an Account link, and reads the app of the bot so that the check can tell whether both
    tokens belong to one app.

`users:read.email`
:   Finds a person by email for an Account link.

Muster never joins a channel on its own, so it asks for no `channels:join`: you invite the bot.

## Install the app and create the tokens

1. In the app's settings, open **Install App** and install it in the workspace. Slack then shows the **Bot User OAuth
   Token** under **OAuth & Permissions**; it starts with `xoxb-`. Copy it. Anyone with the token controls the bot;
   Muster stores it encrypted and never shows it again.
2. In Socket Mode, open **Basic Information → App-Level Tokens** and generate a token with the scope
   `connections:write`. It starts with `xapp-`. Copy it.
3. Over HTTP, open **Basic Information → App Credentials** and copy the **Signing Secret**.

## Create the Connection

In Muster, create a **Connection** of type Slack. Enter a name, choose the event mode, and enter the bot token and, in
Socket Mode, the app-level token, or over HTTP the Slack signing secret. A Slack Connection has no address field, since
Slack's address is fixed. Then **Check connection**. The check runs in steps and shows the latency of each and whether
it went through the proxy:

1. **Token**: `auth.test` with the bot token, which names the bot and the workspace and fails if the token is not a
   bot's;
2. **Scopes**: every scope of the manifest is granted;
3. **App-level token**, in Socket Mode: the token belongs to the same app as the bot token, and Slack opens a
   connection for it. The check opens no WebSocket.

Over HTTP no request can show that Slack reaches the callback address or that the Slack signing secret is right, because
a bot cannot press its own button. The first press does: see [The test message](#the-test-message).

**Check connection** also works in the form before anything is saved. It checks the values in the form and saves
nothing, and no bot name is recorded. A stored token goes to Slack only through the saved proxy: after a change of the
proxy, type the tokens again to check, or save first.

## Invite the bot and create Destinations

A bot posts only to channels it is a member of. In Slack, invite the bot to each channel a Destination posts to, public
or private:

```text
/invite @Muster
```

In Muster, create a **Destination** of type Slack with the Connection and the channel. The form lists the public and
private channels the bot is a member of; for a channel that is not listed, type its id as Slack shows it in the
channel's details, `C…` or `G…`. A direct chat or a person cannot be a Destination. Saving runs the **Destination
check**, which asks Slack about the channel with `conversations.info` and refuses a Destination that fails:

- `channel_not_found`: no such channel, or a private channel the bot is not in;
- `channel_archived`: the channel is archived;
- `bot_not_in_channel`: "Invite the bot to the channel: /invite @{bot}", where `{bot}` is the bot's name as Slack
  reports it for the bot token, such as `muster`.

The bot's name appears in that hint only after **Check connection** has run on the saved Connection; before that it
says only "Invite the bot to the channel."

**Check** on the Destination page runs the same check at any time. When Slack refuses the bot later, for example
because someone removed it from the channel, the Destination becomes **Broken**; Muster runs the check every few
minutes while nothing waits for the Destination, and ends the Broken state once it passes again, for example after
someone invites the bot back. See [Broken Destinations](../concepts/broken-destinations.md).

## Link accounts

A button acts as a Muster User, and a Mention reaches a person, only through an Account link. The person enters their
Slack email or name in their profile, and the bot sends a code by direct message, in the Messages tab of the app. The
code has 8 characters, is valid for 10 minutes and allows 5 attempts (`account_link.slack_code`). See [Link
Slack](account-links.md#link-slack).

## Where answers to presses show

A successful press is not answered: the Root message changes. Whatever the person who pressed must read — that the press
was refused, that their Slack account is not linked to Muster yet, or that the button could not be verified — shows in
the channel as a message only they can see, marked "Only visible to you". Unlike Mattermost, Slack needs no permission
beyond `chat:write` for it. For a press on a Thread reply, such as a Reminder, the answer shows in the Thread. The one
success that is answered is "Still on it", which changes no message.

Slack does not guarantee such a message: the person must be active and in the channel. When Slack refuses it, Muster
sends the same text to the press's `response_url`, on `hooks.slack.com`; an answer that fails both ways is logged and
not tried again. Answers are in the language of the User the account is linked to, and in English for an account
without a link.

A replica handles at most 2 presses at a time (`slack.parallel_presses`); the others wait, already confirmed to Slack. A
press whose deadline ends while it waits for a slot is answered, in English, that it failed.

## Who gets notified

Slack cannot send a message without a notification: whom a new message notifies depends on each member's settings for
the channel and the Thread. So, as in Mattermost, **Quiet** means "without a Mention", not "nobody is notified".

- **Edits are Quiet.** An edit of the Root message carries no Mention, so a Loud change is always a new message: a new
  Root message, or a reply in the Thread. The edit drops the Mentions that the Root message had; Slack may then remove
  them from the person's Activity.
- **Mentions** are `@channel` and `@here`, as the Destination's Mention settings ask, and the people chosen in them,
  through their Account links. A person without a link is named by their display name as text, which notifies nobody. A
  Slack Destination takes neither `@all` nor groups. The footer of a Root message names a linked person without a
  Mention.
- **Thread replies** stay in the Thread: Muster never also broadcasts them to the channel.
- **Alert data and template text never mention anyone.** An `&`, `<` or `>` in a label or an annotation is escaped, and
  so is `@`. A literal `<!here>` or `<@U…>` written in a template is shown as text and notifies nobody. Only the
  Destination's Mention settings, `{{ mention "here" }}` and its siblings, and linked people notify.

See [Loud and Quiet](../concepts/loud-and-quiet.md).

## Rate limits

Muster limits itself, and Slack limits it too.

`connection.slack.limiter`
:   50 requests a minute for every call of the Connection to the Web API together, the minimum Slack documents for the
    methods that edit and delete.

`destination.slack.limiter`
:   30 requests a minute for the sends and edits of a Destination together, half of Slack's "one message per second per
    channel".

Slack limits each method per app and workspace. When it answers `429`, Muster holds every request of the Connection, for
any of its Destinations, for the seconds of `Retry-After`; this is not counted as a failed attempt, and does not make a
Destination Broken. A `429` without `Retry-After` counts as a transient failure. You can change both limiters.

## Edits and deleted Root messages

Muster edits a Root message for as long as its Alert Group is open, which can be days. Slack lets a
bot edit its own messages, and no edit window is known for them. Whether a window that your workspace sets applies to
the messages of a bot, hours or days old, has not been verified; an edit that Slack refuses that way ends that delivery
as Not delivered and does not make the Destination Broken.

When someone deletes a Root message, Muster finds out at its next edit, when Slack answers `message_not_found`. It
checks that the bot is still in the channel and the channel is not archived, and then posts the Alert Group again once,
Quietly, with a note. If the bot is gone or the channel archived, the Destination becomes Broken instead. A Root message
that already had replies stays in the channel as "This message was deleted." with its replies, and Muster's later
replies still go under it until the next edit finds it gone.

A Root message deleted before it had any reply takes no reply: Slack posts a reply to it into the channel, as an
ordinary message, and answers as if it had worked. Muster notices, deletes that message at once and posts the Alert
Group again. The reply may stay in the channel for a moment. If Slack refuses the deletion, Muster does not try again:
the stray reply stays until someone deletes it, and the delivery records why.

## The test message

**Send test message** on the Destination page posts one message marked "🧪 Test message", with the buttons of its status,
and **Preview** shows the message as Muster would send it. No Alert Group changes.

A bot cannot press its own button in Slack, so the test message cannot prove that presses reach Muster, as Mattermost's
does. Press a button of the test message yourself: Muster answers you "This is a test message; nothing was changed",
even from an account without a link. That answer proves that presses reach Muster and, over HTTP, that the Slack
signing secret is right.

## The proxy

A Slack Connection has a proxy section like the others; see [Proxies](proxies.md). The proxy must allow `CONNECT` to
`slack.com`; in Socket Mode also to the hosts under `slack.com` that the socket uses; and to `hooks.slack.com`, where
the fallback answers to presses go. Where only the proxy can resolve outside names and the Organization's outbound
address policy is strict, add `slack.com` and `*.slack.com` to **Allowed**: the domain entry covers `hooks.slack.com`
and the socket's hosts, but not `slack.com` itself. See [Proxies](proxies.md#what-the-outbound-address-policy-checks).

Muster accepts a Socket Mode address only on `slack.com` or a host below it, and an answer address only on
`hooks.slack.com`. If the socket cannot open, the Leader backs off (`slack.socket_backoff`, from 1 second up to 5
minutes), logs [`slack_socket_failed`](../reference/log-events.md#slack_socket_failed), and does not make any
Destination Broken. It waits `slack.hello_timeout`, 10 seconds, for Slack's greeting on a new socket.

## Request log

The **Request log** of a Slack Connection records its calls to the Web API named as Slack names them, such as
`chat.postMessage` and `apps.connections.open`. Over HTTP it records each press that reaches the callback address as
`interactive`, with Muster's answer, refused ones included. In Socket Mode it records each envelope that carries an
envelope id as `interactive`, with the socket's host as its address and Muster's confirmation as its answer; Slack's
greetings, disconnects and pings are not recorded. Tokens, the Slack signing secret and the signature are masked, and so
are the address of the socket, the `response_url` of a press, the signed ids of buttons and the code of an Account link.
See [Request log](../operations/request-log.md).

## Check from the command line

`muster doctor` prints one line per Slack Connection and per Slack Destination with the same checks, without sending
anything, and a `WARN` line for a Connection whose app another program shares.
