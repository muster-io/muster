# 0005. Delivery as desired-state reconciliation

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — a late Publication also after `Transient` retries; the Broken probe uses the Destination check
  when nothing waits; the events mode of outgoing webhooks is not subject to Storms; after recovery, a first
  Publication is Loud only for a firing Alert Group; the Broken probe is delivery work, never on the interactive path;
  delivery events live in a table of their own, outside the Alert Group tables, and the Timeline view merges them; the
  limiters are shared by all replicas: their token buckets live in PostgreSQL; after a live Telegram test
  ([verified facts](../facts.md#telegram)): an admin bot receives the copy of its post, sends and edits take tokens from
  one limiter, a deleted copy leaves the Thread unattached, and Thread replies reach only the discussion group
- Amended: 2026-10-04 — after a live Mattermost test ([verified facts](../facts.md#mattermost)): an edit notifies nobody
  there either, even one that adds a Mention

## Context

Each Alert Group appears as one Root message per Destination, and that message must always show the current state:
status, Owner, Alerts and buttons. Changes come in bursts — ten Alerts arrive, someone acknowledges, two Alerts go away,
all within seconds — while messengers enforce rate limits, per chat and per bot, and fail in many ways: they ask to
retry after N seconds, time out, reject markup, or report that the message no longer exists because someone deleted
it. Delivery must also survive a restart of Muster in the middle of an API call.

Queueing one message operation per state change sends outdated intermediate versions, multiplies API calls during
bursts and needs careful ordering. Sending a message is not idempotent: if Muster stops after the messenger accepted a
message but before Muster recorded that, a retry posts a second copy. And editing a message notifies nobody, in
Mattermost and in Telegram alike, as live tests confirmed ([verified facts](../facts.md)); only a new message can make a
device ring. In Telegram, `disable_notification` delivers a new message without sound, not without a notification.

The objective for L1 (the first release) is a p95 of at most 5 seconds from receiving a webhook to handing the message
to the messenger's API, when the rate limit has room.

## Decision

**Desired state, reconciled.** For each pair Alert Group × Destination, Muster stores the Desired state of the Root
message: rendered text, buttons and a hash. State changes only update the Desired state. The delivery worker brings the
actual message to the latest Desired state with one send or edit, so a cascade of changes collapses into one call.
Retries are idempotent by construction, and after any wait the worker sends the latest Desired state, never an outdated
one. Delivery rows carry `next_attempt_at` and a lease (ADR-0006), and the HTTP call is made outside the database
transaction, through the outbound HTTP client (ADR-0015).

**Who calls messenger APIs.** Only the delivery worker sends or edits Root messages and Thread replies. Everything else
a person waits for — answers to button presses (Telegram `answerCallbackQuery`, Mattermost ephemeral messages), the
bot's messages during account linking (ADR-0013) and Destination test messages — goes through one named interactive
path that calls the messenger at once, outside the delivery queue, and never touches a Root message or a Thread. A test
message is a single create request to the chosen Destination; reconciliation never sees it. An architecture lint allows
messenger send and edit calls only from these two places (ADR-0016).

**Rate limiting.** Each Destination has a limiter, and each Connection has one too, because a messenger limits a bot as
a whole as well as each chat; a delivery waits for both. The limiters are shared by all replicas: their token buckets
live in PostgreSQL, so adding a replica never raises the rate a messenger sees. A call on the interactive path takes a
token from the same limiters ahead of every waiting delivery, never queues, and fails fast — the person is told — when
no token is free within its budget of a few seconds (ADR-0015). Limits default to half of the messenger's documented
limit. Every call takes a token, a send and an edit alike: Telegram counts both against one budget of about 20 per
minute per chat. Outgoing webhooks have no Connection and are limited per Destination. Urgent Alert Groups go first in
the queue but never bypass a limiter.

**Threads.** Follow-ups — new Alerts, ack timeout notices, Reminders — go to the Root message's Thread. New Alerts are
batched with a leading and a trailing edge: the first is sent immediately and opens the Thread batching window (default
60 seconds, a Route setting); everything that arrives within the window is sent as one reply when it closes. A very
large Alert Group shows a summary, the first N Alerts and a link to the rest in Muster. In Telegram the Root message is
a post in a channel and its Thread is the post's comment thread in the linked discussion group. Muster links the
discussion group's automatic copy of the post to the Root message by the original post id, buffers that copy because it
may arrive before or after the response to the send call, and lists the update types it needs explicitly. A live test
confirmed that a bot which is an admin of the discussion group receives the copy, a few seconds after the post; the
fallback below covers a copy that does not come. Thread replies wait for the copy for a limited time. If it does not
come, Muster takes the copy's id from the first comment a person writes under the post, which is a reply to that copy;
until then, or if nobody comments, Thread replies go to the discussion group as a chain of replies not attached to the
post, and the UI marks that Thread as unattached. A copy deleted later loses the Thread the same way: Telegram refuses
replies to it, and the replies go to the discussion group unattached.

**Storm.** A Storm is counted per Route: when a Route produces more than N new Alert Groups per minute (default 20, a
Route setting), each of its Destinations receives one Storm summary for that Route — the number of Alert Groups, how
many are Urgent, and a link to Muster — kept current by reconciliation. Urgent Alert Groups are still published
individually, first in the queue. Once the rate has stayed below the threshold for 5 minutes, the summary gets its final
state, the Alert Groups that are still open are published gradually and Quietly within the limits, and those already
resolved are not published at all. The ack timeout of an Alert Group from a Storm starts at its own publication; the
summary is Loud only on its first publication and has no timer of its own. A Destination shared by several Routes may
receive one summary per Route; its limiters, not the Storm, protect it from the sum. Storms apply to messengers and to
the template mode of outgoing webhooks; the events mode is not subject to them (see below).

**Loud and Quiet.** Because edits notify nobody, **every Loud event is a new message** — a new Root message or a Thread
reply — and an edit of a Root message is always Quiet. L1 uses a fixed matrix; making it configurable per Route comes
later. Loud is everything that needs a reaction: a new Alert Group, a Reopen into firing or into acknowledged, an ack
timeout notice, a Reminder, a Takeover, a Snooze ending while firing, a rise to Urgent that removes the acknowledgement
or the Snooze, an auto-unacknowledge after unanswered Reminders (ADR-0004), the first publication of a Storm summary,
and new Alerts in a firing Alert Group. Everything else is Quiet: new Alerts in acknowledged or snoozed Alert Groups,
Replacements, status changes made by people, resolution by the system, a Reopen into snoozed, a `startsAt` continuation,
updates of annotations or counters, the Publication of open Alert Groups into a newly added Destination (ADR-0003), and
the gradual Publication of open Alert Groups after a Storm, whose summary has already rung. A Reopen into acknowledged
mentions only the Owner, whatever the Destination's mention settings. In Telegram, Quiet new messages carry
`disable_notification`; Thread replies reach only members of the discussion group, and the automatic copy of a Root
message rings them even when the Root message is Quiet — a limitation the documentation names. In Mattermost a Thread
reply notifies the Thread's followers according to their own settings, so there Quiet means "without a Mention"; this
limitation is documented. Each Destination chooses whom to mention on Loud events — nobody by default, everyone in the
chat, Muster users or messenger groups, separately per event — and templates can use the trusted `mention` function.

**Output safety** is the adapter's job: all alert data is escaped for the messenger's markup, `@` in alert data is
neutralized, links are `http(s)` only and values are truncated to 4 KB. If the messenger rejects the markup, the same
text is sent again without markup and the event is counted.

**Errors.** Adapters classify every failure:

- `RetryAfter` — wait exactly as long as the messenger asked; this is not counted as an attempt;
- `Transient` — exponential backoff with jitter, within an attempt budget and a time budget; when either budget runs
  out, the Destination becomes Broken as unavailable;
- `Fatal` — the Destination becomes Broken at once;
- unknown — a response the adapter cannot classify is not retried: that delivery ends as **Not delivered**, and the
  Destination is not marked Broken.

**Broken** means: a warning on the Destination and on its Routes, a metric, the Internal alert `MusterDestinationBroken`
(which has to reach people through another Destination or through the chart's alert rules), and deliveries that wait
instead of failing. A probe is made every few minutes: the oldest waiting delivery or, when nothing waits, the
Destination check of its type — for a messenger, whether the bot can still reach and write to the chat — so that a
Destination fixed while nothing was waiting does not stay Broken, with its critical Internal alert, until the next Alert
Group. Nobody waits for a probe, so it is delivery work on any replica, in the delivery client class (ADR-0015), and
never goes through the interactive path, which is kept for calls a person waits for; the same check started by a person
from the Destination page is interactive. A successful probe, check or Destination test ends the Broken state. **After
recovery, reconciliation sends only the current state**: open Alert Groups that were never published there are
published, Root messages are edited to their current state, intermediate states and the Thread replies that came due
meanwhile are not replayed, and Alert Groups that resolved while the Destination was Broken without ever being published
there are not published at all — they stay in their Timeline and the UI, like Alert Groups resolved during a Storm. A
first Publication after recovery is Loud only if the Alert Group is firing at that moment; an acknowledged or snoozed
one is published Quietly.

**Not delivered** is the terminal state of one delivery that a retry cannot fix and that says nothing about the
Destination as a whole: an unknown response, or an outgoing webhook request whose template failed (ADR-0012). It is
visible on the Alert Group page and in the Timeline; a later change of the Desired state starts a new delivery. An Alert
Group that was resolved while its first Publication waited — for a `RetryAfter`, or for `Transient` retries within the
budget — is still published, Quietly, with a note that it was delivered late and when it started and resolved, except
during a Storm or once the Destination is Broken. Both waits are short and bounded, and a person who saw nothing would
otherwise never learn that the problem happened.

**First publication is not idempotent; a duplicate is better than a loss.** Before calling the API, the worker records
that the Publication started. A retry after such a record publishes again, in Mattermost and in Telegram alike: a
possible duplicate Root message is accepted, logged and recorded as a delivery event. Muster does not search the
messenger for a message it may already have posted. A Publication is never skipped because of the record.

**Deleted Root message.** If the messenger reports that the Root message no longer exists, an open Alert Group is
published again once, Quietly, with a note that the previous message was deleted, and its Thread starts over. If that
message is deleted too, the deletion is treated as intended: the pair is marked as deleted in the messenger and left
alone, visibly in the UI and the Timeline. For a resolved Alert Group only the mark is recorded.

**Delivery events.** What happens while delivering to a Destination — a Publication, a possible duplicate, rejected
markup, Not delivered, a late Publication, a deleted Root message, an unattached Thread, the Destination becoming Broken
or healthy again — is recorded by delivery in a delivery-events table of its own, never in the Alert Group tables, which
only the command layer writes (ADR-0016). Delivery events are not lifecycle events, so the events mode of outgoing
webhooks never sends them. The Timeline view of an Alert Group merges the delivery events about it with its Timeline
entries by time, so people read one history while the Alert Group tables keep their single writer.

**Outgoing webhooks** have two modes, and a Destination may use both. The **template mode** follows the same model as
messengers: a "create" request whose response values (for example a message id) are stored per Alert Group ×
Destination, "update" requests that use those values and are reconciled to the latest state, and optional "open thread"
and "reply in thread" requests; it follows Storms like a messenger. The **events mode** is neither reconciled nor
subject to Storms: it sends one request per lifecycle event, in order within each Alert Group, at least once and never
collapsed, because a receiving endpoint may keep its own record of every step. Each event carries its own `webhook-id`,
kept across retries so that the receiver can drop duplicates, and a body versioned as `version: 1` (`alert_group` plus
`alerts[]`). The next event of an Alert Group waits until the previous one is delivered or ends as Not delivered; events
that came due while the Destination was Broken are sent in order after recovery. Requests in both modes are signed with
the Destination's own Signing secret (ADR-0011). Responses map to the error classes as follows: `2xx` — delivered; `429`
or `503` with `Retry-After` — `RetryAfter`; `408`, other `5xx`, timeouts and network errors — `Transient`; `401`, `403`,
`404` and `410` — `Fatal`; `400`, `413`, `422`, redirects and every other response — unknown.

## Consequences

- A burst costs one API call per Destination, not one per change, and a message never stays in an intermediate state.
- Muster keeps a desired and an actual state per pair; this is the main delivery table. Delivery events have a table
  of their own, and the Timeline view reads two tables instead of one.
- A duplicate Root message is possible in either messenger after a crash during Publication; this is accepted and
  visible.
- Loudness is a property of the message, not of the edit: changing the matrix later means adding or removing Thread
  replies, never making edits ring.
- Interactive replies are fast and unqueued, but they bypass reconciliation; keeping them away from Root messages and
  Threads is what keeps the Desired state the only truth for those.
- Rate-limit pressure delays updates but never drops them. Urgent Alert Groups still wait for their turn in the
  limiters, which bounds what "immediately" means for them.
- The Loud/Quiet matrix is fixed in L1; teams that want a different one wait for the per-Route matrix.
- An outage of a Destination longer than the retry budgets loses nothing that is still open: the Destination turns
  Broken, people are told through another path, and recovery brings every open Alert Group up to date. What resolved
  during the outage before it was ever published there is visible only in Muster.
- Every Destination type with a chat behind it needs a cheap check that runs without an Alert Group; the outgoing
  webhook has none and recovers with its next request or a test.
- Receivers of the events mode get every event at least once and must drop duplicates by `webhook-id`; one stuck event
  holds back the later events of its Alert Group, never those of other Alert Groups.

## Alternatives considered

- **Queue every message operation as a task.** Sends outdated states, needs ordering per message and multiplies API
  calls under load.
- **Idempotency keys for first publication.** The messengers do not offer them; the remaining choice is between a
  possible duplicate and a possible loss, and for alerting a loss is worse.
- **Search Mattermost for an already posted message and adopt it** after a crash during Publication. One more code path
  for a rare case, available in only one messenger; publishing again is the same rule everywhere.
- **Make an edit Loud** (for example by editing the Root message on a Reopen). Edits notify nobody in either messenger.
- **Count Storms per Destination.** A Destination shared by several Routes would mix unrelated traffic in one summary,
  and a Storm on one Route would hide another Route's Alert Groups; per Route, the summary describes one kind of traffic.
- **Send interactive replies through the delivery queue.** A person pressing a button would wait behind a Storm, and
  Telegram expects a callback answer within seconds.
- **Let Urgent messages bypass the rate limit.** The messenger then throttles or rejects the whole bot, the urgent
  message included.
- **Retry unknown errors.** Hides real misconfiguration and wastes the attempt budget; failing with a visible terminal
  state is clearer.
- **End a delivery as Not delivered when its retry budget runs out.** Only a change of the Desired state starts a new
  delivery, so an Alert Group that keeps firing through an outage longer than the budget would never be published, and
  nobody would learn that the Destination is down. Treating budget exhaustion as Broken keeps the delivery waiting and
  raises an alert.
- **Record delivery events as Timeline entries in the Alert Group tables.** Delivery would write tables that only the
  command layer writes, or pass every attempt through the dispatcher; a table of its own keeps one writer per table,
  and merging on read gives people the same single history.
- **Run the Broken probe's Destination check on the interactive path.** A probe would take limiter tokens ahead of
  waiting deliveries and fail within a person's budget, although nobody waits for it.
- **Probe a Broken Destination only with a waiting delivery.** When the last waiting delivery is dropped — for an Alert
  Group that resolved before it was ever published — nothing is left to probe with, and a repaired Destination stays
  Broken, its critical Internal alert firing, until the next Alert Group happens to arrive.
- **Publish late only after a `RetryAfter`.** An Alert Group that resolved during a few `Transient` retries would then
  never be seen in that Destination, although the wait was as short as a `RetryAfter`; one Quiet late message costs
  little.
- **Replay every intermediate state after recovery.** A burst of outdated edits and Thread replies, some about Alert
  Groups that ended long ago, rings people for nothing; the current state is what they need.
- **Reconcile the events mode like Root messages.** Collapsing would drop the steps that a receiving endpoint with its
  own state machine or audit trail needs; reconciliation is kept for the template mode, which edits one message.
- **Stop updating as soon as a Root message is deleted.** Leaves an Alert Group that is still firing without a visible
  message and tells nobody; one Quiet republication is the better default.
