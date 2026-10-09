// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Test panel of the Destination page against `muster dev` and its fake Mattermost server and fake receiver:
// Verification steps 1 to 4, 6 and 7 of its story. "alerts" → "Test" → "Example" → "Send test message" shows
// "Message: Sent" and "Button press: Button presses reach Muster.", and the press error that names
// AllowedUntrustedInternalConnections once the fake no longer allows Muster's address. The template-mode webhook "chat"
// shows "Create: Sent" with the extracted id and its Secret as [redacted]; the events-mode webhook "auto" shows
// "Event: Sent" with the response status 200, and "limited" once a delivery spent its limiter. A Broken "alerts" stays
// Broken when only its button press fails, which the panel explains, and shows "Healthy" without a reload after a
// test that succeeds in every step; a recent Alert Group of its Route, found by its title, is tested and previewed, the
// Mattermost Root message in a frame without links. A reader of Russian runs a test at phone width. No page scrolls sideways at
// 360 px; no Content Security Policy violation.

import { type Browser, expect, type Page, test } from "@playwright/test";

import {
  type Api,
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

const FMM = "http://127.0.0.1:18065/_fake";
const FWH = "http://127.0.0.1:18093";
const BOT = "musterdevbotuserfake000000";
const ALLOWED = "localhost 127.0.0.1";
const TEAM_MM = "s048-mm";
const TEAM_AUTO = "s048-auto";
const PRESS_ERROR =
  "The button press did not reach Muster. Add the host of http://localhost:8081 to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.";
const LIMITED = "The messenger is busy; nothing was sent. Try again in a few seconds.";
const SECRET_VALUE = "chat-token-value";

const NONE = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = {
  new_alert_group: NONE,
  new_alerts: NONE,
  reopen: NONE,
  ack_timeout: NONE,
  snooze_ended: NONE,
  rise_to_urgent: NONE,
};
const CHAT_TEXT =
  '{{ printf "#%d %s (%s)" .AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}';

interface Named {
  id: string;
  name: string;
}

const ids = { alerts: "", auto: "", chat: "", integration: "" };

async function fake(url: string, method: string, body?: unknown): Promise<void> {
  const res = await fetch(url, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${url}: ${res.status}`).toBe(true);
}

/** The addresses the fake Mattermost server lets call internal hosts: Muster's ingest address, or none. */
function allow(hosts: string): Promise<void> {
  return fake(`${FMM}/config`, "PUT", { allowed_untrusted_internal_connections: hosts });
}

function botInChannel(on: boolean): Promise<void> {
  return fake(`${FMM}/channels/ch-alerts/members/${BOT}`, on ? "PUT" : "DELETE");
}

async function received(hook: string): Promise<number> {
  const res = await fetch(`${FWH}/_fake/received/${hook}`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of requests
  return ((await res.json()) as unknown[]).length;
}

async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === TEAM_MM || x.name === TEAM_AUTO)) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => ["alerts", "auto", "chat"].includes(x.name))) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

/** A Route for the alerts with team=<name> that sends to the Destination. */
async function route(admin: Api, name: string, destinationId: string): Promise<void> {
  const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
    "GET",
    "/api/v1/route-profiles",
  );
  await admin.call("POST", "/api/v1/routes", {
    name,
    matchers: [{ label: "team", op: "=", value: name }],
    urgent: false,
    group_key: ["alertname", "cluster"],
    destination_ids: [destinationId],
    policy: profiles.items.find((p) => p.id === "on_call")?.policy,
  });
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

test.beforeAll(async () => {
  await allow(ALLOWED);
  await botInChannel(true);
  await fetch(`${FWH}/_fake/chat/s048`, { method: "DELETE" });
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?type=mattermost&limit=500",
    );
    const connection = connections.items.find((c) => c.name === "Dev Mattermost");
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
    const auto = await admin.call<{ destination: { id: string }; signing_secret: string }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "webhook",
        name: "auto",
        mode: "events",
        events: { url: `${FWH}/hook/auto`, headers: [] },
        proxy: { enabled: false },
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      },
    );
    ids.auto = auto.destination.id;
    await fake(`${FWH}/_fake/secrets/auto`, "PUT", [auto.signing_secret]);
    const base = `${FWH}/chat/s048`;
    const chat = await admin.call<{ destination: { id: string } }>("POST", "/api/v1/destinations", {
      type: "webhook",
      name: "chat",
      mode: "template",
      template: {
        create: {
          method: "POST",
          url: `${base}/messages`,
          headers: [{ name: "Authorization", value: "Bearer {{ .Secrets.token }}" }],
          body: `{"text":${CHAT_TEXT}}`,
          extract: [{ name: "id", path: "$.data.id" }],
        },
        update: {
          method: "PUT",
          url: `${base}/messages/{{ .Response.id }}`,
          headers: [],
          body: `{"text":${CHAT_TEXT}}`,
        },
      },
      proxy: { enabled: false },
      mentions: MENTIONS,
      limiter: { limit: 50, per_seconds: 1 },
    });
    ids.chat = chat.destination.id;
    await admin.call("PUT", `/api/v1/destinations/${ids.chat}/secrets/token`, {
      value: SECRET_VALUE,
    });
    await route(admin, TEAM_MM, ids.alerts);
    await route(admin, TEAM_AUTO, ids.auto);
  } finally {
    await admin.dispose();
  }
  ids.integration = await fakeIntegration("s048");
  await fam("PUT", "/groups/s048", {
    receiver: "s048",
    route: "{}",
    labels: { alertname: "S048" },
  });
});

test.afterAll(async () => {
  await allow(ALLOWED);
  await botInChannel(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

/** Sends a test message from "Test" and waits for its result. */
async function send(page: Page): Promise<void> {
  const panel = page.getByTestId("destination-test-preview");
  const answered = page.waitForResponse(
    (r) => r.url().endsWith("/tests") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "Send test message" }).click();
  await answered;
  await expect(panel.getByRole("button", { name: "Send test message" })).toBeEnabled();
  await expect(panel.getByTestId("test-result")).toBeVisible();
}

/** Opens "Test" on the Destination page with the example, and sends a test message. */
async function sendTest(page: Page): Promise<void> {
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await send(page);
}

function stepTitles(page: Page) {
  return page.getByTestId("test-step-title");
}

test("tests a Mattermost Destination and its button press, and ends its Broken state", async ({
  page,
}) => {
  test.setTimeout(150_000);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/destinations/${ids.alerts}`);
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();

  // 1. "Test" → "Example" → "Send test message" → "Message: Sent" and "Button press: Button presses reach Muster."
  await sendTest(page);
  await expect(stepTitles(page)).toHaveText([
    "Message: Sent",
    "Button press: Button presses reach Muster.",
  ]);
  await expect(page.getByTestId("test-summary")).toHaveText("Test passed");
  await expect(
    page.getByTestId("test-step").first().getByTestId("rendered-header").first(),
  ).toHaveText("Authorization: Bearer [redacted]");
  await shot(page, "destination-test-mattermost");

  // 2. The fake server does not allow Muster's address: the press does not reach Muster.
  await allow("");
  try {
    await sendTest(page);
    await expect(stepTitles(page)).toHaveText(["Message: Sent", `Button press: ${PRESS_ERROR}`]);
    await shot(page, "destination-test-press-not-reached");
  } finally {
    await allow(ALLOWED);
  }

  // 7. "alerts" is Broken after a delivery without the bot in the channel.
  await botInChannel(false);
  await fam("PUT", "/groups/s048/alerts/mm1", {
    labels: { team: TEAM_MM, cluster: "mm1", severity: "warning" },
  });
  await notify(ids.integration, "s048", { reason: "first notification" });
  await expect.poll(() => health(ids.alerts), { timeout: 30_000 }).toBe("broken");
  await expect(page.getByTestId("destination-health")).toHaveText("Broken");
  await page.evaluate(() => {
    document.body.dataset.notReloaded = "yes";
  });
  await botInChannel(true);

  // The message is posted but the press fails: the Destination stays Broken, and the panel says why.
  await allow("");
  try {
    await sendTest(page);
    await expect(stepTitles(page)).toHaveText(["Message: Sent", `Button press: ${PRESS_ERROR}`]);
    await expect(page.getByTestId("test-still-broken")).toHaveText(
      "The test message was posted, but the Destination stays Broken: a test ends the Broken state only when every step succeeds, the button press included.",
    );
    await expect(page.getByTestId("destination-health")).toHaveText("Broken");
    await shot(page, "destination-test-still-broken");
  } finally {
    await allow(ALLOWED);
  }

  // Every step succeeds: "Healthy" without a reload.
  await sendTest(page);
  await expect(page.getByTestId("test-summary")).toHaveText("Test passed");
  await expect(page.getByTestId("destination-health")).toHaveText("Healthy");
  await expect(page.getByTestId("broken-banner")).toHaveCount(0);
  await expect(page.getByTestId("test-still-broken")).toHaveCount(0);
  expect(await page.evaluate(() => document.body.dataset.notReloaded)).toBe("yes");
  expect(await health(ids.alerts)).toBe("healthy");
  await shot(page, "destination-test-healthy");

  // A recent Alert Group of the Destination's Route, by its #N and title, found by the search.
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("searchbox", { name: "Search Alert Groups" }).fill("S048");
  const option = panel.getByLabel("Based on").locator("option", { hasText: /^#\d+ S048/ });
  await expect(option).toHaveCount(1);
  const label = (await option.textContent()) ?? "";
  const number = /^#(\d+)/.exec(label)?.[1] ?? "";
  await panel.getByLabel("Based on").selectOption({ label });
  await send(page);
  await expect(stepTitles(page)).toHaveText([
    "Message: Sent",
    "Button press: Button presses reach Muster.",
  ]);
  await expect(page.getByTestId("rendered-request-body").first()).toContainText(`#${number}`);

  // "Preview" of the same Alert Group: the Root message with its attachment and buttons, in a frame without links.
  await panel.getByRole("tab", { name: "Preview" }).click();
  await expect(panel.getByLabel("Based on")).toHaveValue(/^AG/);
  const frame = panel.frameLocator('iframe[title="Root message in alerts"]');
  await expect(frame.locator("b").first()).toContainText(`#${number}`);
  await expect(frame.getByRole("button", { name: "Ack" })).toBeVisible();
  await expect(frame.locator("a")).toHaveCount(0);
  await expect(frame.locator("body")).not.toContainText("🧪");
  await shot(page, "destination-preview-mattermost");
  expect(csp).toEqual([]);
});

test("tests outgoing webhooks: extracted values, masked Secrets, the response status and limited", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 3. "chat" → "Test" → "Create: Sent" with "Extracted values: id = m1" and Authorization: Bearer [redacted].
  await page.goto(`/destinations/${ids.chat}`);
  await expect(page.getByRole("heading", { name: "chat", level: 1 })).toBeVisible();
  await sendTest(page);
  await expect(stepTitles(page)).toHaveText(["Create: Sent"]);
  const extracted = page.getByTestId("test-step-extracted");
  await expect(extracted.getByText("Extracted values")).toBeVisible();
  await expect(extracted.getByRole("row", { name: "id m1" })).toBeVisible();
  await expect(page.getByTestId("rendered-header").first()).toHaveText(
    "Authorization: Bearer [redacted]",
  );
  expect(await page.content()).not.toContain(SECRET_VALUE);
  await shot(page, "destination-test-chat");
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(page.getByTestId("test-result")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "destination-test-chat-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // 4. "auto" → "Test" → "Event: Sent", response status 200.
  await page.goto(`/destinations/${ids.auto}`);
  await expect(page.getByRole("heading", { name: "auto", level: 1 })).toBeVisible();
  await sendTest(page);
  await expect(stepTitles(page)).toHaveText(["Event: Sent"]);
  await expect(page.getByTestId("test-step-status")).toHaveText("Response status: 200");

  // 6. The limiter of "auto" at 1 per 60 s, spent by a delivery → "Event: The messenger is busy; …"
  const admin = await adminApi();
  try {
    const res = await admin.context.get(`/api/v1/destinations/${ids.auto}`);
    await admin.call(
      "PUT",
      `/api/v1/destinations/${ids.auto}`,
      {
        type: "webhook",
        name: "auto",
        mode: "events",
        events: { url: `${FWH}/hook/auto`, headers: [] },
        proxy: { enabled: false },
        mentions: MENTIONS,
        limiter: { limit: 1, per_seconds: 60 },
      },
      { "If-Match": res.headers().etag ?? "" },
    );
  } finally {
    await admin.dispose();
  }
  const before = await received("auto");
  await fam("PUT", "/groups/s048/alerts/auto1", {
    labels: { team: TEAM_AUTO, cluster: "auto1", severity: "warning" },
  });
  await notify(ids.integration, "s048", { reason: "new alerts added" });
  await expect.poll(() => received("auto"), { timeout: 30_000 }).toBe(before + 1);
  await page.reload();
  await sendTest(page);
  await expect(stepTitles(page)).toHaveText([`Event: ${LIMITED}`]);
  expect(await received("auto")).toBe(before + 1);
  await shot(page, "destination-test-limited");
  expect(csp).toEqual([]);
});

test("a reader of Russian sends a test message at phone width", async ({ browser }) => {
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("s048-test-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "s048-test-ru-1" });
  await admin.dispose();
  try {
    await russianTest(browser);
  } finally {
    const cleanup = await adminApi();
    try {
      await cleanup.call("DELETE", `/api/v1/users/${userId}`);
    } finally {
      await cleanup.dispose();
    }
  }
});

async function russianTest(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "s048-test-ru", "s048-test-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/destinations/${ids.chat}`);
  const panel = page.getByTestId("destination-test-preview");
  await expect(panel.getByRole("heading", { name: "Тест и предпросмотр" })).toBeVisible();
  await panel.getByRole("tab", { name: "Тест" }).click();
  await expect(panel.getByLabel("На основе")).toHaveValue("example");
  const answered = page.waitForResponse((r) => r.url().endsWith("/tests"));
  await panel.getByRole("button", { name: "Отправить тестовое сообщение" }).click();
  await answered;
  await expect(stepTitles(page)).toHaveText(["Создание: Отправлено"]);
  await expect(page.getByTestId("test-summary")).toHaveText("Тест пройден");
  await expect(
    page.getByTestId("test-step-extracted").getByText("Извлечённые значения"),
  ).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "destination-test-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}
