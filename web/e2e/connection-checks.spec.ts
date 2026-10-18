// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of the form's values before they are saved, against `muster dev` and its fakes: the Verification
// "In the browser" of its story. The create form of each type checks its draft — Mattermost on the second fake server
// with the second bot's token, Telegram and the Bridge with their second bots — and a draft without a token gets the
// dry probe alone, its other steps asking for the token; nothing is saved: the Connections and the Audit log stay as
// they were. On the pages of "Dev Mattermost", "Dev Telegram" and "Dev Bridge" the form's changes are checked as a
// draft, which never changes the bot the page names: another server URL gets only the ping, without a token; another
// path prefix fails the dry probe and the fake records no request with a token under it; only the proxy changed gets
// the dry probe through the proxy and asks for the token again, which, typed, runs every step; a base URL that is not a
// bridge fails with its text, and the stored token reaches no fake but the saved address. An Admin reading Russian sees
// the same at phone width. No page scrolls sideways at 360 px, on the create and the edit page, in English and Russian;
// no Content Security Policy violation.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  BRIDGE_SECOND_TOKEN,
  BRIDGE_TOKEN,
  BRIDGE_URL,
  FMM,
  FMM2,
  FTG,
  MM_SECOND_TOKEN,
  MM_TOKEN,
  TG_SECOND_TOKEN,
  adminApi,
  connectionNamed,
  expectNoHorizontalScroll,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

/** The first and the second fake Mattermost server and the fake Telegram server of `muster dev`. */
const MM_URL = "http://127.0.0.1:18065";
const MM2_URL = "http://127.0.0.1:18066";
const TG_URL = "http://127.0.0.1:18081";
/** The development HTTP proxy of `muster dev` and its control API. */
const HTTP_PROXY = "127.0.0.1:18091";
const FPROXY = `http://${HTTP_PROXY}/_fake`;
/** The published token of the demo Connection "Dev Telegram". */
const TG_TOKEN = "123456:dev-telegram-token";

const DRAFT = "The check uses the values in the form and saves nothing.";
const DRAFT_RU = "Проверка берёт значения из формы и ничего не сохраняет.";
const NO_TOKEN = "Skipped: type the bot token to check it";
const NO_TOKEN_RU = "Пропущено: введите токен бота, чтобы проверить его";
const RETYPE = "Skipped: type the bot token again to check the new address, or save first";
const RETYPE_RU =
  "Пропущено: введите токен бота заново, чтобы проверить новый адрес, или сохраните изменения";
const EARLIER = "Skipped: an earlier step failed";
const NOT_BRIDGE =
  "The address answers, but not as a bridge: a bridge answers a request without its token with 401 and a problem.";

interface Recorded {
  method: string;
  path: string;
  query: string;
  headers: Record<string, string[]>;
  body: string;
}

/** What a draft check must leave as it was: the Connections, their versions and bots, and the Audit log. */
interface Saved {
  connections: string[];
  versions: Record<string, string>;
  newestAudit: string;
  connectionAudits: number;
}

interface Listed {
  id: string;
  etag: string;
  bot_username?: string | null;
}

async function saved(admin: Api): Promise<Saved> {
  const list = await admin.call<{ items: Listed[] }>("GET", "/api/v1/connections?limit=500");
  const newest = await admin.call<{ items: { id: string }[] }>("GET", "/api/v1/audit-log?limit=1");
  const audits = await admin.call<{ items: unknown[] }>(
    "GET",
    "/api/v1/audit-log?resource_type=connection&limit=500",
  );
  return {
    connections: list.items.map((c) => c.id).toSorted(),
    versions: Object.fromEntries(
      list.items.map((c) => [c.id, `${c.etag} ${c.bot_username ?? ""}`]),
    ),
    newestAudit: newest.items[0]?.id ?? "",
    connectionAudits: audits.items.length,
  };
}

async function recorded(control: string): Promise<Recorded[]> {
  const res = await fetch(`${control}/requests`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of requests
  return (await res.json()) as Recorded[];
}

async function forget(control: string): Promise<void> {
  const res = await fetch(`${control}/requests`, { method: "DELETE" });
  expect(res.status).toBe(204);
}

/** Whether a request carries token as a whole, not as the start of a longer one, such as the second bot's token. */
function carries(r: Recorded, token: string): boolean {
  const whole = new RegExp(`${token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}([^-\\w]|$)`);
  return [r.path, r.query, r.body, ...Object.values(r.headers).flat()].some((s) => whole.test(s));
}

function steps(page: Page) {
  return page.getByTestId("connection-check-step");
}

function summary(page: Page) {
  return page.getByTestId("connection-check-summary");
}

function checkButton(page: Page, name = "Check connection") {
  return page.getByRole("button", { name, exact: true });
}

/** Opens the bot token's input of the form: "Set a value" on a new Connection, "Replace" on a saved one. */
async function typeToken(page: Page, token: string, label = "Bot token"): Promise<void> {
  const field = page.getByTestId("secret-connection-bot-token");
  await field.getByRole("button", { name: /^(Set a value|Replace)$/ }).click();
  await page.getByLabel(label, { exact: true }).fill(token);
}

test.beforeAll(async () => {
  await resetFakeBridge();
});

test("checks drafts of new Connections of every type and saves nothing", async ({ page }) => {
  test.setTimeout(150_000);
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await signInAdmin(page);
    const before = await saved(admin);

    // Mattermost: the address alone gets the dry probe, and the token step asks for the token.
    await page.goto("/connections/new");
    await page.getByRole("radio", { name: "Mattermost" }).check();
    await expect(page.getByTestId("connection-check-draft")).toHaveText(DRAFT);
    await expect(checkButton(page)).toBeDisabled();
    await page.getByRole("textbox", { name: "Server URL" }).fill(MM2_URL);
    await expect(checkButton(page)).toBeEnabled();
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The address answers as a Mattermost server.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · direct$/,
      `Token: ${NO_TOKEN}`,
    ]);
    await shot(page, "connection-checks-new-no-token");
    // The second bot's token: a change of the form hides the earlier result, and the check names the bot.
    await typeToken(page, MM_SECOND_TOKEN);
    await expect(page.getByTestId("connection-check-result")).toHaveCount(0);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("Connected as muster-dev-bot-b");
    await expect(steps(page)).toHaveText([/^Token: ok · \d+ ms · direct$/]);
    await shot(page, "connection-checks-new-mattermost");

    // Telegram: the three steps with the second bot.
    await page.getByRole("radio", { name: "Telegram" }).check();
    await page.getByRole("textbox", { name: "Bot API base URL" }).fill(TG_URL);
    await typeToken(page, TG_SECOND_TOKEN);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("Connected as @muster_dev_b_bot");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · direct$/,
      /^getMe: ok · \d+ ms · direct · Bot: @muster_dev_b_bot$/,
      /^getWebhookInfo: ok · \d+ ms · direct · \d+ pending updates?/,
    ]);
    await shot(page, "connection-checks-new-telegram");

    // Bridge: "Bridge info" and "Event delivery" with the second bot's token.
    await page.getByRole("radio", { name: "Bridge", exact: true }).check();
    await page.getByTestId("bridge-connection-fields").getByLabel("Base URL").fill(BRIDGE_URL);
    await typeToken(page, BRIDGE_SECOND_TOKEN, "Token");
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The check passed.");
    await expect(steps(page)).toHaveText([
      /^Bridge info: ok · \d+ ms · direct$/,
      /^Event delivery: ok · \d+ ms · direct$/,
    ]);
    await shot(page, "connection-checks-new-bridge");
    await expect(page.getByText(BRIDGE_SECOND_TOKEN)).toHaveCount(0);

    // Leaving the form saves nothing: the Connections, their versions and the Audit log are as they were.
    await page.goto("/connections");
    await expect(page.getByRole("link", { name: "Dev Mattermost", exact: true })).toBeVisible();
    expect(await saved(admin)).toEqual(before);

    // At phone width, a Bridge draft without a token: the page scrolls vertically only.
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto("/connections/new");
    await page.getByRole("radio", { name: "Bridge", exact: true }).check();
    await page.getByTestId("bridge-connection-fields").getByLabel("Base URL").fill(BRIDGE_URL);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The address answers as a bridge.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · direct$/,
      `Bridge info: ${NO_TOKEN}`,
      `Event delivery: ${NO_TOKEN}`,
    ]);
    await expectNoHorizontalScroll(page);
    await shot(page, "connection-checks-new-360");
    expect(await saved(admin)).toEqual(before);
    expect(csp).toEqual([]);
  } finally {
    await admin.dispose();
  }
});

test("checks the changes of saved Connections as drafts, the stored token only at the saved address", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await signInAdmin(page);
    const before = await saved(admin);

    // Dev Mattermost with another server URL: only the ping there, without a token.
    const mm = await connectionNamed(admin, "Dev Mattermost");
    await page.goto(`/connections/${mm}`);
    await expect(page.getByTestId("connection-bot")).toHaveText(/^Bot: /);
    const mmBot = (await page.getByTestId("connection-bot").textContent()) ?? "";
    await expect(page.getByTestId("connection-check-draft")).toHaveCount(0);
    await forget(FMM2);
    await page.getByRole("textbox", { name: "Server URL" }).fill(MM2_URL);
    await expect(page.getByTestId("connection-check-draft")).toHaveText(DRAFT);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The address answers as a Mattermost server.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · direct$/,
      `Token: ${RETYPE}`,
    ]);
    await shot(page, "connection-checks-edit-mattermost");
    const pinged = await recorded(FMM2);
    expect(
      pinged
        .filter((r) => r.path === "/api/v4/system/ping")
        .map((r) => ({ method: r.method, auth: r.headers.Authorization ?? [] })),
    ).toEqual([{ method: "GET", auth: [] }]);
    expect(pinged.filter((r) => carries(r, MM_TOKEN)).map((r) => r.path)).toEqual([]);
    // The token typed again goes to the new server URL; the page still names the saved bot.
    await typeToken(page, MM_SECOND_TOKEN);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("Connected as muster-dev-bot-b");
    await expect(page.getByTestId("connection-bot")).toHaveText(mmBot);
    expect((await recorded(FMM2)).filter((r) => carries(r, MM_TOKEN)).map((r) => r.path)).toEqual(
      [],
    );

    // Dev Telegram with another path prefix: the dry probe fails there and no token goes under it.
    const tg = await connectionNamed(admin, "Dev Telegram");
    await page.goto(`/connections/${tg}`);
    const baseUrl = page.getByRole("textbox", { name: "Bot API base URL" });
    await expect(baseUrl).toHaveValue(TG_URL);
    await expect(page.getByTestId("connection-bot")).toHaveText(/^Bot: /);
    const tgBot = (await page.getByTestId("connection-bot").textContent()) ?? "";
    await forget(FTG);
    await baseUrl.fill(`${TG_URL}/other/`);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The check failed.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: failed \d+ ms · directWrong path prefix: the server answered 404\.$/,
      `getMe: ${EARLIER}`,
      `getWebhookInfo: ${EARLIER}`,
    ]);
    await shot(page, "connection-checks-edit-telegram-prefix");
    const underOther = (await recorded(FTG)).filter((r) => r.path.startsWith("/other/"));
    expect(underOther.length).toBeGreaterThan(0);
    expect(underOther.filter((r) => carries(r, TG_TOKEN)).map((r) => r.path)).toEqual([]);

    // Only the proxy changed, no token: the dry probe through the proxy, and the token's steps ask for it again.
    await baseUrl.fill(TG_URL);
    await page.getByLabel("Use a proxy").check();
    await page.getByLabel("Address", { exact: true }).fill(HTTP_PROXY);
    await forget(FPROXY);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The address answers as a Bot API.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · through the proxy$/,
      `getMe: ${RETYPE}`,
      `getWebhookInfo: ${RETYPE}`,
    ]);
    await shot(page, "connection-checks-edit-telegram-proxy");
    const tunneled = await recorded(FPROXY);
    expect(tunneled.length).toBeGreaterThan(0);
    // The token typed again: every step, through the proxy.
    await typeToken(page, TG_SECOND_TOKEN);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("Connected as @muster_dev_b_bot");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: ok · \d+ ms · through the proxy$/,
      /^getMe: ok · \d+ ms · through the proxy · Bot: @muster_dev_b_bot$/,
      /^getWebhookInfo: ok · \d+ ms · through the proxy/,
    ]);
    await expect(page.getByTestId("connection-bot")).toHaveText(tgBot);

    // Dev Bridge pointed at the fake Mattermost server: not a bridge, and its token never goes there.
    const br = await connectionNamed(admin, "Dev Bridge");
    await page.goto(`/connections/${br}`);
    await forget(FMM);
    const fields = page.getByTestId("bridge-connection-fields");
    await fields.getByLabel("Base URL").fill(MM_URL);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The check failed.");
    await expect(steps(page)).toHaveText([
      /^Dry probe without the token: failed \d+ ms · direct/,
      `Bridge info: ${EARLIER}`,
      `Event delivery: ${EARLIER}`,
    ]);
    await expect(steps(page).first().getByTestId("connection-check-step-reason")).toHaveText(
      NOT_BRIDGE,
    );
    await shot(page, "connection-checks-edit-bridge");
    expect(
      (await recorded(FMM)).filter((r) => carries(r, BRIDGE_TOKEN)).map((r) => r.path),
    ).toEqual([]);
    // Without changes, the page checks the saved Connection again.
    await fields.getByLabel("Base URL").fill(BRIDGE_URL);
    await expect(page.getByTestId("connection-check-draft")).toHaveCount(0);
    await checkButton(page).click();
    await expect(summary(page)).toHaveText("The check passed.");

    // At phone width, a draft of Dev Telegram with only the proxy changed: the page scrolls vertically only.
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(`/connections/${tg}`);
    await page.getByLabel("Use a proxy").check();
    await page.getByLabel("Address", { exact: true }).fill(HTTP_PROXY);
    await checkButton(page).click();
    await expect(steps(page).nth(1)).toHaveText(`getMe: ${RETYPE}`);
    await expectNoHorizontalScroll(page);
    await shot(page, "connection-checks-edit-360");

    // Leaving the pages without saving keeps every Connection as it was; the drafts wrote no Audit log entry.
    await page.goto("/connections");
    expect(await saved(admin)).toEqual(before);
    expect(csp).toEqual([]);
  } finally {
    await admin.dispose();
  }
});

test("an Admin reading Russian checks drafts at phone width", async ({ browser }) => {
  // A second Admin of its own, deleted at the end: the specs after this one expect admin@example.org to be the last
  // active Admin.
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("cc-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "cc-admin-ru-1" });
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
  await signIn(page, "cc-admin-ru", "cc-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  // A new Mattermost Connection without a token: the dry probe, and the token step asks for it.
  await page.goto("/connections/new");
  await page.getByRole("radio", { name: "Mattermost" }).check();
  await expect(page.getByTestId("connection-check-draft")).toHaveText(DRAFT_RU);
  await page.getByRole("textbox", { name: "URL сервера" }).fill(MM2_URL);
  await checkButton(page, "Проверить подключение").click();
  await expect(summary(page)).toHaveText("По этому адресу отвечает сервер Mattermost.");
  await expect(steps(page)).toHaveText([
    /^Пробный запрос без токена: успешно · \d+ мс · напрямую$/,
    `Токен: ${NO_TOKEN_RU}`,
  ]);
  await expectNoHorizontalScroll(page);
  await shot(page, "connection-checks-ru-new-360");

  // Dev Mattermost with another server URL: the token step asks for the token again or a save.
  await page.goto("/connections");
  await page.getByRole("link", { name: "Dev Mattermost", exact: true }).click();
  await page.getByRole("textbox", { name: "URL сервера" }).fill(MM2_URL);
  await expect(page.getByTestId("connection-check-draft")).toHaveText(DRAFT_RU);
  await checkButton(page, "Проверить подключение").click();
  await expect(summary(page)).toHaveText("По этому адресу отвечает сервер Mattermost.");
  await expect(steps(page)).toHaveText([
    /^Пробный запрос без токена: успешно · \d+ мс · напрямую$/,
    `Токен: ${RETYPE_RU}`,
  ]);
  await expectNoHorizontalScroll(page);
  for (const id of ["connection-check-summary", "connection-check-result"]) {
    const fits = await page.getByTestId(id).evaluate((e) => e.scrollWidth <= e.clientWidth);
    expect(fits, id).toBe(true);
  }
  await shot(page, "connection-checks-ru-edit-360");
  await page.setViewportSize({ width: 1280, height: 800 });
  await expect(steps(page).nth(1)).toHaveText(`Токен: ${RETYPE_RU}`);
  await shot(page, "connection-checks-ru-edit");
  expect(csp).toEqual([]);
  await context.close();
}
