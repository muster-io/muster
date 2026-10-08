// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Links block of an Alert Group's page against `muster dev`: Verification step 7 of its story. The Lookup table
// "grafana" holds the row prod, the Link rule "Dashboard" reads it, and the fake Alertmanager sends HighLatency with a
// runbook, a dashboard, a `javascript:` runbook and a generatorURL to the Integration "agl-lab". The page shows the
// links of the built-in "Explore" and the rule "Dashboard", then "Runbook", the annotation's "Dashboard" and "Source",
// each opening in a new tab without access to the page, and no `javascript:` link; at 360 × 740 it does not scroll
// sideways; no Content Security Policy violation.

import { expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const LINK = "https://grafana.example.org/d/latency?var-ns=api";
const URL_TEMPLATE =
  '{{ lookup "grafana" .Labels.cluster "address" }}/d/latency?var-ns={{ .Labels.namespace }}';

let integrationId = "";
let ruleId = "";

/** The Lookup table "grafana" with the row prod, whatever an earlier spec left in it. */
async function resetGrafana(admin: Api): Promise<void> {
  const body = {
    name: "grafana",
    description: "",
    columns: ["address", "datasource_uid"],
    entries: [
      { key: "prod", values: { address: "https://grafana.example.org", datasource_uid: "PROM1" } },
    ],
  };
  const list = await admin.call<{ items: { id: string; name: string; etag: string }[] }>(
    "GET",
    "/api/v1/lookup-tables?limit=50",
  );
  const found = list.items.find((t) => t.name === "grafana");
  if (found === undefined) {
    await admin.call("POST", "/api/v1/lookup-tables", body);
  } else {
    await admin.call("PUT", `/api/v1/lookup-tables/${found.id}`, body, { "If-Match": found.etag });
  }
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await resetGrafana(admin);
    const rules = await admin.call<{ items: { id: string; name: string }[] }>(
      "GET",
      "/api/v1/link-rules?limit=500",
    );
    const existing = rules.items.find((r) => r.name === "Dashboard");
    if (existing !== undefined) {
      await admin.call("DELETE", `/api/v1/link-rules/${existing.id}`);
    }
    const rule = await admin.call<{ id: string }>("POST", "/api/v1/link-rules", {
      name: "Dashboard",
      matchers: [{ label: "cluster", op: "=~", value: ".+" }],
      scope: { type: "alert_group" },
      url_template: URL_TEMPLATE,
    });
    ruleId = rule.id;
  } finally {
    await admin.dispose();
  }
  integrationId = await fakeIntegration("agl-lab");
  await fam("PUT", "/groups/agl1", {
    receiver: "agl-lab",
    route: "{}",
    labels: { alertname: "HighLatency" },
  });
  await fam("PUT", "/groups/agl1/alerts/a", {
    labels: { cluster: "prod", namespace: "api" },
    annotations: {
      runbook_url: "https://wiki.example.org/latency",
      dashboard_url: "https://grafana.example.org/d/annotated",
    },
    generator_url: "http://prometheus:9090/graph?g0.expr=up%3D%3D0",
  });
  await fam("PUT", "/groups/agl1/alerts/b", {
    labels: { cluster: "prod", namespace: "api", pod: "b" },
    annotations: { runbook_url: "javascript:alert(1)" },
  });
  await notify(integrationId, "agl1", { reason: "first notification" });
});

test.afterAll(async () => {
  if (ruleId !== "") {
    const admin = await adminApi();
    try {
      await admin.call("DELETE", `/api/v1/link-rules/${ruleId}`);
    } finally {
      await admin.dispose();
    }
  }
});

test("shows the Links block, each link opening in a new tab, at phone width", async ({
  page,
  context,
}) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const alerts = await admin.call<{ items: { alert_group?: { id: string } }[] }>(
    "GET",
    `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent('pod="b"')}`,
  );
  await admin.dispose();
  const groupId = alerts.items[0]?.alert_group?.id ?? "";
  expect(groupId).not.toBe("");
  // The new tab opens Grafana, which the test answers itself.
  await context.route("https://grafana.example.org/**", (route) =>
    route.fulfill({ status: 200, contentType: "text/plain", body: "grafana" }),
  );

  await signInAdmin(page);
  await page.goto(`/alert-groups/${groupId}`);
  const block = page.getByTestId("alert-group-links");
  const links = block.getByTestId("alert-group-link");
  // The Link rules first, then the annotations runbook_url and dashboard_url, then generatorURL as "Source".
  await expect(links).toHaveText([/^Explore/, /^Dashboard/, /^Runbook/, /^Dashboard/, /^Source/]);
  for (const link of await links.all()) {
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect(link).toHaveAttribute("href", /^https?:\/\//);
  }
  await expect(page.locator('a[href^="javascript:"]')).toHaveCount(0);
  const dashboard = links.nth(1);
  await expect(links.nth(3)).toHaveAttribute("href", "https://grafana.example.org/d/annotated");
  await expect(dashboard).toHaveAttribute("href", LINK);
  await expect(links.filter({ hasText: "Runbook" })).toHaveAttribute(
    "href",
    "https://wiki.example.org/latency",
  );
  await expect(links.filter({ hasText: "Source" })).toHaveAttribute(
    "href",
    "http://prometheus:9090/graph?g0.expr=up%3D%3D0",
  );

  // "Dashboard" opens its link in a new tab.
  const opened = context.waitForEvent("page");
  await dashboard.click();
  const tab = await opened;
  await tab.waitForLoadState();
  expect(tab.url()).toBe(LINK);
  expect(await tab.evaluate(() => window.opener)).toBeNull();
  await tab.close();

  // At 360 × 740 the block wraps and the page does not scroll sideways.
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(dashboard).toBeVisible();
  const widths = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    viewport: window.innerWidth,
  }));
  expect(widths.scroll).toBe(widths.viewport);
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-links-360");
  expect(csp).toEqual([]);
});
