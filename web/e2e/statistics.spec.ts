// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The statistics page and the Lifecycle section of the Route editor against `muster dev`: Verification steps 1 to 3 of
// their story. The fake Alertmanager sends the group s1 to the Integration "stats": three Alerts of the Route "st",
// each its own Alert Group, resolved 10, 20 and 30 minutes after they started by the development clock. The page
// shows 3 Alert Groups with a median time to resolve of 20 min, on one day, by Route and by Integration, with no
// Content Security Policy violation; the Lifecycle section shows the On-call values and saves a new Reopen window.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  advance,
  createRoute,
  devClockOffset,
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
let routeId = "";

test.beforeAll(async () => {
  integrationId = await fakeIntegration("stats");
  routeId = await createRoute("st", "st", ["alertname", "n"]);
  await fam("PUT", "/groups/s1", {
    receiver: "stats",
    route: "{}",
    labels: { alertname: "Stat" },
  });
  for (const n of [1, 2, 3]) {
    await fam("PUT", `/groups/s1/alerts/x${n}`, { labels: { team: "st", n: String(n) } });
  }
  await notify(integrationId, "s1", { reason: "first notification" });
  for (const n of [1, 2, 3]) {
    await advance(600);
    await fam("PUT", `/groups/s1/alerts/x${n}`, {
      labels: { team: "st", n: String(n) },
      status: "resolved",
    });
    await notify(integrationId, "s1", { reason: "some alerts resolved" });
  }
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

/** The day of an instant in the time zone of the specs (playwright.config.ts), as YYYY-MM-DD. */
function berlinDay(instant: Date): string {
  return new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    timeZone: "Europe/Berlin",
  }).format(instant);
}

/** The row of an item of the statistics table. */
function itemRow(page: Page, name: string) {
  return page.getByTestId("statistics-row").filter({
    has: page.getByTestId("statistics-name").getByText(name, { exact: true }),
  });
}

test("shows the statistics per Route and per Integration, with no CSP violation", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  const nav = page.getByRole("navigation", { name: "Main" });
  const links = await nav.getByRole("link").allTextContents();
  expect(links.indexOf("Statistics")).toBe(links.indexOf("Alert Groups") + 1);
  await nav.getByRole("link", { name: "Statistics" }).click();
  await expect(page.getByRole("heading", { name: "Statistics", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "By route" })).toBeChecked();
  await expect(page.getByLabel("Period")).toHaveValue("7d");

  // 1. The Route "st": 3 Alert Groups, a median time to resolve of 20 min, and one day with 3.
  const st = itemRow(page, "st");
  await expect(st.getByTestId("statistics-count")).toHaveText("3");
  await expect(st.getByTestId("statistics-resolve-median")).toHaveText("20 min");
  await expect(st.getByTestId("statistics-resolve-p95")).toHaveText("29 min");
  await expect(st.getByTestId("statistics-ack-median")).toHaveText("—");
  await st.getByRole("button", { name: "st" }).click();
  const days = page.getByRole("table", { name: "Alert Groups per day of start: st" });
  await expect(days.getByTestId("statistics-day")).toHaveCount(1);
  await expect(days.getByTestId("day-count")).toHaveText("3");
  await expect(days.getByTestId("day-resolve")).toHaveText("20 min");
  await shot(page, "statistics-by-route");

  // Only the chosen Routes, kept in the URL.
  await page.getByTestId("statistics-routes").getByRole("combobox").selectOption({ label: "st" });
  await expect
    .poll(() => new URL(page.url()).searchParams.get("route"))
    .toBe(JSON.stringify([routeId]));
  await expect(page.getByTestId("statistics-row")).toHaveCount(1);
  await page.reload();
  await expect(page.getByTestId("statistics-row")).toHaveCount(1);
  await expect(itemRow(page, "st").getByTestId("statistics-count")).toHaveText("3");

  // 2. By Integration: the same three Alert Groups under "stats".
  await page.getByRole("radio", { name: "By integration" }).click();
  await expect(page.getByRole("radio", { name: "By integration" })).toBeChecked();
  await expect(page).toHaveURL(/by=integration/);
  await expect(page).not.toHaveURL(/route=/);
  const stats = itemRow(page, "stats");
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await expect(stats.getByTestId("statistics-resolve-median")).toHaveText("20 min");
  await shot(page, "statistics-by-integration");

  // The other periods, kept in the URL.
  await page.getByLabel("Period").selectOption("30d");
  await expect(page).toHaveURL(/period=30d/);
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await page.getByLabel("Period").selectOption("90d");
  await expect(page).toHaveURL(/period=90d/);
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");

  // A custom period of whole days around the day of the business clock, in the browser's time zone.
  await page.getByLabel("Period").selectOption("custom");
  await expect(page.getByLabel("From", { exact: true })).not.toHaveValue("");
  const now = Date.now() + (await devClockOffset()) * 1000;
  const day = (offset: number) => berlinDay(new Date(now + offset * 24 * 60 * 60 * 1000));
  await page.getByLabel("From", { exact: true }).fill(day(-1));
  await page.getByLabel("To", { exact: true }).fill(day(1));
  await expect(page).toHaveURL(new RegExp(`from=${day(-1)}`));
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");
  await page.reload();
  await expect(itemRow(page, "stats").getByTestId("statistics-count")).toHaveText("3");

  // A custom period without both days, or one that ends before it starts, is not sent.
  await page.getByLabel("From", { exact: true }).fill("");
  await expect(page.getByText("Choose both days of the period.")).toBeVisible();
  await page.getByLabel("From", { exact: true }).fill("2026-10-20");
  await page.getByLabel("To", { exact: true }).fill("2026-10-10");
  await expect(page.getByText("The period must start before it ends.")).toBeVisible();
  await page.getByLabel("Period").selectOption("7d");
  await expect(stats.getByTestId("statistics-count")).toHaveText("3");

  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "statistics-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  expect(csp).toEqual([]);
});

test("the Lifecycle section shows the profile's values and saves new ones", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // A new Route takes the values of its profile.
  await page.goto("/routes/new");
  await page.getByTestId("route-profile-on_call").click();
  const fresh = page.getByTestId("route-lifecycle");
  await expect(fresh.getByLabel("Reopen window")).toHaveValue("15");
  await expect(fresh.getByLabel("Grace period")).toHaveValue("15");
  await expect(
    fresh.getByRole("switch", { name: "A rise to Urgent removes the acknowledgement" }),
  ).toBeChecked();

  // 3. The Route "st": 15 and 15 minutes and the switch on; a Reopen window of 30 is saved and read back.
  await page.goto(`/routes/${routeId}`);
  const lifecycle = page.getByRole("group", { name: "Lifecycle" });
  await expect(lifecycle.getByLabel("Reopen window")).toHaveValue("15");
  await expect(lifecycle.getByLabel("Grace period")).toHaveValue("15");
  await expect(lifecycle.getByText("minutes")).toHaveCount(2);
  await expect(
    lifecycle.getByText(
      "An alert with the same key firing this soon after Muster resolved the Alert Group reopens it.",
    ),
  ).toBeVisible();
  await expect(
    lifecycle.getByText(
      "After a person resolves an Alert Group, alerts that still fire this long start a new one.",
    ),
  ).toBeVisible();
  await expect(
    lifecycle.getByRole("switch", { name: "A rise to Urgent removes the acknowledgement" }),
  ).toBeChecked();
  await shot(page, "route-lifecycle");

  // A negative value is refused in the form.
  await lifecycle.getByLabel("Reopen window").fill("-1");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(lifecycle.getByText("Enter a number of minutes, 0 or more.")).toBeVisible();

  await lifecycle.getByLabel("Reopen window").fill("30");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await page.goto(`/routes/${routeId}`);
  await page.reload();
  await expect(
    page.getByRole("group", { name: "Lifecycle" }).getByLabel("Reopen window"),
  ).toHaveValue("30");
  const admin = await adminApi();
  try {
    const stored = await admin.call<{
      policy: { reopen_window_seconds: number; grace_period_seconds: number };
    }>("GET", `/api/v1/routes/${routeId}`);
    expect(stored.policy.reopen_window_seconds).toBe(1800);
    expect(stored.policy.grace_period_seconds).toBe(900);
  } finally {
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

test("shows the statistics and the Lifecycle section in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  const { token } = await admin.createUser("st-olga", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "st-olga-password-1" });
  await admin.dispose();
  await signIn(page, "st-olga", "st-olga-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page
    .getByRole("navigation", { name: "Основная" })
    .getByRole("link", { name: "Статистика" })
    .click();
  await expect(page.getByRole("heading", { name: "Статистика", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "По маршрутам" })).toBeChecked();
  await expect(page.getByRole("columnheader", { name: "Время до закрытия" })).toBeVisible();
  const st = itemRow(page, "st");
  await expect(st.getByTestId("statistics-resolve-median")).toHaveText("20 мин");
  await st.getByRole("button", { name: "st" }).click();
  await expect(
    page.getByRole("table", { name: "Группы алертов по дням начала: st" }),
  ).toBeVisible();
  await shot(page, "statistics-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "statistics-ru-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  const reader = await adminApi();
  const stored = await reader.call<{ policy: { reopen_window_seconds: number } }>(
    "GET",
    `/api/v1/routes/${routeId}`,
  );
  await reader.dispose();
  await page.goto(`/routes/${routeId}`);
  const lifecycle = page.getByRole("group", { name: "Жизненный цикл" });
  await expect(lifecycle.getByLabel("Окно переоткрытия")).toHaveValue(
    String(stored.policy.reopen_window_seconds / 60),
  );
  await expect(lifecycle.getByLabel("Льготный период")).toHaveValue("15");
  await expect(
    lifecycle.getByRole("switch", { name: "Переход в срочные снимает подтверждение" }),
  ).toBeChecked();
  await shot(page, "route-lifecycle-ru");
  expect(csp).toEqual([]);
});
