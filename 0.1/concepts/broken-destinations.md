# Broken Destinations

A Destination is **Broken** when Muster has decided that it cannot deliver there until something changes: the messenger
refused the bot for good, or the messenger kept failing until Muster ran out of patience. A Broken Destination is not an
error to dismiss. It means that the people it serves are not seeing their alerts, so Muster raises an alert about it.

This page explains when a Destination becomes Broken, what Muster does meanwhile, and how it recovers. For the steps
when `MusterDestinationBroken` fires, use the [runbook](../operations/runbooks/MusterDestinationBroken.md).

## When a Destination becomes Broken

Every failed attempt is sorted into a class, and the class decides what happens:

Retry after
:   examples: the messenger answers `429` and says how long to wait

    Muster waits exactly that long and then sends the latest state. It is not counted as an attempt.

Transient
:   examples: a `5xx`, a timeout, a network error

    Muster retries with exponential backoff and jitter, from 2 seconds up to 5 minutes. After **10 attempts within 30
    minutes** (`delivery.transient_budget`) the Destination becomes Broken as *unavailable*.

Fatal
:   examples: the messenger refuses the bot or the request for good

    The Destination becomes Broken **at once**.

Unknown
:   examples: an answer Muster does not recognize

    Not retried. That one delivery ends as *Not delivered*, and the Destination stays healthy.

What is Fatal depends on the messenger:

- **Mattermost**: `401` or `403`, such as an invalid token or a bot removed from the channel, and `404` for the channel,
  which is not found or archived.
- **Slack**: a token that Slack refuses (`invalid_auth`, `token_revoked`, `token_expired` and the like), a missing
  scope, and a channel the bot cannot use: `channel_not_found`, `not_in_channel` for a bot removed from the channel, and
  `is_archived`. A `429` is not Fatal: Muster holds the whole Connection for the seconds Slack asks.
- **Telegram**: `401`, and "bot was kicked" or missing rights, such as a bot that lost its admin rights in the channel.
- **Bridge**: the bridge answers `forbidden`, for example because the bot was removed from the channel; the bridge's own
  text becomes the reason. A bridge that does not answer at all becomes Broken through the Transient budget.
- **Outgoing webhook**: `401`, `403`, `404` and `410`, and a request that the Organization's outbound address policy blocks,
  since every later request would be blocked too.

Several things look like trouble and do **not** make a Destination Broken:

- A deleted Root message. Muster posts the Alert Group again once, Quietly, with the note "The previous message was
  deleted at HH:MM"; if that one is deleted too, the pair is marked "deleted in the messenger" and left alone. In Slack
  this holds while the bot is still in the channel and the channel is not archived; see [Edits and deleted Root
  messages](../messengers/slack.md#edits-and-deleted-root-messages).
- A Telegram Thread that lost the automatic copy of its Root message. The delivery state shows "Thread not attached".
- A delivery that ends as Not delivered, such as a template that failed or a request the receiver rejected with `400`.
- A Bridge Connection whose bridge cannot be reached. The Connection does not become Broken itself; its Destinations do,
  through their own deliveries, and its Connection check shows the cause.
- Failed polls of a bridge or of Telegram. The poller backs off.

## What you see

- **The Destination and its Routes** show a banner: "Broken since HH:MM: {reason}. Muster tries again every 5 minutes."
  The reason is the error the messenger answered, or "unavailable after repeated failures" and the last error.
- **The Internal alert** `MusterDestinationBroken` is raised, with the severity critical and the labels `destination` and
  `destination_name`. Muster routes it by your Routes like any other Alert, so a Route for Internal alerts should lead to
  a Destination of **another kind** than the one that failed: if all your Telegram Destinations are Broken because
  Telegram is unreachable, an alert that goes to Telegram says nothing. See [Routes and Group
  keys](routes-and-group-keys.md#examples).
- **The chart rule** `MusterDestinationBroken` is the backup for when Muster cannot deliver its own alert. Send it through
  an Alertmanager route that bypasses Muster. See [Internal alerts](../reference/internal-alerts.md) and the
  [metrics](../reference/metrics.md).
- The **Alert Groups** whose delivery waits show a delivery problem. The Alert Group list has the filter **Delivery
  problem**, and the page of each shows the state "Waiting: {Destination} is Broken".
- The **System status** page lists the Broken Destinations and since when.

## What Muster does meanwhile

Everything except the delivery to that Destination goes on: ingestion, routing, Alert Groups, commands from the web UI
and the API, timers, and every other Destination.

- **Deliveries wait instead of failing.** Nothing is dropped, and Muster does not spend retries on a Destination it knows
  is down.
- **Muster probes the Destination every 5 minutes** (`delivery.broken_probe_interval`). If a delivery is waiting, the
  probe is an attempt at the oldest one. If nothing is waiting, the probe is the **Destination check**, which asks the
  messenger whether the bot can still reach and post to the channel, without posting. An outgoing webhook has no
  Destination check, so its next delivery is the probe.
- **A new Alert Group is created and shown in Muster** as usual, but its first Publication waits. Its ack timeout does not
  start until it is published, so a Route whose only Destinations are Broken sends no notices.

## Recovery

The Broken state ends when **any** of these succeeds:

- the automatic probe;
- **Check** on the Destination page, which runs the Destination check at once;
- **Send test message** on the Destination page, when every step succeeds.

Fix the cause first. Typical fixes are a replaced bot token, adding the bot back to the channel or giving it its admin
rights again, enabling comments on a Telegram channel, correcting the address or the proxy of the Connection or the
**Outbound addresses** policy, or waiting out a messenger outage. When the fix is made, press **Check**; if you do
nothing, the next probe notices by itself, with no Alert Group activity needed. The Internal alert then resolves.

### What is sent after recovery

Muster sends only the **current state**. It does not replay what it missed.

| What happened during the outage | After recovery |
|---|---|
| An Alert Group started and is still firing | It is published as a Loud new message. |
| An Alert Group started and is no longer firing, but still open | It is published Quietly. |
| An Alert Group was already published, then changed | Its Root message is edited once to the current state, with no Thread reply. |
| An Alert Group started and resolved during the outage | It is **not** published; it stays in the Timeline and in the web UI. |
| Thread replies came due | They are not sent. |

Urgent Alert Groups are published first, within the Destination's rate limits.

An **outgoing webhook in events mode** is the exception: it is a stream of events, so every event that came due during the
outage is sent after recovery, in order per Alert Group, with none dropped and no age limit. An outgoing webhook in
template mode sends only the current state, like a messenger.

If an Alert Group resolved while its first Publication was still waiting for a retry, but the Destination was not yet
Broken, it is still posted, Quietly, with the note "Delivered late: started HH:MM, resolved HH:MM while this Destination
was unavailable".

## Republishing past an edit window

Muster edits a Root message for as long as its Alert Group is open, which can take days. Whether a messenger lets it
depends on the messenger. This is not about a Broken Destination, but the messages that Muster can no longer edit look
similar from the outside, so here is the whole picture.

Telegram
:   None for the bot's own messages. A bot may edit them at any age, so Muster never republishes because of age.

Slack
:   None known for a bot's own messages. Whether a window that a workspace sets applies to a bot's messages hours or
    days old has not been verified; an edit Slack refuses ends that delivery as Not delivered.

Mattermost
:   The server setting `ServiceSettings.PostEditTimeLimit`, which is unlimited (`-1`) by default. Keep the default. If
    an administrator sets a limit, Muster's edits of older Root messages may be refused, and what Mattermost then
    answers has not been verified. See [Keep posts editable](../messengers/mattermost.md#keep-posts-editable).

Bridge
:   The bridge declares it in its info, as `limits.edit_max_age_seconds`, or declares none.

Outgoing webhook
:   Muster does not know the chat's rules. In template mode an `update` request that the chat refuses ends as Not
    delivered or Broken by the usual classes.

For a **Bridge** with an edit window, Muster stops editing a Root message 60 seconds before the window closes
(`bridge.edit_window_margin`). After that:

- A change that keeps the status of the Alert Group, such as new Alerts or Alerts that resolve, reaches only the Thread.
  The old Root message keeps the state of its last edit.
- A **change of status**, such as Acknowledge, Resolve, Snooze or a Reopen, makes Muster publish a **new Root message**
  Quietly, in the same channel, and reply under the old one: "This message is no longer updated. The current one:
  {link}". Later Thread replies go under the new Root message.
- The old message keeps working: a press on its buttons, or a Text command in its Thread, still acts on the Alert Group.
- The Alert Group page shows "Past the edit window: updated on the next change of status" until then.

A bridge may also refuse an edit before the declared time, with the reason `edit_window_closed`. Muster treats that as the
end of the window, not as a deletion. See [Edit windows](../messengers/bridge.md#edit-windows) for the details.

## After you delete a Destination

A deleted Destination receives one last edit of each open Root message, "No longer updated here; current state in Muster:
{link}", and nothing after that. For a Bridge Root message past its edit window, the last edit is a Thread reply with
the same text. An outgoing webhook in events mode sends no final event, and its queued events end as Not delivered.
