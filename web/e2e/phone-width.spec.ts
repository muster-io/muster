// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alert Group list and page on a phone against `muster dev`: Verification steps 9 to 11 of their story, in English
// and in Russian. The fake Alertmanager sends the group agp to the Integration "ag-phone": an Alert with a long name and
// many long labels that keeps firing, and one that Alertmanager stops reporting until Muster resolves it as Gone. At
// 360 × 740 the list shows compact rows and a "Filters" button, the page puts the header first, then the Alerts, then
// the Timeline, a tap opens the full reason of the Gone Alert, and neither page scrolls sideways.

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
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });
test.use({ viewport: { width: 360, height: 740 }, hasTouch: true });

const TITLE = "PersistentVolumeFillingUpInTheProductionPaymentsClusterWithAVeryLongAlertName";
const GONE_TEXT =
  "Alertmanager no longer reports this alert — it resolved without notice, was silenced or inhibited in Alertmanager, or the Alertmanager routing changed";
const RU_GONE_TEXT =
  "Alertmanager больше не присылает этот алерт — он закрылся без уведомления, был заглушён или подавлен в Alertmanager, либо изменилась маршрутизация Alertmanager";

let integrationId = "";
let routeId = "";

function labels(pod: string): Record<string, string> {
  return {
    team: "ag-phone",
    pod,
    namespace: "payments-production-europe-west-with-a-long-namespace-name",
    persistentvolumeclaim: "data-postgres-cluster-payments-production-0-with-a-long-claim-name",
    image: "registry.example.org/payments/postgres:16.4-with-a-very-long-tag-0123456789abcdef",
    severity: "critical",
  };
}

async function prepare(): Promise<void> {
  integrationId = await fakeIntegration("ag-phone");
  routeId = await createRoute("ag-phone", "ag-phone", ["alertname"]);
  await fam("PUT", "/groups/agp", {
    receiver: "ag-phone",
    route: "{}",
    labels: { alertname: TITLE },
  });
  await fam("PUT", "/groups/agp/alerts/a1", {
    labels: labels("postgres-0"),
    annotations: {
      summary: "The volume of postgres-0 fills up and will be full within four hours at this rate",
    },
  });
  await fam("PUT", "/groups/agp/alerts/b1", { labels: labels("postgres-1") });
  await notify(integrationId, "agp", { reason: "first notification" });
  // b1 missing from the Snapshots past processing.gone_min_absence: Gone.
  for (const seconds of [20, 60, 300]) {
    await advance(seconds);
    await notify(integrationId, "agp", { reason: "repeat interval elapsed", list: ["a1"] });
  }
}

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

/** The section of the page with the test id, as a box on the screen. */
async function top(page: Page, testId: string): Promise<number> {
  const box = await page.getByTestId(testId).boundingBox();
  expect(box, testId).not.toBeNull();
  return box?.y ?? 0;
}

test("a responder reads the list and an Alert Group 360 px wide", async ({ page }) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await prepare();
  await signInAdmin(page);

  // 9. Compact rows and a "Filters" button; nothing wider than the screen.
  await page.goto("/alert-groups");
  const row = page.getByTestId("alert-group-row").filter({ hasText: TITLE });
  await expect(row).toHaveCount(1);
  await expect(row.getByTestId("alert-group-status")).toHaveText("Firing");
  await expect(row.getByTestId("urgent-mark")).toHaveText("Urgent");
  await expect(row.getByTestId("duration")).toBeVisible();
  await expect(page.getByRole("table", { name: "Alert Groups" })).toHaveCount(0);
  await expect(page.getByTestId("filters-button")).toHaveText("Filters");
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-list-360");

  // 10. The filters open in a sheet; the Firing tab; the first row opens with the header first, then the Alerts, then
  // the Timeline.
  await page.getByTestId("filters-button").tap();
  const sheet = page.getByRole("dialog", { name: "Filters" });
  await expect(sheet).toBeVisible();
  await expect(sheet.getByLabel("Urgent", { exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-filters-360");
  await sheet.getByRole("button", { name: "Show Alert Groups" }).tap();
  await expect(sheet).toHaveCount(0);
  await page.getByRole("tab", { name: /^Firing \d+$/ }).tap();
  await expect(page).toHaveURL(/tab=/);
  await row.getByRole("link").tap();
  await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
  const header = await top(page, "alert-group-header");
  const alerts = await top(page, "alert-group-alerts");
  const timeline = await top(page, "timeline");
  expect(header).toBeLessThan(alerts);
  expect(alerts).toBeLessThan(timeline);
  await expect(page.getByTestId("timeline-entry").first()).toBeVisible();
  await expectNoHorizontalScroll(page);

  // 11. A tap on the details of the Gone Alert shows its full reason, without a hover.
  const gone = page
    .getByTestId("alert")
    .filter({ has: page.getByTestId("alert-state").getByText("Resolved: Gone", { exact: true }) });
  await expect(gone).toHaveCount(1);
  const reason = gone.getByTestId("alert-reason");
  await expect(reason).toBeHidden();
  await gone.getByText("Details", { exact: true }).tap();
  await expect(reason).toHaveText(GONE_TEXT);
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-page-360");
  expect(csp).toEqual([]);
});

test("the list and the page in Russian, 360 px wide", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("ag-nina", "responder");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "ag-nina-password-1" });
  await admin.dispose();
  await signIn(page, "ag-nina", "ag-nina-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/alert-groups");
  await expect(page.getByRole("heading", { name: "Группы алертов", level: 1 })).toBeVisible();
  const row = page.getByTestId("alert-group-row").filter({ hasText: TITLE });
  await expect(row.getByTestId("alert-group-status")).toHaveText("Горит");
  await expect(row.getByTestId("urgent-mark")).toHaveText("Срочная");
  await expect(page.getByTestId("filters-button")).toHaveText("Фильтры");
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-list-ru-360");

  await row.getByRole("link").tap();
  await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();
  const gone = page
    .getByTestId("alert")
    .filter({ has: page.getByTestId("alert-state").getByText("Закрыт: пропал", { exact: true }) });
  await gone.getByText("Подробности", { exact: true }).tap();
  await expect(gone.getByTestId("alert-reason")).toHaveText(RU_GONE_TEXT);
  await expect(page.getByRole("heading", { name: "Хронология" })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "alert-group-page-ru-360");
  expect(csp).toEqual([]);
});
