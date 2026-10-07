// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alert Group list against `muster dev`: Verification steps 1 to 4 and 8 of its story. The fake Alertmanager sends
// the group ag1 to the Integration "ag-list": two crash-looping postgres Pods in payments (critical, so Urgent) on the
// Route "ag-k8s", and two Alerts of the Route "ag-mix" in payments and billing. The list opens on "Open" with counts,
// a view built from filters, a range, a label column and a search is reproduced from its URL in another browser, "#N"
// finds an Alert Group outside the range, a new Alert Group appears as "1 new" without moving the rows, a changed one
// updates in place, and a duration shows its absolute start on hover.

import { expect, test, type Page } from "@playwright/test";

import {
  APP,
  adminApi,
  createRoute,
  deleteRoute,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

let integrationId = "";
const routes: string[] = [];

const PAYMENTS = 'namespace="payments"';

function k8s(pod: string, extra: Record<string, unknown> = {}) {
  return {
    labels: { team: "ag-k8s", namespace: "payments", pod, severity: "critical" },
    ...extra,
  };
}

async function prepare(): Promise<void> {
  integrationId = await fakeIntegration("ag-list");
  routes.push(await createRoute("ag-k8s", "ag-k8s", ["alertname", "namespace"]));
  routes.push(await createRoute("ag-mix", "ag-mix", ["alertname"]));
  await fam("PUT", "/groups/ag1", {
    receiver: "ag-list",
    route: "{}",
    labels: { alertname: "KubePodCrashLooping" },
  });
  await fam(
    "PUT",
    "/groups/ag1/alerts/p0",
    k8s("postgres-0", { annotations: { summary: "Pod postgres-0 is crash looping" } }),
  );
  await fam("PUT", "/groups/ag1/alerts/p1", k8s("postgres-1"));
  await fam("PUT", "/groups/ag1/alerts/m0", {
    labels: { team: "ag-mix", namespace: "payments", pod: "a" },
  });
  await fam("PUT", "/groups/ag1/alerts/m1", {
    labels: { team: "ag-mix", namespace: "billing", pod: "b" },
  });
  await notify(integrationId, "ag1", { reason: "first notification" });
}

test.afterAll(async () => {
  for (const id of routes) {
    await deleteRoute(id);
  }
});

/** The rows of the desktop table, the header row left out. */
function rows(page: Page) {
  return page
    .getByRole("table", { name: "Alert Groups" })
    .getByRole("row")
    .filter({
      has: page.getByRole("cell"),
    });
}

/** The row of the Alert Group of the postgres Pods, found by its summary. */
function payRow(page: Page) {
  return rows(page).filter({ hasText: "Pod postgres-0 is crash looping" });
}

/** The numbers of the rows, in their order. */
function numbers(page: Page) {
  return rows(page)
    .getByRole("link", { name: /^#\d+$/ })
    .allTextContents();
}

function tab(page: Page, name: string) {
  return page.getByRole("tab", { name: new RegExp(`^${name} \\d+$`) });
}

async function count(page: Page, name: string): Promise<number> {
  return Number(await tab(page, name).getByTestId("tab-count").textContent());
}

async function addMatcher(page: Page, matcher: string): Promise<void> {
  await page.getByRole("textbox", { name: "Label filters" }).fill(matcher);
  await page.getByRole("button", { name: "Add matcher" }).click();
  await expect(page.getByTestId("matcher-chip").getByText(matcher, { exact: true })).toBeVisible();
}

test("lists Alert Groups with counts, filters, search and live updates in the URL", async ({
  page,
  browser,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await prepare();
  await signInAdmin(page);

  // 1. "/" opens "Alert Groups" on "Open" with counts on every tab.
  await page.goto("/");
  await expect(page).toHaveURL(`${APP}/alert-groups`);
  await expect(page.getByRole("heading", { name: "Alert Groups", level: 1 })).toBeVisible();
  await expect(tab(page, "Open")).toHaveAttribute("aria-selected", "true");
  for (const name of ["Open", "Firing", "Acknowledged", "Snoozed", "Resolved", "All"]) {
    await expect(tab(page, name)).toBeVisible();
  }
  const k8sRow = payRow(page);
  await expect(k8sRow).toHaveCount(1);
  await expect(k8sRow.getByTestId("urgent-mark")).toHaveText("Urgent");
  await expect(k8sRow.getByTestId("alert-group-status")).toHaveText("Firing");
  await expect(k8sRow).toContainText("2 of 2 firing");
  await expect(k8sRow).toContainText("Pod postgres-0 is crash looping");
  await expect(k8sRow).toContainText("ag-k8s");
  await expect(rows(page).filter({ hasText: "ag-mix" })).toHaveCount(1);
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-list-desktop");

  // 2. Labels namespace="payments", Urgent, the last 30 days and the label column pod: the ag-k8s Alert Group only.
  await addMatcher(page, PAYMENTS);
  await page.getByLabel("Urgent", { exact: true }).selectOption({ label: "Urgent only" });
  await page.getByLabel("Time range").selectOption({ label: "Last 30 days" });
  await page.getByRole("textbox", { name: "Label columns" }).fill("pod");
  await page.getByRole("button", { name: "Add column" }).click();
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first()).toContainText("ag-k8s");
  await expect(
    page.getByRole("table", { name: "Alert Groups" }).getByRole("columnheader", { name: "pod" }),
  ).toBeVisible();
  await expect(page).toHaveURL(/label=/);
  await expect(page).toHaveURL(/urgent=true/);
  await expect(page).toHaveURL(/range=30d/);
  await expect(page).toHaveURL(/columns=/);
  const url = page.url();

  // ...and the URL opens the same view in another browser.
  const other = await browser.newContext();
  const second = await other.newPage();
  const secondCsp = watchCsp(second);
  await signInAdmin(second);
  await second.goto(url);
  await expect(rows(second)).toHaveCount(1);
  await expect(rows(second).first()).toContainText("ag-k8s");
  await expect(second.getByTestId("matcher-chip")).toHaveText(PAYMENTS);
  await expect(second.getByLabel("Urgent", { exact: true })).toHaveValue("yes");
  await expect(second.getByLabel("Time range")).toHaveValue("30d");
  await expect(second.getByTestId("label-column-chip")).toHaveText("pod");
  await expect(
    second.getByRole("table", { name: "Alert Groups" }).getByRole("columnheader", { name: "pod" }),
  ).toBeVisible();
  expect(secondCsp).toEqual([]);
  await other.close();

  // 3. Search postgres: one row; #N finds the same row, also with a range it does not overlap.
  await page.getByRole("button", { name: "Clear filters" }).click();
  await page.getByLabel("Search", { exact: true }).fill("postgres");
  await expect(rows(page)).toHaveCount(1);
  await expect(payRow(page)).toHaveCount(1);
  const number = (await payRow(page).getByRole("link").first().textContent()) ?? "";
  expect(number).toMatch(/^#\d+$/);
  await page.getByLabel("Time range").selectOption({ label: "Custom" });
  await page.getByLabel("From", { exact: true }).fill("2020-01-01T00:00");
  await page.getByLabel("To", { exact: true }).fill("2020-01-02T00:00");
  await expect(page.getByText("No Alert Groups match these filters.")).toBeVisible();
  await page.getByLabel("Search", { exact: true }).fill(number);
  await expect(rows(page)).toHaveCount(1);
  await expect(payRow(page)).toHaveCount(1);

  // 4. The Firing tab; a new Alert of a new key appears as "1 new" within 5 seconds and the Firing count grows by
  // one, without moving the rows.
  await page.goto("/alert-groups?tab=%22firing%22");
  await expect(tab(page, "Firing")).toHaveAttribute("aria-selected", "true");
  await expect(payRow(page)).toHaveCount(1);
  const firing = await count(page, "Firing");
  const before = await numbers(page);
  await fam("PUT", "/groups/ag1/alerts/q0", {
    labels: { team: "ag-k8s", namespace: "ledger", pod: "ledger-0", severity: "warning" },
  });
  await notify(integrationId, "ag1", { reason: "new alerts added" });
  await expect(page.getByTestId("new-alert-groups")).toHaveText("1 new", { timeout: 5_000 });
  await expect.poll(() => count(page, "Firing"), { timeout: 5_000 }).toBe(firing + 1);
  expect(await numbers(page)).toEqual(before);
  await shot(page, "alert-group-list-new");

  // A changed Alert Group updates its row in place: one of its Alerts resolves.
  await fam("PUT", "/groups/ag1/alerts/p1", k8s("postgres-1", { status: "resolved" }));
  await notify(integrationId, "ag1", { reason: "some alerts resolved" });
  const pay = payRow(page);
  await expect(pay).toContainText("1 of 2 firing", { timeout: 5_000 });
  expect(await numbers(page)).toEqual(before);

  // "1 new" shows it: the new Alert Group is the first row.
  await page.getByTestId("new-alert-groups").click();
  await expect(page.getByTestId("new-alert-groups")).toHaveCount(0);
  await expect(rows(page)).toHaveCount(before.length + 1);
  await expect(rows(page).first()).toContainText("KubePodCrashLooping");
  await expect(rows(page).first()).toContainText("1 of 1 firing");

  // 8. A duration shows its absolute start, in the time zone of the profile (here the browser's), on hover.
  const duration = pay.getByTestId("duration");
  const admin = await adminApi();
  const found = await admin.call<{ items: { started_at: string }[] }>(
    "GET",
    "/api/v1/alert-groups?q=postgres",
  );
  await admin.dispose();
  const started = found.items[0]?.started_at ?? "";
  const shown = new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
    timeZone: "Europe/Berlin",
  }).format(new Date(started));
  await duration.hover();
  await expect(duration).toHaveAttribute("title", `Since ${shown}`);
  expect(csp).toEqual([]);
});
