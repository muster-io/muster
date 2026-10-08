// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Lookup tables and Link rules against `muster dev`: Verification steps 4 to 6, 8 and 10 of their story. The Lookup
// table "grafana" holds the row prod and the fake Alertmanager sends HighLatency (cluster prod, namespace api) to the
// Integration "links-lab"; the Admin adds the row stage in the grid, creates the Link rule "Dashboard" whose preview
// against the Stored Snapshot renders the link, finds "Explore" built in without "Delete", and cannot delete the table
// a Link rule reads. A Viewer sees both pages read-only; the Audit log names the changed fields; at phone width
// nothing scrolls sideways; no Content Security Policy violation.

import { expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const URL_TEMPLATE =
  '{{ lookup "grafana" .Labels.cluster "address" }}/d/latency?var-ns={{ .Labels.namespace }}';
const LINK = "https://grafana.example.org/d/latency?var-ns=api";
const IN_USE = "This table is used by a Link rule and cannot be deleted.";

interface Table {
  id: string;
  name: string;
  etag: string;
}

let integrationId = "";
let tableId = "";

/** The Lookup table "grafana" with only the row prod, whatever an earlier spec left in it. */
async function resetGrafana(admin: Api): Promise<string> {
  const body = {
    name: "grafana",
    description: "",
    columns: ["address", "datasource_uid"],
    entries: [
      { key: "prod", values: { address: "https://grafana.example.org", datasource_uid: "PROM1" } },
    ],
  };
  const list = await admin.call<{ items: Table[] }>("GET", "/api/v1/lookup-tables?limit=50");
  const found = list.items.find((t) => t.name === "grafana");
  const table =
    found === undefined
      ? await admin.call<Table>("POST", "/api/v1/lookup-tables", body)
      : await admin.call<Table>("PUT", `/api/v1/lookup-tables/${found.id}`, body, {
          "If-Match": found.etag,
        });
  return table.id;
}

async function deleteRule(admin: Api, name: string): Promise<void> {
  const rules = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/link-rules?limit=500",
  );
  const rule = rules.items.find((r) => r.name === name);
  if (rule !== undefined) {
    await admin.call("DELETE", `/api/v1/link-rules/${rule.id}`);
  }
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await deleteRule(admin, "Dashboard");
    tableId = await resetGrafana(admin);
  } finally {
    await admin.dispose();
  }
  integrationId = await fakeIntegration("links-lab");
  await fam("PUT", "/groups/lnk1", {
    receiver: "links-lab",
    route: "{}",
    labels: { alertname: "HighLatency" },
  });
  await fam("PUT", "/groups/lnk1/alerts/a", { labels: { cluster: "prod", namespace: "api" } });
  await notify(integrationId, "lnk1", { reason: "first notification" });
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await deleteRule(admin, "Dashboard");
    await resetGrafana(admin);
  } finally {
    await admin.dispose();
  }
});

test("edits a Lookup table, creates a Link rule with its preview and keeps the table a rule reads", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 10. The navigation lists "Lookup tables" and "Link rules" after "Security", under Organization.
  await expect(nav(page, "Link rules")).toBeVisible();
  const entries = page.getByRole("navigation", { name: "Main" }).getByRole("link");
  const names = await entries.allTextContents();
  const security = names.indexOf("Security");
  expect(names.slice(security, security + 3)).toEqual(["Security", "Lookup tables", "Link rules"]);
  await expect(nav(page, "Lookup tables")).toHaveAttribute(
    "href",
    "/admin/organization/lookup-tables",
  );
  await expect(nav(page, "Link rules")).toHaveAttribute("href", "/admin/organization/link-rules");

  // 4. Organization → Lookup tables → "grafana" → a row stage → "Save": the list shows 2 rows.
  await nav(page, "Lookup tables").click();
  await expect(page.getByRole("heading", { name: "Lookup tables", level: 1 })).toBeVisible();
  const grafanaRow = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "grafana", exact: true }) });
  await expect(grafanaRow).toContainText("address, datasource_uid");
  await expect(grafanaRow.getByTestId("lookup-table-rows")).toHaveText("1 row");
  await grafanaRow.getByRole("link", { name: "grafana", exact: true }).click();
  await expect(page.getByLabel("Key of row 1")).toHaveValue("prod");
  await page.getByRole("button", { name: "Add row" }).click();
  await expect(page.getByLabel("Key of row 2")).toBeFocused();
  await page.getByLabel("Key of row 2").fill("stage");
  await page.getByLabel("address of row stage").fill("https://grafana-stage.example.org");
  await page.getByLabel("datasource_uid of row stage").fill("PROM2");
  await shot(page, "lookup-table-grid");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/organization\/lookup-tables$/);
  await expect(grafanaRow.getByTestId("lookup-table-rows")).toHaveText("2 rows");

  // 5. Link rules → "Create rule": the preview against the Stored Snapshot renders the link → "Save".
  await nav(page, "Link rules").click();
  await page.getByRole("link", { name: "Create rule" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill("Dashboard");
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("cluster");
  await page.getByRole("combobox", { name: "Operator of matcher 1" }).selectOption("=~");
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill(".+");
  await expect(page.getByRole("radio", { name: "Alert Group" })).toBeChecked();
  await page.getByRole("textbox", { name: "URL template" }).fill(URL_TEMPLATE);
  const snapshot = await (async () => {
    const admin = await adminApi();
    try {
      const list = await admin.call<{ items: { id: string }[] }>(
        "GET",
        `/api/v1/stored-snapshots?integration=${integrationId}&limit=1`,
      );
      return list.items[0]?.id ?? "";
    } finally {
      await admin.dispose();
    }
  })();
  const picker = page.getByTestId("sample-picker");
  await expect(picker.locator(`option[value="stored_snapshot:${snapshot}"]`)).toBeAttached();
  await picker.selectOption({ value: `stored_snapshot:${snapshot}` });
  const preview = page.getByTestId("template-preview-output");
  const previewLink = preview.getByRole("link");
  await expect(previewLink).toHaveAttribute("href", LINK);
  await expect(previewLink).toContainText(LINK);
  await expect(previewLink).toHaveAttribute("target", "_blank");
  await expect(previewLink).toHaveAttribute("rel", "noopener noreferrer");
  // The scope "Each value of label" asks for its label.
  await page.getByRole("radio", { name: "Each value of label" }).check();
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByText("Enter a label name.")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Label", exact: true })).toBeFocused();
  await page.getByRole("radio", { name: "Alert Group" }).check();
  await shot(page, "link-rule-preview");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/organization\/link-rules$/);
  const dashboard = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "Dashboard", exact: true }) });
  await expect(dashboard).toContainText("Alert Group");
  await expect(dashboard).toContainText('cluster=~".+"');

  // 6. "Explore" is built in and has no "Delete".
  const explore = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "Explore", exact: true }) });
  await expect(explore.getByTestId("builtin-badge")).toHaveText("Built-in");
  await expect(dashboard.getByTestId("builtin-badge")).toHaveCount(0);
  await explore.getByRole("link", { name: "Explore", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Explore", level: 1 })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Name" })).toHaveAttribute("readonly", "");
  await expect(page.getByRole("button", { name: "Delete" })).toHaveCount(0);

  // 6. Lookup tables → "grafana" → "Delete": a Link rule reads it.
  await nav(page, "Lookup tables").click();
  await grafanaRow.getByRole("link", { name: "grafana", exact: true }).click();
  await page.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByTestId("lookup-table-in-use")).toHaveText(
    `${IN_USE} Link rules that read it: Explore, Dashboard.`,
  );
  await shot(page, "lookup-table-in-use");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // At phone width the grid and the form scroll inside themselves; the pages do not scroll sideways.
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(page.getByLabel("Key of row 2")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await nav(page, "Link rules").click();
  await page.getByRole("link", { name: "Dashboard", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "URL template" })).toHaveValue(URL_TEMPLATE);
  await expectNoHorizontalScroll(page);
  await shot(page, "link-rule-360");
  expect(csp).toEqual([]);
});

test("the Audit log names Rows and the URL template", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  // 10. The update of step 4 names "Rows", the creation of step 5 "URL template".
  await page.goto(`/admin/audit-log?resource_type=lookup_table&resource_id=${tableId}`);
  const updated = page.getByRole("row").filter({ hasText: "lookup_table.updated" }).first();
  await expect(updated.getByTestId("audit-diff")).toContainText("Rows:");
  // The newest update is the save of the grid, which added the row stage.
  await expect(updated.getByTestId("audit-diff")).toContainText("grafana-stage.example.org");
  const admin = await adminApi();
  const rules = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/link-rules?limit=500",
  );
  await admin.dispose();
  const ruleId = rules.items.find((r) => r.name === "Dashboard")?.id ?? "";
  await page.goto(`/admin/audit-log?resource_type=link_rule&resource_id=${ruleId}`);
  const created = page.getByRole("row").filter({ hasText: "link_rule.created" }).first();
  await expect(created.getByTestId("audit-diff")).toContainText("URL template:");
  await expect(created.getByTestId("audit-diff")).toContainText("Matchers:");
  await shot(page, "audit-log-link-rule");
  expect(csp).toEqual([]);
});

test("a Viewer sees the Lookup tables and Link rules read-only", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("links-viewer", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "links-viewer-1" });
  await admin.dispose();
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "links-viewer", "links-viewer-1");

  // 8. No "Create", "Save" or "Delete" on either page.
  await page.goto("/admin/organization/lookup-tables");
  await expect(page.getByRole("link", { name: "grafana", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Create table" })).toHaveCount(0);
  await page.getByRole("link", { name: "grafana", exact: true }).click();
  await expect(page.getByTestId("lookup-table-read-only")).toBeVisible();
  await expect(page.getByRole("cell", { name: "https://grafana-stage.example.org" })).toBeVisible();
  for (const name of ["Save", "Delete", "Add row", "Add column"]) {
    await expect(page.getByRole("button", { name })).toHaveCount(0);
  }
  await expect(page.getByRole("textbox")).toHaveCount(0);
  await expectNoHorizontalScroll(page);

  await page.goto("/admin/organization/link-rules");
  await expect(page.getByRole("link", { name: "Explore", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Create rule" })).toHaveCount(0);
  await page.getByRole("link", { name: "Dashboard", exact: true }).click();
  await expect(page.getByTestId("link-rule-read-only")).toBeVisible();
  await expect(page.getByTestId("matcher-text")).toHaveText(['cluster=~".+"']);
  for (const name of ["Save", "Create", "Delete", "Add matcher"]) {
    await expect(page.getByRole("button", { name })).toHaveCount(0);
  }
  await expect(page.getByRole("textbox", { name: "URL template" })).toHaveAttribute("readonly", "");
  await expectNoHorizontalScroll(page);
  await shot(page, "link-rule-viewer-360");
  expect(csp).toEqual([]);
  await context.close();
});
