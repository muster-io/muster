// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Bridge Destination form, its check, test and preview against `muster dev` and its fake bridge: Verification step
// 2 of their story. The spec creates its own Bridge Connection "br" on the fake bridge's second bot and checks it. The
// Admin picks "br" and the channel ch-nobot, which the bridge forbids: the save is refused with the bridge's text at
// the channel field and nothing is saved; ch-alerts saves, and the page shows the channel's name "alerts" and the
// messenger "Example chat". The Mention settings offer only "Nobody" and chosen users. "Check" lists "Channel"; "Send
// test message" shows the Bridge hint and passes; "Preview" shows the envelope as JSON. The Route editor's picker reads
// the full label. A reader of Russian sees the form and the page at phone width. No page scrolls sideways at 360 px; no
// Content Security Policy violation. Everything the spec creates is deleted at the end.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  createBridgeConnection,
  createRoute,
  deleteRoute,
  expectNoHorizontalScroll,
  removeBridgeConnection,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const ROUTE = "bridge-dest";
const CHANNEL_FORBIDDEN =
  "The bridge refused the channel: it does not exist, or the bot cannot use it. Details: The bot is not a member of this channel.";
const LABEL = "alerts · Example chat · via br · alerts";
const TEST_HINT =
  "Sends one test message, marked 🧪, through the bridge with the saved settings. No Alert Group changes; when a person presses a button of a test message, nothing changes and Muster answers that it is a test message.";

interface Named {
  id: string;
  name: string;
}

let routeId = "";
let destinationId = "";

async function removeRoute(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === ROUTE)) {
    await deleteRoute(r.id);
  }
}

async function cleanUp(): Promise<void> {
  await resetFakeBridge();
  const admin = await adminApi();
  try {
    await removeRoute(admin);
    await removeBridgeConnection(admin);
  } finally {
    await admin.dispose();
  }
}

/** The selects of "whom in the chat to mention" of every kind of Loud event. */
function everyoneSelects(page: Page) {
  return page.getByTestId("mention-kind").locator('select[id$="-everyone"]');
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  await cleanUp();
  const admin = await adminApi();
  try {
    await createBridgeConnection(admin);
  } finally {
    await admin.dispose();
  }
  routeId = await createRoute(ROUTE, ROUTE, ["alertname"]);
});

test.afterAll(cleanUp);

test("creates a Bridge Destination with a channel the bridge allows, and checks, tests and previews it", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // Destinations → "Create destination" → Bridge → "br": the limiter the fake proposes, none per channel → 30 a minute.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  const fields = page.getByTestId("bridge-fields");
  await expect(fields.getByRole("option", { name: "Example Mattermost" })).toHaveCount(0);
  await expect(fields.getByRole("option", { name: "Example Telegram" })).toHaveCount(0);
  await fields.getByLabel("Connection", { exact: true }).selectOption({ label: "br" });
  await expect(page.getByLabel("Requests")).toHaveValue("30");
  await expect(page.getByLabel("Period in seconds")).toHaveValue("60");
  await expect(fields.getByText("As the messenger names it.")).toBeVisible();

  // The Mention settings offer only "Nobody" and chosen users: no chat-wide mention, no groups.
  const selects = everyoneSelects(page);
  await expect(selects).toHaveCount(6);
  for (const select of await selects.all()) {
    await expect(select.locator("option")).toHaveText(["Nobody"]);
    await expect(select).toBeDisabled();
  }
  await expect(page.getByRole("option", { name: /^@/ })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: /^Group to mention for/ })).toHaveCount(0);
  await expect(page.getByLabel("New Alert Group", { exact: true })).toHaveAccessibleDescription(
    "A Bridge has no mention of the whole chat and no groups: only the chosen users are mentioned, through their accounts linked to this Connection.",
  );

  // ch-nobot → "Save" → refused with the bridge's text at the channel field; nothing is saved.
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("alerts");
  const channel = fields.getByLabel("Channel id", { exact: true });
  await channel.fill("ch-nobot");
  await shot(page, "bridge-destination-new");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const channelError = page.getByTestId("destination-type-channel-error-text");
  await expect(channelError).toHaveText(CHANNEL_FORBIDDEN);
  await expect(page).toHaveURL(/\/destinations\/new$/);
  await shot(page, "bridge-destination-forbidden");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  const admin = await adminApi();
  try {
    const listed = await admin.call<{ items: Named[] }>("GET", "/api/v1/destinations?limit=500");
    expect(listed.items.filter((d) => d.name === "alerts")).toEqual([]);
  } finally {
    await admin.dispose();
  }

  // ch-alerts → "Save" → the page shows the channel's name and the messenger.
  await channel.fill("ch-alerts");
  await expect(channelError).toHaveCount(0);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();
  await expect(page.getByTestId("destination-type")).toHaveText("Example chat");
  await expect(page.getByTestId("bridge-found")).toHaveText("Channel: alerts");
  await expect(page.getByTestId("bridge-channel-name")).toHaveText("alerts");
  await expect(
    page.getByTestId("bridge-fields").getByLabel("Connection", { exact: true }),
  ).toHaveValue(/^CN[0-9A-Z]+$/);

  // "Check" → the item "Channel".
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");
  await expect(page.getByTestId("destination-check-item")).toHaveText(["Channel: ok"]);
  await shot(page, "bridge-destination-check");

  // "Test" explains the test of a Bridge and sends one test message through the bridge.
  const panel = page.getByTestId("destination-test-preview");
  await expect(panel.getByTestId("destination-test")).toContainText(TEST_HINT);
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await panel.getByRole("button", { name: "Send test message" }).click();
  await expect(panel.getByTestId("test-summary")).toHaveText("Test passed");
  await shot(page, "bridge-destination-test");

  // "Preview" shows the envelope Muster would post, as JSON.
  await panel.getByRole("tab", { name: "Preview" }).click();
  const envelope = panel.getByTestId("preview-envelope");
  await expect(envelope).toContainText('"kind": "test"');
  await expect(envelope).toContainText('"channel_id": "ch-alerts"');
  await expect(panel.getByTestId("preview-item-error")).toHaveCount(0);
  await shot(page, "bridge-destination-preview");

  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-destination-360");

  // The Route editor's picker offers the Destination by its full label.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/routes/${routeId}`);
  const picker = page.getByTestId("route-destinations").getByLabel("Add a Destination");
  await expect(picker.locator("option", { hasText: LABEL })).toHaveCount(1);
  await expect(picker.locator("option", { hasText: LABEL })).toHaveText(LABEL);
  await shot(page, "bridge-destination-route");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
});

test("a reader of Russian sees the Bridge Destination at phone width", async ({ browser }) => {
  expect(destinationId).not.toBe("");
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("brd-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "brd-admin-ru-1" });
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
  await signIn(page, "brd-admin-ru", "brd-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/destinations/new");
  await page.getByRole("radio", { name: "Мост", exact: true }).check();
  const fields = page.getByTestId("bridge-fields");
  await expect(fields.getByLabel("Подключение", { exact: true })).toBeVisible();
  await expect(fields.getByLabel("ID канала", { exact: true })).toBeVisible();
  await expect(fields.getByText("Так, как его называет мессенджер.")).toBeVisible();
  await expect(everyoneSelects(page).first().locator("option")).toHaveText(["Никого"]);
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-destination-new-ru-360");

  await page.goto(`/destinations/${destinationId}`);
  await expect(page.getByTestId("bridge-found")).toHaveText("Канал: alerts");
  await expect(page.getByTestId("destination-type")).toHaveText("Example chat");
  await page.getByRole("button", { name: "Проверить", exact: true }).click();
  await expect(page.getByTestId("destination-check-item")).toHaveText(["Канал: успешно"]);
  await expectNoHorizontalScroll(page);
  await shot(page, "bridge-destination-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}
