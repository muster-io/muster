// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mattermost Connection pages against `muster dev` and its fake Mattermost server: Verification steps 1 to 5 of
// their story. The Admin creates "mm", sees the bot token as set and the callback address with its hint, checks the
// Connection, with the hint about answers in the Thread while the fake's bot is not a system admin and without it once
// it is, cannot delete "mm" while the Destination "alerts" uses it, and replaces the bot token, which the Audit log
// shows as a changed Secret. An Admin reading Russian sees the same at phone width; a Viewer sees the pages read-only.
// No page scrolls sideways at 360 px; no Content Security Policy violation.

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

/** The control API of the fake Mattermost server of `muster dev`. */
const FMM = "http://127.0.0.1:18065/_fake";
const SERVER_URL = "http://127.0.0.1:18065";
const CALLBACK = /^http:\/\/localhost:8081\/api\/v1\/callbacks\/mattermost\/CN[0-9A-Z]+$/;
const WARNING =
  "The bot may not make ephemeral messages, so answers to button presses show in the Thread of the Root message. Give the bot the create_post_ephemeral permission, for example the system admin role, to show them in the channel.";
const WARNING_RU =
  "Боту нельзя создавать эфемерные сообщения, поэтому ответы на нажатия кнопок появляются в треде корневого сообщения. Чтобы они показывались в канале, дайте боту право create_post_ephemeral, например роль системного администратора.";
const IN_USE = "This Connection is used by 1 Destination. Delete it first.";
const IN_USE_RU = "Это подключение используется в 1 месте доставки. Сначала удалите его.";

interface Named {
  id: string;
  name: string;
}

let connectionId = "";

async function botSystemAdmin(on: boolean): Promise<void> {
  const res = await fetch(`${FMM}/config`, {
    method: "PUT",
    body: JSON.stringify({ bot_system_admin: on }),
  });
  expect(res.ok, `PUT /_fake/config: ${res.status}`).toBe(true);
}

const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (event) => [event, { everyone: "none", user_ids: [], groups: [] }],
  ),
);

/** Deletes the Destination "alerts" and the Connection "mm", whatever an earlier run left. */
async function removeMm(admin: Api): Promise<void> {
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  const alerts = destinations.items.find((d) => d.name === "alerts");
  if (alerts !== undefined) {
    await admin.call("DELETE", `/api/v1/destinations/${alerts.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  const mm = connections.items.find((c) => c.name === "mm");
  if (mm !== undefined) {
    await admin.call("DELETE", `/api/v1/connections/${mm.id}`);
  }
}

/** Changes the limiter of "mm" through the API, as another Admin would. */
async function changeLimiter(limit: number): Promise<void> {
  const admin = await adminApi();
  try {
    const res = await admin.context.get(`/api/v1/connections/${connectionId}`);
    await admin.call(
      "PUT",
      `/api/v1/connections/${connectionId}`,
      {
        type: "mattermost",
        name: "mm",
        server_url: SERVER_URL,
        limiter: { limit, per_seconds: 1 },
        proxy: { enabled: false },
      },
      { "If-Match": res.headers().etag ?? "" },
    );
  } finally {
    await admin.dispose();
  }
}

function nav(page: Page, name: string, label = "Main") {
  return page.getByRole("navigation", { name: label }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await removeMm(admin);
  } finally {
    await admin.dispose();
  }
  await botSystemAdmin(false);
});

test.afterAll(async () => {
  await botSystemAdmin(false);
  const admin = await adminApi();
  try {
    await removeMm(admin);
  } finally {
    await admin.dispose();
  }
});

test("creates, checks and replaces the bot token of a Connection that a Destination uses", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // The navigation lists "Connections" after "Routes".
  await expect(nav(page, "Connections")).toHaveAttribute("href", "/connections");
  const names = await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link")
    .allTextContents();
  expect(names[names.indexOf("Routes") + 1]).toBe("Connections");

  // 1. Connections → "Create connection" → Mattermost → name, server URL, bot token → "Save".
  await nav(page, "Connections").click();
  await expect(page.getByRole("heading", { name: "Connections", level: 1 })).toBeVisible();
  const demo = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "Dev Mattermost", exact: true }) });
  await expect(demo).toContainText("Mattermost");
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Mattermost" }).check();
  await page.getByRole("textbox", { name: "Name" }).fill("mm");
  await page.getByRole("textbox", { name: "Server URL" }).fill(SERVER_URL);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Bot token").fill("mm-dev-token");
  await expect(page.getByLabel("Requests")).toHaveValue("5");
  await expect(page.getByLabel("Period in seconds")).toHaveValue("1");
  await shot(page, "connection-new");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  connectionId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "mm", level: 1 })).toBeVisible();
  await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(/^Set, changed /);
  await expect(page.locator('input[type="password"]')).toHaveCount(0);
  await expect(page.getByTestId("connection-callback-url")).toHaveText(CALLBACK);
  const callback = (await page.getByTestId("connection-callback-url").textContent()) ?? "";
  expect(callback.endsWith(`/${connectionId}`)).toBe(true);
  await expect(page.getByTestId("connection-callback-hint")).toHaveText(
    `Mattermost calls ${callback} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it.`,
  );

  // 2. "Check connection" → "Connected as muster-dev-bot", "direct", a latency in ms, and — the fake's bot is not a
  // system admin — the hint that answers to presses show in the Thread.
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Connected as muster-dev-bot",
  );
  await expect(page.getByTestId("connection-check-step")).toHaveText(
    /^Token: ok · \d+ ms · direct$/,
  );
  await expect(page.getByTestId("connection-check-warning")).toHaveText(WARNING);
  await expect(page.getByTestId("connection-bot")).toHaveText("Bot: muster-dev-bot");
  await shot(page, "connection-check-warning");
  // With the system admin role the bot may make ephemeral posts: no hint.
  await botSystemAdmin(true);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Connected as muster-dev-bot",
  );
  await expect(page.getByTestId("connection-check-warning")).toHaveCount(0);
  await shot(page, "connection-check-admin");
  await botSystemAdmin(false);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-warning")).toHaveText(WARNING);

  // 3. The Destination "alerts" on ch-alerts of "mm", through the API → "Delete" is refused.
  const admin = await adminApi();
  try {
    await admin.call("POST", "/api/v1/destinations", {
      type: "mattermost",
      name: "alerts",
      connection_id: connectionId,
      team_id: "team-dev",
      channel_id: "ch-alerts",
      mentions: MENTIONS,
      limiter: { limit: 5, per_seconds: 1 },
    });
  } finally {
    await admin.dispose();
  }
  await nav(page, "Connections").click();
  const mmRow = page
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name: "mm", exact: true }) });
  await expect(mmRow.getByTestId("connection-destinations")).toHaveText("1");
  await mmRow.getByRole("link", { name: "mm", exact: true }).click();
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog", { name: "Delete Connection mm?" });
  await dialog.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByTestId("connection-delete-error")).toHaveText(IN_USE);
  await shot(page, "connection-in-use");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // 4. Replace the bot token → "Save" → the Audit log shows it as a changed Secret, never its value.
  await page.getByRole("button", { name: "Replace" }).click();
  await page.getByLabel("Bot token").fill("mm-dev-token-2");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("connection-status")).toHaveText("Saved.");
  await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(/^Set, changed /);
  await expect(page.getByText("mm-dev-token-2")).toHaveCount(0);
  await page.goto(`/admin/audit-log?resource_type=connection&resource_id=${connectionId}`);
  const updated = page.getByRole("row").filter({ hasText: "connection.updated" }).first();
  const diff = updated.getByTestId("audit-diff");
  await expect(diff).toContainText("Bot token:");
  await expect(diff).toContainText("changed");
  await expect(diff).not.toContainText("mm-dev-token");
  const created = page.getByRole("row").filter({ hasText: "connection.created" }).first();
  await expect(created.getByTestId("audit-diff")).toContainText("Server URL:");
  await expect(created.getByTestId("audit-diff")).toContainText("Rate limit: requests:");
  await shot(page, "audit-log-connection");

  // A change elsewhere: an untouched form takes the new version and says so; a form with changes keeps them, its save
  // is refused (412) and "Reload" reads the newer version.
  await page.goto(`/connections/${connectionId}`);
  await expect(page.getByLabel("Requests")).toHaveValue("5");
  await changeLimiter(4);
  await expect(page.getByTestId("connection-replaced")).toHaveText(
    "This Connection was changed elsewhere.",
  );
  await expect(page.getByLabel("Requests")).toHaveValue("4");
  await page.getByRole("textbox", { name: "Name" }).fill("mm-renamed");
  await changeLimiter(3);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("connection-stale")).toContainText(
    "Someone else changed this Connection. Reload to see the changes.",
  );
  await expect(page.getByRole("textbox", { name: "Name" })).toHaveValue("mm-renamed");
  await page.getByTestId("connection-stale").getByRole("button", { name: "Reload" }).click();
  await expect(page.getByRole("textbox", { name: "Name" })).toHaveValue("mm");
  await expect(page.getByLabel("Requests")).toHaveValue("3");

  // At phone width the list and the page scroll vertically only.
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto("/connections");
  await expect(mmRow).toBeVisible();
  await expectNoHorizontalScroll(page);
  await page.goto(`/connections/${connectionId}`);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-warning")).toHaveText(WARNING);
  await expectNoHorizontalScroll(page);
  await shot(page, "connection-360");
  expect(csp).toEqual([]);
});

test("an Admin reading Russian sees the check, the hint and the refusal at phone width", async ({
  browser,
}) => {
  // A second Admin of its own, deleted at the end: the specs after this one expect admin@example.org to be the last
  // active Admin.
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("conn-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "conn-admin-ru-1" });
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
  await signIn(page, "conn-admin-ru", "conn-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/connections");
  await expect(page.getByRole("heading", { name: "Подключения", level: 1 })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Места доставки" })).toBeVisible();
  await page.getByRole("link", { name: "mm", exact: true }).click();
  const callback = (await page.getByTestId("connection-callback-url").textContent()) ?? "";
  await expect(page.getByTestId("connection-callback-hint")).toHaveText(
    `Mattermost обращается к ${callback}, когда кто-то нажимает кнопку. Если этот адрес внутренний, добавьте его хост в AllowedUntrustedInternalConnections в настройках сервера Mattermost, иначе нажатия завершатся ошибкой «Action integration error». Проверить это можно тестовым сообщением в место доставки.`,
  );
  await page.getByRole("button", { name: "Проверить подключение" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(
    "Подключено как muster-dev-bot",
  );
  await expect(page.getByTestId("connection-check-step")).toHaveText(
    /^Токен: успешно · \d+ мс · напрямую$/,
  );
  await expect(page.getByTestId("connection-check-warning")).toHaveText(WARNING_RU);
  await expectNoHorizontalScroll(page);
  // The long callback address wraps inside the hint instead of running out of its card.
  for (const id of ["connection-callback-hint", "connection-check-warning"]) {
    const fits = await page.getByTestId(id).evaluate((e) => e.scrollWidth <= e.clientWidth);
    expect(fits, id).toBe(true);
  }
  await shot(page, "connection-ru-360");
  await page.getByRole("button", { name: "Удалить" }).click();
  await page
    .getByRole("dialog", { name: "Удалить подключение mm?" })
    .getByRole("button", { name: "Удалить" })
    .click();
  await expect(page.getByTestId("connection-delete-error")).toHaveText(IN_USE_RU);
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
  await context.close();
}

test("a Viewer sees the Connection pages read-only", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("conn-viewer", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "conn-viewer-1" });
  await admin.dispose();
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "conn-viewer", "conn-viewer-1");

  // 5. No "Create connection"; "mm" without "Save", "Check connection" or "Delete".
  await expect(nav(page, "Connections")).toBeVisible();
  await page.goto("/connections");
  await expect(page.getByRole("link", { name: "mm", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Create connection" })).toHaveCount(0);
  await expectNoHorizontalScroll(page);
  await page.getByRole("link", { name: "mm", exact: true }).click();
  await expect(page.getByTestId("connection-read-only")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Name" })).toBeDisabled();
  await expect(page.getByTestId("connection-callback-url")).toHaveText(CALLBACK);
  for (const name of ["Save", "Check connection", "Delete", "Replace"]) {
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  }
  await page.goto("/connections/new");
  await expect(page.getByText("You do not have permission to see this page.")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "connection-viewer-360");
  expect(csp).toEqual([]);
  await context.close();
});
