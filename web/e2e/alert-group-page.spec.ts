// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alert Group page against `muster dev`: Verification steps 5 to 7 of its story. The fake Alertmanager sends the
// group agd to the Integration "ag-page" on the Route "ag-db" (Group key alertname and cluster): a Replacement of the
// Pod i2 in cluster a, then cluster b resolved by Alertmanager, reopened within the Reopen window, resolved again and,
// after the window, a new Alert Group with the same key. The pages show the header, the notices, the Alerts with their
// details, the labels, the Timeline with loudness and Mentions, and the previous Alert Groups; an Alert Group whose
// details were removed shows the notice; then the page in Russian.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  advance,
  createRoute,
  deleteRoute,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  sql,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const REPLACEMENT =
  "Alerts were replaced because pod changed. Consider removing Instance labels from the rule, for example without(pod, instance).";
const REMOVED =
  "Alerts and Timeline of this Alert Group were removed after 90 days; only its summary and Notes are kept.";

let integrationId = "";
let routeId = "";

function db(cluster: string, pod: string, extra: Record<string, unknown> = {}) {
  return { labels: { team: "ag-db", cluster, pod, severity: "warning" }, ...extra };
}

/** The Alert Group of the Alert with the label pod, as the Integration's Alerts view names it. */
async function groupOf(pod: string): Promise<{ id: string; number: number }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`pod="${pod}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${pod}`).toBeDefined();
    return group ?? { id: "", number: 0 };
  } finally {
    await admin.dispose();
  }
}

async function readGroup(id: string): Promise<{ status: string; reopen_count: number }> {
  const admin = await adminApi();
  try {
    return await admin.call("GET", `/api/v1/alert-groups/${id}`);
  } finally {
    await admin.dispose();
  }
}

function entry(page: Page, text: string) {
  return page.getByTestId("timeline-entry").filter({
    has: page.getByTestId("timeline-text").getByText(text, { exact: true }),
  });
}

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

test("shows the header, notices, Alerts, labels, Timeline and previous Alert Groups", async ({
  page,
}) => {
  test.setTimeout(240_000);
  const csp = watchCsp(page);
  integrationId = await fakeIntegration("ag-page");
  routeId = await createRoute("ag-db", "ag-db", ["alertname", "cluster"]);
  await fam("PUT", "/groups/agd", {
    receiver: "ag-page",
    route: "{}",
    labels: { alertname: "DiskFull" },
  });
  await fam(
    "PUT",
    "/groups/agd/alerts/i1",
    db("a", "i1", {
      annotations: { summary: "Disk on i1 is full", description: "Free space below 5 %" },
    }),
  );
  await fam("PUT", "/groups/agd/alerts/i2", db("a", "i2"));
  await fam("PUT", "/groups/agd/alerts/j1", db("b", "j1"));
  await notify(integrationId, "agd", { reason: "first notification" });
  const g1 = await groupOf("i1");
  const g2 = await groupOf("j1");

  // A Replacement: i2 goes, i2b differs from it only in pod.
  await fam("DELETE", "/groups/agd/alerts/i2");
  await fam("PUT", "/groups/agd/alerts/i2b", db("a", "i2b"));
  await notify(integrationId, "agd", { reason: "new alerts added" });

  // Cluster b resolves by Alertmanager.
  await fam("PUT", "/groups/agd/alerts/j1", db("b", "j1", { status: "resolved" }));
  await notify(integrationId, "agd", { reason: "some alerts resolved" });
  await expect.poll(async () => (await readGroup(g2.id)).status).toBe("resolved");

  await signInAdmin(page);

  // 6. Resolved by the system, with the reason.
  await page.goto(`/alert-groups/${g2.id}`);
  await expect(page.getByTestId("alert-group-status")).toHaveText("Resolved");
  await expect(page.getByTestId("alert-group-resolution")).toHaveText(
    "Resolved: Resolved by Alertmanager",
  );
  await expect(entry(page, "Resolved: Resolved by Alertmanager")).toHaveCount(1);

  // 5. The Replacement: the notice, and "Alert replaced" in the Timeline, Quiet.
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByRole("heading", { name: "DiskFull", level: 1 })).toBeVisible();
  await expect(page.getByTestId("alert-group-number")).toHaveText(`#${g1.number}`);
  await expect(page.getByTestId("alert-group-summary")).toHaveText("Disk on i1 is full");
  await expect(page.getByTestId("fact-route")).toContainText("ag-db");
  await expect(page.getByTestId("fact-integrations")).toContainText("ag-page");
  await expect(page.getByTestId("fact-severity")).toContainText("warning");
  await expect(page.getByTestId("notice-replacement")).toHaveText(REPLACEMENT);
  const replaced = entry(page, "Alert replaced");
  await expect(replaced).toHaveCount(1);
  await expect(replaced.getByTestId("replaced-label")).toHaveText("pod");
  await expect(replaced.getByTestId("timeline-loud")).toHaveCount(0);
  await expect(replaced.getByTestId("timeline-actor")).toHaveText("Muster");
  const created = entry(page, "Alert Group created");
  await expect(created.getByTestId("timeline-loud")).toHaveText("Loud");
  await expect(created.getByTestId("timeline-mentions")).toHaveText("Mentions: new_alert_group");

  // The Alerts, firing first; the details of one open by a click with its annotations, summary first.
  const alerts = page.getByTestId("alert-group-alerts").getByTestId("alert");
  await expect(alerts.first().getByTestId("alert-state")).toHaveText("Firing");
  const i1 = alerts.filter({ hasText: "pod=i1" });
  await i1.getByText("Details", { exact: true }).click();
  await expect(i1.getByTestId("alert-annotation")).toHaveText([
    "Disk on i1 is full",
    "Free space below 5 %",
  ]);
  // The common labels and annotations: summary and description first.
  await expect(page.getByTestId("group-labels")).toContainText("cluster=a");
  await expect(page.getByTestId("common-labels")).toContainText("team=ag-db");
  // The Firing and Resolved filter.
  await page.getByTestId("alert-group-alerts").getByRole("tab", { name: "Resolved" }).click();
  // i2 still fires until Muster sees it gone: nothing has resolved yet.
  await expect(
    page.getByTestId("alert-group-alerts").getByText("No resolved alerts."),
  ).toBeVisible();
  await page.getByTestId("alert-group-alerts").getByRole("tab", { name: "All" }).click();
  await expect(alerts).toHaveCount(3);
  // The Timeline by kind and oldest first.
  await page.getByTestId("timeline").getByRole("button", { name: "Alerts" }).click();
  await expect(entry(page, "Alert Group created")).toHaveCount(0);
  await expect(entry(page, "Alert replaced")).toHaveCount(1);
  await page.getByTestId("timeline").getByRole("button", { name: "Alerts" }).click();
  await page.getByLabel("Order").selectOption("asc");
  await expect(page.getByTestId("timeline-text").first()).toHaveText("Alert Group created");
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-page-replacement");

  // 5. A Reopen within the window: "🔁 Reopened ×1", and a Loud "Reopened" mentioning reopen.
  await advance(480);
  await fam("PUT", "/groups/agd/alerts/j1", db("b", "j1", { status: "firing", starts_at: "now" }));
  await notify(integrationId, "agd", { reason: "new alerts added" });
  await expect.poll(async () => (await readGroup(g2.id)).reopen_count).toBe(1);
  await page.goto(`/alert-groups/${g2.id}`);
  await expect(page.getByTestId("reopen-count")).toHaveText("🔁 Reopened ×1");
  const reopened = entry(page, "Reopened");
  await expect(reopened.getByTestId("timeline-loud")).toHaveText("Loud");
  await expect(reopened.getByTestId("timeline-mentions")).toHaveText("Mentions: reopen");

  // A new Alert Group with the same key after the window lists the earlier one as a previous Alert Group.
  await fam("PUT", "/groups/agd/alerts/j1", db("b", "j1", { status: "resolved" }));
  await notify(integrationId, "agd", { reason: "some alerts resolved" });
  await advance(960);
  await expect
    .poll(async () =>
      Number(
        (await sql(`SELECT count(*) AS n FROM timers WHERE kind = 'reopen_window_end'`)).rows[0]?.n,
      ),
    )
    .toBe(0);
  await fam("PUT", "/groups/agd/alerts/j3", db("b", "j3"));
  await notify(integrationId, "agd", { reason: "new alerts added" });
  const g3 = await groupOf("j3");
  expect(g3.number).toBeGreaterThan(g2.number);

  // 6. Previous Alert Groups: the earlier #N, resolved, with its duration.
  await page.goto(`/alert-groups/${g3.id}`);
  const previous = page.getByTestId("related-alert-group");
  await expect(previous).toHaveCount(1);
  await expect(previous.getByRole("link")).toHaveText(`#${g2.number}`);
  await expect(previous.getByTestId("alert-group-status")).toHaveText("Resolved");
  await expect(previous.getByTestId("related-duration")).toHaveText(/^\d+ (s|min|h)/);
  await expect(previous).toContainText("Resolved: Resolved by Alertmanager");
  await shot(page, "alert-group-page-previous");
  await previous.getByRole("link").click();
  await expect(page.getByTestId("alert-group-number")).toHaveText(`#${g2.number}`);

  // 7. Details removed by retention: the resolved g2 as 91 days old. The shared development clock is not moved that
  // far, as the later specs keep dates relative to the real time; test/e2e covers the clock path.
  await sql(
    `UPDATE alert_groups SET created_at = created_at - interval '91 days',
       resolved_at = resolved_at - interval '91 days', last_changed_at = last_changed_at - interval '91 days'
     WHERE public_id = $1`,
    [g2.id],
  );
  await page.goto(`/alert-groups/${g2.id}`);
  await expect(page.getByTestId("notice-details_removed")).toHaveText(REMOVED);
  await expect(page.getByTestId("alert-group-alerts")).toHaveCount(0);
  await expect(page.getByTestId("alert-group-title")).toHaveText("DiskFull");
  await shot(page, "alert-group-page-details-removed");
  expect(csp).toEqual([]);
});

test("shows the page in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("ag-vera", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "ag-vera-password-1" });
  await admin.dispose();
  await signIn(page, "ag-vera", "ag-vera-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  const g1 = await groupOf("i1");
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByTestId("alert-group-status")).toHaveText("Горит");
  await expect(page.getByTestId("notice-replacement")).toHaveText(
    "Алерты заменились, потому что изменилась метка pod. Стоит убрать метки экземпляра из правила, например without(pod, instance).",
  );
  await expect(entry(page, "Алерт заменён")).toHaveCount(1);
  await expect(entry(page, "Группа алертов создана").getByTestId("timeline-loud")).toHaveText(
    "Громко",
  );
  await expect(page.getByRole("heading", { name: "Хронология" })).toBeVisible();
  await shot(page, "alert-group-page-ru");
  await page.goto("/alert-groups");
  await expect(page.getByRole("heading", { name: "Группы алертов", level: 1 })).toBeVisible();
  await expect(page.getByRole("tab", { name: /^Открытые \d+$/ })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await shot(page, "alert-group-list-ru");
  expect(csp).toEqual([]);
});
