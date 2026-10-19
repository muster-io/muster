# MusterDeliveryFailing

## Meaning

A Destination has deliveries waiting and has not had a successful delivery for 10 minutes. The chart rule is:

```promql
max by (destination) (muster_delivery_queue{job="muster"}) > 0 unless on (destination) sum by (destination) (increase(muster_delivery_attempts_total{job="muster",outcome="delivered"}[10m])) > 0
```

It fires after 5 minutes, with severity `critical` and the label `destination`, the Destination's id;
`muster_destination_info{destination="…"}` gives its name. It is a chart rule only. It fires before the Destination
becomes Broken as well as after: Muster retries a Transient error with a growing pause (`delivery.transient_backoff`,
from 2 s up to 5 min) and marks the Destination Broken only when the Transient budget runs out
(`delivery.transient_budget`, 10 attempts within 30 minutes) or after a Fatal error.

!!! warning "Route it around Muster"
    Send this alert through an Alertmanager route that bypasses Muster: the Destination it is about may be the one
    that would carry it.

## Impact

- The Root messages of the Destination are not created or edited: people in that chat do not see new Alert Groups,
  and the messages they see may show an old status. Thread replies wait too.
- Other Destinations, ingestion, Commands from the UI and the API, and timers keep working. The Alert Group pages show
  the current state, and the Alert Group list filter **Delivery problem** finds the affected Alert Groups.
- Nothing is lost while deliveries wait: once the Destination answers again, Muster delivers the current state of
  each Root message, not every change it missed.

## Diagnosis

1. Open **System status** (`/admin/system-status`): **Delivery queues** shows how many deliveries wait per
   Destination, and **Broken Destinations** whether the Destination is already Broken.
2. Open the Destination page (`/destinations/<id>`). **Health** says Healthy or Broken; a Broken Destination shows
   "Broken since HH:MM: {reason}. Muster tries again every {interval}."
3. Look at the outcomes of its attempts:
   `sum by (outcome) (increase(muster_delivery_attempts_total{job="muster",destination="<id>"}[15m]))`. `transient`
   and `unknown` point at the network or the messenger; `retry_after` at rate limits; `fatal` and `markup_rejected` at
   the configuration or the message; `template_error` at a template, see [MusterTemplateError](MusterTemplateError.md).
4. Read the log lines `delivery_attempt` for the Destination (`destination` is its id): `outcome`, `code` (the HTTP
   status of the answer or the `error_code` of a Telegram answer, 0 when the outcome carries no status), `reason` (the
   masked error the messenger answered or the call failed with), `attempt`, `duration_ms` and `retry_after_ms`.
   `outbound_blocked` means that the outbound address policy refused the address or the proxy (`rule` names why);
   `delivery_work_failed` that the worker itself could not attempt, for example because the database connection was
   lost.
5. Compare with all outbound calls of the delivery client class:
   `sum by (outcome) (rate(muster_client_requests_total{job="muster",client="delivery"}[5m]))`. Failures across
   every Destination point at the network, a proxy or DNS rather than one messenger.
6. Press **Check** on the Destination page, or run `muster doctor`, which prints one line per Mattermost and Telegram
   Destination (`destination <name>`) with the result of its check.

## Fix

- Network, DNS or proxy: fix the path from the replicas to the messenger, the proxy of the Connection, or the
  Organization's **Outbound addresses** (`/admin/organization/outbound`) when `outbound_blocked` names the address.
- Credentials or permissions: replace the bot token on the Connection, or give the bot back the rights the
  Destination check asks for, then press **Check**.
- Telegram is blocked in your network: see
  [Telegram in restricted networks](../../messengers/telegram-restricted-networks.md).
- When the Destination has become Broken, follow [MusterDestinationBroken](MusterDestinationBroken.md).
- The alert resolves once a delivery to the Destination succeeds, or its queue is empty.
