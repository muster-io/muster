# MusterNotificationGatewayBroken

## Meaning

A [Notification gateway](../../messengers/notification-gateways.md) is **Broken**: a call through it was answered with
a Fatal error — the bridge, or the provider behind it, refused the token or the credentials (`401` or `403`, the class
`forbidden`) — or a call failed until its retry budget ran out (`delivery.transient_budget`, 10 attempts within 30
minutes). Muster then probes the gateway every `delivery.broken_probe_interval` (5 minutes) by reading its `info`, and
ends the Broken state on the first success. A successful check of its Connection ends it too.

This alert has two sources with the same name, severity `critical` and this page:

- The **Internal alert**, raised by Muster itself as an Alert of the built-in "Muster" Integration when the gateway
  becomes Broken, with the labels `connection` (the id of the gateway's Connection) and `connection_name`. It is
  resolved when a probe or a Connection check succeeds, and when the Connection is deleted. The suggested Route
  "Muster internal alerts" groups by `connection`, so every Broken gateway has an Alert Group of its own.
- The **chart rule**, the backup for when Muster cannot deliver the Internal alert:

    ```promql
    max by (connection) (muster_notification_gateway_broken{job="muster"}) == 1
    ```

    It fires after 5 minutes with the label `connection`. `muster_notification_gateway_broken` is set by the Leader.

!!! warning "Route it around the gateway"
    A Broken gateway cannot call anybody about itself. Route the Internal alert to a messenger Destination, and send
    the chart rule through an Alertmanager route that bypasses Muster.

## Impact

- No call goes through the gateway. Every call that comes due while it is Broken — from an Escalation step or a
  Notification rule — ends Not delivered: each of its Alert Groups records `page_not_delivered` with the reason
  `gateway_broken` in its Timeline, naming the person and the gateway.
- **The calls that came due meanwhile were not sent, and are not sent after the gateway recovers.** Nobody was called
  for those Alert Groups through this gateway; check them by hand.
- The Destinations of the same bridge, if it is also a messenger, are not affected: a Broken gateway never makes a
  Destination Broken, nor the other way round. Other gateways, ingestion, delivery to messengers and timers keep
  working.

## Diagnosis

1. Open the gateway's Connection page (`/connections/<id>`). Its read in the API, `GET /api/v1/connections/<id>`,
   has `gateway` with `broken`, `broken_since`, `reason` and `next_probe_at`. The reason is the error the bridge
   answered to the call or to the last probe, with secrets and the contacts and texts of calls masked; it starts with
   "unavailable after repeated failures:" when the retry budget ran out.
2. Open the tab **Request log** of the Connection and narrow it to the operations `sendNotification` and `getInfo`
   (see [Request log](../request-log.md#where-to-find-it)): the record shows the status and the body the bridge
   answered. The probes are the `getInfo` requests of the kind **Broken probe**.
3. Find the log line `gateway_broken` (`connection`, `cause`, `reason`): `cause` is `fatal` after a Fatal answer and
   `unavailable` after a call used up its retry budget. The lines `gateway_call_not_delivered` with the reason
   `gateway_broken` name the calls that were not sent.
4. Run `muster bridge check <base URL> --token-file <file> --contact <your contact> --no-interactive`, and
   `--channel <id>` when the bridge is also a messenger, against the bridge to see which step of the Bridge protocol
   fails, outside Muster: it sends one test notification to your contact (see
   [`muster bridge check`](../../messengers/write-a-bridge.md#muster-bridge-check)).

## Fix

- `cause` `fatal`, a `401` or `403`: the bridge refuses Muster's token, or the provider refuses the bridge's
  credentials. Replace the token on the Connection if it is Muster's that changed; otherwise fix the credentials or
  the account of the provider in the bridge's own configuration — an expired API key, a blocked account, a used-up
  balance.
- `cause` `unavailable`: the bridge or its provider did not answer, or answered `transient` or asked to wait, until the
  retry budget ran out. Fix the bridge, its provider or the network path to it, the proxy of the Connection or the
  **Outbound addresses** (`/admin/organization/outbound`).
- Then press **Check connection** on the Connection page: a check whose step `info` passes ends the Broken state at
  once. Otherwise the next probe ends it by itself, within `delivery.broken_probe_interval`.
- The Internal alert resolves and `gateway_recovered` (`connection`, `by` — `probe` or `check` — and
  `broken_for_s`) is logged when the Broken state ends; the chart rule resolves at the Leader's next update of
  `muster_notification_gateway_broken`, within 5 minutes.
- Calls that come due afterwards go as usual. Check the Alert Groups whose Timeline shows `page_not_delivered` with
  `gateway_broken`: their people were not called.
