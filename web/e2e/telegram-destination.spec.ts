// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Telegram Destination form, its check and the lost Thread against `muster dev` and its fake Telegram server:
// Verification steps 5 to 8 of their story. The Admin picks "Dev Telegram" and a channel only: @no_comments is refused
// because comments are not enabled, @muster_alerts while the bot is a plain member of the discussion group, and nothing
// is saved; once the bot is an admin there, the Destination is saved and shows the channel and the discussion group it
// found. "Check" lists the four Telegram checks. A Route sends an Alert Group there; with the copy of its post deleted
// in the fake, the next Thread reply goes unattached and the Alert Group's Delivery section says so. A reader of
// Russian sees the Destination and the Delivery section at phone width. No page scrolls sideways at 360 px; no Content
// Security Policy violation.

import { type Browser, expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  advance,
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

/** The control API of the fake Telegram server of `muster dev`. */
const FTG = "http://127.0.0.1:18081/_fake";
const BOT_ID = 123456;
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;
const TEAM = "tgd-team";
const NO_COMMENTS =
  "Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.";
const NOT_ADMIN =
  "The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed to post messages.";
const NOT_ATTACHED = "Thread not attached to the Root message";

interface Named {
  id: string;
  name: string;
}

interface Message {
  message_id: number;
  is_automatic_forward?: boolean;
  forward_origin?: { message_id: number };
}

let destinationId = "";
let integrationId = "";

async function fake(method: string, path: string, body?: unknown): Promise<void> {
  const res = await fetch(`${FTG}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${path}: ${res.status} ${await res.text()}`).toBe(true);
}

async function messages(chat: number): Promise<Message[]> {
  const res = await fetch(`${FTG}/messages?chat=${chat}`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of messages
  return (await res.json()) as Message[];
}

/** The bot in the discussion group: an admin allowed to post, or a plain member. */
async function botInGroup(admin: boolean): Promise<void> {
  await fake(
    "PUT",
    `/chats/${GROUP_ID}/members/${BOT_ID}`,
    admin
      ? { status: "administrator", can_post_messages: true, can_edit_messages: true }
      : { status: "member" },
  );
}

/** Deletes the Route "tgd" and the Destination "muster_alerts", whatever an earlier run left. */
async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === "tgd")) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => x.name === "muster_alerts")) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

/** The Alert Group of the Alert with the label cluster. */
async function groupOf(cluster: string): Promise<string> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${cluster}`).toBeDefined();
    return group?.id ?? "";
  } finally {
    await admin.dispose();
  }
}

async function threadNotAttached(alertGroupId: string): Promise<boolean> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { thread_not_attached: boolean }[] }>(
      "GET",
      `/api/v1/alert-groups/${alertGroupId}/deliveries`,
    );
    return page.items[0]?.thread_not_attached ?? false;
  } finally {
    await admin.dispose();
  }
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

test.beforeAll(async () => {
  await botInGroup(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  await botInGroup(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("creates a Telegram Destination with the channel only, and checks it", async ({ page }) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 5. Destinations → "Create destination" → Telegram → "Dev Telegram" → @no_comments → "Save" → refused.
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Telegram" }).check();
  const fields = page.getByTestId("telegram-fields");
  await expect(fields.getByRole("option", { name: "Dev Mattermost" })).toHaveCount(0);
  await fields.getByLabel("Connection", { exact: true }).selectOption({ label: "Dev Telegram" });
  const channel = fields.getByLabel("Channel", { exact: true });
  await expect(channel).toHaveAttribute("placeholder", "@username or chat id");
  await expect(
    fields.getByText(
      "Enable comments on the channel and make the bot an admin of the channel and of its discussion group.",
    ),
  ).toBeVisible();
  await expect(page.getByTestId("mattermost-fields")).toHaveCount(0);
  // destination.telegram.limiter, 10 per minute, also bounds the checks on the interactive path: a higher limit lets
  // the spec check several times within a minute.
  await expect(page.getByLabel("Requests")).toHaveValue("10");
  await expect(page.getByLabel("Period in seconds")).toHaveValue("60");
  await page.getByLabel("Requests").fill("100");
  await channel.fill("@no_comments");
  await expect(page.getByRole("textbox", { name: "Name", exact: true })).toHaveValue("no_comments");
  await shot(page, "telegram-destination-new");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const channelError = page.getByTestId("destination-type-channel-error-text");
  await expect(channelError).toHaveText(NO_COMMENTS);
  await expect(page).toHaveURL(/\/destinations\/new$/);
  await shot(page, "telegram-destination-no-comments");

  // 6. The bot a plain member of the discussion group → @muster_alerts → refused; an admin again → saved, with the
  // channel and the discussion group found, read-only.
  await botInGroup(false);
  await channel.fill("@muster_alerts");
  await expect(channelError).toHaveCount(0);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(channelError).toHaveText(NOT_ADMIN);
  await expect(page).toHaveURL(/\/destinations\/new$/);
  const admin = await adminApi();
  try {
    const listed = await admin.call<{ items: Named[] }>("GET", "/api/v1/destinations?limit=500");
    expect(
      listed.items.filter((d) => d.name === "muster_alerts" || d.name === "no_comments"),
    ).toEqual([]);
  } finally {
    await admin.dispose();
  }
  await botInGroup(true);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "muster_alerts", level: 1 })).toBeVisible();
  await expect(page.getByTestId("destination-type")).toHaveText("Telegram");
  await expect(page.getByTestId("telegram-discussion-group")).toHaveText(
    `Discussion group: Muster alerts Chat (${GROUP_ID})`,
  );
  await expect(page.getByTestId("telegram-channel-title")).toHaveText("Channel: Muster alerts");
  await expect(page.getByTestId("telegram-found").locator("input")).toHaveCount(0);
  await expect(
    page.getByTestId("telegram-fields").getByLabel("Channel", { exact: true }),
  ).toHaveValue("@muster_alerts");

  // 7. "Check" → the four Telegram checks, all ok → "Check passed".
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");
  await expect(page.getByTestId("destination-check-item")).toHaveText([
    "Channel exists: ok",
    "Discussion group: ok",
    "Bot rights in the channel: ok",
    "Bot rights in the discussion group: ok",
  ]);
  await shot(page, "telegram-destination-check");
  // A failing check shows its message in the list.
  await botInGroup(false);
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check failed");
  await expect(page.getByTestId("destination-check-item").nth(3)).toHaveText(
    `Bot rights in the discussion group: failed ${NOT_ADMIN}`,
  );
  await botInGroup(true);
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");

  await page.setViewportSize({ width: 360, height: 740 });
  await page.reload();
  await expect(page.getByTestId("telegram-discussion-group")).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "telegram-destination-360");
  expect(csp).toEqual([]);
});

test("a Thread that lost the copy of its post shows as not attached in the Delivery section", async ({
  page,
}) => {
  test.setTimeout(120_000);
  expect(destinationId).not.toBe("");
  const csp = watchCsp(page);

  // 8. A Route to the Destination, an Alert Group posted to the channel with its copy in the discussion group.
  integrationId = await fakeIntegration("tgd-ui");
  const api = await adminApi();
  try {
    const profiles = await api.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await api.call("POST", "/api/v1/routes", {
      name: "tgd",
      matchers: [{ label: "team", op: "=", value: TEAM }],
      urgent: false,
      group_key: ["alertname", "cluster"],
      destination_ids: [destinationId],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
  } finally {
    await api.dispose();
  }
  const before = (await messages(CHANNEL_ID)).length;
  await fam("PUT", "/groups/tgd", {
    receiver: "tgd-ui",
    route: "{}",
    labels: { alertname: "Tgd" },
  });
  await fam("PUT", "/groups/tgd/alerts/a", { labels: { team: TEAM, cluster: "t1" } });
  await notify(integrationId, "tgd", { reason: "first notification" });
  const group = await groupOf("t1");
  await expect
    .poll(async () => (await messages(CHANNEL_ID)).length, { timeout: 30_000 })
    .toBe(before + 1);
  const post = (await messages(CHANNEL_ID)).at(-1)?.message_id ?? 0;
  const copyOf = async () =>
    (await messages(GROUP_ID)).find(
      (m) => m.is_automatic_forward === true && m.forward_origin?.message_id === post,
    );
  await expect.poll(async () => (await copyOf()) !== undefined, { timeout: 30_000 }).toBe(true);
  await signInAdmin(page);
  await page.goto(`/alert-groups/${group}`);
  const section = page.getByTestId("alert-group-deliveries");
  await expect(section.getByTestId("delivery-state")).toHaveText(/^Delivered/);
  await expect(section.getByTestId("delivery-thread-not-attached")).toHaveCount(0);

  // The copy is deleted in the fake; the next new Alert's Thread reply is refused, sent unattached, and the section
  // shows the mark without a reload.
  const copy = await copyOf();
  await fake("DELETE", `/chats/${GROUP_ID}/messages/${copy?.message_id ?? 0}`);
  await fam("PUT", "/groups/tgd/alerts/b", { labels: { team: TEAM, cluster: "t1", n: "2" } });
  await notify(integrationId, "tgd", { reason: "new alerts added" });
  await advance(61);
  await expect.poll(() => threadNotAttached(group), { timeout: 30_000 }).toBe(true);
  const row = section.getByTestId("delivery-row");
  await expect(row.getByRole("link", { name: "muster_alerts" })).toBeVisible();
  await expect(row.getByTestId("delivery-thread-not-attached")).toHaveText(NOT_ATTACHED);
  await shot(page, "telegram-thread-not-attached");
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(row.getByTestId("delivery-thread-not-attached")).toBeVisible();
  await expectNoHorizontalScroll(page);
  expect(csp).toEqual([]);
});

test("a reader of Russian sees the Destination and the Delivery section at phone width", async ({
  browser,
}) => {
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("tgd-admin-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "tgd-admin-ru-1" });
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
  await signIn(page, "tgd-admin-ru", "tgd-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto(`/destinations/${destinationId}`);
  await expect(page.getByTestId("telegram-discussion-group")).toHaveText(
    `Группа обсуждения: Muster alerts Chat (${GROUP_ID})`,
  );
  await expect(
    page.getByTestId("telegram-fields").getByLabel("Канал", { exact: true }),
  ).toHaveAttribute("placeholder", "@username или id чата");
  await page.getByRole("button", { name: "Проверить", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Проверка пройдена");
  await expect(page.getByTestId("destination-check-item")).toHaveText([
    "Канал существует: успешно",
    "Группа обсуждения: успешно",
    "Права бота в канале: успешно",
    "Права бота в группе обсуждения: успешно",
  ]);
  await expectNoHorizontalScroll(page);
  await shot(page, "telegram-destination-ru-360");

  const group = await groupOf("t1");
  await page.goto(`/alert-groups/${group}`);
  await expect(
    page.getByTestId("alert-group-deliveries").getByTestId("delivery-thread-not-attached"),
  ).toHaveText("Тред не прикреплён к корневому сообщению");
  await expectNoHorizontalScroll(page);
  await shot(page, "telegram-thread-not-attached-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}
