// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The tutorial "First alert in a messenger" (docs/first-alert.md) and its screenshots, step by step through the UI
// against the fake Alertmanager and the fake Telegram server of muster dev: (1) the Integration "Production
// Alertmanager" and its token, which the fake Alertmanager then sends with; (2) the Connection "Telegram" with the fake's
// second bot on the default Bot API base URL, checked; (3) the Destination "alerts" for the channel @muster_alerts,
// checked, and a test message; (4) the Route "checkout" that sends to it; (5) the Admin's Telegram account linked with
// /start through the bot; (6) a critical alert of the service checkout, whose Alert Group the spec finds by the number
// the list shows, with the Destination's preview of its Root message; (7) Ack pressed in Telegram by the Admin's
// account. It ends asserting that the Alert Group is Acknowledged by the Admin and that its Timeline names the
// Transport Telegram, and removes what it created.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  adminApi,
  fam,
  ftg,
  INGEST_URL,
  notify,
  removeAccountLinks,
  removeConnection,
  removeIntegration,
  removeRoute,
  signInAdmin,
  TELEGRAM_BOT_API,
  TG_SECOND_TOKEN,
} from "../support";
import { capture, cardOf, scrollToTop } from "./capture";

const DOCS = "first-alert";
const INTEGRATION = "Production Alertmanager";
const CONNECTION = "Telegram";
const DESTINATION = "alerts";
const ROUTE = "checkout";
/** The fake's channel with comments enabled, its discussion group, and the second bot. */
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;
const SECOND_BOT_ID = 234567;
/** The Admin's Telegram account. */
const TELEGRAM_ACCOUNT = { id: 7101, username: "oncall_admin", first_name: "Admin" };
/** The receiver and the group of the fake Alertmanager. */
const RECEIVER = "production";
const AM_GROUP = "first-alert";
const ALERT = {
  labels: { alertname: "HighErrorRate", service: "checkout", severity: "critical" },
  annotations: {
    summary: "Error rate of checkout is above 5 %",
    description: "5xx responses are 7.2 % of all requests over the last 5 minutes.",
  },
};

interface Message {
  message_id: number;
  text: string;
  is_automatic_forward?: boolean;
  reply_markup?: { inline_keyboard?: { text: string }[][] };
}

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    await removeRoute(admin, ROUTE);
    await removeConnection(admin, CONNECTION);
    await removeAccountLinks(admin, "telegram");
    await removeIntegration(admin, INTEGRATION);
  } finally {
    await admin.dispose();
  }
}

function nav(page: Page, name: string): Locator {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

/** The channel post of the Alert Group number that the second bot sent, once it carries the button Ack. */
async function rootMessage(number: number): Promise<Message> {
  let found: Message | undefined;
  await expect
    .poll(async () => {
      const messages = await ftg<Message[]>("GET", `/messages?chat=${CHANNEL_ID}`);
      // The test message of step 3 shows the example, which may carry the same number: it is marked 🧪.
      found = messages.findLast(
        (m) =>
          m.is_automatic_forward !== true &&
          m.text.includes(`#${number} `) &&
          !m.text.includes("🧪") &&
          (m.reply_markup?.inline_keyboard ?? []).flat().some((b) => b.text === "Ack"),
      );
      return found !== undefined;
    })
    .toBe(true);
  return found ?? { message_id: 0, text: "" };
}

test.beforeAll(async () => {
  await cleanUp();
  await ftg("PUT", `/chats/${CHANNEL_ID}`, { linked_chat_id: GROUP_ID });
  for (const chat of [CHANNEL_ID, GROUP_ID]) {
    await ftg("PUT", `/chats/${chat}/members/${SECOND_BOT_ID}`, {
      status: "administrator",
      can_post_messages: true,
      can_edit_messages: true,
    });
  }
});

test.afterAll(cleanUp);

test("an alert reaches Telegram and is acknowledged from there", async ({ page }) => {
  test.setTimeout(300_000);
  await signInAdmin(page);

  // 1. Create the Integration and its token; the fake Alertmanager sends with the token from now on.
  await nav(page, "Integrations").click();
  await page.getByRole("link", { name: "Create integration" }).click();
  await page.getByLabel("Name", { exact: true }).fill(INTEGRATION);
  await capture(page, DOCS, "01-create-integration");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: INTEGRATION, level: 1 })).toBeVisible();
  const integrationId = new URL(page.url()).pathname.split("/")[2] ?? "";
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("alertmanager");
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  const token = (await dialog.getByTestId("integration-token-value").textContent()) ?? "";
  expect(token).toMatch(/^mstr_int_\S+$/);
  await capture(page, DOCS, "02-integration-token", {
    mask: [
      dialog.getByTestId("integration-token-value"),
      dialog.getByTestId("integration-token-snippet"),
    ],
  });
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await fam("POST", "/receivers", { name: RECEIVER, url: INGEST_URL, token });
  await fam("PUT", `/groups/${AM_GROUP}`, {
    receiver: RECEIVER,
    route: "{}",
    labels: { alertname: ALERT.labels.alertname },
  });

  // 2. Connect Telegram: the default Bot API base URL and the bot token, checked before saving.
  await nav(page, "Connections").click();
  await page.getByRole("link", { name: "Create connection" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  await expect(page.getByRole("textbox", { name: "Bot API base URL" })).toHaveValue(
    TELEGRAM_BOT_API,
  );
  await page.getByRole("textbox", { name: "Name" }).fill(CONNECTION);
  await page.getByRole("button", { name: "Set a value" }).click();
  await page.getByLabel("Bot token").fill(TG_SECOND_TOKEN);
  await page.getByRole("button", { name: "Check connection" }).click();
  const summary = page.getByTestId("connection-check-summary");
  await expect(summary).toHaveText("Connected as @muster_dev_b_bot");
  await capture(page, DOCS, "03-connection-check", { locator: cardOf(summary) });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("heading", { name: CONNECTION, level: 1 })).toBeVisible();

  // 3. Add a Destination for the channel; its check finds the discussion group. A test message reaches the channel.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  const fields = page.getByTestId("telegram-fields");
  await fields.getByLabel("Connection", { exact: true }).selectOption({ label: CONNECTION });
  await fields.getByLabel("Channel", { exact: true }).fill("@muster_alerts");
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(DESTINATION);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  const destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: DESTINATION, level: 1 })).toBeVisible();
  await expect(page.getByText("Muster alerts Chat").first()).toBeVisible();
  await capture(page, DOCS, "04-destination");
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Test" }).click();
  await panel.getByLabel("Based on").selectOption({ label: "Example" });
  await panel.getByRole("button", { name: "Send test message" }).click();
  await expect(panel.getByTestId("test-summary")).toHaveText("Test passed");
  await scrollToTop(panel);
  await capture(page, DOCS, "05-test-message", { waitFor: panel.getByTestId("test-summary") });

  // 4. The Route "checkout": the Matcher service="checkout", the Group key alertname and service, the Destination.
  await nav(page, "Routes").click();
  await page.getByRole("link", { name: "Create route" }).click();
  await page.getByRole("button", { name: "On-call" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill(ROUTE);
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("service");
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill("checkout");
  for (const label of ["severity", "cluster"]) {
    await page.getByRole("button", { name: `Remove ${label} from the Group key` }).click();
  }
  await page.getByRole("textbox", { name: "Group key" }).fill("service");
  await page.getByRole("button", { name: "Add label" }).click();
  await expect(page.getByTestId("group-key-label")).toHaveText(["alertname", "service"]);
  await page.getByLabel("Add a Destination").selectOption(destinationId);
  await expect(page.getByTestId("route-destination")).toHaveCount(1);
  await scrollToTop(page.getByRole("heading", { name: "Create route", level: 1 }));
  // The Destination's label settles once the Destinations and the Connections are read.
  await capture(page, DOCS, "06-route", {
    waitFor: page.getByTestId("route-destination").getByText("Healthy"),
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(
    page
      .getByTestId("route-row")
      .filter({ has: page.getByRole("link", { name: ROUTE, exact: true }) }),
  ).toContainText('service="checkout"');

  // 5. Link the Admin's Telegram account through the bot: the deep link's /start reaches the bot.
  await page.goto("/profile");
  await page.getByRole("button", { name: "Link Telegram" }).click();
  const link = page.getByRole("dialog");
  await link.getByLabel("Connection").selectOption({ label: CONNECTION });
  await link.getByRole("button", { name: "Get link" }).click();
  const open = link.getByRole("link", { name: "Open Telegram" });
  await expect(open).toHaveAttribute(
    "href",
    /^https:\/\/t\.me\/muster_dev_b_bot\?start=[A-Za-z0-9_-]+$/,
  );
  const qr = link.getByRole("img", { name: "QR code of the Telegram link" });
  const expires = link.getByText(/^This link works once and expires at \d\d:\d\d\.$/);
  await capture(page, DOCS, "07-link-telegram", {
    locator: link,
    waitFor: qr,
    mask: [qr, expires],
    highlight: open,
  });
  const start = new URL((await open.getAttribute("href")) ?? "").searchParams.get("start") ?? "";
  await ftg("POST", "/private", {
    token: TG_SECOND_TOKEN,
    from: TELEGRAM_ACCOUNT,
    text: `/start ${start}`,
  });
  await expect(link).toBeHidden({ timeout: 15_000 });
  const accounts = cardOf(page.getByRole("heading", { name: "Messenger accounts", exact: true }));
  await expect(accounts.getByTestId("account-link-summary")).toHaveText(
    "Telegram · @oncall_admin · for all Telegram bots",
  );
  await capture(page, DOCS, "08-telegram-linked", { locator: accounts });

  // 6. A critical alert of the service checkout, as the Quick start's curl sends it; the list shows its Alert Group.
  await fam("PUT", `/groups/${AM_GROUP}/alerts/a1`, ALERT);
  await notify(integrationId, AM_GROUP, { reason: "first notification" });
  await page.goto("/alert-groups");
  // The row of this spec's Alert Group: its title, its Route and its Integration.
  const row = page
    .getByRole("row")
    .filter({
      has: page.getByTestId("alert-group-title").getByText(ALERT.labels.alertname, { exact: true }),
    })
    .filter({ has: page.getByRole("link", { name: ROUTE, exact: true }) })
    .filter({ hasText: INTEGRATION });
  await expect(row).toHaveCount(1);
  const numberText =
    (await row
      .getByRole("link", { name: /^#\d+$/ })
      .first()
      .textContent()) ?? "";
  const number = Number(numberText.replace("#", ""));
  expect(number).toBeGreaterThan(0);
  await row.getByRole("link", { name: numberText, exact: true }).first().click();
  const status = page.getByTestId("alert-group-status");
  await expect(status).toHaveText("Firing");
  await expect(page.getByTestId("alert-group-number")).toHaveText(`#${number}`);
  await capture(page, DOCS, "09-alert-group", { waitFor: status });
  const root = await rootMessage(number);

  // The Destination's preview shows the Root message that Muster posted in the channel.
  await page.goto(`/destinations/${destinationId}`);
  const preview = page.getByTestId("destination-test-preview");
  await preview.getByRole("tab", { name: "Preview" }).click();
  await preview
    .getByRole("searchbox", { name: "Search Alert Groups" })
    .fill(ALERT.labels.alertname);
  const option = preview.getByLabel("Based on").locator("option", { hasText: `#${number} ` });
  await expect(option).toHaveCount(1);
  await preview.getByLabel("Based on").selectOption({ label: (await option.textContent()) ?? "" });
  const frame = preview.frameLocator("iframe");
  await expect(frame.locator("body")).toContainText(`#${number}`);
  await preview.scrollIntoViewIfNeeded();
  await capture(page, DOCS, "10-preview", { locator: preview });

  // 7. The Admin presses Ack under the Root message in Telegram.
  const pressed = await ftg<{ callback_query_id: string }>("POST", "/press", {
    chat: CHANNEL_ID,
    message_id: root.message_id,
    from: TELEGRAM_ACCOUNT,
    button: "Ack",
  });
  await expect
    .poll(async () => {
      const answers = await ftg<{ callback_query_id: string; text: string }[]>("GET", "/answers");
      return answers.find((a) => a.callback_query_id === pressed.callback_query_id)?.text;
    })
    .toBe("Done: Acknowledge");
  await page.goto("/alert-groups");
  await row
    .getByRole("link", { name: `#${number}`, exact: true })
    .first()
    .click();
  await expect(status).toHaveText("Acknowledged");
  await expect(page.getByTestId("alert-group-owner")).toHaveText("Owner: admin");
  const acknowledged = page.getByTestId("timeline-entry").filter({
    has: page.getByTestId("timeline-actor").getByText("admin via Telegram", { exact: true }),
  });
  await expect(acknowledged).toHaveCount(1);
  await expect(acknowledged).toContainText("Acknowledged");
  // The edit of the Root message that shows the acknowledgement has reached Telegram.
  const delivery = page.getByTestId("alert-group-deliveries").getByTestId("delivery-state");
  await expect(async () => {
    await page.reload();
    await expect(delivery).toHaveText("Delivered (opens in a new tab)", { timeout: 2000 });
  }).toPass();
  await expect(acknowledged).toHaveCount(1);
  await acknowledged.scrollIntoViewIfNeeded();
  await capture(page, DOCS, "11-acknowledged", { waitFor: acknowledged });
});
