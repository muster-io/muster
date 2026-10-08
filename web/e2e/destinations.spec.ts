// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination pages and the Route's Destinations and Delivery sections against `muster dev` and its fake
// Mattermost server: Verification steps 1 to 6 and 8 of their story. The Admin creates the Destination "alerts" (a
// channel without the bot is refused next to the channel), adds it to the Route "db" with a Thread batching window and
// a Storm threshold, which the Audit log names, accepts the suggestion "Send Muster's own alerts to a Destination", sees
// the Broken banner on the list and on the Route after the bot leaves the channel, "Check passed" and "Healthy" without
// a reload after it is back, and the Storm banner of "db". An Admin reading Russian sees the pages at phone width; a
// Viewer sees them read-only. No page scrolls sideways at 360 px; no Content Security Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  createRoute,
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

/** The control API of the fake Mattermost server of `muster dev`. */
const FMM = "http://127.0.0.1:18065/_fake";
const SERVER_URL = "http://127.0.0.1:18065";
const BOT = "musterdevbotuserfake000000";
const NOT_MEMBER = "The bot is not a member of this channel.";
const INTERNAL_ROUTE = "Muster internal alerts";
const TEAM = "dest-db";

interface Named {
  id: string;
  name: string;
}

interface Health {
  state: string;
  since?: string | null;
  reason?: string | null;
}

let integrationId = "";
let routeId = "";
let destinationId = "";

async function member(on: boolean, channel = "ch-alerts"): Promise<void> {
  const res = await fetch(`${FMM}/channels/${channel}/members/${BOT}`, {
    method: on ? "PUT" : "DELETE",
  });
  expect(res.ok, `the bot's membership: ${res.status}`).toBe(true);
}

/** Deletes what an earlier run left: the Routes of this spec, the Destination "alerts" and the Connection "mm". */
async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === "db" || x.name === INTERNAL_ROUTE)) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === "alerts")) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === "mm")) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

async function health(id: string): Promise<Health> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ health: Health }>("GET", `/api/v1/destinations/${id}`)).health;
  } finally {
    await admin.dispose();
  }
}

/** "HH:MM" of an instant in Berlin, the time zone of the specs, on a 24-hour clock. */
function berlinTime(iso: string, locale = "en-US"): string {
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: "Europe/Berlin",
  }).format(new Date(iso));
}

function brokenText(h: Health): string {
  const reason = (h.reason ?? "").trim().replace(/\.$/, "");
  return `Broken since ${berlinTime(h.since ?? "")}: ${reason}. Muster tries again every 5 min.`;
}

function nav(page: Page, name: string, label = "Main") {
  return page.getByRole("navigation", { name: label }).getByRole("link", { name, exact: true });
}

/** An Alert of the Route "db". */
function put(cluster: string): Promise<void> {
  return fam("PUT", `/groups/dest/alerts/${cluster}`, {
    labels: { team: TEAM, cluster, severity: "warning" },
  });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    await member(true);
    await member(true, "ch-alerts-prod");
    await admin.call("POST", "/api/v1/connections", {
      type: "mattermost",
      name: "mm",
      server_url: SERVER_URL,
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
  } finally {
    await admin.dispose();
  }
  integrationId = await fakeIntegration("dest-ui");
  routeId = await createRoute("db", TEAM, ["alertname", "cluster"]);
  await fam("PUT", "/groups/dest", {
    receiver: "dest-ui",
    route: "{}",
    labels: { alertname: "Dest" },
  });
});

test.afterAll(async () => {
  await member(true);
  await member(true, "ch-alerts-prod");
  await fetch(`${FMM}/faults`, { method: "DELETE" });
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("creates a Mattermost Destination, refusing a channel without the bot next to the channel", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await nav(page, "Destinations").click();
  await expect(page.getByRole("heading", { name: "Destinations", level: 1 })).toBeVisible();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Mattermost" }).check();
  await page.getByLabel("Connection", { exact: true }).selectOption({ label: "mm" });
  await page.getByLabel("Team", { exact: true }).selectOption({ label: "dev" });
  // The list holds the channels the bot is a member of, so the fake's "no-bot" is not offered; the bot leaves
  // "alerts-prod" from a terminal after the list was read, and the save's Destination check refuses it.
  const channels = page.getByLabel("Channel", { exact: true });
  await expect(channels.getByRole("option")).toHaveText([
    "Choose a channel",
    "alerts",
    "Alerts prod (alerts-prod)",
  ]);
  await expect(
    page.getByText(
      "The list shows the channels the bot is a member of: add the bot to a channel to send there.",
    ),
  ).toBeVisible();
  await channels.selectOption({ label: "Alerts prod (alerts-prod)" });
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("alerts-prod");
  await expect(page.getByLabel("Requests")).toHaveValue("50");
  await member(false, "ch-alerts-prod");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-type-channel-error-text")).toHaveText(NOT_MEMBER);
  await expect(page.getByLabel("Channel", { exact: true })).toBeFocused();
  await shot(page, "destination-not-member");
  await member(true, "ch-alerts-prod");

  await page.getByLabel("Channel", { exact: true }).selectOption({ label: "alerts" });
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("alerts");
  await page.getByLabel("New Alerts", { exact: true }).selectOption({ label: "@channel" });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();
  await expect(page.getByLabel("New Alerts", { exact: true })).toHaveValue("channel");

  await page.getByRole("link", { name: "Destinations" }).first().click();
  const row = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "alerts", exact: true }) });
  await expect(row.getByRole("cell").nth(0)).toHaveText("Mattermost");
  await expect(row.getByRole("cell").nth(2)).toHaveText("Healthy");
  await expect(page.getByTestId("broken-destinations")).toHaveCount(0);
  await shot(page, "destinations-list");
  expect(csp).toEqual([]);
});

test("adds the Destination to the Route db with its delivery settings, named in the Audit log", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await nav(page, "Routes").click();
  await page.getByRole("link", { name: "db", exact: true }).click();
  await expect(page.getByTestId("route-destinations-none")).toBeVisible();
  await page.getByLabel("Add a Destination").selectOption({ label: "alerts (Mattermost)" });
  await expect(page.getByTestId("route-destination")).toContainText("alerts");
  await expect(page.getByTestId("route-destination")).toContainText("Healthy");
  await page.getByLabel("Thread batching window", { exact: true }).fill("120");
  await page.getByLabel("Storm threshold", { exact: true }).fill("30");
  await expect(
    page.getByText(
      "New alerts that arrive within this time after a Thread reply are collected into one reply.",
    ),
  ).toBeVisible();
  await expect(
    page.getByText(
      "When more new Alert Groups than this start on the route within a minute, its Destinations get one Storm summary instead of a message each.",
    ),
  ).toBeVisible();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);

  await page.goto(`/routes/${routeId}`);
  await page.reload();
  await expect(page.getByLabel("Thread batching window", { exact: true })).toHaveValue("120");
  await expect(page.getByLabel("Storm threshold", { exact: true })).toHaveValue("30");
  await expect(page.getByTestId("route-destination")).toContainText("alerts");
  await shot(page, "route-destinations");

  await page.goto(`/admin/audit-log?resource_type=route&resource_id=${routeId}`);
  const diff = page
    .getByRole("row")
    .filter({ hasText: "route.updated" })
    .first()
    .getByTestId("audit-diff");
  await expect(diff).toContainText("Destinations:");
  await expect(diff).toContainText("Thread batching window (seconds):");
  await expect(diff).toContainText("Storm threshold (new Alert Groups per minute):");
  expect(csp).toEqual([]);
});

test("accepts the suggestion to send Muster's own alerts to the Destination", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await nav(page, "Routes").click();
  const card = page
    .getByTestId("route-suggestion")
    .filter({ hasText: "Send Muster's own alerts to a Destination" });
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "Create the route" }).click();
  await expect(card.getByText("Choose the Destination for Muster's own alerts.")).toBeVisible();
  await card.getByLabel("Destination").selectOption({ label: "alerts (Mattermost)" });
  await shot(page, "routes-internal-suggestion");
  await card.getByRole("button", { name: "Create the route" }).click();
  await expect(card).toHaveCount(0);
  await expect(page.getByRole("link", { name: INTERNAL_ROUTE, exact: true })).toBeVisible();

  const admin = await adminApi();
  try {
    const routes = await admin.call<{ items: (Named & { destination_ids: string[] })[] }>(
      "GET",
      "/api/v1/routes",
    );
    expect(routes.items.find((r) => r.name === INTERNAL_ROUTE)?.destination_ids).toEqual([
      destinationId,
    ]);
  } finally {
    await admin.dispose();
  }
  expect(csp).toEqual([]);
});

test("shows the Broken banner on the list and on the Route, and Check makes it healthy without a reload", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await member(false);
  await put("broken-1");
  await notify(integrationId, "dest", { reason: "first notification" });
  await expect.poll(async () => (await health(destinationId)).state).toBe("broken");
  const broken = await health(destinationId);
  expect(broken.reason ?? "").toContain("403");
  const text = brokenText(broken);

  await signInAdmin(page);
  await nav(page, "Destinations").click();
  await expect(page.getByTestId("broken-destinations").getByTestId("broken-banner")).toHaveText(
    `alerts: ${text}`,
  );
  const row = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "alerts", exact: true }) });
  await expect(row.getByRole("cell").nth(2)).toHaveText("Broken");
  await shot(page, "destinations-broken");

  await nav(page, "Routes").click();
  await page.getByRole("link", { name: "db", exact: true }).click();
  await expect(page.getByTestId("route-destination").getByTestId("broken-banner-text")).toHaveText(
    text,
  );

  await page.goto(`/destinations/${destinationId}`);
  await expect(page.getByTestId("broken-banner-text")).toHaveText(text);
  await member(true);
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");
  await expect(
    page.getByTestId("destination-check-item").filter({ hasText: "Bot in the channel" }),
  ).toHaveText("Bot in the channel: ok");
  await expect(page.getByTestId("destination-health").first()).toHaveText("Healthy");
  await expect(page.getByTestId("broken-banner")).toHaveCount(0);
  await shot(page, "destination-check-passed");
  expect(csp).toEqual([]);
});

test("shows the Storm banner of the Route while its Storm is active", async ({ page }) => {
  const csp = watchCsp(page);
  for (let i = 1; i <= 31; i++) {
    await put(`storm-${i}`);
  }
  await notify(integrationId, "dest", { reason: "new alerts added" });
  const admin = await adminApi();
  type Storm = { since: string; alert_group_count: number };
  const read = async () =>
    (await admin.call<{ storm_active: boolean; storm?: Storm }>("GET", `/api/v1/routes/${routeId}`))
      .storm;
  let storm: Storm | undefined;
  try {
    // The Storm's count settles once every new Alert Group of the notification is processed.
    await expect
      .poll(
        async () => {
          const before = await read();
          await new Promise((done) => setTimeout(done, 1000));
          storm = await read();
          return storm !== undefined && before?.alert_group_count === storm.alert_group_count;
        },
        { timeout: 30_000 },
      )
      .toBe(true);
  } finally {
    await admin.dispose();
  }
  const count = storm?.alert_group_count ?? 0;
  await signInAdmin(page);
  await page.goto(`/routes/${routeId}`);
  await expect(page.getByTestId("storm-banner")).toHaveText(
    `Storm since ${berlinTime(storm?.since ?? "")}: ${count} new Alert ${count === 1 ? "Group" : "Groups"}. Destinations receive a Storm summary.`,
  );
  await shot(page, "route-storm");
  expect(csp).toEqual([]);
});

async function russianAdmin(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "dest-admin-ru", "dest-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/destinations");
  await expect(page.getByRole("heading", { name: "Места доставки", level: 1 })).toBeVisible();
  const row = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "alerts", exact: true }) });
  await expect(row.getByRole("cell").nth(2)).toHaveText("Работает");
  await expectNoHorizontalScroll(page);
  await shot(page, "destinations-ru-360");

  await row.getByRole("link", { name: "alerts", exact: true }).click();
  await expect(page.getByLabel("Новые алерты", { exact: true })).toHaveValue("channel");
  await expect(page.getByTestId("mention-note")).toHaveText(
    "Напоминания и автоматическая отмена подтверждения всегда упоминают владельца, а перехват — прежнего владельца.",
  );
  await expect(page.getByRole("button", { name: "Проверить", exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "destination-ru-360");

  await page.goto(`/routes/${routeId}`);
  await expect(page.getByLabel("Окно объединения в треде", { exact: true })).toHaveValue("120");
  await expect(page.getByTestId("storm-banner")).toHaveText(
    /^Шторм с \d\d:\d\d: \d+ (новая группа|новые группы|новых групп) алертов\. Места доставки получают сводку шторма\.$/,
  );
  await expectNoHorizontalScroll(page);
  await shot(page, "route-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}

test("an Admin reading Russian sees the pages at phone width without scrolling sideways", async ({
  browser,
}) => {
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("dest-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "dest-admin-ru-1" });
  await admin.dispose();
  try {
    await russianAdmin(browser);
  } finally {
    const cleanup = await adminApi();
    try {
      await cleanup.call("DELETE", `/api/v1/users/${userId}`);
    } finally {
      await cleanup.dispose();
    }
  }
});

test("a Viewer sees the Destination pages read-only", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("dest-viewer", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "dest-viewer-1" });
  await admin.dispose();
  // The Viewer stays: users.spec.ts counts the deleted Viewers of the run.
  await viewer(browser);
});

async function viewer(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "dest-viewer", "dest-viewer-1");
  await page.goto("/destinations");
  await expect(page.getByRole("link", { name: "alerts", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Create destination" })).toHaveCount(0);
  await page.getByRole("link", { name: "alerts", exact: true }).click();
  await expect(page.getByTestId("destination-read-only")).toHaveText(
    "You can view this Destination but not change it.",
  );
  await expect(page.getByTestId("stored-channel")).toHaveText("alerts");
  for (const name of ["Save", "Check", "Delete"]) {
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  }
  await expectNoHorizontalScroll(page);
  await shot(page, "destination-viewer-360");
  expect(csp).toEqual([]);
  await context.close();
}
