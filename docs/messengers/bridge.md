# Connect a messenger through a bridge

A **bridge** connects Muster to a messenger that Muster does not support natively. It is a separate service that
speaks the Bridge protocol with Muster on one side and talks to your messenger on the other: Muster sends it every
message as an **envelope** — the status, the title, a link, the text and the buttons — and the bridge renders the
envelope in its messenger. A **Bridge Connection** is one bot of your messenger behind one bridge: one token is one
bot. Muster posts one **Root message** per Alert Group to a channel of that messenger, edits it as the Alert Group
changes, and replies in its **Thread** for what happens later, as it does for Mattermost and Telegram.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org`, `MUSTER_INGEST_URL` is
`https://ingest.muster.example.org` and the bridge listens on `https://bridge.example.org`. The web UI has no form for
Bridge Connections and Bridge Destinations yet, so the examples create them through the [API](../reference/api.md),
with a Personal access token of an Admin in `MUSTER_TOKEN`.

## Create the Connection

From your bridge, you need:

- its **base URL**, an `http` or `https` address with an optional path prefix, such as
  `https://bridge.example.org/muster`. Muster sends every request to `<base URL>/v1/…`, and only there;
- the **token** it gave your bot. Muster sends it as `Authorization: Bearer` on every request, stores it encrypted and
  never shows it again.

Choose how the bridge reports **events** — the presses of buttons and the messages people write in a Thread — with the
**event delivery** mode:

- **`poll`** when the bridge cannot reach Muster. The replica that leads asks the bridge for new events, so the bridge
  needs no address of Muster.
- **`push`** when the bridge can reach `MUSTER_INGEST_URL`. The bridge posts each event to the Connection's callback
  address, `https://ingest.muster.example.org/api/v1/callbacks/bridge/CN…`, which the Connection shows in
  `callback_url`, signed with a **push secret** that you configure on both sides. The push secret has the Standard
  Webhooks form, `whsec_` followed by base64, and is write-only like the token.

A Connection also has a **proxy**, for a bridge that Muster reaches only through one, and a **limiter**: 10 requests
per second by default, shared by all its Destinations. When the bridge answers that it is rate-limited, Muster waits
as long as it asks, whatever the limiter allows.

```sh
curl -X POST https://muster.example.org/api/v1/connections \
  -H "Authorization: Bearer $MUSTER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"bridge","name":"Team chat","base_url":"https://bridge.example.org/muster",
       "bot_token":"the-token-of-the-bot","event_delivery":"poll","proxy":{"enabled":false},
       "limiter":{"limit":10,"per_seconds":1}}'
```

For `push`, set `"event_delivery":"push"` and add `"push_secret":"whsec_…"`.

## What the check shows

Check the Connection after you create it, and after every change of the bridge:

```sh
curl -X POST https://muster.example.org/api/v1/connections/CNK7M3QX9P2RTA/checks \
  -H "Authorization: Bearer $MUSTER_TOKEN"
```

The check calls the bridge's `getInfo` and shows two steps, each with its latency and whether it went through the
proxy:

1. **`info`**: the bridge's name and version, the messenger's name, the protocol version — Muster speaks `1` — what
   the bridge offers, such as buttons and Thread messages, its limits, such as the longest text of a message, how it
   finds people for Account links, and its event delivery modes. Muster keeps this copy on the
   Connection and reads it again before a call that depends on it once it is older than 10 minutes, so a change in the
   bridge reaches Muster without a restart.
2. **`event_delivery`**: whether the bridge offers the Connection's event delivery mode.

A failed step names its cause with a code:

| Code | Cause |
|---|---|
| `token_invalid` | The bridge refused the token. |
| `protocol_version_unknown` | The bridge speaks a protocol version that this Muster does not speak. |
| `protocol_mismatch` | The answer does not match the protocol, or is larger than 64 KiB. |
| `event_delivery_not_offered` | The bridge does not offer the Connection's event delivery mode. |
| `proxy_refused` | The Connection's proxy refused its credentials (`407`). |
| `timeout`, `connect_failed`, `dns_failed`, `tls_failed` | Muster got no answer from the bridge. |
| `blocked` | The outbound address policy does not allow the bridge's address. |
| `request_failed` | The bridge answered with another error; the message names its class and the bridge's text. |

## Add a Destination

A **Bridge Destination** is one channel of the messenger, named by its **channel id** — the channel as your messenger
names it, which the bridge's documentation explains how to find. Its **Mention settings** take nobody or chosen
users; everyone in the chat and messenger groups are not available through a bridge. Its limiter allows 30 requests
per minute by default, sends and edits together.

```sh
curl -X POST https://muster.example.org/api/v1/destinations \
  -H "Authorization: Bearer $MUSTER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"bridge","name":"alerts","connection_id":"CNK7M3QX9P2RTA","channel_id":"C024BE91L",
       "mentions":{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},
                   "new_alerts":{"everyone":"none","user_ids":[],"groups":[]},
                   "reopen":{"everyone":"none","user_ids":[],"groups":[]},
                   "ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},
                   "snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},
                   "rise_to_urgent":{"everyone":"none","user_ids":[],"groups":[]}},
       "limiter":{"limit":30,"per_seconds":60}}'
```

Saving the Destination runs its **Destination check**, the one item `channel`: the bridge's `checkChannel`, which
posts nothing. It passes when the bot can post, edit and reply in the channel, and the Destination then shows the
channel's name as the bridge reports it. It fails with the bridge's own text when the bridge refuses the channel —
it does not exist, or the bot is not in it — with `proxy_refused` when the Connection's proxy refuses its credentials,
and with the class of the bridge's answer otherwise; a Destination that fails the check is not saved.
`POST /api/v1/destinations/DS…/checks` runs the same check at any time.

## What Muster posts

- **The envelope.** Every Root message carries its status with a colour and an emoji, the title `#N` and the Alert
  Group's title, the link "Open in Muster" to the Alert Group page, and its text in CommonMark: the labels, the
  summary, one line per Alert, the links, the notices and the footer. A Thread reply carries its text in CommonMark.
  Alert data are escaped, so a label value cannot format the message, link anywhere or mention anyone. A Loud message
  asks the bridge to notify; a Quiet one does not.
- **The length.** The text fits the longest text the bridge declares, read from the Connection's copy of its info: a
  long Root message drops its Alert lines first, then the label sections, but always keeps its title, its status, its
  footer, its buttons and its link to Muster. The bridge may still shorten what it renders, and says so.
- **Buttons.** When the bridge offers buttons, a Root message carries the buttons of its status — Ack, Resolve and one
  per Snooze duration, for example — with a style hint for each, at most as many as the bridge allows; the Snooze
  buttons of the longest durations go first. Without buttons, a bridge that forwards the messages of a Thread gets a
  last line in each Root message that is not resolved, such as "Reply in the Thread: ack · resolve · snooze 1h".
- **Edits and Thread replies.** An edit sends the whole envelope of the new state, Quiet and without Mentions. A Thread
  reply goes under the Root message with the Thread reference the bridge returned last.
- **Posts are not repeated.** Every new message carries an idempotency key, the same for every retry of it. When an
  answer is lost — a timeout, or a connection cut after the request — Muster retries with the same key, the bridge
  answers with the message it posted already, and Muster edits it to the current state. When the bridge did not know
  the key, the retry may have posted a second message: the Timeline records a **possible duplicate**.
- **The token never shows** in a log line, an error, the Timeline or a metric; a test shows it as `[redacted]`.

## Test messages and previews

A **test message** checks how a Destination looks without touching any Alert Group:

```sh
curl -X POST https://muster.example.org/api/v1/destinations/DSK7M3QX9P2RTA/tests \
  -H "Authorization: Bearer $MUSTER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"source":{"kind":"example"}}'
```

It posts one message of the kind `test`, Quiet, marked "🧪 Test message" before its text, with the buttons of its
status, whose presses change nothing. It goes through the same limiters as delivery, and the result shows the step
`message` with the request — the token masked — the bridge's answer and its error class. Use
`{"source":{"kind":"alert_group","alert_group_id":"AG…"}}` to render a recent Alert Group of one of the
Destination's Routes instead of the example. `POST …/previews` with the same body returns, without posting anything,
the envelope the test would post, as JSON in the item `message`.

## Broken Destinations

A Bridge Destination becomes **Broken** when the bridge refuses it — it answers `forbidden`, for example because the
bot was removed from the channel — with the bridge's text as the reason, or when the bridge keeps failing or does not
answer for the whole Transient budget: 10 attempts within 30 minutes. A bridge that asks Muster to wait does not make
a Destination Broken unless it asks for more than 15 minutes; an answer the bridge calls `invalid` ends that one
delivery as Not delivered and leaves the Destination healthy. When the bridge answers that a Root message is gone,
Muster publishes the Alert Group again, once.

While a Destination is Broken, Muster tries again every 5 minutes: with the oldest waiting delivery, or, when nothing
waits, with the Destination check, which posts nothing. The first success ends the Broken state, and so does a
successful check or test message. See [MusterDestinationBroken](../operations/runbooks/MusterDestinationBroken.md).
A Bridge Connection whose bridge cannot be reached does not become Broken itself: its Destinations do, and its
Connection check shows the cause.

## Account links

A press or a Mention through a bridge names a person through their Account link for that Connection; see
[Link your messenger accounts](account-links.md).

## Check from the command line

`muster doctor` prints one line per Bridge Connection, with the result of `getInfo`, and one line per Bridge
Destination, with the result of `checkChannel`, without posting anything.
