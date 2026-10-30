// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Alert Group, delivery and Alert reads for the web tests. Every required field holds a neutral value — a
// firing Alert Group with one firing Alert, no allowed Commands and no Teams, a delivered Root message to a healthy
// Destination, a firing Alert in no Alertmanager group and no Team — and a test passes, field by field, only what it
// cares about. A required field added to a read is set here, the one place the TypeScript check names, instead of in
// every test that builds one.

import type { AlertGroup, AlertGroupDelivery, IntegrationAlert } from "../../api/gen/model";

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
    ...overrides,
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
