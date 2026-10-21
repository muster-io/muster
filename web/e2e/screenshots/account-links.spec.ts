// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Link your messenger accounts" (docs/messengers/account-links.md): the Admin links their own
// accounts in their profile, one way after another, through Connections of the fake servers' second bots on the
// addresses muster dev routes to them, made through the API: Telegram with the deep link, whose /start the fake
// Telegram server sends from the Admin's private chat; Mattermost on https://chat.example.org and Slack, each with the
// code its bot sent by direct message; and a Bridge on https://bridge.example.org, first with the token sent to its
// bot, then with the code it sent. Then the list of the four links and the unlinking of the Telegram one. It removes
// the links and the Connections, and puts back the fake bridge's and the fake Slack server's configuration.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  adminApi,
  type Api,
  BRIDGE_EXAMPLE_URL,
  BRIDGE_SECOND_TOKEN,
  CHAT_URL,
  checkConnection,
  createSlackConnection,
  fbr,
  fmm,
  fsl,
  ftg,
  MM_SECOND_TOKEN,
  removeAccountLinks,
  removeConnection,
  resetFakeBridge,
  resetFakeSlack,
  signInAdmin,
  TELEGRAM_BOT_API,
  TG_SECOND_TOKEN,
} from "../support";
import { capture, cardOf } from "./capture";

const DOCS = "messengers/account-links";
const TELEGRAM = "Telegram";
const MATTERMOST = "chat.example.org";
const SLACK = "Example workspace";
const BRIDGE = "Team chat";
/** The Admin's Telegram account. */
const TELEGRAM_ACCOUNT = { id: 7101, username: "oncall_admin", first_name: "Admin" };
/** The Admin's accounts on the fake servers: alice on Mattermost, in the Slack workspace and on the bridge. */
const MATTERMOST_USER = "u-alice";
const SLACK_USER = "U0FAKEALICE";
const BRIDGE_USER = "u-alice";
const CODE = /\b([2-9A-HJKMNP-Z]{8})\b/;

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  const admin = await adminApi();
  try {
    for (const messenger of ["telegram", "mattermost", "slack", "bridge"]) {
      await removeAccountLinks(admin, messenger);
    }
    for (const name of [TELEGRAM, MATTERMOST, SLACK, BRIDGE]) {
      await removeConnection(admin, name);
    }
  } finally {
    await admin.dispose();
  }
  await resetFakeBridge();
  await resetFakeSlack();
}

/** Creates a Connection through the API and checks it, so that Muster knows its bot. */
async function connection(admin: Api, body: Record<string, unknown>): Promise<void> {
  const created = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
    proxy: { enabled: false },
    ...body,
  });
  await checkConnection(admin, created.id);
}

/** The texts of the direct messages one of the fakes recorded for a person, read until the next one has a code. */
async function nextCode(read: () => Promise<string[]>, before: number): Promise<string> {
  let code = "";
  await expect
    .poll(async () => {
      const list = await read();
      code = list.length > before ? (CODE.exec(list.at(-1) ?? "")?.[1] ?? "") : "";
      return code;
    })
    .not.toBe("");
  return code;
}

async function mattermostDms(): Promise<string[]> {
  const all = (await fmm<{ user_id: string; message: string }[] | null>("GET", "/dms")) ?? [];
  return all.filter((d) => d.user_id === MATTERMOST_USER).map((d) => d.message);
}

async function slackDms(): Promise<string[]> {
  const all = (await fsl<{ text: string }[] | null>("GET", `/dms?user=${SLACK_USER}&app=b`)) ?? [];
  return all.map((d) => d.text);
}

async function bridgeDms(): Promise<string[]> {
  const all =
    (await fbr<{ bot: number; user_external_id: string; body: string }[] | null>(
      "GET",
      "/direct-messages",
    )) ?? [];
  return all.filter((d) => d.bot === 2 && d.user_external_id === BRIDGE_USER).map((d) => d.body);
}

function accounts(page: Page): Locator {
  return cardOf(page.getByRole("heading", { name: "Messenger accounts", exact: true }));
}

test.beforeAll(async () => {
  await cleanUp();
  const admin = await adminApi();
  try {
    await connection(admin, {
      type: "telegram",
      name: TELEGRAM,
      bot_token: TG_SECOND_TOKEN,
      bot_api_base_url: TELEGRAM_BOT_API,
      update_mode: "long_polling",
      limiter: { limit: 30, per_seconds: 1 },
    });
    await connection(admin, {
      type: "mattermost",
      name: MATTERMOST,
      server_url: CHAT_URL,
      bot_token: MM_SECOND_TOKEN,
      limiter: { limit: 50, per_seconds: 1 },
    });
    await createSlackConnection(admin, SLACK);
    await connection(admin, {
      type: "bridge",
      name: BRIDGE,
      base_url: BRIDGE_EXAMPLE_URL,
      bot_token: BRIDGE_SECOND_TOKEN,
      event_delivery: "poll",
      limiter: { limit: 10, per_seconds: 1 },
    });
  } finally {
    await admin.dispose();
  }
});

test.afterAll(cleanUp);

test("links Telegram, Mattermost, Slack and a Bridge, and unlinks", async ({ page }) => {
  test.setTimeout(240_000);
  await signInAdmin(page);
  await page.goto("/profile");
  await expect(accounts(page).getByText("No messenger accounts linked yet.")).toBeVisible();

  // Link Telegram: the deep link to the bot, whose /start the Admin's Telegram account sends.
  await page.getByRole("button", { name: "Link Telegram" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("Connection").selectOption({ label: TELEGRAM });
  await dialog.getByRole("button", { name: "Get link" }).click();
  const open = dialog.getByRole("link", { name: "Open Telegram" });
  await expect(open).toHaveAttribute("href", /^https:\/\/t\.me\/muster_dev_b_bot\?start=/);
  const qr = dialog.getByRole("img", { name: "QR code of the Telegram link" });
  await capture(page, DOCS, "01-link-telegram", {
    locator: dialog,
    waitFor: qr,
    mask: [qr],
    highlight: open,
  });
  const start = new URL((await open.getAttribute("href")) ?? "").searchParams.get("start") ?? "";
  await ftg("POST", "/private", {
    token: TG_SECOND_TOKEN,
    from: TELEGRAM_ACCOUNT,
    text: `/start ${start}`,
  });
  await expect(dialog).toBeHidden({ timeout: 15_000 });

  // Link Mattermost: the username, then the code the bot sent by direct message.
  await page.getByRole("button", { name: "Link Mattermost" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Connection").selectOption({ label: MATTERMOST });
  await dialog.getByLabel("Your Mattermost username").fill("alice");
  await capture(page, DOCS, "02-link-mattermost", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Send code" }),
  });
  let before = (await mattermostDms()).length;
  await dialog.getByRole("button", { name: "Send code" }).click();
  await expect(dialog.getByText("5 attempts left")).toBeVisible();
  await capture(page, DOCS, "03-mattermost-code", { locator: dialog });
  await dialog
    .getByLabel("Enter the code the bot sent you in Mattermost")
    .fill(await nextCode(mattermostDms, before));
  await dialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(dialog).toBeHidden();

  // Link Slack: the email, then the code the bot sent in the Messages tab of its app.
  await page.getByRole("button", { name: "Link Slack" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Connection").selectOption({ label: SLACK });
  await dialog.getByLabel("Your Slack email or name").fill("alice@example.org");
  await capture(page, DOCS, "04-link-slack", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Send code" }),
  });
  before = (await slackDms()).length;
  await dialog.getByRole("button", { name: "Send code" }).click();
  await expect(dialog.getByText("5 attempts left")).toBeVisible();
  await dialog
    .getByLabel("Enter the code the bot sent you in Slack")
    .fill(await nextCode(slackDms, before));
  await dialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(dialog).toBeHidden();

  // Link through a Bridge: by the token sent to the bot, then by the code the bot sent.
  await page.getByRole("button", { name: `Link through ${BRIDGE}`, exact: true }).click();
  dialog = page.getByRole("dialog");
  const token = dialog.getByTestId("link-bridge-token");
  await expect(token).toHaveText(/^[A-Za-z0-9_-]{43}$/);
  // The token, made anew in each run, shows as a fixed one of the same form.
  await capture(page, DOCS, "05-bridge-token", {
    locator: dialog,
    replace: [[(await token.textContent()) ?? "", "Xk2f9QhLmP4tRw8ZyN3bVc6DsJ7gHa1EeUo5Ti0KqBn"]],
  });
  await fbr("POST", "/link", { user_id: BRIDGE_USER, token: await token.textContent(), bot: 2 });
  await expect(dialog).toBeHidden({ timeout: 15_000 });

  await page.getByRole("button", { name: `Link through ${BRIDGE}`, exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("tab", { name: "Code" }).click();
  await dialog.getByLabel("Login", { exact: true }).fill("alice");
  before = (await bridgeDms()).length;
  await dialog.getByRole("button", { name: "Send code" }).click();
  await expect(dialog.getByTestId("link-bridge-attempts")).toHaveText("5 attempts left");
  await capture(page, DOCS, "06-bridge-code", { locator: dialog });
  await dialog
    .getByLabel("Enter the code the bot sent you")
    .fill(await nextCode(bridgeDms, before));
  await dialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(dialog).toBeHidden();

  // The four links, and the Telegram one unlinked.
  const rows = accounts(page).getByTestId("account-link-row");
  await expect(rows).toHaveCount(4);
  await capture(page, DOCS, "07-linked-accounts", { locator: accounts(page) });
  await page.getByRole("button", { name: "Unlink @oncall_admin", exact: true }).click();
  dialog = page.getByRole("dialog");
  await capture(page, DOCS, "08-unlink", {
    locator: dialog,
    highlight: dialog.getByRole("button", { name: "Unlink", exact: true }),
  });
  await dialog.getByRole("button", { name: "Unlink", exact: true }).click();
  await expect(rows).toHaveCount(3);
});
