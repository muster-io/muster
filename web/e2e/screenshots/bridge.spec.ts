// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Connect a messenger through a bridge" (docs/messengers/bridge.md), inside its sections "Create
// the Connection" and "Add a Destination": the Admin creates the Bridge Connection "Team chat" on
// https://bridge.example.org with the fake bridge's second bot, polling, which muster dev routes to the fake, checks it
// before saving and saves it, which shows what the bridge offers; then adds the Destination "alerts" for the channel
// ch-alerts and sends a test message. It removes what it created.

import { expect, test, type Page } from "@playwright/test";

import {
  adminApi,
  BRIDGE_EXAMPLE_URL,
  BRIDGE_SECOND_TOKEN,
  removeConnection,
  resetFakeBridge,
  signInAdmin,
} from "../support";
import { capture, cardOf, scrollToTop } from "./capture";

const DOCS = "messengers/bridge";
const CONNECTION = "Team chat";

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

test.beforeAll(async () => {
  await cleanUp();
  await resetFakeBridge();
});
test.afterAll(cleanUp);

test("creates a Bridge Connection and a Destination", async ({ page }) => {
  test.setTimeout(180_000);
  await signInAdmin(page);

  // Create the Connection: the form filled in, the result of "Check connection", the saved page with the info.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  const fields = page.getByTestId("bridge-connection-fields");
  await page.getByRole("textbox", { name: "Name" }).fill(CONNECTION);
  await fields.getByLabel("Base URL").fill(BRIDGE_EXAMPLE_URL);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Token", { exact: true }).fill(BRIDGE_SECOND_TOKEN);
  await expect(fields.getByRole("radio", { name: "Polling" })).toBeChecked();
  await capture(page, DOCS, "01-new-connection");

  const check = page.getByRole("button", { name: "Check connection" });
  await check.click();
  const summary = page.getByTestId("connection-check-summary");
  await expect(summary).toHaveText("The check passed.");
  await capture(page, DOCS, "02-check-connection", { locator: cardOf(summary) });

  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/connections\/CN[0-9A-Z]+$/);
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();
  await check.click();
  const info = page.getByTestId("bridge-info");
  await expect(info.getByTestId("bridge-info-messenger")).toHaveText("Messenger: Fake chat");
  await scrollToTop(info);
  await capture(page, DOCS, "03-saved-connection", { locator: info });

  // Add a Destination: the channel id, the saved Destination with the channel's name, a test message.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Bridge", exact: true }).check();
  const destination = page.getByTestId("bridge-fields");
  await destination.getByLabel("Connection", { exact: true }).selectOption({ label: CONNECTION });
  await destination.getByLabel("Channel id", { exact: true }).fill("ch-alerts");
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("alerts");
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
  await scrollToTop(panel);
  await capture(page, DOCS, "06-test-message", { waitFor: panel.getByTestId("test-summary") });
});
