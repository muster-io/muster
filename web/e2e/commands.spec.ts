// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Commands on the Alert Group page against `muster dev`: Verification steps 1 to 5 and 7 to 10 of their story, and
// the Russian texts. The fake Alertmanager sends the group cmd to the Integration "cmd-ui" on the Route "ops" (Group
// key alertname and cluster), one Alert Group per cluster. The Admin acknowledges, Bob takes over, the Admin snoozes
// with no end, resolves with a Note and unresolves; a quick duration and an end of the user's choosing snooze another
// one until the development clock passes the end; Alert Groups resolved by the system and resolved by a person with a
// newer one open offer no Unresolve; "Mine" and "Snoozed with no end" filter the list; the Route editor saves a new
// Snooze duration; the statistics show the time to acknowledge of C-10.AC-14; a Viewer sees no Commands.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  APP,
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
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const NO_END_WARNING = "This Alert Group stays snoozed until someone unsnoozes it.";
// Bob of the story; profile.spec.ts creates a "bob" of its own in the same run.
const BOB = "cmd-bob";
const BOB_PASSWORD = "cmd-bob-password-1";

let integrationId = "";
let routeId = "";
let statsRouteId = "";

/** Creates a local user with a password, unless a spec before this one did. */
async function ensureUser(login: string, role: string, password: string): Promise<void> {
  const admin = await adminApi();
  try {
    const directory = await admin.call<{ items: { login?: string }[] }>(
      "GET",
      `/api/v1/user-directory?q=${encodeURIComponent(login)}`,
    );
    const found = directory.items.some((u) => u.login === login);
    if (!found) {
      const { token } = await admin.createUser(login, role);
      await admin.call("POST", "/api/v1/password-setups", { token, password });
    }
  } finally {
    await admin.dispose();
  }
}

function alert(cluster: string, extra: Record<string, unknown> = {}) {
  return { labels: { team: "cmd-ops", cluster, severity: "warning" }, ...extra };
}

/** The Alert Group of the Alert with the label cluster, as the Integration's Alerts view names it now. */
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

async function readGroup(id: string): Promise<{ status: string; snooze_until?: string | null }> {
  const admin = await adminApi();
  try {
    return await admin.call("GET", `/api/v1/alert-groups/${id}`);
  } finally {
    await admin.dispose();
  }
}

/** The browser's clock set to the business clock of `muster dev`, as both agree outside development mode. */
async function alignClock(page: Page): Promise<number> {
  const now = Date.now() + (await devClockOffset()) * 1000;
  await page.clock.setSystemTime(now);
  return now;
}

/** A page of its own signed in as Bob. */
async function bobPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext({
    baseURL: APP,
    locale: "en-US",
    timezoneId: "Europe/Berlin",
  });
  const page = await context.newPage();
  await signIn(page, BOB, BOB_PASSWORD);
  return page;
}

function entry(page: Page, text: string | RegExp) {
  return page.getByTestId("timeline-entry").filter({
    has: page.getByTestId("timeline-text").getByText(text, { exact: true }),
  });
}

/** An Alert of the Route "ack-stats". */
function put(cluster: string, extra: Record<string, unknown> = {}): Promise<void> {
  return fam("PUT", `/groups/cmd/alerts/s-${cluster}`, {
    labels: { team: "cmd-stats", cluster },
    ...extra,
  });
}

function buttons(page: Page) {
  return page.getByTestId("command-buttons").getByRole("button");
}

test.beforeAll(async () => {
  await ensureUser(BOB, "responder", BOB_PASSWORD);
  integrationId = await fakeIntegration("cmd-ui");
  routeId = await createRoute("ops", "cmd-ops", ["alertname", "cluster"]);
  await fam("PUT", "/groups/cmd", {
    receiver: "cmd-ui",
    route: "{}",
    labels: { alertname: "Cmd" },
  });
  for (const c of ["c1", "c2", "c3", "c4", "c6"]) {
    await fam("PUT", `/groups/cmd/alerts/${c}`, alert(c));
  }
  await notify(integrationId, "cmd", { reason: "first notification" });
});

test.afterAll(async () => {
  for (const id of [routeId, statsRouteId]) {
    if (id !== "") {
      await deleteRoute(id);
    }
  }
});

test("acknowledge, take over, snooze with no end, resolve with a Note and unresolve", async ({
  page,
  browser,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  const g1 = await groupOf("c1");
  await signInAdmin(page);

  // 1. A firing Alert Group offers Acknowledge, Resolve and Snooze; Acknowledge makes the Admin its Owner.
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByTestId("alert-group-status")).toHaveText("Firing");
  await expect(buttons(page)).toHaveText(["Acknowledge", "Resolve", "Snooze"]);
  await expect(page.getByTestId("note-box")).toBeVisible();
  await shot(page, "commands-firing");
  await page.getByRole("button", { name: "Acknowledge", exact: true }).click();
  await expect(page.getByTestId("alert-group-owner")).toHaveText("Owner: admin");
  await expect(page.getByTestId("alert-group-status")).toHaveText("Acknowledged");
  await expect(buttons(page)).toHaveText(["Unacknowledge", "Resolve", "Snooze"]);
  await shot(page, "commands-acknowledged");

  // 2. Bob sees "Take over from admin" and takes it over without a confirmation.
  const bob = await bobPage(browser);
  await bob.goto(`/alert-groups/${g1.id}`);
  const takeOver = bob.getByRole("button", { name: "Take over from admin", exact: true });
  await expect(takeOver).toBeVisible();
  await shot(bob, "commands-take-over");
  await takeOver.click();
  await expect(bob.getByRole("dialog")).toHaveCount(0);
  await expect(bob.getByTestId("alert-group-owner")).toHaveText("Owner: cmd-bob");
  await bob.context().close();

  // Back as the Admin: the live hint shows the new Owner, and the Timeline the Loud Takeover.
  await expect(page.getByTestId("alert-group-owner")).toHaveText("Owner: cmd-bob");
  await expect(
    page.getByRole("button", { name: "Take over from cmd-bob", exact: true }),
  ).toBeVisible();
  const takeover = entry(page, "Taken over from admin");
  await expect(takeover.getByTestId("timeline-loud")).toHaveText("Loud");
  await expect(takeover.getByTestId("timeline-mentions")).toHaveText("Mentions: previous_owner");
  await expect(takeover.getByTestId("timeline-actor")).toHaveText("cmd-bob via the UI");

  // 3. Snooze: the Route's quick durations, then "No end" with its warning and the confirmation.
  await page.getByRole("button", { name: "Snooze", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `Snooze #${g1.number}` });
  for (const name of ["1 h", "4 h", "24 h", "Until", "No end"]) {
    await expect(dialog.getByRole("radio", { name, exact: true })).toBeAttached();
  }
  await shot(page, "snooze-dialog");
  await dialog.getByText("No end", { exact: true }).click();
  await expect(dialog.getByTestId("snooze-no-end-warning")).toHaveText(NO_END_WARNING);
  const snooze = dialog.getByRole("button", { name: "Snooze", exact: true });
  await expect(snooze).toBeDisabled();
  await shot(page, "snooze-dialog-no-end");
  await dialog.getByLabel("I understand").check();
  await expect(snooze).toBeEnabled();
  await snooze.click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId("alert-group-snooze")).toHaveText("Snoozed with no end");
  await expect(page.getByTestId("alert-group-snoozed-by")).toHaveText("Snoozed by admin");
  await expect(buttons(page)).toHaveText(["Acknowledge", "Resolve", "Snooze", "Unsnooze"]);
  await shot(page, "commands-snoozed");

  // 4. Resolve with a Note: the Note shows in the Timeline with its author and Transport; then Unresolve.
  await page.getByRole("button", { name: "Resolve", exact: true }).click();
  const resolve = page.getByRole("dialog", { name: `Resolve #${g1.number}` });
  await resolve.getByLabel("Add a note (optional)").fill("Rolled back the deploy.");
  await expect(resolve.getByTestId("note-counter")).toHaveText("23/4000");
  await resolve.getByRole("button", { name: "Resolve", exact: true }).click();
  await expect(resolve).toHaveCount(0);
  await expect(page.getByTestId("alert-group-status")).toHaveText("Resolved");
  await expect(page.getByTestId("alert-group-resolution")).toHaveText("Resolved by admin");
  const note = page
    .getByTestId("timeline-entry")
    .filter({ has: page.getByTestId("note-body").getByText("Rolled back the deploy.") });
  await expect(note.getByTestId("timeline-actor")).toHaveText("admin via the UI");
  await expect(buttons(page)).toHaveText(["Unresolve"]);
  await shot(page, "commands-resolved");
  await page.getByRole("button", { name: "Unresolve", exact: true }).click();
  const unresolve = page.getByRole("dialog", { name: `Unresolve #${g1.number}` });
  await expect(
    unresolve.getByText("Bring this Alert Group back as firing without an Owner?"),
  ).toBeVisible();
  await unresolve.getByRole("button", { name: "Unresolve", exact: true }).click();
  await expect(page.getByTestId("alert-group-status")).toHaveText("Firing");
  await expect(page.getByTestId("alert-group-owner")).toHaveCount(0);
  await expect(entry(page, "Unresolved")).toHaveCount(1);

  // The Note box: the counter, and the Note in the Timeline.
  const box = page.getByTestId("note-box");
  const text = "Checked the disk; <b>not</b> the cause.";
  await box.getByLabel("Note").fill(text);
  await expect(box.getByTestId("note-counter")).toHaveText(`${text.length}/4000`);
  await shot(page, "note-box");
  await box.getByRole("button", { name: "Add note", exact: true }).click();
  await expect(box.getByLabel("Note")).toHaveValue("");
  await expect(page.getByTestId("note-body").getByText(text, { exact: true })).toBeVisible();
  expect(await page.locator("[data-testid=timeline] b").count()).toBe(0);
  expect(csp).toEqual([]);
});

test("a quick duration and an end of the user's choosing, until the clock passes it", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  const g2 = await groupOf("c2");
  await signInAdmin(page);
  await alignClock(page);
  await page.goto(`/alert-groups/${g2.id}`);

  // A quick duration: 4 hours from now.
  await page.getByRole("button", { name: "Snooze", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: `Snooze #${g2.number}` });
  await dialog.getByText("4 h", { exact: true }).click();
  const pressed = await alignClock(page);
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  await expect(page.getByTestId("alert-group-snooze")).toHaveText(/^Snoozed until /);
  const quick = Date.parse((await readGroup(g2.id)).snooze_until ?? "");
  expect(Math.abs(quick - pressed - 4 * 3600 * 1000)).toBeLessThan(60_000);

  // Until: 30 minutes from the business clock's now, as a date and a time in Berlin.
  const end = new Date(Math.ceil(((await alignClock(page)) + 30 * 60 * 1000) / 60_000) * 60_000);
  const parts = new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: "Europe/Berlin",
  }).formatToParts(end);
  const part = (type: string) => parts.find((p) => p.type === type)?.value ?? "";
  await page.getByRole("button", { name: "Snooze", exact: true }).click();
  dialog = page.getByRole("dialog", { name: `Snooze #${g2.number}` });
  await dialog.getByText("Until", { exact: true }).click();
  await expect(dialog.getByText("Time zone: Europe/Berlin")).toBeVisible();
  await dialog.getByLabel("Date").fill(`${part("year")}-${part("month")}-${part("day")}`);
  await dialog.getByLabel("Time").fill(`${part("hour")}:${part("minute")}`);
  await shot(page, "snooze-dialog-until");
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  await expect(page.getByTestId("alert-group-snooze")).toContainText(
    `${part("hour")}:${part("minute")}`,
  );
  expect(Date.parse((await readGroup(g2.id)).snooze_until ?? "")).toBe(end.getTime());

  // The development clock passes the end: the Alert Group fires again, and the Timeline says so.
  await advance(31 * 60);
  await expect
    .poll(async () => (await readGroup(g2.id)).status, { timeout: 30_000 })
    .toBe("firing");
  await page.reload();
  await expect(page.getByTestId("alert-group-status")).toHaveText("Firing");
  await expect(entry(page, "Snooze ended while alerts still fire")).toHaveCount(1);
  expect(csp).toEqual([]);
});

test("no Unresolve for a system resolve, and a link to the newer Alert Group", async ({ page }) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  // c3 resolves in Alertmanager: Muster resolves it.
  const g3 = await groupOf("c3");
  await fam("PUT", "/groups/cmd/alerts/c3", alert("c3", { status: "resolved" }));
  await notify(integrationId, "cmd", { reason: "some alerts resolved" });
  await expect.poll(async () => (await readGroup(g3.id)).status).toBe("resolved");
  // c4 is resolved by a person while its Alert keeps firing; after the Grace period a newer one starts.
  const g4 = await groupOf("c4");
  const admin = await adminApi();
  await admin.call("POST", `/api/v1/alert-groups/${g4.id}/resolve`, {});
  await admin.dispose();
  await advance(16 * 60);
  await notify(integrationId, "cmd", { reason: "repeat interval elapsed" });
  await expect.poll(async () => (await groupOf("c4")).id).not.toBe(g4.id);
  const g5 = await groupOf("c4");

  await signInAdmin(page);
  await page.goto(`/alert-groups/${g3.id}`);
  await expect(page.getByTestId("alert-group-status")).toHaveText("Resolved");
  await expect(page.getByTestId("note-box")).toBeVisible();
  await expect(page.getByRole("button", { name: "Unresolve", exact: true })).toHaveCount(0);

  await page.goto(`/alert-groups/${g4.id}`);
  await expect(page.getByTestId("alert-group-resolution")).toHaveText("Resolved by admin");
  const link = page.getByRole("link", { name: `A newer Alert Group exists: #${g5.number}` });
  await expect(link).toBeVisible();
  await expect(page.getByRole("button", { name: "Unresolve", exact: true })).toHaveCount(0);
  await expect(page.getByTestId("notice-newer_alert_group_exists")).toHaveCount(0);
  await shot(page, "newer-alert-group-link");
  await link.click();
  await expect(page).toHaveURL(new RegExp(`/alert-groups/${g5.id}$`));
  await expect(page.getByTestId("alert-group-number")).toHaveText(`#${g5.number}`);
  expect(csp).toEqual([]);
});

test("Mine, an Owner and Snoozed with no end filter the list, in the URL", async ({ page }) => {
  const csp = watchCsp(page);
  const g6 = await groupOf("c6");
  const g5 = await groupOf("c4");
  const admin = await adminApi();
  await admin.call("POST", `/api/v1/alert-groups/${g5.id}/acknowledge`);
  await admin.dispose();
  await signInAdmin(page);
  await page.goto("/alert-groups");

  // 7. "Mine": only what the Admin owns, and owner=me in the URL.
  await page.getByRole("button", { name: "Mine", exact: true }).click();
  await expect(page.getByRole("button", { name: "Mine", exact: true })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect.poll(() => new URL(page.url()).searchParams.get("owner")).toBe("me");
  const table = page.getByRole("table", { name: "Alert Groups" });
  await expect(table.getByRole("link", { name: `#${g5.number}`, exact: true })).toBeVisible();
  const owners = await table.getByTestId("owner").allTextContents();
  expect(owners.length).toBeGreaterThan(0);
  expect(new Set(owners)).toEqual(new Set(["admin"]));
  await expect(table.getByRole("columnheader", { name: "Owner" })).toBeVisible();
  await expect(table.getByRole("columnheader", { name: "Snooze end" })).toBeVisible();
  await shot(page, "list-mine");

  // An Owner from the user directory, sent as the user's id.
  const filters = page.getByTestId("alert-group-filters");
  await filters.getByLabel("Find a user").fill("cmd-bob");
  await expect(filters.getByLabel("Owner").locator("option", { hasText: "cmd-bob" })).toHaveCount(
    1,
  );
  await filters.getByLabel("Owner").selectOption({ label: "cmd-bob" });
  await expect
    .poll(() => new URL(page.url()).searchParams.get("owner"))
    .toMatch(/^"?[0-9A-Z]{14}"?$/);
  await filters.getByLabel("Owner").selectOption({ label: "Anyone" });
  await expect.poll(() => new URL(page.url()).searchParams.get("owner")).toBeNull();

  // Another Alert Group snoozed with no end from its row's menu; "Snoozed with no end" lists it alone.
  await page.goto(`/alert-groups?q=%23${g6.number}`);
  const row = table
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: `#${g6.number}`, exact: true }) });
  await row.getByRole("button", { name: `Commands for #${g6.number}` }).click();
  await page.getByRole("menuitem", { name: "Snooze", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `Snooze #${g6.number}` });
  await dialog.getByText("No end", { exact: true }).click();
  await dialog.getByLabel("I understand").check();
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  await expect(row.getByTestId("snooze-end")).toHaveText("No end");
  await page.goto("/alert-groups");
  // The box follows the URL, which the router changes a moment after the click.
  const noEnd = page.getByTestId("alert-group-filters").getByLabel("Snoozed with no end");
  await noEnd.click();
  await expect(noEnd).toBeChecked();
  await expect.poll(() => new URL(page.url()).searchParams.get("snoozed_no_end")).toBe("true");
  await expect(table.getByRole("row")).toHaveCount(2);
  await expect(table.getByRole("link", { name: `#${g6.number}`, exact: true })).toBeVisible();
  await expect(table.getByTestId("snooze-end")).toHaveText(["No end"]);
  await shot(page, "list-snoozed-no-end");
  await page.reload();
  await expect(
    page.getByTestId("alert-group-filters").getByLabel("Snoozed with no end"),
  ).toBeChecked();
  expect(csp).toEqual([]);
});

test("the Route editor saves a Snooze duration that the dialog then offers", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  // A new Route starts with the durations of its profile.
  await page.goto("/routes/new");
  await page.getByTestId("route-profile-on_call").click();
  await expect(page.getByTestId("route-snooze").getByTestId("snooze-duration")).toHaveText([
    "1 h",
    "4 h",
    "24 h",
  ]);

  // 8. "ops": add 2 h, save, and the Snooze dialog of an "ops" Alert Group offers it.
  await page.goto(`/routes/${routeId}`);
  const section = page.getByRole("group", { name: "Snooze durations" });
  await section.getByLabel("Duration", { exact: true }).fill("2");
  await section.getByLabel("Unit").selectOption("hours");
  await section.getByRole("button", { name: "Add", exact: true }).click();
  await expect(section.getByTestId("snooze-duration")).toHaveText(["1 h", "2 h", "4 h", "24 h"]);
  await shot(page, "route-snooze-durations");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await page.goto(`/routes/${routeId}`);
  await expect(
    page.getByRole("group", { name: "Snooze durations" }).getByTestId("snooze-duration"),
  ).toHaveText(["1 h", "2 h", "4 h", "24 h"]);

  const g1 = await groupOf("c1");
  await page.goto(`/alert-groups/${g1.id}`);
  await page.getByRole("button", { name: "Snooze", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `Snooze #${g1.number}` });
  await expect(dialog.getByRole("radio", { name: "2 h", exact: true })).toBeAttached();
  expect(csp).toEqual([]);
});

test("the statistics show the time to acknowledge of C-10.AC-14", async ({ page, browser }) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  statsRouteId = await createRoute("ack-stats", "cmd-stats", ["alertname", "cluster"]);
  for (const c of ["s1", "s2", "s3"]) {
    await put(c);
  }
  await notify(integrationId, "cmd", { reason: "new alerts added" });
  const [s1, s2, s3] = [await groupOf("s1"), await groupOf("s2"), await groupOf("s3")];
  const admin = await adminApi();
  const command = (id: string, name: string) =>
    admin.call("POST", `/api/v1/alert-groups/${id}/${name}`, name === "resolve" ? {} : undefined);
  await advance(300);
  await command(s1.id, "acknowledge");
  await advance(600);
  await command(s2.id, "acknowledge");
  await command(s2.id, "unacknowledge");
  await advance(60);
  await command(s2.id, "acknowledge");
  // Bob takes it over; then it resolves in Alertmanager and reopens within the Reopen window.
  const bob = await bobPage(browser);
  await bob.goto(`/alert-groups/${s2.id}`);
  await bob.getByRole("button", { name: "Take over from admin", exact: true }).click();
  await expect(bob.getByTestId("alert-group-owner")).toHaveText("Owner: cmd-bob");
  await bob.context().close();
  await put("s2", { status: "resolved" });
  await notify(integrationId, "cmd", { reason: "some alerts resolved" });
  await advance(120);
  await put("s2");
  await notify(integrationId, "cmd", { reason: "new alerts added" });
  await expect.poll(async () => (await readGroup(s2.id)).status).toBe("acknowledged");
  await command(s3.id, "resolve");
  await admin.dispose();

  // 9. Two of the three were acknowledged; the median of 5 and 15 minutes is 10.
  await signInAdmin(page);
  await page.goto("/statistics");
  const row = page.getByTestId("statistics-row").filter({
    has: page.getByTestId("statistics-name").getByText("ack-stats", { exact: true }),
  });
  await expect(row.getByTestId("statistics-count")).toHaveText("3");
  await expect(row.getByTestId("statistics-ack-count")).toHaveText("2");
  await expect(row.getByTestId("statistics-ack-median")).toHaveText("10 min");
  await shot(page, "statistics-time-to-acknowledge");
  expect(csp).toEqual([]);
});

test("a Viewer sees no Commands and no Note box", async ({ page }) => {
  const csp = watchCsp(page);
  await ensureUser("cmd-vera", "viewer", "cmd-vera-password-1");
  await signIn(page, "cmd-vera", "cmd-vera-password-1");
  const g1 = await groupOf("c1");
  // 10. The page and the list have no Commands.
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(page.getByTestId("alert-group-status")).toBeVisible();
  await expect(page.getByTestId("timeline")).toBeVisible();
  await expect(page.getByTestId("command-buttons")).toHaveCount(0);
  await expect(page.getByTestId("note-box")).toHaveCount(0);
  await page.goto("/alert-groups");
  await expect(
    page.getByRole("table", { name: "Alert Groups" }).getByRole("row").nth(1),
  ).toBeVisible();
  await expect(page.getByTestId("command-menu")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Select", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Mine", exact: true })).toBeVisible();
  expect(csp).toEqual([]);
});

test("the Commands, the dialogs and the Note box in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  await ensureUser("cmd-nina", "responder", "cmd-nina-password-1");
  await signIn(page, "cmd-nina", "cmd-nina-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  const g1 = await groupOf("c1");
  await page.goto(`/alert-groups/${g1.id}`);
  await expect(buttons(page)).toHaveText(["Подтвердить", "Закрыть", "Отложить"]);
  await expect(page.getByRole("heading", { name: "Добавить заметку" })).toBeVisible();
  await page.getByRole("button", { name: "Отложить", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `Отложить #${g1.number}` });
  await dialog.getByText("Без срока", { exact: true }).click();
  await expect(dialog.getByTestId("snooze-no-end-warning")).toHaveText(
    "Эта группа алертов останется отложенной, пока кто-нибудь не снимет откладывание.",
  );
  await expect(dialog.getByRole("button", { name: "Отложить", exact: true })).toBeDisabled();
  await shot(page, "snooze-dialog-ru");
  await dialog.getByRole("button", { name: "Отмена", exact: true }).click();
  await page.getByRole("button", { name: "Подтвердить", exact: true }).click();
  await expect(page.getByTestId("alert-group-owner")).toHaveText("Владелец: cmd-nina");
  await shot(page, "commands-ru");
  await page.goto("/alert-groups");
  await expect(page.getByRole("button", { name: "Мои", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Выбрать", exact: true }).click();
  await page.getByRole("checkbox", { name: `Выбрать #${g1.number}` }).check();
  await expect(page.getByTestId("bulk-count")).toHaveText("Выбрана 1");
  await shot(page, "bulk-ru");
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
});
