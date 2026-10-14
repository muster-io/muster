# MusterNoLeader

## Meaning

Every replica of Muster reports that it is not the Leader. The chart rule is:

```promql
max(muster_leader{job="muster"}) == 0
```

It fires after 2 minutes, with severity `critical`. It is a chart rule only. One replica, the **Leader**, holds a lock
in the database and runs the Leader tasks; when it cannot confirm its lock within `leader.fencing_timeout` (15 s) it
stops them, and any replica may take the lock. This alert means that none has for 2 minutes.

!!! warning "Route it around Muster"
    Send this alert through an Alertmanager route that bypasses Muster. Muster still delivers without a Leader, but
    the problem behind the missing Leader — usually the database — may stop delivery next.

## Impact

What stops while no replica leads:

- **Telegram polling**: Connections in the long polling update mode receive no button presses and no messages from
  the discussion group.
- The **Heartbeat** check and the **Stale** check: no Integration becomes Heartbeat lost, and no alert is resolved as
  Stale.
- Partition maintenance, retention and the pruning of short-lived state, the outgoing heartbeat, the expiry check of
  the OIDC client secret and the Leader-only metrics (`muster_alert_groups`, `muster_delivery_queue`, `muster_destination_broken`,
  `muster_heartbeat_lost`, `muster_ingest_backlog`, `muster_storm_active`), which go stale or disappear.

What keeps working: ingestion, delivery to every Destination, timers (the end of a Snooze, Ack timeouts and Reminders),
the UI, the API and the webhook update mode of Telegram. After `leader.absence_notice` (2 minutes) without a Leader, the
UI shows the banner "No replica is leading: Telegram polling, Heartbeat and Stale checks are paused."

## Diagnosis

1. Open **System status** (`/admin/system-status`). **Replicas** lists every replica that refreshed its record, when
   it last did ("Refreshed …"), and which one is the **Leader**; with no Leader, no replica carries the flag.
2. Check `muster_leader` per replica: `max by (instance) (muster_leader{job="muster"})`. A replica that does not
   appear at all is not scraped; see [MusterDown](MusterDown.md).
3. Read the logs of the replicas:
    - `leadership_lost` (`replica`, `error`): the Leader could not confirm its lock within `leader.fencing_timeout`,
      or its lock session failed.
    - `leadership_acquired` (`replica`): a replica took the lock. Alternating lost and acquired lines mean that the
      lock keeps breaking.
    - `leader_task_failed` (`task`, `error`): a Leader task failed, usually because the database was unavailable.
4. Run `muster doctor` with the replicas' settings. `session_connection` must say "advisory locks and LISTEN work":
   the Leader lock is an advisory lock on the session connection. Behind a pooler in transaction mode, such as
   PgBouncer, advisory locks do not hold, and `MUSTER_DATABASE_SESSION_URL` must point at the database directly or
   at a pool in session mode.
5. Check the database itself: its availability, failovers, connection limits and the latency between the replicas
   and the database.

## Fix

- Restore the database or the network path to it. Once the session connection works again, a replica takes the lock
  within seconds and the banner disappears.
- Set `MUSTER_DATABASE_SESSION_URL` when the main connection goes through a pooler in transaction mode, and restart
  the replicas.
- Raise the database's connection limit if the session connections are refused: each replica holds about three of
  them besides its main pool.
- Once a Leader returns, the paused tasks run again at their next interval: the new Leader polls Telegram, and the
  Heartbeat and Stale checks resume from the signals and Snapshots that ingestion recorded meanwhile.
