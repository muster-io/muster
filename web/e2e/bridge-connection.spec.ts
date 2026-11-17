// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Bridge Connection pages against `muster dev` and its fake bridge: Verification step 1 of their story. The Admin
// creates "br" on the fake bridge's second bot, polling; "Check connection" reads the bridge's info in the steps
// "Bridge info" and "Event delivery", and the page shows what the bridge offers. "Push" shows the push secret and the
// callback address with its hint, without saving. With the fake speaking protocol version 2, the check fails with the
// translated text and the bridge's message as its detail. The Connections list names the messenger and the host. An
// Admin reading Russian sees «Мост» and the steps at phone width. No page scrolls sideways at 360 px, a new or saved
// Connection, in polling and in push mode with the push secret and the callback address, in English and Russian; no
// Content Security Policy violation. The spec creates its own "br", deletes it from its page, which reads it no more
// once the deletion starts, and puts the fake's configuration back.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  BRIDGE_SECOND_TOKEN,
  BRIDGE_URL,
  DEFAULT_BRIDGE_CAPABILITIES,
  GATEWAY_NAME,
  adminApi,
  alertGroupOf,
  confirmContact,
  createBridgeConnection,
  ensureGateway,
  ensureLocalUser,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  fbr,
  notify,
  removeBridgeConnection,
  removeIntegration,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
  watchDeletion,
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
    // Event delivery makes no call of its own and shows no latency.
    "Event delivery: ok · direct",
  ]);
  const info = page.getByTestId("bridge-info");
  await expect(info.getByTestId("bridge-info-bridge")).toHaveText("Bridge: Example bridge 1.0.0");
  await expect(info.getByTestId("bridge-info-messenger")).toHaveText("Messenger: Example chat");
  await expect(info.getByTestId("bridge-info-protocol")).toHaveText("Protocol version 1");
  await expect(info.getByTestId("bridge-info-features")).toContainText("Buttons");
  await expect(info.getByTestId("bridge-info-features")).toContainText("Thread messages");
  await expect(info.getByTestId("bridge-info-lookup")).toHaveText("Finds people by login");
  await expect(info.getByTestId("bridge-info-event-delivery")).toHaveText(
    "Event delivery: Polling, Push",
  );
  await expect(info.getByTestId("bridge-info-read-at")).toHaveText(/^Read /);
  await expect(info.getByTestId("bridge-info-features")).not.toContainText("Group mentions");
  await expect(info.getByTestId("bridge-info-features")).not.toContainText("Mentions of everyone");
  await shot(page, "bridge-connection-check");

  // The bridge declares group mentions and the mentions of @channel and @here: the check lists them among the features.
  await fbr("PUT", "/config", {
    capabilities: {
      ...DEFAULT_BRIDGE_CAPABILITIES,
      group_mentions: true,
      everyone_mentions: ["channel", "here"],
    },
  });
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check passed.");
  await expect(info.getByTestId("bridge-info-features")).toContainText("Group mentions");
  await expect(info.getByTestId("bridge-info-features")).toContainText(
    "Mentions of everyone: @channel, @here",
  );
  await shot(page, "bridge-connection-check-mentions");
  await resetFakeBridge();
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(info.getByTestId("bridge-info-features")).not.toContainText("Group mentions");

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
  await expect(row).toContainText("Example chat");
  await expect(row.getByTestId("connection-account")).toHaveText("127.0.0.1:18094");
  await shot(page, "bridge-connection-list");
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(row).toContainText("Example chat");
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

const GATEWAY_GUIDE =
  "https://muster-io.github.io/muster/latest/messengers/notification-gateways/#connect-a-notification-gateway";

test("the demo Notification gateway is marked in the list, shows its roles and health, Broken after a refused call and Healthy after a check", async ({
  page,
}) => {
  test.setTimeout(150_000);
  const csp = watchCsp(page);
  const user = "gwb-petr";
  const admin = await adminApi();
  let gatewayId = "";
  let userId = "";
  try {
    gatewayId = await ensureGateway(admin);
    userId = await ensureLocalUser(admin, user, "responder");
    await confirmContact(user, gatewayId, "+10000000004");
    await removeIntegration(admin, "gwb-ui");
  } finally {
    await admin.dispose();
  }

  await signInAdmin(page);
  await page.goto("/connections");
  const gatewayRow = page.getByRole("row").filter({ hasText: GATEWAY_NAME });
  await expect(gatewayRow.getByTestId("connection-gateway-mark")).toHaveText(
    "Notification gateway",
  );
  await expect(
    page
      .getByRole("row")
      .filter({ hasText: "Example Bridge" })
      .getByTestId("connection-gateway-mark"),
  ).toHaveCount(0);
  await shot(page, "connections-gateway-mark");

  await gatewayRow.getByRole("link", { name: GATEWAY_NAME }).click();
  await expect(page.getByRole("heading", { name: GATEWAY_NAME, level: 1 })).toBeVisible();
  const health = page.getByTestId("gateway-health");
  await expect(health.getByRole("heading", { name: "Notification gateway" })).toBeVisible();
  await expect(health.getByTestId("gateway-roles")).toHaveText("Roles: Notification gateway");
  await expect(health.getByTestId("destination-health")).toHaveText("Healthy");
  await expect(page.getByTestId("bridge-info-roles")).toHaveText("Roles: Notification gateway");
  await expect(page.getByTestId("page-title").getByRole("link")).toHaveAttribute(
    "href",
    GATEWAY_GUIDE,
  );
  await expect(page.getByTestId("gateway-only-warning")).toHaveCount(0);

  // The check lists the roles.
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-roles")).toHaveText(
    "Roles: Notification gateway",
  );

  // A call the gateway refuses with 401 makes it Broken, with the reason and the time.
  const integrationId = await fakeIntegration("gwb-ui");
  await fam("PUT", "/groups/gwb", {
    receiver: "gwb-ui",
    route: "{}",
    labels: { alertname: "Gwb" },
  });
  await fam("PUT", "/groups/gwb/alerts/g1", { labels: { cluster: "g1" } });
  await notify(integrationId, "gwb", { reason: "first notification" });
  const group = await alertGroupOf(integrationId, "Gwb");
  try {
    await fbr("POST", "/faults", { path: "/gateway/v1/notifications", status: 401, times: 1 });
    const queued = await fetch("http://localhost:8082/_dev/gateway-calls", {
      method: "POST",
      body: JSON.stringify({ user_id: userId, connection_id: gatewayId, alert_group_ids: [group] }),
    });
    expect(queued.status, await queued.text()).toBe(204);
    await expect(async () => {
      await page.reload();
      await expect(page.getByTestId("gateway-broken-banner")).toBeVisible({ timeout: 2_000 });
    }).toPass({ timeout: 60_000 });
    await expect(page.getByTestId("gateway-broken-banner")).toHaveText(
      /^Broken since \d{2}:\d{2}: .*401.*\. Muster tries again every 5\smin\.$/,
    );
    await expect(health.getByTestId("destination-health")).toHaveText("Broken");
    await shot(page, "gateway-broken");

    // A check that passes ends the state.
    await fbr("DELETE", "/faults");
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect(page.getByTestId("connection-check-summary")).toBeVisible();
    await expect(async () => {
      await page.reload();
      await expect(health.getByTestId("destination-health")).toHaveText("Healthy", {
        timeout: 2_000,
      });
    }).toPass({ timeout: 30_000 });
    await expect(page.getByTestId("gateway-broken-banner")).toHaveCount(0);
  } finally {
    await fbr("DELETE", "/faults");
    const admin3 = await adminApi();
    try {
      await removeIntegration(admin3, "gwb-ui");
    } finally {
      await admin3.dispose();
    }
  }
  expect(csp).toEqual([]);
});

test("a Connection that declares only the role gateway while it still has a Destination shows the warning about it", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const admin = await adminApi();
  let connection = "";
  try {
    connection = await createBridgeConnection(admin, "br-gw");
    const nobody = { everyone: "none", user_ids: [], groups: [] };
    await admin.call("POST", "/api/v1/destinations", {
      type: "bridge",
      name: "br-gw-alerts",
      connection_id: connection,
      channel_id: "ch-alerts",
      mentions: Object.fromEntries(
        [
          "new_alert_group",
          "new_alerts",
          "reopen",
          "ack_timeout",
          "snooze_ended",
          "rise_to_urgent",
        ].map((k) => [k, nobody]),
      ),
      limiter: { limit: 30, per_seconds: 60 },
    });
    // The bridge now declares only the role gateway; a check reads that.
    await fbr("PUT", "/config", { token: BRIDGE_SECOND_TOKEN, roles: ["gateway"] });
    await admin.call("POST", `/api/v1/connections/${connection}/checks`);
  } finally {
    await admin.dispose();
  }
  try {
    await signInAdmin(page);
    await page.goto(`/connections/${connection}`);
    await expect(page.getByTestId("gateway-roles")).toHaveText("Roles: Notification gateway");
    await expect(page.getByTestId("gateway-only-warning")).toHaveText(
      "This bridge now declares only the role gateway: its 1 Destination no longer receives messages. Remove it or change the bridge.",
    );
    await shot(page, "gateway-only-warning");
    await page.goto("/profile");
    await page.getByLabel("Language").selectOption("ru");
    await page.goto(`/connections/${connection}`);
    await expect(page.getByTestId("gateway-only-warning")).toHaveText(
      "Мост теперь объявляет только роль gateway: его место доставки (1) больше не получает сообщений. Удалите его или измените мост.",
    );
  } finally {
    // The Admin reads English again, whatever the page showed.
    await page.goto("/profile");
    await page.getByLabel(/^(Language|Язык)$/).selectOption("en");
    await fbr("PUT", "/config", { token: BRIDGE_SECOND_TOKEN, roles: null });
    const cleaner = await adminApi();
    try {
      await removeBridgeConnection(cleaner, "br-gw");
    } finally {
      await cleaner.dispose();
    }
  }
});

test("deletes the Bridge Connection from its page", async ({ page }) => {
  expect(connectionId).not.toBe("");
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/connections/${connectionId}`);
  await expect(page.getByRole("heading", { name: "br", level: 1 })).toBeVisible();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const deletion = await watchDeletion(page, `/api/v1/connections/${connectionId}`, {
    type: "connection",
    id: connectionId,
  });
  await page
    .getByRole("dialog", { name: "Delete Connection br?" })
    .getByRole("button", { name: "Delete", exact: true })
    .click();
  await expect(page).toHaveURL(/\/connections$/);
  await expect(page.getByRole("link", { name: "br", exact: true })).toHaveCount(0);
  // Its page read the Connection no more once the deletion started, not even on the live hint of the deletion.
  expect(deletion.reads).toEqual([]);
  expect(deletion.errors).toEqual([]);
  expect(csp).toEqual([]);
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
    "Доставка событий: успешно · напрямую",
  ]);
  await expect(page.getByTestId("bridge-info-protocol")).toHaveText("Версия протокола 1");
  await expect(page.getByTestId("bridge-info-messenger")).toHaveText("Мессенджер: Example chat");
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
  await expect(row).toContainText("Example chat");
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
  await context.close();
}
