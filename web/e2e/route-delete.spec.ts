// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Deleting a Route with open Alert Groups, the Alert Group column of the Alerts view and the Integration delete dialog
// against `muster dev`: Verification steps 4 and 5 of their story. The fake Alertmanager sends the group d1 to the
// Integration "rd": two DiskFull Alerts of the Route "db" in the clusters a and b, so two open Alert Groups. "Move and
// delete" moves them to the Default route, whose Timeline shows the move, and deletes the Route; the Integration then
// still has the two open Alert Groups that deleting it would resolve. A deletion refused because an Alert Group
// started meanwhile asks again with the new count.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
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

let integrationId = "";
const leftover: string[] = [];

test.beforeAll(async () => {
  integrationId = await fakeIntegration("rd");
  leftover.push(await createRoute("db", "rd-db", ["alertname", "cluster"]));
  await fam("PUT", "/groups/d1", {
    receiver: "rd",
    route: "{}",
    labels: { alertname: "DiskFull" },
  });
  await fam("PUT", "/groups/d1/alerts/a", {
    labels: { team: "rd-db", cluster: "a", pod: "i1" },
  });
  await fam("PUT", "/groups/d1/alerts/b", {
    labels: { team: "rd-db", cluster: "b", pod: "j1" },
  });
  await notify(integrationId, "d1", { reason: "first notification" });
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    const routes = await admin.call<{ items: { id: string }[] }>("GET", "/api/v1/routes");
    for (const id of leftover) {
      if (routes.items.some((r) => r.id === id)) {
        await deleteRoute(id);
      }
    }
  } finally {
    await admin.dispose();
  }
});

/** The rows of the Alerts view, the header row left out. */
function alertRows(page: Page) {
  return page
    .getByTestId("integration-alerts")
    .getByRole("row")
    .filter({ has: page.getByRole("cell") });
}

test("moves the open Alert Groups and deletes the Route", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 4. Routes → "db" → Delete: the dialog asks to move the two open Alert Groups.
  await page.goto("/routes");
  await page.getByRole("link", { name: "db", exact: true }).click();
  await expect(page.getByRole("heading", { name: "db", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("route-delete-description")).toHaveText(
    "This route has 2 open Alert Groups. Move them to the Default route to delete it.",
  );
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
  await shot(page, "route-delete-move");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "route-delete-move-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  await dialog.getByRole("button", { name: "Move and delete" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(page.getByRole("link", { name: "db", exact: true })).toHaveCount(0);

  // 5. The Integration's Alerts view links each Alert to its Alert Group as #N.
  await page.goto(`/integrations/${integrationId}`);
  await expect(alertRows(page)).toHaveCount(2);
  const headers = await page
    .getByTestId("integration-alerts")
    .getByRole("columnheader")
    .allTextContents();
  const column = headers.indexOf("Alert Group");
  expect(column).toBeGreaterThan(0);
  const link = alertRows(page).first().getByRole("cell").nth(column).getByRole("link");
  await expect(link).toHaveText(/^#\d+$/);
  await shot(page, "integration-alerts-alert-group");

  // A moved Alert Group: its Route is the Default route, and the Timeline shows the move.
  await link.click();
  await expect(page).toHaveURL(/\/alert-groups\/AG/);
  await expect(page.getByTestId("fact-route")).toContainText("Default");
  await expect(
    page.getByTestId("timeline-entry").filter({
      has: page
        .getByTestId("timeline-text")
        .getByText("Moved to the Default route", { exact: true }),
    }),
  ).toHaveCount(1);

  // The Integration delete dialog says how many open Alert Groups it would resolve.
  await page.goto(`/integrations/${integrationId}`);
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(
    page.getByRole("dialog").getByTestId("integration-delete-open-alert-groups"),
  ).toHaveText("2 open Alert Groups will be resolved.");
  await shot(page, "integration-delete-open-alert-groups");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("a deletion refused because an Alert Group started meanwhile asks again", async ({ page }) => {
  const csp = watchCsp(page);
  const late = await createRoute("db-late", "rd-late", ["alertname"]);
  leftover.push(late);
  // Without live updates the dialog keeps the count it read when it opened.
  await page.route("**/api/v1/live-updates", (route) => route.abort());
  await signInAdmin(page);
  await page.goto(`/routes/${late}`);
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("route-delete-description")).toHaveText(
    "Alerts it would take go to the next matching Route.",
  );

  await fam("PUT", "/groups/d1/alerts/late", { labels: { team: "rd-late", pod: "k1" } });
  await notify(integrationId, "d1", { reason: "new alerts added" });

  await dialog.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(dialog.getByTestId("route-delete-description")).toHaveText(
    "This route has 1 open Alert Group. Move it to the Default route to delete the route.",
  );
  await expect(dialog.getByTestId("route-delete-refused")).toHaveText(
    "New Alert Groups started on this route meanwhile.",
  );
  await dialog.getByRole("button", { name: "Move and delete" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(page.getByRole("link", { name: "db-late", exact: true })).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("shows the delete dialogs in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const ru = await createRoute("db-ru", "rd-ru", ["alertname"]);
  leftover.push(ru);
  await fam("PUT", "/groups/d1/alerts/ru", { labels: { team: "rd-ru", pod: "m1" } });
  await notify(integrationId, "d1", { reason: "new alerts added" });

  const admin = await adminApi();
  const { token } = await admin.createUser("rd-ivan", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "rd-ivan-password-1" });
  await admin.dispose();
  await signIn(page, "rd-ivan", "rd-ivan-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto(`/routes/${ru}`);
  await page.getByRole("button", { name: "Удалить", exact: true }).click();
  await expect(page.getByRole("dialog").getByTestId("route-delete-description")).toHaveText(
    "У этого маршрута 1 открытая группа алертов. Чтобы удалить маршрут, перенесите её в маршрут по умолчанию.",
  );
  await expect(
    page.getByRole("dialog").getByRole("button", { name: "Перенести и удалить" }),
  ).toBeVisible();
  await shot(page, "route-delete-move-ru");
  await page.getByRole("dialog").getByRole("button", { name: "Отмена" }).click();

  await page.goto(`/integrations/${integrationId}`);
  await expect(
    page.getByTestId("integration-alerts").getByRole("columnheader", { name: "Группа алертов" }),
  ).toBeVisible();
  const reader = await adminApi();
  const open = (
    await reader.call<{ open_alert_group_count: number }>(
      "GET",
      `/api/v1/integrations/${integrationId}`,
    )
  ).open_alert_group_count;
  await reader.dispose();
  // a, b, late and ru: four, the "few" form of the Russian plural.
  expect(open).toBe(4);
  await page.getByRole("button", { name: "Удалить", exact: true }).click();
  await expect(
    page.getByRole("dialog").getByTestId("integration-delete-open-alert-groups"),
  ).toHaveText(`Будут закрыты ${open} открытые группы алертов.`);
  await shot(page, "integration-delete-open-alert-groups-ru");
  await page.getByRole("dialog").getByRole("button", { name: "Отмена" }).click();
  expect(csp).toEqual([]);
});
