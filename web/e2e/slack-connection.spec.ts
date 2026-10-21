// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Slack Connection pages against `muster dev` and its fake Slack server: Verification steps 1, 2 and 6 of their
// story. The Admin creates "ui" on the fake's second app in Socket Mode, checks the form's values before saving —
// "Token", "Scopes" and "App-level token" with the bot, the workspace and the app, and no Connection saved — and saves
// it; the saved page checks it again and names the bot, the workspace and the app. HTTP mode drops the app-level token
// field and shows the Slack signing secret with the callback address and its hint, without saving. With another
// program connected to the app and the socket refreshed, the page warns that the app is shared. An HTTP Connection is
// created with its Slack signing secret and shows its callback address once saved, and is deleted from its page while
// no Destination uses it. An Admin reading Russian sees «Режим событий» and «Проверить подключение». No page scrolls
// sideways at 360 px; no Content Security Policy violation. The spec deletes its own Connections and puts the fake's
// configuration back.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  SLACK_SECOND_APP_TOKEN,
  SLACK_SECOND_BOT_TOKEN,
  adminApi,
  expectNoHorizontalScroll,
  fsl,
  removeSlackConnection,
  resetFakeSlack,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
  watchDeletion,
} from "./support";

test.describe.configure({ mode: "serial" });

const SOCKET_HINT =
  "The Leader holds the Socket Mode connection, so Slack needs no address of Muster. Presses made while the Leader changes are lost; use HTTP if that matters.";
const CALLBACK_URL = /^http:\/\/localhost:8081\/api\/v1\/callbacks\/slack\/CN[0-9A-Z]+$/;
const CALLBACK_HINT =
  /^Slack sends button presses to http:\/\/localhost:8081\/api\/v1\/callbacks\/slack\/CN[0-9A-Z]+\. Enter it as the Interactivity Request URL of the Slack app, and the app's Slack signing secret here\. A press on a test message checks both\.$/;
const SHARED =
  "Another program is connected to this Slack app through Socket Mode: presses are shared between them at random. Use one Slack app for each Connection and share it with no other Connection or installation.";
const IDENTITY = "Bot: muster-dev-bot-b · Workspace: Fake workspace B · App: A0MUSTERDEVB";

let connectionId = "";

function steps(page: Page) {
  return page.getByTestId("connection-check-step");
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

async function typeSecret(page: Page, testId: string, label: string, value: string) {
  await page
    .getByTestId(testId)
    .getByRole("button", { name: /^(Set a value|Replace)$/ })
    .click();
  await page.getByLabel(label, { exact: true }).fill(value);
}

async function connectionIds(admin: Api): Promise<string[]> {
  const list = await admin.call<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/connections?limit=500",
  );
  return list.items.map((c) => c.id).toSorted();
}

interface Sockets {
  apps: Record<string, { open: number; hellos: number }>;
}

async function cleanUp(): Promise<void> {
  await resetFakeSlack();
  const admin = await adminApi();
  try {
    await removeSlackConnection(admin, "ui");
    await removeSlackConnection(admin, "ui-http");
  } finally {
    await admin.dispose();
  }
}

test.beforeAll(cleanUp);
test.afterAll(cleanUp);

test("creates a Slack Connection in Socket Mode, checks it before and after saving", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await signInAdmin(page);
    const before = await connectionIds(admin);

    // 1. Connections → "Create connection" → Slack → name, the bot token, Socket Mode, the app-level token.
    await nav(page, "Connections").click();
    await page.getByRole("link", { name: "Create connection" }).click();
    await page.getByRole("radio", { name: "Slack", exact: true }).check();
    await expect(page.getByRole("heading", { name: "Slack", exact: true, level: 2 })).toBeVisible();
    const fields = page.getByTestId("slack-connection-fields");
    await expect(fields.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
    await expect(fields.getByTestId("slack-mode-hint-socket")).toHaveText(SOCKET_HINT);
    await expect(page.getByLabel("Server URL")).toHaveCount(0);
    await expect(page.getByLabel("Requests")).toHaveValue("50");
    await expect(page.getByLabel("Period in seconds")).toHaveValue("60");
    await page.getByRole("textbox", { name: "Name" }).fill("ui");
    await typeSecret(page, "secret-connection-bot-token", "Bot token", SLACK_SECOND_BOT_TOKEN);
    await typeSecret(
      page,
      "secret-connection-app-token",
      "App-level token",
      SLACK_SECOND_APP_TOKEN,
    );

    // "Check connection" checks the form's values and saves nothing.
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect(page.getByTestId("connection-check-summary")).toHaveText(
      "Connected as muster-dev-bot-b",
    );
    await expect(steps(page)).toHaveText([
      /^Token: ok · \d+ ms · direct · Bot: muster-dev-bot-b · Workspace: Fake workspace B$/,
      "Scopes: ok · direct",
      /^App-level token: ok · \d+ ms · direct · App: A0MUSTERDEVB$/,
    ]);
    await shot(page, "slack-connection-new-check");
    expect(await connectionIds(admin)).toEqual(before);

    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
    connectionId = new URL(page.url()).pathname.split("/").pop() ?? "";
    await expect(page.getByRole("heading", { name: "ui", level: 1 })).toBeVisible();
    await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(
      /^Set, changed /,
    );
    await expect(page.getByTestId("secret-connection-app-token-status")).toHaveText(
      /^Set, changed /,
    );
    await expect(page.getByText(SLACK_SECOND_BOT_TOKEN)).toHaveCount(0);
    await expect(page.getByText(SLACK_SECOND_APP_TOKEN)).toHaveCount(0);

    // The saved Connection's check stores what it found, which the page names.
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect(page.getByTestId("connection-check-summary")).toHaveText(
      "Connected as muster-dev-bot-b",
    );
    await expect(page.getByTestId("connection-bot")).toHaveText(IDENTITY);
    await shot(page, "slack-connection-saved");

    // HTTP mode: the app-level token is gone; the Slack signing secret and the callback address with its hint.
    await fields.getByRole("radio", { name: "HTTP" }).check();
    await expect(fields.getByText("App-level token", { exact: true })).toHaveCount(0);
    await expect(fields.getByText("Slack signing secret", { exact: true })).toBeVisible();
    await expect(fields.getByTestId("slack-callback-url")).toHaveText(CALLBACK_URL);
    await expect(fields.getByTestId("slack-callback-hint")).toHaveText(CALLBACK_HINT);
    await shot(page, "slack-connection-http");

    // 6. At phone width the page scrolls vertically only, in both modes.
    await page.setViewportSize({ width: 360, height: 740 });
    await expectNoHorizontalScroll(page);
    await fields.getByRole("radio", { name: "Socket Mode" }).check();
    await expect(fields.getByText("App-level token", { exact: true })).toBeVisible();
    await expectNoHorizontalScroll(page);
    await shot(page, "slack-connection-360");

    // The Connections list names the bot and the workspace.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/connections");
    const row = page
      .getByRole("row")
      .filter({ has: page.getByRole("link", { name: "ui", exact: true }) });
    await expect(row).toContainText("Slack");
    await expect(row.getByTestId("connection-account")).toHaveText(
      "muster-dev-bot-b · Fake workspace B",
    );
    expect(csp).toEqual([]);
  } finally {
    await admin.dispose();
  }
});

test("warns while another program is connected to the Slack app", async ({ page }) => {
  test.setTimeout(90_000);
  expect(connectionId).not.toBe("");
  const csp = watchCsp(page);
  // 2. The Leader holds the socket of "ui"; another program joins the app, and the socket is refreshed.
  await expect
    .poll(async () => (await fsl<Sockets>("GET", "/socket")).apps.b?.open ?? 0, { timeout: 30_000 })
    .toBeGreaterThan(0);
  await fsl("PUT", "/config", { extra_connections: { b: 1 } });
  await fsl("POST", "/socket", { app: "b", disconnect: "refresh_requested" });
  const admin = await adminApi();
  try {
    await expect
      .poll(
        async () =>
          (
            await admin.call<{ socket?: { other_connections: number } | null }>(
              "GET",
              `/api/v1/connections/${connectionId}`,
            )
          ).socket?.other_connections ?? 0,
        { timeout: 30_000 },
      )
      .toBe(1);
  } finally {
    await admin.dispose();
  }
  await signInAdmin(page);
  await page.goto(`/connections/${connectionId}`);
  await expect(page.getByTestId("slack-shared-app")).toHaveText(`${SHARED} How to fix`);
  // The check passes, with the warning.
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Connected as muster-dev-bot-b",
  );
  await expect(page.getByTestId("connection-check-warning")).toHaveText(`${SHARED} How to fix`);
  await shot(page, "slack-connection-shared");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);

  // The other program leaves: the next hello counts none, and the warning goes.
  await fsl("PUT", "/config", { extra_connections: { b: null } });
  await fsl("POST", "/socket", { app: "b", disconnect: "refresh_requested" });
  await expect
    .poll(
      async () => {
        await page.reload();
        await expect(page.getByTestId("connection-bot")).toHaveText(IDENTITY);
        return page.getByTestId("slack-shared-app").count();
      },
      { timeout: 30_000 },
    )
    .toBe(0);
  expect(csp).toEqual([]);
});

test("creates a Slack Connection in HTTP mode and deletes it from its page", async ({ page }) => {
  test.setTimeout(90_000);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/connections/new");
  await page.getByRole("radio", { name: "Slack", exact: true }).check();
  const fields = page.getByTestId("slack-connection-fields");
  await fields.getByRole("radio", { name: "HTTP" }).check();
  await expect(fields.getByTestId("slack-callback-after-save")).toHaveText(
    "The callback address appears once the Connection is saved.",
  );
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByRole("textbox", { name: "Name" }).fill("ui-http");
  await typeSecret(page, "secret-connection-bot-token", "Bot token", SLACK_SECOND_BOT_TOKEN);
  // HTTP needs the Slack signing secret.
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByTestId("secret-connection-slack-signing-secret").getByText("Fill in this field."),
  ).toBeVisible();
  await typeSecret(
    page,
    "secret-connection-slack-signing-secret",
    "Slack signing secret",
    "muster-dev-slack-signing-secret-b",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  const id = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "ui-http", level: 1 })).toBeVisible();
  await expect(fields.getByRole("radio", { name: "HTTP" })).toBeChecked();
  await expect(fields.getByTestId("slack-callback-url")).toHaveText(
    `http://localhost:8081/api/v1/callbacks/slack/${id}`,
  );
  await expect(fields.getByTestId("slack-callback-hint")).toHaveText(CALLBACK_HINT);
  // HTTP mode has no step "App-level token".
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(steps(page)).toHaveText([
    /^Token: ok · \d+ ms · direct · Bot: muster-dev-bot-b · Workspace: Fake workspace B$/,
    "Scopes: ok · direct",
  ]);
  await expect(page.getByTestId("connection-bot")).toHaveText(
    "Bot: muster-dev-bot-b · Workspace: Fake workspace B",
  );
  await shot(page, "slack-connection-http-saved");

  // No Destination uses it: Delete removes it.
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const deletion = await watchDeletion(page, `/api/v1/connections/${id}`, {
    type: "connection",
    id,
  });
  await page
    .getByRole("dialog", { name: "Delete Connection ui-http?" })
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await expect(page).toHaveURL(/\/connections$/);
  await expect(page.getByRole("link", { name: "ui-http", exact: true })).toHaveCount(0);
  expect(deletion.reads).toEqual([]);
  expect(deletion.errors).toEqual([]);
  expect(csp).toEqual([]);
});

test("an Admin reading Russian sees the Slack Connection at phone width", async ({ browser }) => {
  expect(connectionId).not.toBe("");
  // A second Admin of its own, deleted at the end: the specs after this one expect admin@example.org to be the last
  // active Admin.
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("sl-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "sl-admin-ru-1" });
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
  await signIn(page, "sl-admin-ru", "sl-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto(`/connections/${connectionId}`);
  await expect(page.getByRole("heading", { name: "ui", level: 1 })).toBeVisible();
  await expect(page.getByText("Режим событий", { exact: true })).toBeVisible();
  await expect(page.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
  await expect(page.getByText("Токен уровня приложения", { exact: true })).toBeVisible();
  await expect(page.getByTestId("connection-bot")).toHaveText(
    "Бот: muster-dev-bot-b · Рабочее пространство: Fake workspace B · Приложение: A0MUSTERDEVB",
  );
  await page.getByRole("button", { name: "Проверить подключение" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Подключено как muster-dev-bot-b",
  );
  await expect(steps(page)).toHaveText([
    /^Токен: успешно · \d+ мс · напрямую · Бот: muster-dev-bot-b · Рабочее пространство: Fake workspace B$/,
    "Права: успешно · напрямую",
    /^Токен уровня приложения: успешно · \d+ мс · напрямую · Приложение: A0MUSTERDEVB$/,
  ]);
  await expectNoHorizontalScroll(page);
  await shot(page, "slack-connection-ru-360");
  await page.getByRole("radio", { name: "HTTP" }).check();
  await expect(page.getByText("Секрет подписи Slack", { exact: true })).toBeVisible();
  await expect(page.getByTestId("slack-callback-hint")).toHaveText(
    /^Slack отправляет нажатия кнопок на http:\/\/localhost:8081\/api\/v1\/callbacks\/slack\/CN[0-9A-Z]+\. /,
  );
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
  await context.close();
}
