// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Alert Group, delivery and Alert reads for the web tests. Every required field holds a neutral value — a
// firing Alert Group with one firing Alert, no allowed Commands and no Teams, a delivered Root message to a healthy
// Destination, a firing Alert in no Alertmanager group and no Team — and a test passes, field by field, only what it
// cares about. A required field added to a read is set here, the one place the TypeScript check names, instead of in
// every test that builds one.

import type {
  AlertGroup,
  AlertGroupChain,
  AlertGroupDelivery,
  Assignee,
  IntegrationAlert,
} from "../../api/gen/model";

export function alertGroup(overrides: Partial<AlertGroup> = {}): AlertGroup {
  return {
    id: "AG0000000000AA",
    number: 412,
    title: "Disk full",
    status: "firing",
    severity_level: "warning",
    urgent: false,
    route: { id: "RT0000000000OP", name: "ops" },
    integrations: [],
    started_at: "2026-10-12T09:00:00Z",
    last_changed_at: "2026-10-12T09:00:00Z",
    reopen_count: 0,
    firing_alert_count: 1,
    resolved_alert_count: 0,
    unclaimed: false,
    delivery_problem: false,
    allowed_commands: [],
    teams: [],
    escalation_chains: [],
    assignees: [],
    ...overrides,
  };
}

/** A run of an Escalation chain on an Alert Group: "devops", running at its step 1 with the next one due at 10:05. */
export function alertGroupChain(overrides: Partial<AlertGroupChain> = {}): AlertGroupChain {
  return {
    run_id: "ER0000000000AA",
    chain: { id: "EC0000000DEVO1", name: "devops", owning_team: null },
    chain_name: "devops",
    sources: [{ kind: "route", team: null }],
    state: "running",
    step: { number: 1, kind: "page_on_call" },
    next_step_at: "2026-10-12T10:05:00Z",
    started_at: "2026-10-12T09:00:00Z",
    ...overrides,
  };
}

/** A person an Escalation chain Paged and reached, with the owning Team of the chain; none for a chain no Team owns. */
export function assignee(login: string, team: string | null = null): Assignee {
  return {
    user: {
      id: `SR00000000${login.toUpperCase().padEnd(4, "0").slice(0, 4)}`,
      name: login,
      login,
      deactivated: false,
    },
    team:
      team === null
        ? null
        : {
            id: `TM0000000${team.toUpperCase().padEnd(5, "0").slice(0, 5)}`,
            name: team,
            slug: team,
            deleted: false,
          },
    chain: { id: "EC0000000DEVO1", name: "devops", owning_team: null },
    since: "2026-10-12T09:00:00Z",
  };
}

export function alertGroupDelivery(
  overrides: Partial<AlertGroupDelivery> = {},
): AlertGroupDelivery {
  return {
    destination: {
      id: "DS0000000000AA",
      name: "alerts",
      type: "mattermost",
      health: { state: "healthy" },
    },
    entries: [],
    state: "delivered",
    thread_not_attached: false,
    possible_duplicate: false,
    past_edit_window: false,
    ...overrides,
  };
}

export function integrationAlert(overrides: Partial<IntegrationAlert> = {}): IntegrationAlert {
  return {
    fingerprint: "a1",
    labels: { alertname: "DiskFull" },
    state: "firing",
    starts_at: "2026-10-07T08:00:00Z",
    last_seen_at: "2026-10-07T09:00:00Z",
    alertmanager_groups: [],
    teams: [],
    ...overrides,
  };
}
