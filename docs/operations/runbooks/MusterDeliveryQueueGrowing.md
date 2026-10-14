# MusterDeliveryQueueGrowing

## Meaning

A Destination has more than 100 deliveries waiting, and the number keeps growing. The chart rule is:

```promql
max by (destination) (muster_delivery_queue{job="muster"}) > 100 and max by (destination) (deriv(muster_delivery_queue{job="muster"}[15m])) > 0
```

It fires after 15 minutes, with severity `warning` and the label `destination`, the Destination's id;
`muster_destination_info` gives its name. It is a chart rule only. `muster_delivery_queue` is counted by the Leader
every 15 seconds: the pending deliveries and Thread replies that are due.

## Impact

- The messages of that Destination fall further and further behind: new Alert Groups and their changes appear late.
  Deliveries are still made; nothing is lost.
- Other Destinations, the UI and the API keep working.

## Diagnosis

1. Open **System status** (`/admin/system-status`): **Delivery queues** shows the waiting deliveries per Destination,
   **Storms** the Routes in a Storm and **Broken Destinations** whether the Destination stopped.
2. Look at the outcomes of its attempts:
   `sum by (outcome) (increase(muster_delivery_attempts_total{job="muster",destination="<id>"}[15m]))`. Many
   `retry_after` mean that the messenger limits the rate; `transient` or `unknown` that calls fail; few attempts of any
   kind that Muster's own limiter holds them back.
3. Check whether a Storm feeds the queue: `muster_storm_active` by `route` and the log line `storm_started`.
   `route.storm_threshold` is 20 new Alert Groups per minute.
4. For Telegram, compare the load with the limiters: `destination.telegram.limiter` allows 10 requests per minute per
   Destination, sends and edits together, in the channel and its discussion group.
5. When no delivery succeeds at all, follow [MusterDeliveryFailing](MusterDeliveryFailing.md) instead.

## Fix

- Reduce what feeds the Destination: tune the grouping of its Routes, create an Alertmanager silence for a noisy source,
  or move some Routes to another Destination.
- When calls fail, fix the cause as in [MusterDeliveryFailing](MusterDeliveryFailing.md).
- A queue that only grows during a Storm shrinks after it: Muster coalesces the changes of a Root message, so it
  delivers the current state rather than every change.
