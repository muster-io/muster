// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Connect Mattermost" (docs/messengers/mattermost.md), its sections "Create the Connection" and "Add
// a Destination": the Admin creates the Connection "Mattermost" on https://chat.example.org with the fake Mattermost
// server's second bot, which muster dev routes to the fake, checks it before saving and saves it, which shows the
// callback address; then adds the Destination "alerts" for the channel the bot is a member of and sends a test message,
// whose button press reaches Muster at the callback address. It removes what it created.

import { expect, test, type Page } from "@playwright/test";

import { adminApi, CHAT_URL, MM_SECOND_TOKEN, removeConnection, signInAdmin } from "../support";
import { capture, cardOf, scrollToTop } from "./capture";

const DOCS = "messengers/mattermost";
const CONNECTION = "Mattermost";

test.describe.configure({ mode: "serial" });

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

test.beforeAll(cleanUp);
test.afterAll(cleanUp);

test("creates a Mattermost Connection and a Destination", async ({ page }) => {
  test.setTimeout(180_000);
  await signInAdmin(page);

  // Create the Connection: the form filled in, the result of "Check connection", the saved page.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Mattermost" }).check();
  await page.getByRole("textbox", { name: "Name" }).fill(CONNECTION);
  await page.getByRole("textbox", { name: "Server URL" }).fill(CHAT_URL);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Bot token").fill(MM_SECOND_TOKEN);
  await capture(page, DOCS, "01-new-connection");

  const check = page.getByRole("button", { name: "Check connection" });
  await check.click();
  const summary = page.getByTestId("connection-check-summary");
  await expect(summary).toHaveText("Connected as muster-dev-bot-b");
  await capture(page, DOCS, "02-check-connection", { locator: cardOf(summary) });

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();
  await check.click();
  await expect(page.getByTestId("connection-bot")).toHaveText("Bot: muster-dev-bot-b");
  const callback = page.getByTestId("connection-callback-url");
  await expect(callback).toHaveText(
    /^http:\/\/localhost:8081\/api\/v1\/callbacks\/mattermost\/CN[0-9A-Z]+$/,
  );
  await capture(page, DOCS, "03-saved-connection", { locator: cardOf(callback) });

  // Add a Destination: the team and a channel the bot is a member of, the saved Destination, a test message.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Mattermost" }).check();
  await page.getByLabel("Connection", { exact: true }).selectOption({ label: CONNECTION });
  await page.getByLabel("Team", { exact: true }).selectOption({ label: "dev" });
  await page.getByLabel("Channel", { exact: true }).selectOption({ label: "alerts" });
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("alerts");
  await capture(page, DOCS, "04-new-destination");

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();
  await capture(page, DOCS, "05-saved-destination");

  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await panel.getByRole("button", { name: "Send test message" }).click();
  await expect(panel.getByTestId("test-summary")).toHaveText("Test passed");
  await expect(panel.getByTestId("test-step-title")).toContainText([
    "Message: Sent",
    "Button press:",
  ]);
  await scrollToTop(panel);
  await capture(page, DOCS, "06-test-message", { waitFor: panel.getByTestId("test-summary") });
  const press = panel.getByTestId("test-step").nth(1);
  await expect(press.getByTestId("test-step-title")).toHaveText(
    "Button press: Button presses reach Muster.",
  );
  await capture(page, DOCS, "07-button-press", { locator: press });

  // The Buttons section of the Destination, for "Button sets" of Statuses and commands: its own set of Acknowledge,
  // "24 h" and "Until tomorrow", with the buttons a firing and an acknowledged Root message then show. Not saved.
  const buttons = page.getByTestId("button-set");
  await buttons.scrollIntoViewIfNeeded();
  await page.getByLabel("Own for this Destination").check();
  for (const key of ["resolve", "snooze:3600", "snooze:14400", "preset:week_start"]) {
    await page.getByTestId(`button-set-item-${key}`).uncheck();
  }
  await expect(page.getByTestId("button-set-preview-firing").getByRole("listitem")).toHaveText([
    "Ack",
    "Snooze 24 h",
    "Until tomorrow 09:00",
  ]);
  await capture(page, "concepts/statuses-and-commands", "07-destination-buttons", {
    locator: cardOf(buttons),
  });
});
