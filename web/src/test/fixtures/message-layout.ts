// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A complete MessageLayout for the web tests: the firing Alert Group #1287 of three KubernetesContainerOOMKilled Alerts
// of C-29's first scenario. A test passes, field by field, only what it cares about.

import type { MessageLayout } from "../../api/gen/model";

export function messageLayout(overrides: Partial<MessageLayout> = {}): MessageLayout {
  return {
    number: 1287,
    title: "KubernetesContainerOOMKilled",
    status: { key: "firing", word: "Firing" },
    fields: [
      { name: "Severity", value: "warning" },
      { name: "Route", value: "Infrastructure" },
      { name: "Teams", value: "payments" },
      { name: "Started", value: "03:12" },
    ],
    summary: "Container restarted for lack of memory",
    description: "A container in checkout went over its memory limit and was stopped.",
    labels: [
      { key: "system", value: "kubernetes" },
      { key: "team", value: "payments" },
      { key: "namespace", value: "checkout" },
    ],
    alerts: {
      label: "pod",
      values: [
        { value: "checkout-api-7d9f4", resolved: false },
        { value: "checkout-api-5b2c1", resolved: false },
        { value: "checkout-worker-8e6a0", resolved: true },
      ],
      total: 3,
    },
    notices: [],
    assignees: [
      { name: "@ivan", ref: null, team: { name: "devops" } },
      { name: "@petr", ref: null, team: { name: "payments" } },
    ],
    owner: null,
    links: [
      { label: "Open in Muster", url: "https://muster.example.org/alert-groups/AG1287" },
      { label: "Runbook", url: "https://wiki.example.org/runbooks/oom" },
      { label: "Source", url: "javascript:alert(1)" },
    ],
    ...overrides,
  };
}
