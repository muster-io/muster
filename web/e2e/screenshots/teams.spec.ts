// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Teams" and "Routing to Teams" (docs/concepts/teams.md, docs/concepts/routing-to-teams.md), all in
// docs/assets/screenshots/concepts/teams/. Through the API the spec makes a Mattermost Connection with the five
// Destinations of operations and team channels (infra-critical, infra-notice, payments-critical, payments-notice and
// search-notice), the Teams "payments", with alice and bob by hand, petr through the identity provider group
// teams/payments/*, and its two Destinations by Severity, and "search", with nobody in it and its notice channel, and
// the Route "Infrastructure" with the option "Also to the Teams from the label". The fake Alertmanager then sends
// Alerts of category=infra for payments (a warning), for search (a critical) and for two values of the Team label that
// name no Team. The Admin reads the Teams list with its unknown values, the Team page of payments, the Route form with
// the option and the preview, and the Alert Group of the payments Alert with its Teams. It deletes what it created.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  alertGroupOf,
  type Api,
  ensureLocalUser,
  fakeIntegration,
  fam,
  nextIdpUser,
  notify,
  removeIntegration,
  removeNamed,
  removeRoute,
  removeTeamsBySlug,
  signInAdmin,
} from "../support";
import { capture, nav } from "./capture";

const TEAMS = "concepts/teams";
const INTEGRATION = "infra-am";
const CONNECTION = "Chat";
const ROUTE = "Infrastructure";
const AM_GROUP = "infra-alerts";
const PETR = "petr";
const PETR_SUB = "u-docs-teams-petr";
const NONE = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NONE],
  ),
);
const CHANNELS = {
  "infra-critical": "ch-infra-critical",
  "infra-notice": "ch-infra-notice",
  "payments-critical": "ch-payments-critical",
  "payments-notice": "ch-payments-notice",
  "search-notice": "ch-search-notice",
} as const;
type DestinationName = keyof typeof CHANNELS;

test.describe.configure({ mode: "serial" });

const destinationIds = new Map<DestinationName, string>();
let integrationId = "";
let routeId = "";
let paymentsId = "";

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    await removeRoute(admin, ROUTE);
    await removeTeamsBySlug(admin, ["payments", "search"]);
    for (const name of Object.keys(CHANNELS)) {
      await removeNamed(admin, "/api/v1/destinations", name, "?limit=500");
    }
    await removeNamed(admin, "/api/v1/connections", CONNECTION, "?limit=500");
    await removeIntegration(admin, INTEGRATION);
  } finally {
    await admin.dispose();
  }
}

async function putTeam(
  admin: Api,
  id: string,
  body: Record<string, unknown>,
): Promise<void> {
  const team = await admin.call<{ name: string; slug: string; etag: string }>(
    "GET",
    `/api/v1/teams/${id}`,
  );
  await admin.call(
    "PUT",
    `/api/v1/teams/${id}`,
    { name: team.name, slug: team.slug, own_working_hours: false, ...body },
    { "If-Match": team.etag },
  );
}

/** Sends one Alert of category=infra through the fake Alertmanager. */
async function alert(
  id: string,
  alertname: string,
  team: string,
  severity: string,
): Promise<void> {
  await fam("PUT", `/groups/${AM_GROUP}/alerts/${id}`, {
    labels: { alertname, category: "infra", team, system: "k8s", severity },
    annotations: { summary: `${alertname} is firing` },
  });
}

test.beforeAll(async () => {
  test.setTimeout(240_000);
  await cleanUp();
  const admin = await adminApi();
  try {
    const alice = await ensureLocalUser(admin, "alice", "responder");
    const bob = await ensureLocalUser(admin, "bob", "responder");
    await admin.call("POST", "/api/v1/oidc-settings/checks");

    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: CONNECTION,
      server_url: "http://127.0.0.1:18065",
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    for (const [name, channel] of Object.entries(CHANNELS)) {
      const created = await admin.call<{ destination: { id: string } }>(
        "POST",
        "/api/v1/destinations",
        {
          type: "mattermost",
          name,
          connection_id: connection.id,
          team_id: "team-dev",
          channel_id: channel,
          mentions: MENTIONS,
          limiter: { limit: 50, per_seconds: 1 },
        },
      );
      destinationIds.set(name as DestinationName, created.destination.id);
    }
    const id = (name: DestinationName) => destinationIds.get(name) ?? "";

    const payments = await admin.call<{ id: string }>("POST", "/api/v1/teams", {
      name: "payments",
      slug: "payments",
      description: "Card payments",
      own_working_hours: false,
    });
    paymentsId = payments.id;
    for (const user of [alice, bob]) {
      await admin.call("POST", `/api/v1/teams/${payments.id}/users`, { user_id: user });
    }
    await putTeam(admin, payments.id, {
      description: "Card payments",
      idp_entries: [{ group: "teams/payments/*" }],
      destination_entries: [
        { destination_id: id("payments-critical"), severity_condition: "critical" },
        { destination_id: id("payments-notice"), severity_condition: "below_critical" },
      ],
    });
    const search = await admin.call<{ id: string }>("POST", "/api/v1/teams", {
      name: "search",
      slug: "search",
      description: "Product search",
      own_working_hours: false,
    });
    await putTeam(admin, search.id, {
      description: "Product search",
      destination_entries: [{ destination_id: id("search-notice"), severity_condition: "any" }],
    });

    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    const route = await admin.call<{ id: string }>("POST", "/api/v1/routes", {
      name: ROUTE,
      matchers: [{ label: "category", op: "=", value: "infra" }],
      group_key: ["alertname", "system", "team"],
      destination_entries: [
        { destination_id: id("infra-critical"), severity_condition: "critical" },
        { destination_id: id("infra-notice"), severity_condition: "below_critical" },
      ],
      deliver_to_teams: true,
      policy: profiles.items.find((p) => p.id === "paging")?.policy,
    });
    routeId = route.id;
  } finally {
    await admin.dispose();
  }

  integrationId = await fakeIntegration(INTEGRATION);
  await fam("PUT", `/groups/${AM_GROUP}`, {
    receiver: INTEGRATION,
    route: "{}",
    labels: { job: "kube-state-metrics" },
  });
  await alert("oom", "KubernetesContainerOOMKilled", "payments", "warning");
  await alert("node", "KubernetesNodeNotReady", "search", "critical");
  await alert("pods", "KubernetesPodCrashLooping", "checkout", "warning");
  await alert("errors", "HighErrorRate", "Payments", "warning");
  await notify(integrationId, AM_GROUP, { reason: "first notification" });
});

test.afterAll(cleanUp);

async function openTeams(page: Page): Promise<void> {
  await nav(page, "Teams").click();
  await expect(page.getByRole("heading", { name: "Teams", level: 1 })).toBeVisible();
}

test("shows the Teams, the Team page, the Route form and the Alert Group", async ({
  page,
  browser,
}) => {
  test.setTimeout(240_000);

  // petr signs in through the identity provider with a group that the entry of payments takes.
  const context = await browser.newContext();
  const petr = await context.newPage();
  await nextIdpUser(PETR_SUB, PETR, ["oncall", "teams/payments/oncall"]);
  await petr.goto("/sign-in");
  await petr.getByText("Sign in with Example SSO").click();
  await expect(petr.getByTestId("user-menu-name")).toHaveText(PETR);
  await context.close();

  await signInAdmin(page);

  // The Teams list: both Teams with their people, and the values of the Team label that name no Team.
  await openTeams(page);
  await expect(page.getByTestId("team-label-values")).toContainText("checkout");
  await expect(page.getByTestId("team-label-values")).toContainText("Payments");
  await capture(page, TEAMS, "01-teams-list", {
    waitFor: page.getByRole("table", { name: "Teams" }).getByRole("link", { name: "payments" }),
  });

  // The Team page of payments: its people, by hand and through the identity provider group.
  await page
    .getByRole("table", { name: "Teams" })
    .getByRole("link", { name: "payments", exact: true })
    .click();
  await expect(page.getByRole("heading", { name: "payments", level: 1 })).toBeVisible();
  const people = page.getByTestId("team-people");
  await expect(people.getByRole("row").filter({ hasText: PETR })).toContainText(
    "group teams/payments/*",
  );
  await capture(page, TEAMS, "02-team-people", { locator: people, padding: 16 });
  await capture(page, TEAMS, "03-team-idp", { locator: page.getByTestId("team-idp"), padding: 16 });

  // Its Destinations by Severity.
  const destinations = page.getByTestId("team-destinations-section");
  await expect(destinations.getByTestId("team-destination")).toHaveCount(2);
  await capture(page, TEAMS, "04-team-destinations", { locator: destinations, padding: 16 });

  // The Route form: the Destinations with their conditions, the option, and where the Alerts go.
  await page.goto(`/routes/${routeId}`);
  await expect(page.getByRole("heading", { name: ROUTE, level: 1 })).toBeVisible();
  await expect(page.getByTestId("route-deliver-to-teams")).toBeChecked();
  await capture(page, TEAMS, "05-route-destinations", {
    locator: page.getByTestId("route-destinations"),
    padding: 16,
  });
  await capture(page, TEAMS, "06-route-teams-option", {
    locator: page.getByTestId("route-teams-option"),
    padding: 16,
  });
  const preview = page.getByTestId("route-delivery-preview");
  await expect(preview.getByTestId("delivery-group")).toHaveCount(4);
  await expect(
    preview
      .getByTestId("delivery-group")
      .filter({ hasText: "KubernetesContainerOOMKilled" })
      .getByTestId("delivery-group-teams"),
  ).toHaveText("Teams: payments");
  await capture(page, TEAMS, "07-route-preview", { locator: preview, padding: 16 });

  // The Alert Group of the payments Alert: its Teams, and the Destinations it reached.
  const group = await alertGroupOf(integrationId, "KubernetesContainerOOMKilled");
  await page.goto(`/alert-groups/${group}`);
  await expect(page.getByTestId("alert-group-teams")).toContainText("payments — by the Team label");
  await capture(page, TEAMS, "08-alert-group-teams", {
    locator: page.getByTestId("alert-group-header"),
    padding: 16,
  });
  expect(paymentsId).not.toBe("");
});
