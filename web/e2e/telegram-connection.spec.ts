// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Telegram Connection pages against `muster dev` and its fake Telegram server: Verification steps 1 to 4 of their
// story. The Admin creates "tg" on the fake through http, which shows the warning about the token in clear text;
// checks it in three steps with the bot, a webhook that is set and the warning it gets; checks an unsaved address,
// which gets only the dry probe while the fake records no request with the token there; and checks an address that
// refuses connections, whose message does not carry the token. The update mode explains each mode. An Admin reading
// Russian sees the same at phone width. No page scrolls sideways at 360 px; no Content Security Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

/** The fake Telegram server of `muster dev` and its control API. */
const FTG = "http://127.0.0.1:18081";
const TOKEN = "777010:ui-token";
const HINT =
  "A self-hosted Bot API server keeps the bot on that server only. Do not call api.telegram.org with this token from anywhere else: even getMe moves the bot back to Telegram's cloud, and presses and comments start to go missing without an error.";
const HINT_RU =
  "Собственный сервер Bot API держит бота только на этом сервере. Не обращайтесь к api.telegram.org с этим токеном ни из какого другого места: даже getMe возвращает бота в облако Telegram, и нажатия кнопок и комментарии начинают пропадать без всякой ошибки.";
const HTTP_WARNING =
  "The bot token travels in clear text over http. Use https unless this path is a private network or a tunnel.";
const HTTP_WARNING_RU =
  "По http токен бота передаётся открытым текстом. Используйте https, если только этот путь не проходит по частной сети или туннелю.";
const WEBHOOK_SET = "A webhook is set: long polling fails with 409 while it stays.";
const SKIPPED = "Skipped: save the address first";

interface Named {
  id: string;
  name: string;
}

interface Recorded {
  path: string;
  query: string;
  headers: Record<string, string[]>;
  body: string;
}

let connectionId = "";

/** Deletes the Connection "tg", whatever an earlier run left. */
async function removeTg(admin: Api): Promise<void> {
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === "tg")) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

/** Calls the fake's Bot API with the token of "tg", as another client of the bot would. */
async function botApi(method: string, query = ""): Promise<void> {
  const res = await fetch(`${FTG}/bot${TOKEN}/${method}${query}`, { method: "POST" });
  expect(res.ok, `${method}: ${res.status}`).toBe(true);
}

async function recorded(): Promise<Recorded[]> {
  const res = await fetch(`${FTG}/_fake/requests`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of requests
  return (await res.json()) as Recorded[];
}

function steps(page: Page) {
  return page.getByTestId("connection-check-step");
}

function nav(page: Page, name: string, label = "Main") {
  return page.getByRole("navigation", { name: label }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await removeTg(admin);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  await fetch(`${FTG}/bot${TOKEN}/deleteWebhook`, { method: "POST" });
  const admin = await adminApi();
  try {
    await removeTg(admin);
  } finally {
    await admin.dispose();
  }
});

test("creates a Telegram Connection and checks it step by step, an unsaved address with the dry probe only", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // The list names both messengers.
  await nav(page, "Connections").click();
  await expect(page.getByText("How Muster reaches messengers:", { exact: false })).toHaveText(
    "How Muster reaches messengers: a bot account on a Mattermost server, or a Telegram bot.",
  );
  const demo = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "Dev Telegram", exact: true }) });
  await expect(demo).toContainText("Telegram");

  // 1. "Create connection" → Telegram → the default base URL with its hint → name, token, an http base URL → "Save" →
  // the warning about the token in clear text.
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  const baseUrl = page.getByRole("textbox", { name: "Bot API base URL" });
  await expect(baseUrl).toHaveValue("https://api.telegram.org");
  await expect(page.getByTestId("telegram-base-url-hint")).toHaveText(HINT);
  await expect(page.getByTestId("telegram-http-warning")).toHaveCount(0);
  await expect(page.getByLabel("Update mode")).toHaveValue("long_polling");
  await expect(page.getByTestId("telegram-update-mode-hint")).toContainText(
    "Muster needs no public address",
  );
  await page.getByLabel("Update mode").selectOption("webhook");
  await expect(page.getByTestId("telegram-update-mode-hint")).toContainText(
    "Saving in this mode sets the webhook",
  );
  await page.getByLabel("Update mode").selectOption("long_polling");
  await page.getByRole("textbox", { name: "Name" }).fill("tg");
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Bot token").fill(TOKEN);
  await baseUrl.fill(FTG);
  await expect(page.getByLabel("Requests")).toHaveValue("15");
  await shot(page, "telegram-connection-new");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  connectionId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "tg", level: 1 })).toBeVisible();
  await expect(page.getByTestId("telegram-http-warning")).toHaveText(HTTP_WARNING);
  await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(/^Set, changed /);
  await expect(page.locator('input[type="password"]')).toHaveCount(0);
  await expect(page.getByText(TOKEN)).toHaveCount(0);
  await expect(page.getByTestId("connection-callback-url")).toHaveCount(0);

  // 2. "Check connection" → the three steps, each "ok", "direct" and a latency, with the bot.
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Connected as @muster_dev_bot",
  );
  await expect(steps(page)).toHaveText([
    /^Dry probe without the token: ok · \d+ ms · direct$/,
    /^getMe: ok · \d+ ms · direct · Bot: @muster_dev_bot$/,
    /^getWebhookInfo: ok · \d+ ms · direct · \d+ pending updates?$/,
  ]);
  await expect(page.getByTestId("connection-check-warning")).toHaveCount(0);
  await expect(page.getByTestId("connection-bot")).toHaveText("Bot: @muster_dev_bot");
  await shot(page, "telegram-connection-check");
  // A webhook that another client set: getWebhookInfo names its host, and long polling gets the warning.
  await botApi("setWebhook", `?url=${encodeURIComponent("https://hooks.example.org/tg")}`);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-warning")).toHaveText(WEBHOOK_SET);
  await expect(steps(page).nth(2)).toContainText("A webhook is set at hooks.example.org.");
  await shot(page, "telegram-connection-webhook-set");
  await botApi("deleteWebhook");
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Connected as @muster_dev_bot",
  );
  await expect(page.getByTestId("connection-check-warning")).toHaveCount(0);

  // 3. An unsaved base URL with a wrong path prefix → only the dry probe runs there, the other steps are skipped, and
  // the fake records no request with the token under that prefix.
  await baseUrl.fill(`${FTG}/other/`);
  await expect(page.getByTestId("connection-check-unsaved")).toHaveText(
    "The address is not saved: the check makes only the dry probe against it, without the token.",
  );
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check failed.");
  await expect(steps(page)).toHaveText([
    /^Dry probe without the token: failed \d+ ms · directWrong path prefix: the server answered 404\.$/,
    `getMe: ${SKIPPED}`,
    `getWebhookInfo: ${SKIPPED}`,
  ]);
  await shot(page, "telegram-connection-unsaved");
  const underOther = (await recorded()).filter((r) => r.path.startsWith("/other/"));
  expect(underOther.length).toBeGreaterThan(0);
  expect(underOther.filter((r) => /777010|ui-token/.test(JSON.stringify(r)))).toEqual([]);

  // 4. A base URL that refuses connections → "Save", which needs the token again → "Check connection" → the dry
  // probe's message without the token, and the token's steps skipped.
  await baseUrl.fill("http://127.0.0.1:1");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByText(
      "Enter the bot token again: the stored token is sent only to the server it was entered for.",
    ),
  ).toBeVisible();
  await expect(page.getByLabel("Bot token")).toBeFocused();
  await page.getByLabel("Bot token").fill(TOKEN);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("connection-status")).toHaveText("Saved.");
  await expect(baseUrl).toHaveValue("http://127.0.0.1:1");
  await expect(page.getByTestId("connection-check-unsaved")).toHaveCount(0);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check failed.");
  await expect(steps(page).first()).toContainText("Dry probe without the token: failed");
  await expect(steps(page).nth(1)).toHaveText("getMe: Skipped: an earlier step failed");
  const probe = (await steps(page).first().textContent()) ?? "";
  expect(probe).not.toContain("ui-token");
  expect(probe).not.toContain("777010");
  await expect(page.getByText(/ui-token/)).toHaveCount(0);
  await shot(page, "telegram-connection-refused");

  // Back to the fake for the Russian Admin.
  await baseUrl.fill(FTG);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByLabel("Bot token").fill(TOKEN);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("connection-status")).toHaveText("Saved.");

  // At phone width the page scrolls vertically only.
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto(`/connections/${connectionId}`);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(steps(page)).toHaveCount(3);
  await expectNoHorizontalScroll(page);
  await shot(page, "telegram-connection-360");
  expect(csp).toEqual([]);
});

test("an Admin reading Russian sees the hint, the warning and the steps at phone width", async ({
  browser,
}) => {
  // A second Admin of its own, deleted at the end: the specs after this one expect admin@example.org to be the last
  // active Admin.
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("tg-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "tg-admin-ru-1" });
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

async function russianAdmin(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "tg-admin-ru", "tg-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/connections");
  await expect(page.getByRole("heading", { name: "Подключения", level: 1 })).toBeVisible();
  await expect(
    page.getByText(
      "Как Muster связывается с мессенджерами: учётная запись бота на сервере Mattermost или бот Telegram.",
    ),
  ).toBeVisible();
  await page.getByRole("link", { name: "tg", exact: true }).click();
  await expect(page.getByTestId("telegram-base-url-hint")).toHaveText(HINT_RU);
  await expect(page.getByTestId("telegram-http-warning")).toHaveText(HTTP_WARNING_RU);
  await expect(page.getByLabel("Режим получения обновлений")).toHaveValue("long_polling");
  await page.getByRole("textbox", { name: "Базовый URL Bot API" }).fill(`${FTG}/other/`);
  await page.getByRole("button", { name: "Проверить подключение" }).click();
  await expect(steps(page)).toHaveText([
    /^Пробный запрос без токена: ошибка \d+ мс · напрямуюWrong path prefix: the server answered 404\.$/,
    "getMe: Пропущен: сначала сохраните адрес",
    "getWebhookInfo: Пропущен: сначала сохраните адрес",
  ]);
  await expectNoHorizontalScroll(page);
  for (const id of ["telegram-base-url-hint", "telegram-http-warning"]) {
    const fits = await page.getByTestId(id).evaluate((e) => e.scrollWidth <= e.clientWidth);
    expect(fits, id).toBe(true);
  }
  await shot(page, "telegram-connection-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}
