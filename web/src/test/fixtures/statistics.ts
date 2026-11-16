// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete statistics reads for the web tests: an item of By route, By integration or By team, a person of By people,
// and the data of C-09.AC-28 (alice 3 · 0 · 4 min · 27 min, bob 1 · 2 · 10 min · 10 min, the Service account terraform
// 0 · 1 · — · —). Every required field holds a neutral value — no Alert Groups, nothing to measure — and a test passes,
// field by field, only what it cares about. A required field added to a read is set here, the one place the TypeScript
// check names.

import type {
  ActorRef,
  AlertGroupActorStatisticsItem,
  AlertGroupStatisticsItem,
  DurationStats,
} from "../../api/gen/model";

/** Nothing to measure. */
export const NO_DURATION: DurationStats = { count: 0, median_seconds: null, p95_seconds: null };

export function statisticsItem(
  overrides: Partial<AlertGroupStatisticsItem> = {},
): AlertGroupStatisticsItem {
  return {
    subject: { id: "RT00000000000S", name: "st" },
    alert_group_count: 0,
    time_to_acknowledge: NO_DURATION,
    time_to_resolve: NO_DURATION,
    per_day: [],
    ...overrides,
  };
}

export function actor(overrides: Partial<ActorRef> = {}): ActorRef {
  return {
    kind: "user",
    id: "SR0000000000AL",
    name: "alice",
    login: "alice",
    deactivated: false,
    ...overrides,
  };
}

/** A row of By people; `actor` null is the row "Unknown". */
export function personItem(
  overrides: Partial<AlertGroupActorStatisticsItem> = {},
): AlertGroupActorStatisticsItem {
  return {
    actor: actor(),
    acknowledged_first_count: 0,
    resolved_count: 0,
    time_to_acknowledge: NO_DURATION,
    ...overrides,
  };
}

/** The data of C-09.AC-28, in the order the server gives: most first acknowledgements first. */
export function acceptanceRows(): AlertGroupActorStatisticsItem[] {
  return [
    personItem({
      acknowledged_first_count: 3,
      resolved_count: 0,
      time_to_acknowledge: { count: 3, median_seconds: 240, p95_seconds: 1644 },
    }),
    personItem({
      actor: actor({ id: "SR0000000000BO", name: "bob", login: "bob" }),
      acknowledged_first_count: 1,
      resolved_count: 2,
      time_to_acknowledge: { count: 1, median_seconds: 600, p95_seconds: 600 },
    }),
    personItem({
      actor: actor({ kind: "service_account", id: "SA0000000000TF", name: "terraform" }),
      resolved_count: 1,
    }),
  ];
}
