# MusterScheduleGap

## Meaning

A Schedule has a **gap** now or one that starts within the next 7 days (`schedule.gap_alert_horizon`): a time when no
Override and no Rotation layer has a Shift with a person who may be Paged, so nobody is On-call. A person may not be
Paged when they are a Viewer, disabled, or no longer in the Schedule's Team, and their Shifts are skipped; see
[Schedules](../../concepts/schedules.md#gaps-and-the-internal-alert).

Only a Schedule whose gap alert is active reports its gaps: by default (`schedule.gap_alert` `by_default`) the Team's
main Schedule, and any Schedule an Admin set to `on`. The Leader looks for gaps every 5 minutes
(`schedule.gap_scan_interval`).

This alert has two sources with the same name, severity `warning` and this page:

- The **Internal alert**, raised by Muster itself as an Alert of the built-in "Muster" Integration, with the labels
  `schedule` (the Schedule's id), `schedule_name`, `team_id`, `team_name` and, under the Organization's Team label
  (`team` by default), the Team's slug, so that the alert belongs to the Team: its Alert Group lists the Team, and a
  Route can pick it by the Team label. The annotation `gap_start` says when the nearest gap starts. It stays raised
  while the gap lasts, and resolves at the next scan once no gap is current or within the horizon, when the gap alert
  is no longer active, or when the Schedule is deleted.
- The **chart rule**, the backup for when Muster cannot deliver the Internal alert:

    ```promql
    max by (schedule) (muster_schedule_gap{job="muster"}) == 1
    ```

    It fires at once with the label `schedule`. `muster_schedule_gap` is set by the Leader: 1 for a Schedule whose gap
    alert is active and that has a gap now or within the horizon, 0 for every other Schedule.

## Impact

- During the gap nobody is On-call in the Schedule: "On call now" shows the gap, and nobody of this Schedule can be
  Paged for the Team's Alert Groups then.
- Alert Groups are still created, routed and delivered to their Destinations; only the On-call is missing.

## Diagnosis

1. Read the labels: `schedule_name` and `team_name` name the Schedule, and `gap_start` says when the gap starts.
2. Read the gaps of the Schedule: `GET /api/v1/schedules/<schedule>/on-call` lists a warning `gap` with its `start`
   and `end` for each gap of the next 14 days, and the warnings `disabled`, `viewer` and `not_in_team` name the people
   whose Shifts are skipped. `GET /api/v1/schedules/<schedule>` gives the nearest gap as `next_gap`.
3. A gap where a person should be On-call usually means that the person was disabled, turned into a Viewer or taken
   out of the Team, for example by the groups of the identity provider; a gap at the edge of a Rotation layer's limits
   usually means that the limits leave hours uncovered.
4. Find the log line `schedule_gap_detected` (`schedule`, `team`, `gap_start`) of the raise.

## Fix

- **Fill the gap with an Override**: any Responder of the Team, or an Admin, makes the person who takes the time
  On-call from the start to the end of the gap with `POST /api/v1/schedules/<schedule>/overrides`. The next scan
  resolves the alert once no other gap is within the horizon.
- **Change the Rotation layers**: an Admin replaces a person who may no longer be Paged, adds people to a Rotation
  layer, widens its limits or adds a Rotation layer below that covers the rest of the time.
- **Turn the alert off** for a Schedule that is meant to cover only part of the day, such as a second line in working
  hours: set `gap_alert` to `off` on it. A Schedule that is not its Team's main one reports its gaps only with
  `gap_alert` `on`.
- The Internal alert resolves and `schedule_gap_cleared` (`schedule`, `team`) is logged at the next scan; the chart
  rule resolves when the Leader sets `muster_schedule_gap` to 0 at the same scan.
