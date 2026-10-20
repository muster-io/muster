# MusterDeliverySlow

## Meaning

Deliveries are slow: the 95th percentile of the time from receiving a Snapshot to the API call that delivers the
resulting change is above 5 seconds, the objective Muster keeps while its rate limiters have room. The chart rule is:

```promql
histogram_quantile(0.95, sum by (le) (rate(muster_delivery_latency_seconds_bucket{job="muster"}[15m]))) > 5
```

It fires after 15 minutes, with severity `warning`. It is a chart rule only and has no labels of its own: it covers
every Destination together. Changes made by Commands or timers are not measured, only those caused by Snapshots.

## Impact

- New Alert Groups and their changes appear in the messengers later than usual. Nothing is lost.
- The UI and the API show the current state without delay; Commands keep working.

## Diagnosis

1. Find the slow Destinations:
   `histogram_quantile(0.95, sum by (le, destination) (rate(muster_delivery_latency_seconds_bucket{job="muster"}[15m])))`.
   `muster_destination_info` gives their names.
2. Look for rate limits: `increase(muster_delivery_attempts_total{job="muster",outcome="retry_after"}[15m])` by
   `destination`. A messenger that answers "retry after" makes Muster wait as long as it asks.
3. Look for a Storm: `muster_storm_active` by `route`, the log line `storm_started` and **Storms** on the System
   status page (`/admin/system-status`). More than `route.storm_threshold` (20) new Alert Groups per minute on a
   Route fill the limiters.
4. Check Muster's own limiters. Telegram: `destination.telegram.limiter` (10 requests per minute per Destination, sends
   and edits together, in the channel and its discussion group) and `connection.telegram.limiter` (15 messages per
   second per Connection). Slack: `destination.slack.limiter` (30 requests per minute per Destination, sends and edits
   together) and `connection.slack.limiter` (50 requests per minute per Connection). A busy Destination waits for its
   limiter, which is expected and not a fault.
5. Check the latency of the calls themselves:
   `histogram_quantile(0.95, sum by (le, outcome) (rate(muster_client_request_duration_seconds_bucket{job="muster",client="delivery"}[15m])))`.
   Slow calls point at the network, a proxy or the messenger.
6. Check **Delivery queues** on the System status page; when a queue keeps growing, see
   [MusterDeliveryQueueGrowing](MusterDeliveryQueueGrowing.md).

## Fix

- A Storm or a noisy Route: tune the Route's grouping so that one problem makes one Alert Group, or create an
  Alertmanager silence for the source. The Storm summary already replaces separate messages while the Storm lasts.
- Rate limits of one chat: spread the Routes of that Destination over more Destinations (chats or channels).
- Slow calls: fix the network path or the proxy of the Connection; for Telegram in a restricted network, see
  [Telegram in restricted networks](../../messengers/telegram-restricted-networks.md).
- The alert clears once the 95th percentile over 15 minutes is back under 5 seconds.
