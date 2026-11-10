// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Routes and Group keys" (docs/concepts/routes-and-group-keys.md) and "Messages and templates"
// (docs/configure/messages-and-templates.md). The fake Alertmanager first sends the four KubePodCrashLooping Alerts of
// the page's Group key table, all with team=payments, to the Integration "payments-am", so that the Group key preview
// has Stored Snapshots to read; the outgoing webhook Destination "payments" is made through the API. The Admin then
// creates the Route "payments" with the Paging profile, the Matcher team="payments", the default Group key and its
// preview, and the Destination; previews another Group key on the saved Route; makes its Alert line Custom with the
// preview of the built-in example; creates the Lookup table "dashboards" and the Link rule "Dashboard" that reads it.
// The Default route is not edited. A second test makes the Team "payments", the Route "Infrastructure" and three
// KubernetesContainerOOMKilled Alerts, and takes the four tabs of the preview of the Route's Root message against
// their Alert Group, with the layout of each messenger. It deletes what it created.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  adminApi,
  alertGroupOf,
  ensureTeam,
  fakeIntegration,
  fam,
  notify,
  removeIntegration,
  removeNamed,
  removeRoute,
  removeTeamsBySlug,
  signInAdmin,
} from "../support";
import { capture, nav, organizationNav, scrollToTop } from "./capture";

const ROUTES = "concepts/routes-and-group-keys";
const TEMPLATES = "configure/messages-and-templates";
const INTEGRATION = "payments-am";
const ROUTE = "payments";
const DESTINATION = "payments";
const TABLE = "dashboards";
const RULE = "Dashboard";
const AM_GROUP = "payments-crashloop";
const INFRA_INTEGRATION = "infra-am";
const INFRA_ROUTE = "Infrastructure";
const INFRA_GROUP = "infra-oom";
const OOM = "KubernetesContainerOOMKilled";
/** The pods of the three KubernetesContainerOOMKilled Alerts of the preview of the Root message. */
/** The pod name that the capture would take for an id, as it is quoted in the layout's JSON and has 20 characters or more. */
const POD_KEEP: [string, string][] = [['"checkout-worker-8e6a0"', '"checkout-worker-8e6a0"']];
const OOM_PODS = ["checkout-api-5b2c1", "checkout-api-7d9f4", "checkout-worker-8e6a0"];
/** The four Alerts of the Group key table of the Routes page: cluster, namespace and pod. */
const ALERTS: [string, string, string][] = [
  ["prod", "payments", "api-1"],
  ["prod", "payments", "api-2"],
  ["prod", "search", "web-1"],
  ["staging", "payments", "api-1"],
];
const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

test.describe.configure({ mode: "serial" });

let destinationId = "";

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    await removeNamed(admin, "/api/v1/link-rules", RULE);
    await removeNamed(admin, "/api/v1/lookup-tables", TABLE, "?limit=50");
    await removeRoute(admin, ROUTE);
    await removeRoute(admin, INFRA_ROUTE);
    await removeTeamsBySlug(admin, ["payments"]);
    await removeNamed(admin, "/api/v1/destinations", DESTINATION, "?limit=500");
    await removeIntegration(admin, INTEGRATION);
    await removeIntegration(admin, INFRA_INTEGRATION);
  } finally {
    await admin.dispose();
  }
}

/** The Group key editor with its preview, side by side in the Route form. */
function groupKeyArea(page: Page): Locator {
  return page.getByTestId("group-key-preview").locator("xpath=..");
}

async function removeKeyLabel(page: Page, label: string): Promise<void> {
  await page.getByRole("button", { name: `Remove ${label} from the Group key` }).click();
  await expect(page.getByTestId("group-key-label").filter({ hasText: label })).toHaveCount(0);
}

async function addKeyLabel(page: Page, label: string): Promise<void> {
  await page.getByRole("textbox", { name: "Group key" }).fill(label);
  await page.getByRole("button", { name: "Add label" }).click();
  await expect(page.getByTestId("group-key-label").filter({ hasText: label })).toBeVisible();
}

test.beforeAll(async () => {
  await cleanUp();
  const integrationId = await fakeIntegration(INTEGRATION);
  await fam("PUT", `/groups/${AM_GROUP}`, {
    receiver: INTEGRATION,
    route: "{}",
    labels: { alertname: "KubePodCrashLooping" },
  });
  for (const [i, [cluster, namespace, pod]] of ALERTS.entries()) {
    await fam("PUT", `/groups/${AM_GROUP}/alerts/a${i + 1}`, {
      labels: { severity: "warning", team: "payments", cluster, namespace, pod },
      annotations: { summary: `Pod ${namespace}/${pod} is restarting` },
    });
  }
  await notify(integrationId, AM_GROUP, { reason: "first notification" });
  const admin = await adminApi();
  try {
    const created = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "webhook",
        name: DESTINATION,
        mode: "events",
        events: { url: "http://127.0.0.1:18093/hook/payments", headers: [] },
        proxy: { enabled: false },
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    destinationId = created.destination.id;
  } finally {
    await admin.dispose();
  }
});

test.afterAll(cleanUp);

test("creates a Route, edits its message, and adds a Lookup table and a Link rule", async ({
  page,
}) => {
  test.setTimeout(240_000);
  await signInAdmin(page);

  // Create a Route: the profile, the Matcher, the Group key with its preview, the Destination, the policy, the list.
  await nav(page, "Routes").click();
  await page.getByRole("link", { name: "Create route" }).click();
  await expect(page.getByRole("button", { name: "Paging" })).toBeVisible();
  await capture(page, ROUTES, "04-choose-profile", {
    highlight: page.getByRole("button", { name: "Paging" }),
  });
  await page.getByRole("button", { name: "Paging" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill(ROUTE);
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("team");
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill("payments");
  const matchers = page.getByRole("group", { name: "Matchers" });
  await capture(page, ROUTES, "01-matchers", { locator: matchers, padding: 16 });

  await expect(page.getByTestId("group-key-label")).toHaveText([
    "alertname",
    "severity",
    "cluster",
  ]);
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByTestId("preview-proposed")).toHaveText("Proposed: 2 Alert Groups");
  await capture(page, ROUTES, "02-group-key", { locator: groupKeyArea(page), padding: 16 });
  await scrollToTop(page.getByRole("heading", { name: "Create route", level: 1 }));
  await capture(page, ROUTES, "05-new-route", { waitFor: page.getByTestId("preview-proposed") });

  await page.getByLabel("Add a Destination").selectOption(destinationId);
  const destinations = page.getByTestId("route-destinations");
  await expect(page.getByTestId("route-destination")).toHaveCount(1);
  await capture(page, ROUTES, "06-destinations", {
    locator: destinations,
    padding: 16,
    waitFor: page.getByTestId("route-destination").getByText("Healthy"),
  });
  const severity = page.getByTestId("route-severity-section");
  await capture(page, ROUTES, "07-minimum-severity", {
    locator: severity,
    padding: 16,
    waitFor: severity.getByLabel("Minimum severity", { exact: true }),
  });
  await scrollToTop(page.getByTestId("route-lifecycle"));
  await capture(page, ROUTES, "08-policy", { waitFor: page.getByTestId("route-timers") });

  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
  const row = page
    .getByTestId("route-row")
    .filter({ has: page.getByRole("link", { name: ROUTE, exact: true }) });
  await expect(row.getByTestId("route-matchers")).toHaveText('team="payments"');
  await capture(page, ROUTES, "09-saved-route", { waitFor: row });

  // Preview before you save: the saved Route with a proposed Group key beside its current one.
  await row.getByRole("link", { name: ROUTE, exact: true }).click();
  await expect(page.getByRole("heading", { name: ROUTE, level: 1 })).toBeVisible();
  const routeUrl = page.url();
  await removeKeyLabel(page, "severity");
  await addKeyLabel(page, "namespace");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByTestId("preview-current")).toHaveText("Current: 2 Alert Groups");
  await expect(page.getByTestId("preview-proposed")).toHaveText("Proposed: 3 Alert Groups");
  await capture(page, ROUTES, "03-preview", { locator: groupKeyArea(page), padding: 16 });

  // Edit a Route's message: the Message section, then the Alert line made Custom with its preview.
  await page.goto(routeUrl);
  const message = page.getByTestId("route-message");
  await scrollToTop(message);
  await expect(message.getByLabel("Language")).toHaveValue("en");
  await expect(message.getByTestId("message-label").first()).toBeVisible();
  await capture(page, TEMPLATES, "01-message", {
    waitFor: page.getByTestId("template-line").getByTestId("template-preview-output"),
  });
  const line = page.getByTestId("template-line");
  await line.getByRole("radio", { name: "Custom" }).check();
  await line
    .getByRole("textbox", { name: "Alert line" })
    .fill("{{ .Labels.pod }}: {{ .Annotations.summary }}");
  const output = line.getByTestId("template-preview-output");
  await expect(output).toContainText("checkout-1: Error rate above 5%");
  await scrollToTop(line);
  await capture(page, TEMPLATES, "02-alert-line", { locator: line, padding: 16 });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);

  // Add a Lookup table: dashboards with the column url and a row per cluster.
  await nav(page, "Organization").click();
  await organizationNav(page, "Lookup tables").click();
  await page.getByRole("link", { name: "Create table" }).click();
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(TABLE);
  await page.getByLabel("Name of column 1").fill("url");
  for (const [index, key] of ["prod-eu", "prod"].entries()) {
    await page.getByRole("button", { name: "Add row" }).click();
    await page.getByLabel(`Key of row ${index + 1}`).fill(key);
    await page
      .getByLabel(`url of row ${key}`, { exact: true })
      .fill(`https://grafana.example.org/d/abc123/${key}`);
  }
  await capture(page, TEMPLATES, "03-new-lookup-table", {
    highlight: page.getByRole("button", { name: "Create", exact: true }),
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const table = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: TABLE, exact: true }) });
  await expect(table.getByTestId("lookup-table-rows")).toHaveText("2 rows");
  await capture(page, TEMPLATES, "04-lookup-tables", { waitFor: table });

  // Add a Link rule: Dashboard for every Alert Group, its URL template read from the table, with its preview.
  await organizationNav(page, "Link rules").click();
  await page.getByRole("link", { name: "Create rule" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill(RULE);
  await expect(page.getByRole("radio", { name: "Alert Group" })).toBeChecked();
  await page
    .getByRole("textbox", { name: "URL template" })
    .fill('{{ lookup "dashboards" .Labels.cluster "url" }}');
  await page.getByTestId("sample-picker").selectOption({ label: "Built-in example" });
  await expect(page.getByTestId("template-preview-output").getByRole("link")).toHaveAttribute(
    "href",
    "https://grafana.example.org/d/abc123/prod",
  );
  await capture(page, TEMPLATES, "05-new-link-rule", {
    highlight: page.getByRole("button", { name: "Create", exact: true }),
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const rule = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: RULE, exact: true }) });
  await expect(rule).toBeVisible();
  await capture(page, TEMPLATES, "06-link-rules", { waitFor: rule });
});

test("shows the Root message in Mattermost, Slack, Telegram and through a Bridge", async ({
  page,
}) => {
  test.setTimeout(240_000);
  const admin = await adminApi();
  let routeId = "";
  try {
    // The Route "payments" of the first test would take the Alerts of team=payments first.
    await removeRoute(admin, ROUTE);
    await ensureTeam(admin, "payments");
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    const route = await admin.call<{ id: string }>("POST", "/api/v1/routes", {
      name: INFRA_ROUTE,
      matchers: [{ label: "alertname", op: "=", value: OOM }],
      group_key: ["alertname", "system", "team"],
      destination_entries: [],
      policy: profiles.items.find((p) => p.id === "paging")?.policy,
    });
    routeId = route.id;
  } finally {
    await admin.dispose();
  }
  const integrationId = await fakeIntegration(INFRA_INTEGRATION);
  await fam("PUT", `/groups/${INFRA_GROUP}`, {
    receiver: INFRA_INTEGRATION,
    route: "{}",
    labels: { alertname: OOM },
  });
  for (const [i, pod] of OOM_PODS.entries()) {
    await fam("PUT", `/groups/${INFRA_GROUP}/alerts/a${i + 1}`, {
      labels: {
        severity: "warning",
        system: "kubernetes",
        team: "payments",
        namespace: "checkout",
        pod,
      },
      annotations: {
        summary: "Container killed for memory",
        description: "The container exceeded its limit.",
      },
    });
  }
  await notify(integrationId, INFRA_GROUP, { reason: "first notification" });
  const group = await alertGroupOf(integrationId, OOM);

  await signInAdmin(page);
  await page.goto(`/routes/${routeId}`);
  const root = page.getByTestId("template-root_message");
  await scrollToTop(page.getByTestId("route-message"));
  await expect(root.getByRole("radio", { name: "Built-in" })).toBeChecked();
  await root.getByTestId("sample-picker").selectOption({ value: `alert_group:${group}` });
  const tabs: [string, string, string][] = [
    ["Mattermost", "layout-mattermost", "07-preview-mattermost"],
    ["Slack", "layout-slack", "08-preview-slack"],
    ["Telegram", "layout-telegram", "09-preview-telegram"],
  ];
  for (const [tab, id, name] of tabs) {
    await root.getByRole("tab", { name: tab }).click();
    await expect(root.getByTestId(id).getByTestId("layout-title")).toContainText("— Firing");
    await expect(root.getByTestId(id).getByTestId("layout-alerts")).toContainText(
      "checkout-worker-8e6a0",
    );
    await expect(root.getByTestId(id).getByTestId("layout-fields")).toContainText("Teams");
    await capture(page, TEMPLATES, name, { locator: root, padding: 16, replace: POD_KEEP });
  }
  await root.getByRole("tab", { name: "Bridge" }).click();
  await expect(root.getByTestId("layout-json")).toContainText('"key": "firing"');
  await capture(page, TEMPLATES, "10-preview-bridge", {
    locator: root,
    padding: 16,
    replace: [...POD_KEEP, ["http://localhost:8080", "https://muster.example.org"]],
  });
});
