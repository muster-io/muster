# MusterClockSkew

## Meaning

A replica's clock differs from the database clock by more than 2 seconds. The chart rule is:

```promql
max by (instance) (abs(muster_clock_skew_seconds{job="muster"})) > 2
```

It fires after 5 minutes, with severity `warning` and the label `instance`. It is a chart rule only. Each replica
measures its clock against the database every minute and exports the difference as `muster_clock_skew_seconds`,
positive when the replica is ahead.

## Impact

Every replica takes the time from its own system clock, and the replicas share one database, so a replica that is off
disagrees with the others:

- Timestamps it writes — of Alerts, Alert Groups and Timeline entries — are off by the skew, and the timers and
  windows it evaluates, such as Snooze ends, Ack timeouts and the Reopen window, run early or late.
- TOTP codes may be refused at sign-in.
- ID tokens of the identity provider may be refused as not yet valid or expired, so OIDC sign-in fails.
- Signatures of outgoing webhooks carry a timestamp that receivers may reject.
- Leases of claimed rows and of the Leader lock expire too early or too late, which can make work run twice or wait.

## Diagnosis

1. Find the replica: `muster_clock_skew_seconds` by `instance`.
2. Read its log lines `clock_skew` (`skew_seconds`, `warning_seconds`): logged on every measurement above
   `process.clock_skew_warning` (2 s).
3. Run `muster doctor` on the same host or node. The line `clock_skew` prints the measured skew, and warns with
   "… against the database clock, more than 2s" above the limit.
4. Check time synchronisation on the node of the replica and on the database server: whether NTP (chrony,
   systemd-timesyncd or the cloud provider's time service) runs and is in sync.

## Fix

- Synchronise the clocks of the nodes and of the database server with NTP. The skew is measured again every minute,
  and the alert resolves 5 minutes after it falls under 2 seconds.
- When only one node is off, move the replica to another node until its clock is fixed.
