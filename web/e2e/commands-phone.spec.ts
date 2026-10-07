// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Commands on a phone against `muster dev`: Verification step 11 of their story, in English and in Russian. At
// 360 × 740 the Commands sit right under the header, "Snooze" and "Resolve" open full-screen sheets, the Note box and
// the bulk bar fit, a row's "…" menu opens, and nothing scrolls sideways.

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
test.use({ viewport: { width: 360, height: 740 }, hasTouch: true });

const TITLE = "DiskWillFillUpWithinFourHoursOnTheProductionDatabaseHosts";

let integrationId = "";
let routeId = "";
let group = { id: "", number: 0 };

test.beforeAll(async () => {
  integrationId = await fakeIntegration("cmd-phone");
  routeId = await createRoute("cmd-phone", "cmd-phone", ["alertname"]);
  await fam("PUT", "/groups/cmdp", {
    receiver: "cmd-phone",
    route: "{}",
    labels: { alertname: TITLE },
  });
  await fam("PUT", "/groups/cmdp/alerts/p1", {
    labels: { team: "cmd-phone", instance: "db-0", severity: "critical" },
  });
  await notify(integrationId, "cmdp", { reason: "first notification" });
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts`,
    );
    group = page.items[0]?.alert_group ?? group;
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

async function box(page: Page, testId: string) {
  const b = await page.getByTestId(testId).boundingBox();
  expect(b, testId).not.toBeNull();
  return b ?? { x: 0, y: 0, width: 0, height: 0 };
}

/** A dialog covers the whole screen. */
async function expectSheet(page: Page, name: string): Promise<void> {
  const dialog = page.getByRole("dialog", { name });
  await expect(dialog).toBeVisible();
  // The sheet opens with a short animation.
  await expect
    .poll(async () => {
      const b = await dialog.boundingBox();
      return b === null ? null : [b.x, b.y, b.width, b.height].map(Math.round);
    })
    .toEqual([0, 0, 360, 740]);
}

test("the Commands, the sheets, the Note box and the bulk bar at 360 px", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/alert-groups/${group.id}`);
  await expect(page.getByRole("heading", { name: TITLE, level: 1 })).toBeVisible();

  // The Commands right under the header, before the Alerts.
  const header = await box(page, "alert-group-header");
  const commands = await box(page, "command-buttons");
  const alerts = await box(page, "alert-group-alerts");
  expect(commands.y).toBeGreaterThanOrEqual(header.y + header.height);
  expect(commands.y - (header.y + header.height)).toBeLessThanOrEqual(32);
  expect(commands.y + commands.height).toBeLessThanOrEqual(alerts.y);
  await expect(page.getByTestId("command-buttons").getByRole("button")).toHaveText([
    "Acknowledge",
    "Resolve",
    "Snooze",
  ]);
  await expectNoHorizontalScroll(page);
  await shot(page, "commands-phone");

  // "Snooze" and "Resolve" open full-screen sheets.
  await page.getByRole("button", { name: "Snooze", exact: true }).tap();
  await expectSheet(page, `Snooze #${group.number}`);
  const snooze = page.getByRole("dialog", { name: `Snooze #${group.number}` });
  await snooze.getByText("Until", { exact: true }).tap();
  await expect(snooze.getByLabel("Date")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "snooze-sheet-phone");
  await snooze.getByRole("button", { name: "Cancel", exact: true }).tap();
  await page.getByRole("button", { name: "Resolve", exact: true }).tap();
  await expectSheet(page, `Resolve #${group.number}`);
  await shot(page, "resolve-sheet-phone");
  await page
    .getByRole("dialog", { name: `Resolve #${group.number}` })
    .getByRole("button", { name: "Cancel", exact: true })
    .tap();

  // The Note box fits, with a long word in it.
  const note = page.getByTestId("note-box");
  await note.getByLabel("Note").fill(`See ${"x".repeat(200)} and the runbook.`);
  await note.scrollIntoViewIfNeeded();
  const noteBox = await box(page, "note-box");
  expect(noteBox.x).toBeGreaterThanOrEqual(0);
  expect(noteBox.x + noteBox.width).toBeLessThanOrEqual(360);
  await expectNoHorizontalScroll(page);
  await shot(page, "note-box-phone");

  // The list: the row's menu, and the bulk bar with a row selected.
  await page.goto(`/alert-groups?route=${encodeURIComponent(JSON.stringify([routeId]))}`);
  const row = page.getByTestId("alert-group-row").filter({ hasText: TITLE });
  await row.getByRole("button", { name: `Commands for #${group.number}`, exact: true }).tap();
  await expect(page.getByRole("menuitem", { name: "Acknowledge", exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "row-menu-phone");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await page.getByRole("button", { name: "Select", exact: true }).tap();
  await row.getByRole("checkbox", { name: `Select #${group.number}`, exact: true }).check();
  await expect(page.getByTestId("bulk-count")).toHaveText("1 selected");
  const bar = await box(page, "bulk-bar");
  expect(bar.x + bar.width).toBeLessThanOrEqual(360);
  await expectNoHorizontalScroll(page);
  await shot(page, "bulk-bar-phone");
  expect(csp).toEqual([]);
});

test("the Commands and the Snooze sheet in Russian, 360 px wide", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("cmd-olga", "responder");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "cmd-olga-password-1" });
  await admin.dispose();
  await signIn(page, "cmd-olga", "cmd-olga-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/alert-groups/${group.id}`);
  await expect(page.getByTestId("command-buttons").getByRole("button")).toHaveText([
    "Подтвердить",
    "Закрыть",
    "Отложить",
  ]);
  await expectNoHorizontalScroll(page);
  await shot(page, "commands-phone-ru");
  await page.getByRole("button", { name: "Отложить", exact: true }).tap();
  await expectSheet(page, `Отложить #${group.number}`);
  const sheet = page.getByRole("dialog", { name: `Отложить #${group.number}` });
  await sheet.getByText("Без срока", { exact: true }).tap();
  await expect(sheet.getByTestId("snooze-no-end-warning")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "snooze-sheet-phone-ru");
  expect(csp).toEqual([]);
});
