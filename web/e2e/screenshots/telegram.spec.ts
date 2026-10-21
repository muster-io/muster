// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Connect Telegram" (docs/messengers/telegram.md), its sections "Create the Connection" and "Add a
// Destination": the Admin creates the Connection "Telegram" with the fake Telegram server's second bot on the default
// Bot API base URL, which muster dev routes to the fake, checks it before saving and saves it; then adds the Destination
// "alerts" for the channel @muster_alerts, whose Destination check finds its discussion group, and sends a test message.
// It removes what it created.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  ftg,
  removeConnection,
  signInAdmin,
  TELEGRAM_BOT_API,
  TG_SECOND_TOKEN,
} from "../support";
import { capture, cardOf, scrollToTop } from "./capture";

const DOCS = "messengers/telegram";
const CONNECTION = "Telegram";
/** The fake's channel with comments enabled, its discussion group, and the second bot. */
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;
const SECOND_BOT_ID = 234567;

test.describe.configure({ mode: "serial" });

/** The channel with comments enabled and the second bot an admin of the channel and of its discussion group. */
async function prepareChannel(): Promise<void> {
  await ftg("PUT", `/chats/${CHANNEL_ID}`, { linked_chat_id: GROUP_ID });
  for (const chat of [CHANNEL_ID, GROUP_ID]) {
    await ftg("PUT", `/chats/${chat}/members/${SECOND_BOT_ID}`, {
      status: "administrator",
      can_post_messages: true,
      can_edit_messages: true,
    });
  }
}

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    await removeConnection(admin, CONNECTION);
  } finally {
    await admin.dispose();
  }
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  await cleanUp();
  await prepareChannel();
});

test.afterAll(cleanUp);

test("creates a Telegram Connection and a Destination", async ({ page }) => {
  test.setTimeout(180_000);
  await signInAdmin(page);

  // Create the Connection: the form filled in, the result of "Check connection", the saved page.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  await expect(page.getByRole("textbox", { name: "Bot API base URL" })).toHaveValue(
    TELEGRAM_BOT_API,
  );
  await page.getByRole("textbox", { name: "Name" }).fill(CONNECTION);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Bot token").fill(TG_SECOND_TOKEN);
  await expect(page.getByTestId("telegram-http-warning")).toHaveCount(0);
  await capture(page, DOCS, "01-new-connection");

  const check = page.getByRole("button", { name: "Check connection" });
  await check.click();
  const summary = page.getByTestId("connection-check-summary");
  await expect(summary).toHaveText("Connected as @muster_dev_b_bot");
  await expect(page.getByTestId("connection-check-step")).toHaveCount(3);
  await capture(page, DOCS, "02-check-connection", { locator: cardOf(summary) });

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();
  await expect(page.getByTestId("secret-connection-bot-token-status")).toHaveText(/^Set, changed /);
  await expect(page.getByTestId("telegram-http-warning")).toHaveCount(0);
  // The check of the saved Connection records its bot, which the page then names.
  await check.click();
  await expect(page.getByTestId("connection-bot")).toHaveText("Bot: @muster_dev_b_bot");
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, DOCS, "03-saved-connection", { waitFor: page.getByTestId("connection-bot") });

  // Add a Destination: the form with the Connection and the channel, the saved Destination with the discussion group
  // its check found, and the result of a test message.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  const fields = page.getByTestId("telegram-fields");
  await fields.getByLabel("Connection", { exact: true }).selectOption({ label: CONNECTION });
  await fields.getByLabel("Channel", { exact: true }).fill("@muster_alerts");
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("alerts");
  const save = page.getByRole("button", { name: "Save", exact: true });
  await capture(page, DOCS, "04-new-destination", { highlight: save });

  await save.click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();
  await expect(page.getByText("Muster alerts Chat").first()).toBeVisible();
  await capture(page, DOCS, "05-saved-destination");

  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await panel.getByRole("button", { name: "Send test message" }).click();
  await expect(panel.getByTestId("test-summary")).toHaveText("Test passed");
  await scrollToTop(panel);
  await capture(page, DOCS, "06-test-message", { waitFor: panel.getByTestId("test-summary") });
});
