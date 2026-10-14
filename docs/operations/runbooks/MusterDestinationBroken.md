# MusterDestinationBroken

## Meaning

A Destination is **Broken**: Muster stopped sending to it after a Fatal error — the messenger refused the bot or the
request for good — or after its Transient budget ran out (`delivery.transient_budget`, 10 attempts within 30
minutes). Muster then probes it every `delivery.broken_probe_interval` (5 minutes) with the oldest waiting delivery,
or with the Destination check when nothing waits, and ends the Broken state on the first success.

This alert has two sources with the same name, severity `critical` and this page:

- The **Internal alert**, raised by Muster itself as an Alert of the built-in "Muster" Integration when the
  Destination becomes Broken, with the labels `destination` (the Destination's id) and `destination_name`. It is
  resolved when a probe, the Destination check or a test message succeeds, and when the Destination is deleted.
- The **chart rule**, the backup for when Muster cannot deliver the Internal alert:

    ```promql
    max by (destination) (muster_destination_broken{job="muster"}) == 1
    ```

    It fires after 5 minutes with the label `destination`. `muster_destination_broken` is set by the Leader.

!!! warning "Route it around Muster"
    Send the chart rule through an Alertmanager route that bypasses Muster. Route the Internal alert to a Destination
    of another kind than the Broken one — a Broken Telegram Destination usually means that Telegram is unreachable
    for every Telegram Destination, so send `MusterDestinationBroken` of Telegram Destinations to Mattermost or an
    outgoing webhook.

## Impact

- Nothing is sent to the Destination: no new Root messages, no edits, no Thread replies. Its deliveries wait, and the
  Alert Groups that wait for it show a delivery problem; the Alert Group list filter **Delivery problem** finds them.
- Other Destinations, ingestion, Commands and timers keep working.
- When the Broken state ends, Muster brings every waiting Root message to its current state; it does not replay the
  changes it missed one by one.

## Diagnosis

1. Open **System status** (`/admin/system-status`): **Broken Destinations** lists every Broken Destination and
   since when. The Destination list (`/destinations`) has the filter **Broken Destinations** too.
2. Open the Destination page (`/destinations/<id>`). The banner says "Broken since HH:MM: {reason}. Muster tries
   again every {interval}." The reason is the error the messenger answered, with secrets masked.
3. Find the log line `destination_broken` (`destination`, `cause`, `reason`): `cause` is `fatal` after a Fatal error
   and `unavailable` after the Transient budget ran out. `outbound_blocked` lines for the same address mean that the
   outbound address policy refused it.
4. Press **Check** on the Destination page to run the Destination check now, or run `muster doctor`, which prints a
   line `destination <name>` with the failing step. For a Connection problem, press **Check connection** on the
   Connection page (`/connections/<id>`).

## Fix

- `cause` `fatal`: fix what the reason names. Typical cases are a revoked bot token (replace it on the Connection),
  a bot that lost its admin rights in a Telegram channel or its discussion group, a deleted channel or chat, or an
  outgoing webhook that answers with a permanent error. Then press **Check**: a check that passes ends the Broken
  state at once.
- `cause` `unavailable`: the messenger or the path to it was down long enough to use up the Transient budget. Fix the
  network, the proxy of the Connection or the **Outbound addresses** (`/admin/organization/outbound`); the next probe
  ends the Broken state by itself, or press **Check** to end it now.
- Telegram is unreachable from your network: see
  [Telegram in restricted networks](../../messengers/telegram-restricted-networks.md).
- **Send test message** on the Destination page also ends the Broken state when every step succeeds.
- The Internal alert resolves and `destination_recovered` (`destination`, `broken_for_s`) is logged when the Broken
  state ends; the chart rule resolves at the Leader's next update of `muster_destination_broken`.
