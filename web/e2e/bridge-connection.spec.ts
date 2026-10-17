// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Bridge Connection pages against `muster dev` and its fake bridge: Verification step 1 of their story. The Admin
// creates "br" on the fake bridge's second bot, polling; "Check connection" reads the bridge's info in the steps
// "Bridge info" and "Event delivery", and the page shows what the bridge offers. "Push" shows the push secret and the
// callback address with its hint, without saving. With the fake speaking protocol version 2, the check fails with the
// translated text and the bridge's message as its detail. The Connections list names the messenger and the host. An
// Admin reading Russian sees «Мост» and the steps at phone width. No page scrolls sideways at 360 px, a new or saved
// Connection, in polling and in push mode with the push secret and the callback address, in English and Russian; no
// Content Security Policy violation. The spec creates and deletes its own "br" and puts the fake's configuration back.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  BRIDGE_SECOND_TOKEN,
  BRIDGE_URL,
  adminApi,
  expectNoHorizontalScroll,
  fbr,
  removeBridgeConnection,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const CALLBACK_URL = /^http:\/\/localhost:8081\/api\/v1\/callbacks\/bridge\/CN[0-9A-Z]+$/;
const CALLBACK_HINT =
  /^The bridge posts events to http:\/\/localhost:8081\/api\/v1\/callbacks\/bridge\/CN[0-9A-Z]+, signed with the push secret\. Configure the same address and secret in the bridge\.$/;
const CALLBACK_HINT_RU =
  /^Мост отправляет события на http:\/\/localhost:8081\/api\/v1\/callbacks\/bridge\/CN[0-9A-Z]+, подписывая их секретом push-отправки\. Укажите в мосте тот же адрес и секрет\.$/;
const VERSION_UNKNOWN = "The bridge speaks a protocol version this Muster does not.";
const VERSION_MESSAGE = "The bridge speaks protocol version 2; this Muster speaks 1.";

let connectionId = "";

function steps(page: Page) {
  return page.getByTestId("connection-check-step");
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

async function cleanUp(): Promise<void> {
  await resetFakeBridge();
  const admin = await adminApi();
  try {
    await removeBridgeConnection(admin);
  } finally {
    await admin.dispose();
  }
}

test.beforeAll(cleanUp);
test.afterAll(cleanUp);

test("creates a Bridge Connection, checks it and shows what the bridge offers", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 1. Connections → "Create connection" → Bridge → name, base URL, token, polling → "Save".
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  const fields = page.getByTestId("bridge-connection-fields");
  await expect(fields.getByRole("radio", { name: "Polling" })).toBeChecked();
  await expect(fields.getByTestId("bridge-mode-hint-poll")).toHaveText(
    "Muster polls the bridge; the bridge needs no address of Muster",
  );
  await expect(fields.getByTestId("bridge-mode-hint-push")).toHaveText(
    "The bridge pushes events to Muster's ingest address",
  );
  await expect(page.getByText("Push secret", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("Requests")).toHaveValue("10");
  await expect(page.getByLabel("Period in seconds")).toHaveValue("1");
  await page.getByRole("textbox", { name: "Name" }).fill("br");
  await fields.getByLabel("Base URL").fill(BRIDGE_URL);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Token", { exact: true }).fill(BRIDGE_SECOND_TOKEN);
  await shot(page, "bridge-connection-new");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  connectionId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "br", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Bridge", exact: true, level: 2 })).toBeVisible();
  await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(/^Set, changed /);
  await expect(page.getByText(BRIDGE_SECOND_TOKEN)).toHaveCount(0);

  // 2. "Check connection" → "Bridge info" and "Event delivery" pass; the page shows what the bridge offers.
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check passed.");
  await expect(steps(page)).toHaveText([
    /^Bridge info: ok · \d+ ms · direct$/,
    /^Event delivery: ok · \d+ ms · direct$/,
  ]);
  const info = page.getByTestId("bridge-info");
  await expect(info.getByTestId("bridge-info-bridge")).toHaveText(
    "Bridge: Muster fake bridge 1.0.0",
  );
  await expect(info.getByTestId("bridge-info-messenger")).toHaveText("Messenger: Fake chat");
  await expect(info.getByTestId("bridge-info-protocol")).toHaveText("Protocol version 1");
  await expect(info.getByTestId("bridge-info-features")).toContainText("Buttons");
  await expect(info.getByTestId("bridge-info-features")).toContainText("Thread messages");
  await expect(info.getByTestId("bridge-info-lookup")).toHaveText("Finds people by login");
  await expect(info.getByTestId("bridge-info-event-delivery")).toHaveText(
    "Event delivery: Polling, Push",
  );
  await expect(info.getByTestId("bridge-info-read-at")).toHaveText(/^Read /);
  await shot(page, "bridge-connection-check");

  // 3. "Push" → the push secret and the callback address with its hint; back to polling without saving.
  await fields.getByRole("radio", { name: "Push" }).check();
  await expect(fields.getByText("Push secret", { exact: true })).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-url")).toHaveText(CALLBACK_URL);
  await expect(fields.getByTestId("bridge-callback-hint")).toHaveText(CALLBACK_HINT);
  await shot(page, "bridge-connection-push");
  await fields.getByRole("radio", { name: "Polling" }).check();
  await expect(fields.getByText("Push secret", { exact: true })).toHaveCount(0);

  // 4. The fake speaks protocol version 2 → the check fails with the translated text and the bridge's message.
  await fbr("PUT", "/config", { protocol_version: "2" });
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check failed.");
  await expect(steps(page)).toHaveText([
    /^Bridge info: failed \d+ ms · direct/,
    "Event delivery: Skipped: an earlier step failed",
  ]);
  await expect(steps(page).first().getByTestId("connection-check-step-reason")).toHaveText(
    VERSION_UNKNOWN,
  );
  await expect(steps(page).first().getByTestId("connection-check-step-detail")).toHaveText(
    `Details: ${VERSION_MESSAGE}`,
  );
  await shot(page, "bridge-connection-version-unknown");
  await fbr("PUT", "/config", { protocol_version: "1" });
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check passed.");

  // At phone width the page scrolls vertically only, in polling and in push mode with its callback address, the
  // longest text of the page; back to polling without saving.
  await page.setViewportSize({ width: 360, height: 740 });
  await page.reload();
  await expect(page.getByTestId("bridge-info-protocol")).toHaveText("Protocol version 1");
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-360");
  await fields.getByRole("radio", { name: "Push" }).check();
  await expect(fields.getByText("Push secret", { exact: true })).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-url")).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-url")).toHaveText(CALLBACK_URL);
  await expect(fields.getByTestId("bridge-callback-hint")).toHaveText(CALLBACK_HINT);
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-push-360");
  await fields.getByRole("radio", { name: "Polling" }).check();
  await expect(fields.getByText("Push secret", { exact: true })).toHaveCount(0);

  // 5. The Connections list names the bridge's messenger and the host of its base URL.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/connections");
  const row = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "br", exact: true }) });
  await expect(row).toContainText("Fake chat");
  await expect(row.getByTestId("connection-account")).toHaveText("127.0.0.1:18094");
  await shot(page, "bridge-connection-list");
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(row).toContainText("Fake chat");
  await expectNoHorizontalScroll(page);

  // A new Bridge Connection at phone width, in push mode too: the callback address comes once it is saved.
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  await expect(fields.getByRole("radio", { name: "Polling" })).toBeChecked();
  await expectNoHorizontalScroll(page);
  await fields.getByRole("radio", { name: "Push" }).check();
  await expect(fields.getByText("Push secret", { exact: true })).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-after-save")).toHaveText(
    "The callback address appears once the Connection is saved.",
  );
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-new-push-360");
  await fields.getByRole("radio", { name: "Polling" }).check();
  expect(csp).toEqual([]);
});

test("an Admin reading Russian sees the Bridge and its check at phone width", async ({
  browser,
}) => {
  expect(connectionId).not.toBe("");
  // A second Admin of its own, deleted at the end: the specs after this one expect admin@example.org to be the last
  // active Admin.
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("br-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "br-admin-ru-1" });
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
  await signIn(page, "br-admin-ru", "br-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  const fields = page.getByTestId("bridge-connection-fields");
  await page.goto("/connections/new");
  await expect(page.getByRole("radio", { name: "Мост", exact: true })).toBeVisible();
  await page.getByRole("radio", { name: "Мост", exact: true }).check();
  await expect(page.getByRole("heading", { name: "Мост", exact: true, level: 2 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "Опрос" })).toBeChecked();
  await expectNoHorizontalScroll(page);
  await fields.getByRole("radio", { name: "Push-отправка" }).check();
  await expect(fields.getByText("Секрет push-отправки", { exact: true })).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-after-save")).toHaveText(
    "Адрес обратного вызова появится после сохранения подключения.",
  );
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-ru-new-push-360");
  await fields.getByRole("radio", { name: "Опрос" }).check();

  await page.goto(`/connections/${connectionId}`);
  await expect(page.getByRole("heading", { name: "Мост", exact: true, level: 2 })).toBeVisible();
  await expect(page.getByRole("radio", { name: "Опрос" })).toBeChecked();
  await page.getByRole("button", { name: "Проверить подключение" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("Проверка прошла.");
  await expect(steps(page)).toHaveText([
    /^Сведения о мосте: успешно · \d+ мс · напрямую$/,
    /^Доставка событий: успешно · \d+ мс · напрямую$/,
  ]);
  await expect(page.getByTestId("bridge-info-protocol")).toHaveText("Версия протокола 1");
  await expect(page.getByTestId("bridge-info-messenger")).toHaveText("Мессенджер: Fake chat");
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-ru-360");
  // Push mode with the callback address, without saving.
  await fields.getByRole("radio", { name: "Push-отправка" }).check();
  await expect(fields.getByText("Секрет push-отправки", { exact: true })).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-url")).toBeVisible();
  await expect(fields.getByTestId("bridge-callback-url")).toHaveText(CALLBACK_URL);
  await expect(fields.getByTestId("bridge-callback-hint")).toHaveText(CALLBACK_HINT_RU);
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-connection-ru-push-360");
  await fields.getByRole("radio", { name: "Опрос" }).check();
  await expect(fields.getByText("Секрет push-отправки", { exact: true })).toHaveCount(0);

  await page.goto("/connections");
  const row = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "br", exact: true }) });
  await expect(row).toContainText("Fake chat");
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
  await context.close();
}
