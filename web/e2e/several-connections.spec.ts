// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Several Connections of one messenger against `muster dev`: the pages of the Verification of their stories with two
// Mattermost Connections on the first fake Mattermost server ("Example Mattermost" and "mm-b", its second bot), one on
// the second server ("mm-2"), two Telegram Connections ("Example Telegram" and "tg-b", the second bot, both checked so
// that their @bot shows), a Destination of each in one channel and an outgoing webhook, all on one Route. The
// Connections list with the Mattermost server or the Telegram @bot; the Destinations list with "Connection · channel";
// a Telegram Destination of the second bot; the Route's Destinations with their full labels, the picker's options and
// one duplicate-chat warning per chat; the delivery section and the Timeline of an Alert Group with the full label as
// tooltip; the "Delivery problem" filter; the profile's Messenger accounts with the Telegram row, "Relink Telegram"
// and the Mattermost hint; and the user page's Account links — each at 1280 and 360 px wide, in English and Russian.
// With MUSTER_E2E_SCREENSHOTS set, each page is saved as <name>-desktop.png, <name>-360.png, <name>-ru-desktop.png and
// <name>-ru-360.png. Everything the spec creates is removed at the end, and every language it switches is restored, so
// the specs after it see only their own configuration.

import { type Page, expect, test } from "@playwright/test";

import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
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
const CHAT_ID = "-1001000000001";
const HOOK_HOST = "127.0.0.1:18093";
type Lang = "en" | "ru";

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

/**
 * Shoots the page at 1280 px as <name>-desktop and at 360 px as <name>-360 (with "-ru" before the width in Russian),
 * checking on the phone that the page does not scroll sideways.
 */
async function both(page: Page, name: string, lang: Lang): Promise<void> {
  const prefix = lang === "ru" ? `${name}-ru` : name;
  await shot(page, `${prefix}-desktop`);
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, `${prefix}-360`);
  await page.setViewportSize({ width: 1280, height: 800 });
}

/** The language of the signed-in user, set through the profile as people do. */
async function useLanguage(page: Page, lang: Lang): Promise<void> {
  if (lang === "en") {
    return;
  }
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
}

/** Clears the language a Russian pass chose, back to the default the other specs expect. */
async function restoreLanguage(login: string, password: string): Promise<void> {
  const api = await Api.signIn(login, password);
  try {
    const me = await api.call<{ user: { name: string } }>("GET", "/api/v1/me");
    await api.call("PUT", "/api/v1/me", { name: me.user.name, language: null });
  } finally {
    await api.dispose();
  }
}

/** Runs a page's checks in English, then in Russian with the user's language restored afterwards. */
function inBothLanguages(
  title: string,
  user: { login: string; password: string },
  body: (page: Page, lang: Lang) => Promise<void>,
): void {
  for (const lang of ["en", "ru"] as const) {
    test(`${title} (${lang})`, async ({ page }) => {
      const csp = watchCsp(page);
      if (user.login === ADMIN_LOGIN) {
        await signInAdmin(page);
      } else {
        await signIn(page, user.login, user.password);
      }
      try {
        await useLanguage(page, lang);
        await body(page, lang);
      } finally {
        if (lang === "ru") {
          await restoreLanguage(user.login, user.password);
        }
      }
      expect(csp).toEqual([]);
    });
  }
}

const ADMIN = { login: ADMIN_LOGIN, password: ADMIN_PASSWORD };
const RESPONDER = { login: LOGIN, password: PASSWORD };

/** The full label of a Destination in a language: "<name> · <messenger> · via <Connection> · <channel>". */
function label(lang: Lang, name: string, messenger: string, connection: string, place: string) {
  return [name, messenger, `${lang === "ru" ? "через" : "via"} ${connection}`, place].join(" · ");
}

const LABELS = (lang: Lang): Record<string, string> => ({
  "mm-a-alerts": label(lang, "mm-a-alerts", "Mattermost", "Example Mattermost", "#alerts"),
  "mm-b-alerts": label(lang, "mm-b-alerts", "Mattermost", "mm-b", "#alerts"),
  "mm-2-alerts": label(lang, "mm-2-alerts", "Mattermost", "mm-2", "#alerts"),
  "tg-a-alerts": label(lang, "tg-a-alerts", "Telegram", "Example Telegram", "Muster alerts"),
  "tg-b-alerts": label(lang, "tg-b-alerts", "Telegram", "tg-b", "Muster alerts"),
  "s069-hook": [
    "s069-hook",
    lang === "ru" ? "Исходящий вебхук" : "Outgoing webhook",
    HOOK_HOST,
  ].join(" · "),
});

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    integrationId = await fakeIntegration("s069");
    const mmA = await connectionId(admin, "Example Mattermost");
    const tgA = await connectionId(admin, "Example Telegram");
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
    // A Connection check finds each Telegram bot's username, which the Connections list shows.
    for (const [id, bot] of [
      [tgA, "muster_dev_bot"],
      [tgB, "muster_dev_b_bot"],
    ]) {
      const checked = await admin.call<{ ok: boolean }>(
        "POST",
        `/api/v1/connections/${id}/checks`,
        {},
      );
      expect(checked.ok).toBe(true);
      const stored = await admin.call<{ bot_username?: string | null }>(
        "GET",
        `/api/v1/connections/${id}`,
      );
      expect(stored.bot_username).toBe(bot);
    }
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
    // The Destination check found the numeric chat id of @muster_alerts, which makes tg-a and tg-b one chat.
    const telegram = await admin.call<{
      items: { type: string; channel_chat_id?: string | null }[];
    }>("GET", "/api/v1/destinations?limit=500");
    expect([
      ...new Set(telegram.items.filter((d) => d.type === "telegram").map((d) => d.channel_chat_id)),
    ]).toEqual([CHAT_ID]);
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
        policy: profiles.items.find((p) => p.id === "paging")?.policy,
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

inBothLanguages(
  "the Connections list shows each Mattermost server and Telegram bot",
  ADMIN,
  async (page, lang) => {
    await page.goto("/connections");
    await expect(
      page.getByRole("heading", { name: lang === "ru" ? "Подключения" : "Connections", level: 1 }),
    ).toBeVisible();
    const account = (name: string) =>
      page
        .getByRole("row")
        .filter({ has: page.getByRole("link", { name, exact: true }) })
        .getByTestId("connection-account");
    await expect(account("Example Mattermost")).toHaveText("127.0.0.1:18065");
    await expect(account("mm-b")).toHaveText("127.0.0.1:18065");
    await expect(account("mm-2")).toHaveText("127.0.0.1:18066");
    await expect(account("Example Telegram")).toHaveText("@muster_dev_bot");
    await expect(account("tg-b")).toHaveText("@muster_dev_b_bot");
    await both(page, "connections", lang);
  },
);

inBothLanguages(
  "the Destinations list shows the Connection and channel of each Destination",
  ADMIN,
  async (page, lang) => {
    await page.goto("/destinations");
    await expect(
      page.getByRole("heading", {
        name: lang === "ru" ? "Места доставки" : "Destinations",
        level: 1,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("columnheader", {
        name: lang === "ru" ? "Подключение · канал" : "Connection · channel",
      }),
    ).toBeVisible();
    const place = (name: string) =>
      page
        .getByRole("row")
        .filter({ has: page.getByRole("link", { name, exact: true }) })
        .getByTestId("destination-place");
    await expect(place("mm-a-alerts")).toHaveText("Example Mattermost · #alerts");
    await expect(place("mm-b-alerts")).toHaveText("mm-b · #alerts");
    await expect(place("mm-2-alerts")).toHaveText("mm-2 · #alerts");
    await expect(place("tg-a-alerts")).toHaveText("Example Telegram · Muster alerts");
    await expect(place("tg-b-alerts")).toHaveText("tg-b · Muster alerts");
    await expect(place("s069-hook")).toHaveText(HOOK_HOST);
    await both(page, "destinations", lang);
  },
);

inBothLanguages(
  "the Telegram Destination of the second bot shows its channel and discussion group",
  ADMIN,
  async (page, lang) => {
    await page.goto(`/destinations/${ids["tg-b-alerts"]}`);
    await expect(page.getByRole("heading", { name: "tg-b-alerts", level: 1 })).toBeVisible();
    await expect(page.getByTestId("destination-type")).toHaveText("Telegram");
    await expect(page.getByTestId("telegram-discussion-group")).toContainText(
      "Muster alerts Chat (-1001000000002)",
    );
    await expect(
      page.getByLabel(lang === "ru" ? "Подключение" : "Connection", { exact: true }),
    ).toHaveValue(ids.tgB ?? "");
    await both(page, "destination-tg-b", lang);
  },
);

inBothLanguages(
  "the Route labels its Destinations and warns about the chats two of them share",
  ADMIN,
  async (page, lang) => {
    const labels = LABELS(lang);
    await page.goto(`/routes/${routeId}`);
    const section = page.getByTestId("route-destinations");
    await expect(section.getByTestId("route-destination")).toHaveCount(6);
    for (const name of DESTINATIONS) {
      const row = section.getByTestId("route-destination").filter({ hasText: labels[name] });
      await expect(row).toHaveCount(1);
      await expect(row.getByTestId("route-destination-label")).toHaveText(labels[name] ?? "");
      await expect(row.getByTestId("destination-health")).toHaveText(
        lang === "ru" ? "Работает" : "Healthy",
      );
    }
    const warnings = section.getByTestId("duplicate-chat-warning");
    await expect(warnings).toHaveCount(2);
    const and = lang === "ru" ? "и" : "and";
    const sentence = (a: string, b: string) =>
      lang === "ru"
        ? `Два места доставки отправляют сообщения в один чат: ${a} ${and} ${b}. Каждое сообщение появится в нём дважды.`
        : `Two Destinations deliver to the same chat: ${a} ${and} ${b}. Each message will appear there twice.`;
    await expect(warnings.nth(0)).toHaveText(sentence("mm-a-alerts", "mm-b-alerts"));
    await expect(warnings.nth(1)).toHaveText(sentence("tg-a-alerts", "tg-b-alerts"));
    await expect(warnings.filter({ hasText: "mm-2-alerts" })).toHaveCount(0);

    // The picker offers the Destinations the Route lacks by the same label: remove tg-b-alerts, see it offered.
    await section
      .getByRole("button", { name: `${lang === "ru" ? "Убрать" : "Remove"} tg-b-alerts` })
      .click();
    await expect(warnings).toHaveCount(1);
    const picker = section.getByLabel(
      lang === "ru" ? "Добавить место доставки" : "Add a Destination",
    );
    await expect(picker.locator("option", { hasText: labels["tg-b-alerts"] })).toHaveCount(1);
    await expect(picker.locator("option", { hasText: labels["tg-b-alerts"] })).toHaveText(
      labels["tg-b-alerts"] ?? "",
    );
    await picker.selectOption(ids["tg-b-alerts"] ?? "");
    await expect(warnings).toHaveCount(2);
    await expect(picker.locator("option")).toHaveCount(1);

    await section.scrollIntoViewIfNeeded();
    await both(page, "route-editor-destinations", lang);

    // Saving is never blocked by the warning.
    const save = page.getByRole("button", {
      name: lang === "ru" ? "Сохранить" : "Save",
      exact: true,
    });
    await expect(save).toBeEnabled();
    const saved = page.waitForResponse(
      (r) => r.url().endsWith(`/api/v1/routes/${routeId}`) && r.request().method() === "PUT",
    );
    await save.click();
    expect((await saved).status()).toBe(200);
    await expect(page).toHaveURL(/\/routes$/);
  },
);

inBothLanguages(
  "the Alert Group page names each Destination with its full label as tooltip",
  ADMIN,
  async (page, lang) => {
    const labels = LABELS(lang);
    const g = groups[0];
    await page.goto(`/alert-groups/${g?.id}`);
    const section = page.getByTestId("alert-group-deliveries");
    for (const name of [...MM_DESTINATIONS, ...TG_DESTINATIONS]) {
      const link = section.getByRole("link", { name, exact: true });
      await expect(link).toBeVisible();
      await expect(link).toHaveAttribute("title", labels[name] ?? "");
      await expect(link).toHaveAccessibleDescription(labels[name] ?? "");
    }
    await expect(section.getByTestId("delivery-state")).toHaveCount(5);
    for (const state of await section.getByTestId("delivery-state").all()) {
      await expect(state).toHaveText(lang === "ru" ? /^Доставлено/ : /^Delivered/);
    }
    await section.scrollIntoViewIfNeeded();
    await both(page, "alert-group-deliveries", lang);

    const timeline = page.getByTestId("timeline");
    const entry = timeline
      .getByTestId("timeline-entry")
      .filter({ hasText: "tg-b-alerts" })
      .first()
      .getByTestId("timeline-text");
    await expect(entry).toHaveAttribute("title", labels["tg-b-alerts"] ?? "");
    await expect(entry).toHaveAccessibleDescription(labels["tg-b-alerts"] ?? "");
    await timeline.scrollIntoViewIfNeeded();
    await both(page, "alert-group-timeline", lang);
  },
);

test("a failed edit on the second server goes Not delivered", async () => {
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
});

inBothLanguages("the failed edit shows under Delivery problem", ADMIN, async (page, lang) => {
  const g = groups[1];
  await page.goto("/alert-groups?delivery_problem=true");
  await expect(
    page
      .getByTestId("alert-group-filters")
      .getByLabel(lang === "ru" ? "Проблема доставки" : "Delivery problem"),
  ).toBeChecked();
  const row = page
    .getByRole("table")
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: `#${g?.number}`, exact: true }) });
  await expect(row.getByTestId("delivery-problem-mark")).toBeVisible();
  await both(page, "delivery-problem-filter", lang);
});

inBothLanguages(
  "the profile shows the Telegram link for all Telegram bots and Mattermost links per Connection",
  RESPONDER,
  async (page, lang) => {
    const ru = lang === "ru";
    await page.goto("/profile");
    await expect(
      page.getByRole("heading", { name: ru ? "Аккаунты в мессенджерах" : "Messenger accounts" }),
    ).toBeVisible();
    const rows = page.getByTestId("account-link-row");
    await expect(rows).toHaveCount(2);
    await expect(rows.filter({ hasText: "Mattermost · mm-b · @dana" })).toHaveCount(1);
    const telegram = rows.filter({ hasText: "Telegram · @s069_t" });
    await expect(telegram.getByTestId("account-link-summary")).toHaveText(
      `Telegram · @s069_t · ${ru ? "для всех Telegram-ботов" : "for all Telegram bots"}`,
    );
    await expect(telegram.getByTestId("account-link-via")).toContainText(
      ru ? "привязан через tg-b" : "linked via tg-b",
    );
    await expect(page.getByTestId("mattermost-links-hint")).toHaveText(
      ru
        ? "Привязки Mattermost — для каждого подключения отдельно: двум ботам на одном сервере нужны две привязки."
        : "Mattermost links are per Connection: two bots on one server need two links.",
    );

    await page
      .getByRole("button", { name: ru ? "Привязать Mattermost" : "Link Mattermost" })
      .click();
    const mm = page.getByRole("dialog").getByLabel(ru ? "Подключение" : "Connection");
    for (const name of ["Example Mattermost", "mm-b", "mm-2"]) {
      await expect(mm.getByRole("option", { name, exact: true })).toHaveCount(1);
    }
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toBeHidden();
    const relink = ru ? "Перепривязать Telegram" : "Relink Telegram";
    await expect(
      page.getByRole("button", { name: ru ? "Привязать Telegram" : "Link Telegram", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: relink }).click();
    await expect(page.getByRole("dialog").getByRole("heading", { name: relink })).toBeVisible();
    const tg = page.getByRole("dialog").getByLabel(ru ? "Подключение" : "Connection");
    for (const name of ["Example Telegram", "tg-b"]) {
      await expect(tg.getByRole("option", { name, exact: true })).toHaveCount(1);
    }
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toBeHidden();
    await page.getByTestId("mattermost-links-hint").scrollIntoViewIfNeeded();
    await both(page, "profile-messenger-accounts", lang);
  },
);

inBothLanguages("the user page shows the same Telegram row", ADMIN, async (page, lang) => {
  const ru = lang === "ru";
  await page.goto(`/admin/users/${userId}`);
  await expect(
    page.getByRole("heading", { name: ru ? "Привязанные аккаунты" : "Account links" }),
  ).toBeVisible();
  const rows = page.getByTestId("account-link-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.filter({ hasText: "Mattermost · mm-b · @dana" })).toHaveCount(1);
  const telegram = rows.filter({ hasText: "Telegram · @s069_t" });
  await expect(telegram.getByTestId("account-link-summary")).toHaveText(
    `Telegram · @s069_t · ${ru ? "для всех Telegram-ботов" : "for all Telegram bots"}`,
  );
  await expect(telegram.getByTestId("account-link-via")).toContainText(
    ru ? "привязан через tg-b" : "linked via tg-b",
  );
  await page
    .getByRole("heading", { name: ru ? "Привязанные аккаунты" : "Account links" })
    .scrollIntoViewIfNeeded();
  await both(page, "admin-user-account-links", lang);
});
