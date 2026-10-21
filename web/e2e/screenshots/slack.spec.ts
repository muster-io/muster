// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Connect Slack" (docs/messengers/slack.md), inside its sections "Create the Connection" and
// "Invite the bot and create Destinations": the Admin creates the Connection "Slack" in Socket Mode with the tokens of
// the fake Slack server's second app, which muster dev routes to the fake, checks it before saving and saves it; then
// adds the Destination "alerts" for the channel #alerts the bot is in and sends a test message. The request line of
// the test message, which names the fake server under muster dev, is masked. It removes what it created.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  removeConnection,
  resetFakeSlack,
  signInAdmin,
  SLACK_SECOND_APP_TOKEN,
  SLACK_SECOND_BOT_TOKEN,
} from "../support";
import { capture, cardOf, scrollToTop } from "./capture";

const DOCS = "messengers/slack";
const CONNECTION = "Slack";

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

async function typeSecret(page: Page, testId: string, label: string, value: string): Promise<void> {
  await page.getByTestId(testId).getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel(label, { exact: true }).fill(value);
}

test.beforeAll(async () => {
  await cleanUp();
  await resetFakeSlack();
});
test.afterAll(cleanUp);

test("creates a Slack Connection and a Destination", async ({ page }) => {
  test.setTimeout(180_000);
  await signInAdmin(page);

  // Create the Connection: the form filled in, the result of "Check connection", the saved page.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Slack", exact: true }).check();
  const fields = page.getByTestId("slack-connection-fields");
  await expect(fields.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
  await page.getByRole("textbox", { name: "Name" }).fill(CONNECTION);
  await typeSecret(page, "secret-connection-bot-token", "Bot token", SLACK_SECOND_BOT_TOKEN);
  await typeSecret(page, "secret-connection-app-token", "App-level token", SLACK_SECOND_APP_TOKEN);
  await capture(page, DOCS, "01-new-connection");

  const check = page.getByRole("button", { name: "Check connection" });
  await check.click();
  const summary = page.getByTestId("connection-check-summary");
  await expect(summary).toHaveText("Connected as muster-dev-bot-b");
  await expect(page.getByTestId("connection-check-step")).toHaveCount(3);
  await capture(page, DOCS, "02-check-connection", { locator: cardOf(summary) });

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();
  await check.click();
  await expect(page.getByTestId("connection-bot")).toHaveText(
    "Bot: muster-dev-bot-b · Workspace: Example workspace B · App: A0MUSTERDEVB",
  );
  await page.evaluate(() => window.scrollTo(0, 0));
  await capture(page, DOCS, "03-saved-connection", { waitFor: page.getByTestId("connection-bot") });

  // Invite the bot and create Destinations: the channels the bot is in, the saved Destination, a test message.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Slack", exact: true }).check();
  const destination = page.getByTestId("slack-fields");
  await destination.getByLabel("Connection", { exact: true }).selectOption({ label: CONNECTION });
  const picker = destination.getByLabel("Channel", { exact: true });
  await expect(picker.getByRole("option", { name: "#alerts" })).toHaveCount(1);
  await picker.selectOption({ label: "#alerts" });
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("alerts");
  await capture(page, DOCS, "04-new-destination");

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: "alerts", level: 1 })).toBeVisible();
  await expect(page.getByTestId("slack-channel-name")).toHaveText("#alerts");
  await capture(page, DOCS, "05-saved-destination");

  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await panel.getByRole("button", { name: "Send test message" }).click();
  await expect(panel.getByTestId("test-summary")).toHaveText("Test passed");
  await scrollToTop(panel);
  await capture(page, DOCS, "06-test-message", {
    waitFor: panel.getByTestId("test-summary"),
    mask: [panel.getByTestId("rendered-request-line")],
  });
});
