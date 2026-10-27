// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The System status page against `muster dev`: Verification steps 1, 2, 3, 6 and 7 of its story. The Mattermost
// Destination "alerts" is Broken after a delivery without the bot in its channel, and the Integration "hb" has lost its
// Heartbeat after one signal and the development clock; the page shows the Leader, both of them and the suggestion of
// an outgoing heartbeat, links "alerts" to its page and has no button. With the bot back and the clock moved past the
// probe, "Broken Destinations" says "None" without a reload. A Responder sees no entry and no page, and the page fits
// 360 px. No Content Security Policy violation.

import { expect, type Page, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  advance,
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

const FMM = "http://127.0.0.1:18065/_fake";
const BOT = "musterdevbotuserfake000000";
const HEARTBEAT_URL = "http://localhost:8081/api/v1/heartbeat";
const TIME_ZONE = "Europe/Berlin";
const ROUTE = "ss";
const SUGGESTION = "Set up an outgoing heartbeat so that a stopped Muster is noticed.";
const NO_PERMISSION = "You do not have permission to see this page.";

const NONE = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = {
  new_alert_group: NONE,
  new_alerts: NONE,
  reopen: NONE,
  ack_timeout: NONE,
  snooze_ended: NONE,
  rise_to_urgent: NONE,
};

interface Named {
  id: string;
  name: string;
}

interface Status {
  broken_destinations: { destination: Named; reason: string }[];
  integrations_attention: { integration: Named; issue: string; since?: string | null }[];
}

const ids = { alerts: "", hb: "", integration: "" };
/** Whether a Destination of another spec was Broken before this one started; then "None" cannot be expected. */
let otherBroken = false;

/** A time as the pages show it to the development Admin: date and time in the browser's time zone. */
function fmt(iso: string): string {
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
    timeZone: TIME_ZONE,
  }).format(new Date(iso));
}

function botInChannel(on: boolean): Promise<Response> {
  return fetch(`${FMM}/channels/ch-alerts/members/${BOT}`, { method: on ? "PUT" : "DELETE" });
}

/** Sets or clears the outgoing heartbeat URL, keeping every other field of the Organization as read. */
async function heartbeatUrl(admin: Api, url: string | null): Promise<void> {
  const res = await admin.context.get("/api/v1/organization");
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the Organization as the API reads it
  const org = (await res.json()) as Record<string, unknown> & {
    outgoing_heartbeat: { proxy: Record<string, unknown> };
  };
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  await admin.call(
    "PUT",
    "/api/v1/organization",
    { ...rest, outgoing_heartbeat: { url, proxy } },
    { "If-Match": res.headers().etag ?? "" },
  );
}

async function status(): Promise<Status> {
  const admin = await adminApi();
  try {
    return await admin.call<Status>("GET", "/api/v1/system-status");
  } finally {
    await admin.dispose();
  }
}

async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === ROUTE)) {
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
  const integrations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/integrations?limit=500",
  );
  for (const i of integrations.items.filter((x) => ["hb", "ss-ui"].includes(x.name))) {
    await admin.call("DELETE", `/api/v1/integrations/${i.id}`);
  }
  await heartbeatUrl(admin, null);
}

async function health(id: string): Promise<string> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ health: { state: string } }>("GET", `/api/v1/destinations/${id}`))
      .health.state;
  } finally {
    await admin.dispose();
  }
}

async function heartbeatState(id: string): Promise<string> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ heartbeat: { state: string } }>("GET", `/api/v1/integrations/${id}`))
      .heartbeat.state;
  } finally {
    await admin.dispose();
  }
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

/** An entry of the sub-navigation of Organization. */
function sub(page: Page, name: string) {
  return page
    .getByRole("navigation", { name: "Organization" })
    .getByRole("link", { name, exact: true });
}

function region(page: Page, name: string) {
  return page.getByRole("region", { name });
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  expect((await botInChannel(true)).ok).toBe(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?type=mattermost&limit=500",
    );
    const connection = connections.items.find((c) => c.name === "Example Mattermost");
    expect(connection, "the Connection of muster dev").toBeDefined();
    const alerts = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "mattermost",
        name: "alerts",
        connection_id: connection?.id,
        team_id: "team-dev",
        channel_id: "ch-alerts",
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    ids.alerts = alerts.destination.id;
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await admin.call("POST", "/api/v1/routes", {
      name: ROUTE,
      matchers: [{ label: "team", op: "=", value: ROUTE }],
      urgent: false,
      group_key: ["alertname", "cluster"],
      destination_ids: [ids.alerts],
      policy: profiles.items.find((p) => p.id === "paging")?.policy,
    });

    // "hb" with a Heartbeat that gets one signal and then none.
    const hb = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
      name: "hb",
      connection_mode: "webhook_only",
      static_labels: {},
      duplicate_window_seconds: 45,
      heartbeat: { enabled: true, timeout_seconds: 300 },
    });
    ids.hb = hb.id;
    const token = await admin.call<{ value: string }>(
      "POST",
      `/api/v1/integrations/${ids.hb}/tokens`,
      { name: "hb" },
    );
    const signal = await fetch(HEARTBEAT_URL, {
      method: "POST",
      headers: { Authorization: `Bearer ${token.value}` },
    });
    expect(signal.status).toBe(204);
  } finally {
    await admin.dispose();
  }
  otherBroken = (await status()).broken_destinations.length > 0;

  // "alerts" is Broken after a delivery without the bot in its channel.
  ids.integration = await fakeIntegration("ss-ui");
  await fam("PUT", "/groups/ss", { receiver: "ss-ui", route: "{}", labels: { alertname: "SS" } });
  expect((await botInChannel(false)).ok).toBe(true);
  await fam("PUT", "/groups/ss/alerts/c1", {
    labels: { team: ROUTE, cluster: "c1", severity: "warning" },
  });
  await notify(ids.integration, "ss", { reason: "first" });
  await expect.poll(() => health(ids.alerts), { timeout: 30_000 }).toBe("broken");

  // "hb" loses its Heartbeat once the clock passes its timeout.
  await advance(360);
  await expect.poll(() => heartbeatState(ids.hb), { timeout: 30_000 }).toBe("lost");
});

test.afterAll(async () => {
  await botInChannel(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("shows the Leader, a Broken Destination and a lost Heartbeat, only links, and follows the hints", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 1. Organization → the sub-navigation shows "System status" → the replicas with one Leader, "alerts" with a
  // reason containing 403, "hb — Heartbeat lost since {time}" and the suggestion of an outgoing heartbeat.
  await nav(page, "Organization").click();
  await sub(page, "System status").click();
  await expect(page).toHaveURL(/\/admin\/system-status$/);
  await expect(nav(page, "Organization")).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("heading", { name: "System status", level: 1 })).toBeVisible();
  await expect(region(page, "Replicas").getByTestId("replica-leader")).toHaveCount(1);
  await expect(region(page, "Replicas").getByTestId("replica-leader")).toHaveText("Leader");
  const broken = region(page, "Broken Destinations")
    .getByRole("listitem")
    .filter({ has: page.getByRole("link", { name: "alerts", exact: true }) });
  await expect(broken).toContainText("403");
  const since = (await status()).integrations_attention.find(
    (a) => a.integration.id === ids.hb,
  )?.since;
  expect(since, "the time the Heartbeat of hb was lost").toBeTruthy();
  await expect(
    region(page, "Integrations needing attention")
      .getByRole("listitem")
      .filter({ has: page.getByRole("link", { name: "hb", exact: true }) }),
  ).toHaveText(`hb — Heartbeat lost since ${fmt(since ?? "")}`);
  await expect(region(page, "Outgoing heartbeat")).toContainText(SUGGESTION);
  await shot(page, "system-status");

  // 2. No button, field or switch besides the navigation; "alerts" links to its page.
  const main = page.locator("main");
  for (const role of ["button", "textbox", "checkbox", "combobox", "switch", "radio"] as const) {
    await expect(main.getByRole(role)).toHaveCount(0);
  }
  await expect(broken.getByRole("link", { name: "alerts", exact: true })).toHaveAttribute(
    "href",
    `/destinations/${ids.alerts}`,
  );

  // 3. The bot is back and the clock passes the probe → "alerts" is healthy without a reload.
  await page.evaluate(() => {
    document.body.dataset.notReloaded = "yes";
  });
  expect((await botInChannel(true)).ok).toBe(true);
  await advance(300);
  await expect.poll(() => health(ids.alerts), { timeout: 30_000 }).toBe("healthy");
  await expect(broken).toHaveCount(0, { timeout: 30_000 });
  if (!otherBroken) {
    await expect(
      region(page, "Broken Destinations").getByText("None", { exact: true }),
    ).toBeVisible();
  }
  expect(await page.evaluate(() => document.body.dataset.notReloaded)).toBe("yes");
  expect(csp).toEqual([]);
});

test("a Responder sees neither the entries nor the pages", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    const { token } = await admin.createUser("ss-rita", "responder");
    await admin.call("POST", "/api/v1/password-setups", { token, password: "ss-rita-password-1" });
  } finally {
    await admin.dispose();
  }
  await signIn(page, "ss-rita", "ss-rita-password-1");

  // 6. No "System status" or "Outgoing heartbeat" entry, in the main navigation or under Organization; both addresses
  // refuse.
  await expect(nav(page, "Alert Groups")).toBeVisible();
  await expect(nav(page, "System status")).toHaveCount(0);
  await expect(nav(page, "Outgoing heartbeat")).toHaveCount(0);
  await nav(page, "Organization").click();
  await expect(sub(page, "Lookup tables")).toBeVisible();
  await expect(sub(page, "System status")).toHaveCount(0);
  await expect(sub(page, "Outgoing heartbeat")).toHaveCount(0);
  for (const path of ["/admin/system-status", "/admin/organization/outgoing-heartbeat"]) {
    await page.goto(path);
    await expect(page.getByText(NO_PERMISSION)).toBeVisible();
  }
  expect(csp).toEqual([]);
});

test("the System status fits 360 px", async ({ page }) => {
  const csp = watchCsp(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await signInAdmin(page);

  // 7. At 360 × 740 the page scrolls only vertically.
  await page.goto("/admin/system-status");
  await expect(page.getByRole("heading", { name: "Replicas", level: 2 })).toBeVisible();
  await expectNoHorizontalScroll(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
  await shot(page, "system-status-phone");
  expect(csp).toEqual([]);
});
