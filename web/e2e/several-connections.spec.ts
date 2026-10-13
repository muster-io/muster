// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Several Connections of one messenger against `muster dev`: the pages of the Verification of their story with two
// Mattermost Connections on the first fake Mattermost server ("Dev Mattermost" and "mm-b", its second bot), one on the
// second server ("mm-2"), two Telegram Connections ("Dev Telegram" and "tg-b", the second bot), a Destination of
// each in one channel and an outgoing webhook, all on one Route. The Connections and Destinations lists, a Telegram
// Destination of the second bot, the Route's Destinations, the delivery section of an Alert Group, the "Delivery
// problem" filter, the profile's Messenger accounts and the user page's Account links, each at 1280 and 360 px wide.
// With MUSTER_E2E_SCREENSHOTS set, each page is saved as <name>-desktop.png and <name>-360.png. Everything the spec
// creates is removed at the end, so the specs after it see only their own configuration.

import { type Page, expect, test } from "@playwright/test";

import {
  Api,
  FMM,
  FMM2,
  FTG,
  MM_SECOND_TOKEN,
  MM_TOKEN,
  TG_SECOND_TOKEN,
  adminApi,
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

const TEAM = "s069";
const ROUTE = "s069-multi";
const LOGIN = "s069-responder";
const PASSWORD = "s069-password-1";
const SECOND_BOT = "musterdevbotbuserfake00000";
const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);
const MM_DESTINATIONS = ["mm-a-alerts", "mm-b-alerts", "mm-2-alerts"];
const TG_DESTINATIONS = ["tg-a-alerts", "tg-b-alerts"];
const DESTINATIONS = [...MM_DESTINATIONS, ...TG_DESTINATIONS, "s069-hook"];
const CONNECTIONS = ["mm-b", "mm-2", "tg-b"];

/** The base URL of the fake server whose control API is at control. */
const origin = (control: string): string => new URL(control).origin;

interface Named {
  id: string;
  name: string;
}

let integrationId = "";
let routeId = "";
let userId = "";
const ids: Record<string, string> = {};
const groups: { id: string; number: number }[] = [];

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
  for (const d of destinations.items.filter((x) => DESTINATIONS.includes(x.name))) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => CONNECTIONS.includes(x.name))) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

async function connectionId(admin: Api, name: string): Promise<string> {
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  const found = connections.items.find((c) => c.name === name);
  expect(found, `the Connection ${name}`).toBeDefined();
  return found?.id ?? "";
}

/** The paths of the requests a fake server recorded. */
async function fakePaths(base: string): Promise<string[]> {
  const res = await fetch(`${base}/requests`);
  expect(res.ok).toBe(true);
  const all: unknown = await res.json();
  return Array.isArray(all)
    ? all.flatMap((r: unknown) =>
        typeof r === "object" && r !== null && "path" in r && typeof r.path === "string"
          ? [r.path]
          : [],
      )
    : [];
}

/** The Alert Group of the Alert with the label cluster. */
async function groupOf(admin: Api, cluster: string): Promise<{ id: string; number: number }> {
  let group: { id: string; number: number } | undefined;
  await expect
    .poll(async () => {
      const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
        "GET",
        `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
      );
      group = page.items[0]?.alert_group;
      return group !== undefined;
    })
    .toBe(true);
  return group ?? { id: "", number: 0 };
}

async function fire(
  admin: Api,
  cluster: string,
  alertname: string,
): Promise<{ id: string; number: number }> {
  await fam("PUT", `/groups/${cluster}`, { receiver: "s069", route: "{}", labels: { alertname } });
  await fam("PUT", `/groups/${cluster}/alerts/a`, { labels: { team: TEAM, cluster } });
  await notify(integrationId, cluster, { reason: "first notification" });
  const group = await groupOf(admin, cluster);
  await expect
    .poll(
      async () => {
        const d = await admin.call<{ items: { state: string }[] }>(
          "GET",
          `/api/v1/alert-groups/${group.id}/deliveries`,
        );
        return d.items.filter((x) => x.state === "delivered").length;
      },
      { timeout: 30_000 },
    )
    .toBe(5);
  groups.push(group);
  return group;
}

/** The direct messages the bot sent to the Mattermost person on the first fake server. */
async function dmsTo(person: string, bot: string): Promise<string[]> {
  const res = await fetch(`${FMM}/dms`);
  expect(res.ok).toBe(true);
  const all: unknown = await res.json();
  return Array.isArray(all)
    ? all.flatMap((d: unknown) =>
        typeof d === "object" &&
        d !== null &&
        "user_id" in d &&
        d.user_id === person &&
        "bot_id" in d &&
        d.bot_id === bot &&
        "message" in d &&
        typeof d.message === "string"
          ? [d.message]
          : [],
      )
    : [];
}

/** The code of the first direct message of the bot to the person after the before ones. */
async function codeFrom(person: string, bot: string, before: number): Promise<string> {
  let code = "";
  await expect
    .poll(async () => {
      const list = await dmsTo(person, bot);
      code =
        list.length > before ? (/\b([2-9A-HJKMNP-Z]{8})\b/.exec(list.at(-1) ?? "")?.[1] ?? "") : "";
      return code;
    })
    .not.toBe("");
  return code;
}

/** Shoots the page at 1280 px as <name>-desktop and at 360 px as <name>-360, checking the width on the phone. */
async function both(page: Page, name: string, list: boolean): Promise<void> {
  await shot(page, `${name}-desktop`);
  await page.setViewportSize({ width: 360, height: 740 });
  if (list) {
    await expectNoHorizontalScroll(page);
  }
  await shot(page, `${name}-360`);
  await page.setViewportSize({ width: 1280, height: 800 });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    integrationId = await fakeIntegration("s069");
    const mmA = await connectionId(admin, "Dev Mattermost");
    const tgA = await connectionId(admin, "Dev Telegram");
    const mmB = (
      await admin.call<Named>("POST", "/api/v1/connections", {
        type: "mattermost",
        name: "mm-b",
        server_url: origin(FMM),
        bot_token: MM_SECOND_TOKEN,
        proxy: { enabled: false },
        limiter: { limit: 50, per_seconds: 1 },
      })
    ).id;
    const mm2 = (
      await admin.call<Named>("POST", "/api/v1/connections", {
        type: "mattermost",
        name: "mm-2",
        server_url: origin(FMM2),
        bot_token: MM_TOKEN,
        proxy: { enabled: false },
        limiter: { limit: 50, per_seconds: 1 },
      })
    ).id;
    const tgB = (
      await admin.call<Named>("POST", "/api/v1/connections", {
        type: "telegram",
        name: "tg-b",
        bot_token: TG_SECOND_TOKEN,
        bot_api_base_url: origin(FTG),
        update_mode: "long_polling",
        proxy: { enabled: false },
        limiter: { limit: 30, per_seconds: 1 },
      })
    ).id;
    Object.assign(ids, { mmA, mmB, mm2, tgA, tgB });
    await expect
      .poll(async () => (await fakePaths(FTG)).includes(`/bot${TG_SECOND_TOKEN}/getUpdates`))
      .toBe(true);
    const mm = async (name: string, connection: string) =>
      (
        await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
          type: "mattermost",
          name,
          connection_id: connection,
          team_id: "team-dev",
          channel_id: "ch-alerts",
          mentions: MENTIONS,
          limiter: { limit: 10, per_seconds: 1 },
        })
      ).destination.id;
    const tg = async (name: string, connection: string) =>
      (
        await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
          type: "telegram",
          name,
          connection_id: connection,
          channel_id: "@muster_alerts",
          mentions: MENTIONS,
          limiter: { limit: 20, per_seconds: 60 },
        })
      ).destination.id;
    ids["mm-a-alerts"] = await mm("mm-a-alerts", mmA);
    ids["mm-b-alerts"] = await mm("mm-b-alerts", mmB);
    ids["mm-2-alerts"] = await mm("mm-2-alerts", mm2);
    ids["tg-a-alerts"] = await tg("tg-a-alerts", tgA);
    ids["tg-b-alerts"] = await tg("tg-b-alerts", tgB);
    ids["s069-hook"] = (
      await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
        type: "webhook",
        name: "s069-hook",
        mode: "events",
        events: { url: "http://127.0.0.1:18093/hook/s069", headers: [] },
        proxy: { enabled: false },
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      })
    ).destination.id;
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    routeId = (
      await admin.call<{ id: string }>("POST", "/api/v1/routes", {
        name: ROUTE,
        matchers: [{ label: "team", op: "=", value: TEAM }],
        urgent: false,
        group_key: ["alertname", "cluster"],
        destination_ids: DESTINATIONS.map((d) => ids[d]),
        policy: profiles.items.find((p) => p.id === "on_call")?.policy,
      })
    ).id;
    await fire(admin, "s069-1", "DiskFull");
    await fire(admin, "s069-2", "HostDown");

    // A Responder linked through the second bots: Mattermost through mm-b, Telegram through tg-b.
    const created = await admin.createUser(LOGIN);
    userId = created.id;
    await admin.call("POST", "/api/v1/password-setups", {
      token: created.token,
      password: PASSWORD,
    });
  } finally {
    await admin.dispose();
  }
  const responder = await Api.signIn(LOGIN, PASSWORD);
  try {
    const before = (await dmsTo("u-dana", SECOND_BOT)).length;
    const started = await responder.call<{ request_id: string }>(
      "POST",
      "/api/v1/me/account-links/mattermost",
      { connection_id: ids.mmB, username: "dana" },
    );
    const code = await codeFrom("u-dana", SECOND_BOT, before);
    await responder.call("POST", "/api/v1/me/account-links/mattermost/confirmation", {
      request_id: started.request_id,
      code,
    });
    const link = await responder.call<{ deep_link: string }>(
      "POST",
      "/api/v1/me/account-links/telegram",
      {
        connection_id: ids.tgB,
      },
    );
    expect(link.deep_link).toMatch(/^https:\/\/t\.me\/muster_dev_b_bot\?start=/);
    const token = new URL(link.deep_link).searchParams.get("start") ?? "";
    const res = await fetch(`${FTG}/private`, {
      method: "POST",
      body: JSON.stringify({
        token: TG_SECOND_TOKEN,
        from: { id: 6901, username: "s069_t" },
        text: `/start ${token}`,
      }),
    });
    expect(res.ok).toBe(true);
    await expect
      .poll(
        async () =>
          (await responder.call<{ items: unknown[] }>("GET", "/api/v1/me/account-links")).items
            .length,
      )
      .toBe(2);
  } finally {
    await responder.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    for (const g of groups) {
      await admin.call("POST", `/api/v1/alert-groups/${g.id}/resolve`, {});
    }
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("the Connections list shows two Connections of each messenger", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/connections");
  await expect(page.getByRole("heading", { name: "Connections", level: 1 })).toBeVisible();
  for (const name of ["Dev Mattermost", "mm-b", "mm-2", "Dev Telegram", "tg-b"]) {
    await expect(page.getByRole("row").filter({ hasText: name }).first()).toBeVisible();
  }
  await both(page, "connections", true);
  expect(csp).toEqual([]);
});

test("the Destinations list shows a Destination of each Connection and the webhook", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/destinations");
  await expect(page.getByRole("heading", { name: "Destinations", level: 1 })).toBeVisible();
  for (const name of DESTINATIONS) {
    await expect(page.getByRole("row").filter({ hasText: name })).toHaveCount(1);
  }
  await both(page, "destinations", true);
  expect(csp).toEqual([]);
});

test("the Telegram Destination of the second bot shows its channel and discussion group", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/destinations/${ids["tg-b-alerts"]}`);
  await expect(page.getByRole("heading", { name: "tg-b-alerts", level: 1 })).toBeVisible();
  await expect(page.getByTestId("destination-type")).toHaveText("Telegram");
  await expect(page.getByTestId("telegram-discussion-group")).toHaveText(
    "Discussion group: Muster alerts Chat (-1001000000002)",
  );
  await expect(page.getByLabel("Connection", { exact: true })).toHaveValue(ids.tgB ?? "");
  await both(page, "destination-tg-b", false);
  expect(csp).toEqual([]);
});

test("the Route lists its six Destinations", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/routes/${routeId}`);
  const section = page.getByTestId("route-destinations");
  await expect(section.getByTestId("route-destination")).toHaveCount(6);
  for (const name of DESTINATIONS) {
    await expect(section.getByTestId("route-destination").filter({ hasText: name })).toHaveCount(1);
  }
  await section.scrollIntoViewIfNeeded();
  await both(page, "route-editor-destinations", false);
  expect(csp).toEqual([]);
});

test("the Alert Group page shows one delivery per messenger Destination", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  const g = groups[0];
  await page.goto(`/alert-groups/${g?.id}`);
  const section = page.getByTestId("alert-group-deliveries");
  for (const name of [...MM_DESTINATIONS, ...TG_DESTINATIONS]) {
    await expect(section.getByRole("link", { name, exact: true })).toBeVisible();
  }
  await expect(section.getByTestId("delivery-state")).toHaveCount(5);
  for (const state of await section.getByTestId("delivery-state").all()) {
    await expect(state).toHaveText(/^Delivered/);
  }
  await both(page, "alert-group-deliveries", true);
  expect(csp).toEqual([]);
});

test("a failed edit on the second server shows under Delivery problem", async ({ page }) => {
  const csp = watchCsp(page);
  const g = groups[1];
  const res = await fetch(`${FMM2}/faults`, {
    method: "POST",
    body: JSON.stringify({
      path: "/api/v4/posts/*/patch",
      status: 400,
      body: '{"message":"bad patch"}',
      times: 1,
    }),
  });
  expect(res.ok).toBe(true);
  const admin = await adminApi();
  try {
    await admin.call("POST", `/api/v1/alert-groups/${g?.id}/acknowledge`);
    await expect
      .poll(
        async () =>
          (
            await admin.call<{ items: { destination: Named; state: string }[] }>(
              "GET",
              `/api/v1/alert-groups/${g?.id}/deliveries`,
            )
          ).items.find((d) => d.destination.name === "mm-2-alerts")?.state,
        { timeout: 30_000 },
      )
      .toBe("not_delivered");
  } finally {
    await admin.dispose();
  }
  await signInAdmin(page);
  await page.goto("/alert-groups?delivery_problem=true");
  await expect(
    page.getByTestId("alert-group-filters").getByLabel("Delivery problem"),
  ).toBeChecked();
  const row = page
    .getByRole("table")
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: `#${g?.number}`, exact: true }) });
  await expect(row.getByTestId("delivery-problem-mark")).toBeVisible();
  await both(page, "delivery-problem-filter", true);
  expect(csp).toEqual([]);
});

test("the profile offers every Connection and lists the links made through the second bots", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signIn(page, LOGIN, PASSWORD);
  await page.goto("/profile");
  await expect(page.getByRole("heading", { name: "Messenger accounts" })).toBeVisible();
  const rows = page.getByTestId("account-link-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.filter({ hasText: "Mattermost · mm-b · @dana" })).toHaveCount(1);
  await expect(rows.filter({ hasText: "Telegram · tg-b · @s069_t" })).toHaveCount(1);

  await page.getByRole("button", { name: "Link Mattermost" }).click();
  const mm = page.getByRole("dialog").getByLabel("Connection");
  for (const name of ["Dev Mattermost", "mm-b", "mm-2"]) {
    await expect(mm.getByRole("option", { name, exact: true })).toHaveCount(1);
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
  await page.getByRole("button", { name: "Link Telegram" }).click();
  const tg = page.getByRole("dialog").getByLabel("Connection");
  for (const name of ["Dev Telegram", "tg-b"]) {
    await expect(tg.getByRole("option", { name, exact: true })).toHaveCount(1);
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
  await both(page, "profile-messenger-accounts", true);
  expect(csp).toEqual([]);
});

test("the user page lists the Account links through the second bots", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/admin/users/${userId}`);
  await expect(page.getByRole("heading", { name: "Account links" })).toBeVisible();
  const rows = page.getByTestId("account-link-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.filter({ hasText: "Mattermost · mm-b · @dana" })).toHaveCount(1);
  await expect(rows.filter({ hasText: "Telegram · tg-b · @s069_t" })).toHaveCount(1);
  await page.getByRole("heading", { name: "Account links" }).scrollIntoViewIfNeeded();
  await both(page, "admin-user-account-links", true);
  expect(csp).toEqual([]);
});
