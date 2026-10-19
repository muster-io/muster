// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze presets in the web UI against `muster dev`: Verification steps 1 to 3 and 5 to 7 of their story; step 4,
// the General page, is in organization-settings.spec.ts. The fake Alertmanager sends the group spr to the Integration
// "snooze-presets" on the On-call Route "spr", two firing Alert Groups; the Responder "spr-anna" reads Muster in
// Russian. The development clock moves to the next 02:00 in Berlin, the time zone of the browser and of the profiles,
// which follow it. The Snooze dialog offers the On-call durations, then "Until tomorrow 09:00", which ends the same
// morning, and "Until Monday 09:00", which ends on the next Monday, and snoozes until 09:00; a bulk Snooze gives both
// Alert Groups the same end; the profile names the Organization's time zone, UTC; the Russian texts; at 360 × 740 the
// dialog is a full-screen sheet with both presets and their ends in view; and at 08:59 the preset still ends at 09:00.

import { TZDate } from "@date-fns/tz";
import { type Browser, type Locator, type Page, expect, test } from "@playwright/test";

import {
  APP,
  Api,
  SUBPIXEL,
  adminApi,
  advance,
  createRoute,
  deleteRoute,
  devClockOffset,
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

const ZONE = "Europe/Berlin";
const ANNA = "spr-anna";
const ANNA_PASSWORD = "spr-anna-password-1";
const PROFILE_HINT =
  "Snooze presets pressed in messengers end in this time zone. While it follows the browser, they use the " +
  "Organization's time zone, UTC.";

let integrationId = "";
let routeId = "";
const groups: { id: string; number: number }[] = [];

/** The business clock of `muster dev` now: the real clock, which the specs share with it, plus its offset. */
async function businessNow(): Promise<Date> {
  return new Date(Date.now() + (await devClockOffset()) * 1000);
}

/**
 * Moves the development clock forward to the next hour:minute on the wall clock of Berlin; a second step corrects a
 * change of offset on the way. Returns the business clock's now there.
 */
async function advanceTo(hour: number, minute: number): Promise<TZDate> {
  for (let step = 0; step < 3; step++) {
    const now = new TZDate((await businessNow()).getTime(), ZONE);
    if (now.getHours() === hour && now.getMinutes() === minute) {
      return now;
    }
    const wall = now.getHours() * 3600 + now.getMinutes() * 60 + now.getSeconds();
    await advance((hour * 3600 + minute * 60 - wall + 86400) % 86400);
  }
  const reached = new TZDate((await businessNow()).getTime(), ZONE);
  expect([reached.getHours(), reached.getMinutes()]).toEqual([hour, minute]);
  return reached;
}

/**
 * The ends of the presets before the working day starts (C-10.FR-17): 09:00 the same day, and 09:00 on the next Monday,
 * which is today on a Monday.
 */
function endsBefore9(now: TZDate): { tomorrow: Date; weekStart: Date } {
  const [year, month, day] = [now.getFullYear(), now.getMonth(), now.getDate()];
  const days = (1 - now.getDay() + 7) % 7;
  return {
    tomorrow: new Date(new TZDate(year, month, day, 9, 0, ZONE).getTime()),
    weekStart: new Date(new TZDate(year, month, day + days, 9, 0, ZONE).getTime()),
  };
}

/** An instant as the UI writes a date with its time in the language, formatted by the page's own Intl. */
function shown(page: Page, instant: Date, language: "en" | "ru"): Promise<string> {
  return page.evaluate(
    ([iso, locale, timeZone]) =>
      new Intl.DateTimeFormat(locale, {
        dateStyle: "medium",
        timeStyle: "short",
        hourCycle: "h23",
        timeZone,
      }).format(new Date(iso)),
    [instant.toISOString(), language, ZONE] as const,
  );
}

/** The browser's clock set to the business clock of `muster dev`, as both agree outside development mode. */
async function alignClock(page: Page): Promise<void> {
  await page.clock.setSystemTime(Date.now() + (await devClockOffset()) * 1000);
}

/** The names of the choices of a Snooze dialog, in order. */
function choices(dialog: Locator): Promise<string[]> {
  return dialog
    .getByRole("radio")
    .evaluateAll((radios) =>
      radios.map(
        (r) => document.getElementById(r.getAttribute("aria-labelledby") ?? "")?.textContent ?? "",
      ),
    );
}

/** Opens the Snooze dialog of an Alert Group, whose button and title read "Snooze" in the language. */
async function openSnooze(
  page: Page,
  group: { id: string; number: number },
  snooze = "Snooze",
): Promise<Locator> {
  await page.goto(`/alert-groups/${group.id}`);
  await page.getByRole("button", { name: snooze, exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `${snooze} #${group.number}` });
  await expect(dialog).toBeVisible();
  return dialog;
}

async function groupOf(cluster: string): Promise<{ id: string; number: number }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${cluster}`).toBeDefined();
    return group ?? { id: "", number: 0 };
  } finally {
    await admin.dispose();
  }
}

/** A page of its own signed in as Anna, who reads Muster in Russian. */
async function annaPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext({ baseURL: APP, locale: "en-US", timezoneId: ZONE });
  const page = await context.newPage();
  await signIn(page, ANNA, ANNA_PASSWORD);
  return page;
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    const { token } = await admin.createUser(ANNA, "responder");
    await admin.call("POST", "/api/v1/password-setups", { token, password: ANNA_PASSWORD });
  } finally {
    await admin.dispose();
  }
  const anna = await Api.signIn(ANNA, ANNA_PASSWORD);
  try {
    await anna.call("PUT", "/api/v1/me", { name: ANNA, language: "ru" });
  } finally {
    await anna.dispose();
  }
  integrationId = await fakeIntegration("snooze-presets");
  routeId = await createRoute("spr", "spr", ["alertname", "cluster"]);
  await fam("PUT", "/groups/spr", {
    receiver: "snooze-presets",
    route: "{}",
    labels: { alertname: "SnoozePresets" },
  });
  for (const c of ["p1", "p2"]) {
    await fam("PUT", `/groups/spr/alerts/${c}`, { labels: { team: "spr", cluster: c } });
  }
  await notify(integrationId, "spr", { reason: "first notification" });
  groups.push(await groupOf("p1"), await groupOf("p2"));
  await advanceTo(2, 0);
});

test.afterAll(async () => {
  if (routeId !== "") {
    await deleteRoute(routeId);
  }
});

test("at 02:00 the presets end at 09:00 the same day and on Monday, and snooze until 09:00", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const ends = endsBefore9(new TZDate((await businessNow()).getTime(), ZONE));
  const [g1] = groups;
  await signInAdmin(page);
  await alignClock(page);

  // 1. The On-call durations, both presets with their ends, "Until" and "No end".
  const dialog = await openSnooze(page, g1 ?? { id: "", number: 0 });
  await expect(dialog.getByTestId("snooze-preset-end-tomorrow")).toHaveText(
    `Ends ${await shown(page, ends.tomorrow, "en")}`,
  );
  await expect(dialog.getByTestId("snooze-preset-end-week_start")).toHaveText(
    `Ends ${await shown(page, ends.weekStart, "en")}`,
  );
  expect(await choices(dialog)).toEqual([
    "1 h",
    "4 h",
    "24 h",
    "Until tomorrow 09:00",
    "Until Monday 09:00",
    "Until",
    "No end",
  ]);
  await expect(dialog.getByRole("radio", { name: "1 h", exact: true })).toBeChecked();
  await shot(page, "snooze-presets-0200-en");

  // 2. "Until tomorrow 09:00" → "Snooze": the header shows the end, which is 09:00 in Berlin.
  await dialog.getByTestId("snooze-choice-tomorrow").click();
  await expect(
    dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true }),
  ).toBeChecked();
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  await expect(page.getByTestId("alert-group-snooze")).toHaveText(
    `Snoozed until ${await shown(page, ends.tomorrow, "en")}`,
  );
  await expect(page.getByTestId("alert-group-snoozed-by")).toHaveText("Snoozed by admin");
  const stored = await sql("SELECT snooze_until FROM alert_groups WHERE public_id = $1", [g1?.id]);
  const until: unknown = stored.rows[0]?.snooze_until;
  expect(until instanceof Date ? until.toISOString() : until).toBe(ends.tomorrow.toISOString());

  // 5. The profile names the Organization's time zone under the time zone.
  await page.goto("/profile");
  await expect(page.getByLabel("Time zone", { exact: true })).toHaveAccessibleDescription(
    PROFILE_HINT,
  );
  await expect(page.getByText(PROFILE_HINT, { exact: true })).toBeVisible();
  await shot(page, "snooze-presets-profile-en");
  expect(csp).toEqual([]);
});

test("a bulk Snooze until Monday gives both Alert Groups the same end", async ({ page }) => {
  const csp = watchCsp(page);
  const ends = endsBefore9(new TZDate((await businessNow()).getTime(), ZONE));
  await signInAdmin(page);
  await alignClock(page);

  // 3. List → both Alert Groups → "Snooze" → "Until Monday 09:00".
  await page.goto(`/alert-groups?route=${encodeURIComponent(JSON.stringify([routeId]))}`);
  const table = page.getByRole("table", { name: "Alert Groups" });
  await expect(table.getByRole("row")).toHaveCount(3);
  await page.getByRole("button", { name: "Select", exact: true }).click();
  const bar = page.getByTestId("bulk-bar");
  await bar.getByRole("button", { name: "Select all shown", exact: true }).click();
  await expect(page.getByTestId("bulk-count")).toHaveText("2 selected");
  await bar.getByRole("button", { name: "Snooze", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Snooze 2 Alert Groups" });
  const monday = await shown(page, ends.weekStart, "en");
  await expect(dialog.getByTestId("snooze-preset-end-week_start")).toHaveText(`Ends ${monday}`);
  expect(await choices(dialog)).toEqual([
    "Until tomorrow 09:00",
    "Until Monday 09:00",
    "Until",
    "No end",
  ]);
  await expect(
    dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true }),
  ).toBeChecked();
  await dialog.getByTestId("snooze-choice-week_start").click();
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  const result = page.getByRole("dialog", { name: "Result: Snooze" });
  const items = result.getByTestId("bulk-result-item");
  await expect(items).toHaveCount(2);
  // The rows of one result keep no fixed order.
  expect((await items.allTextContents()).toSorted()).toEqual(
    groups.map((g) => `#${g.number}: done`).toSorted(),
  );
  await result.getByRole("button", { name: "Close", exact: true }).last().click();
  await expect(table.getByTestId("snooze-end")).toHaveText([monday, monday]);
  await shot(page, "snooze-presets-bulk-en");
  expect(csp).toEqual([]);
});

test("in Russian, and as a full-screen sheet 360 px wide in both languages", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const ends = endsBefore9(new TZDate((await businessNow()).getTime(), ZONE));
  const [, g2] = groups;
  const group = g2 ?? { id: "", number: 0 };
  const anna = await annaPage(browser);
  try {
    // 6. «До завтра 09:00» and «До понедельника 09:00», each with «Закончится …».
    let dialog = await openSnooze(anna, group, "Отложить");
    await expect(dialog.getByTestId("snooze-preset-end-tomorrow")).toHaveText(
      `Закончится ${await shown(anna, ends.tomorrow, "ru")}`,
    );
    await expect(dialog.getByTestId("snooze-preset-end-week_start")).toHaveText(
      `Закончится ${await shown(anna, ends.weekStart, "ru")}`,
    );
    expect(await choices(dialog)).toEqual([
      "1 ч",
      "4 ч",
      "24 ч",
      "До завтра 09:00",
      "До понедельника 09:00",
      "До даты",
      "Без срока",
    ]);
    await shot(anna, "snooze-presets-0200-ru");

    // 7. At 360 × 740 the dialog covers the screen with both presets and their ends in view.
    await anna.setViewportSize({ width: 360, height: 740 });
    dialog = await openSnooze(anna, group, "Отложить");
    await expectSheet(dialog);
    await expect(dialog.getByTestId("snooze-preset-end-week_start")).toBeVisible();
    await expectPresetsInView(dialog);
    await expectNoHorizontalScroll(anna);
    await shot(anna, "snooze-presets-phone-ru");
  } finally {
    await anna.context().close();
  }

  await page.setViewportSize({ width: 360, height: 740 });
  await signInAdmin(page);
  const dialog = await openSnooze(page, group);
  await expectSheet(dialog);
  await expect(dialog.getByTestId("snooze-preset-end-week_start")).toBeVisible();
  await expectPresetsInView(dialog);
  await expectNoHorizontalScroll(page);
  await shot(page, "snooze-presets-phone-en");
  expect(csp).toEqual([]);
});

test("at 08:59 the preset still ends at 09:00 the same day", async ({ page, browser }) => {
  const csp = watchCsp(page);
  const [g1] = groups;
  const group = g1 ?? { id: "", number: 0 };
  // Both sign in first: the advance stays within the idle timeout of their sessions.
  await signInAdmin(page);
  const anna = await annaPage(browser);
  try {
    const ends = endsBefore9(await advanceTo(8, 59));
    await alignClock(page);
    const dialog = await openSnooze(page, group);
    await expect(dialog.getByTestId("snooze-preset-end-tomorrow")).toHaveText(
      `Ends ${await shown(page, ends.tomorrow, "en")}`,
    );
    await shot(page, "snooze-presets-0859-en");
    const russian = await openSnooze(anna, group, "Отложить");
    await expect(russian.getByTestId("snooze-preset-end-tomorrow")).toHaveText(
      `Закончится ${await shown(anna, ends.tomorrow, "ru")}`,
    );
    await shot(anna, "snooze-presets-0859-ru");
  } finally {
    await anna.context().close();
  }
  expect(csp).toEqual([]);
});

/** The dialog covers the whole screen of a phone. */
async function expectSheet(dialog: Locator): Promise<void> {
  // The sheet opens with a short animation.
  await expect
    .poll(async () => {
      const b = await dialog.boundingBox();
      return b === null ? null : [b.x, b.y, b.width, b.height].map(Math.round);
    })
    .toEqual([0, 0, 360, 740]);
}

/** Both presets with their ends lie entirely on the screen, within its width. */
async function expectPresetsInView(dialog: Locator): Promise<void> {
  for (const preset of ["tomorrow", "week_start"]) {
    const pill = dialog.getByTestId(`snooze-choice-${preset}`);
    await expect(pill).toBeInViewport({ ratio: 1 });
    await expect(dialog.getByTestId(`snooze-preset-end-${preset}`)).toBeInViewport({ ratio: 1 });
    const b = await pill.boundingBox();
    expect(b === null ? Infinity : b.x + b.width).toBeLessThanOrEqual(360 + SUBPIXEL);
  }
}
