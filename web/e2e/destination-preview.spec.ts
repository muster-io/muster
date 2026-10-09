// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Preview panel of the Destination page against `muster dev` and its fake Telegram server and fake receiver:
// Verification step 5 of its story. "tg-alerts" → "Preview" → "Example" shows the example Alert Group's heading without
// the 🧪 mark, the expandable label section and the buttons in a frame with sandbox=""; the template-mode webhook
// "chat" shows "Create", "Update", "Open thread" and "Reply in thread" with their URLs and bodies; the fakes record no
// new message. A reader of Russian previews at phone width. No page scrolls sideways at 360 px; no Content Security
// Policy violation.

import { type Browser, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const FTG = "http://127.0.0.1:18081/_fake";
const FWH = "http://127.0.0.1:18093";
const BOT_ID = 123456;
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;

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

const ids = { telegram: "", chat: "" };

async function telegramMessages(): Promise<number> {
  const res = await fetch(`${FTG}/messages?chat=${CHANNEL_ID}`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of messages
  return ((await res.json()) as unknown[]).length;
}

async function chatMessages(): Promise<number> {
  const res = await fetch(`${FWH}/_fake/chat/s048p`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own state of the chat
  const chat = (await res.json()) as { messages: unknown[]; edits: unknown[]; threads: unknown[] };
  return chat.messages.length + chat.edits.length + chat.threads.length;
}

async function cleanUp(admin: Api): Promise<void> {
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => ["tg-alerts", "chat"].includes(x.name))) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

test.beforeAll(async () => {
  const res = await fetch(`${FTG}/chats/${GROUP_ID}/members/${BOT_ID}`, {
    method: "PUT",
    body: JSON.stringify({
      status: "administrator",
      can_post_messages: true,
      can_edit_messages: true,
    }),
  });
  expect(res.ok).toBe(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?type=telegram&limit=500",
    );
    const connection = connections.items.find((c) => c.name === "Dev Telegram");
    expect(connection, "the Telegram Connection of muster dev").toBeDefined();
    const telegram = await admin.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "telegram",
        name: "tg-alerts",
        connection_id: connection?.id,
        channel_id: "@muster_alerts",
        mentions: MENTIONS,
        limiter: { limit: 30, per_seconds: 1 },
      },
    );
    ids.telegram = telegram.destination.id;
    const base = `${FWH}/chat/s048p`;
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
        open_thread: {
          method: "POST",
          url: `${base}/threads`,
          headers: [],
          body: '{"root":"{{ .Response.id }}"}',
          extract: [{ name: "thread", path: "$.thread.id" }],
        },
        reply_in_thread: {
          method: "POST",
          url: `${base}/threads/{{ .Response.thread }}/messages`,
          headers: [],
          body: '{"text":{{ printf "%s %v" .Event .Notify | toJson }}}',
        },
      },
      proxy: { enabled: false },
      mentions: MENTIONS,
      limiter: { limit: 50, per_seconds: 1 },
    });
    ids.chat = chat.destination.id;
    await admin.call("PUT", `/api/v1/destinations/${ids.chat}/secrets/token`, {
      value: "chat-preview-token",
    });
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("previews a Telegram Root message and the requests of an outgoing webhook without sending", async ({
  page,
}) => {
  const csp = watchCsp(page);
  const telegramBefore = await telegramMessages();
  const chatBefore = await chatMessages();
  await signInAdmin(page);

  // 5. "tg-alerts" → "Preview" → "Example": the heading without the test mark, the expandable labels and the buttons.
  await page.goto(`/destinations/${ids.telegram}`);
  await expect(page.getByRole("heading", { name: "tg-alerts", level: 1 })).toBeVisible();
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Preview" }).click();
  await expect(panel.getByLabel("Based on")).toHaveValue("example");
  await expect(panel.getByRole("heading", { name: "Root message" })).toBeVisible();
  const frameElement = panel.getByTitle("Root message in tg-alerts");
  await expect(frameElement).toHaveAttribute("sandbox", "");
  const frame = panel.frameLocator('iframe[title="Root message in tg-alerts"]');
  await expect(frame.locator("b").first()).toContainText("#1 HighErrorRate");
  await expect(frame.locator("body")).not.toContainText("🧪");
  await expect(frame.locator("body")).not.toContainText("Test message");
  await expect(frame.getByText("Expandable quote")).toBeVisible();
  await expect(frame.locator("blockquote")).toContainText("cluster: prod");
  for (const name of ["Ack", "Resolve", "Snooze 1 h"]) {
    await expect(frame.getByRole("button", { name })).toBeVisible();
  }
  await expect(frame.locator("a")).toHaveCount(0);
  await shot(page, "destination-preview-telegram");

  // "chat" → "Preview": "Create", "Update", "Open thread" and "Reply in thread" with their URLs and bodies.
  await page.goto(`/destinations/${ids.chat}`);
  await expect(page.getByRole("heading", { name: "chat", level: 1 })).toBeVisible();
  await panel.getByRole("tab", { name: "Preview" }).click();
  const items = panel.getByTestId("preview-item");
  await expect(items.getByRole("heading")).toHaveText([
    "Create",
    "Update",
    "Open thread",
    "Reply in thread",
  ]);
  await expect(items.getByTestId("rendered-request-line")).toHaveText([
    `POST ${FWH}/chat/s048p/messages`,
    `PUT ${FWH}/chat/s048p/messages/example-id`,
    `POST ${FWH}/chat/s048p/threads`,
    `POST ${FWH}/chat/s048p/threads/example-thread/messages`,
  ]);
  await expect(items.getByTestId("rendered-request-body").first()).toContainText(
    '"text": "#1 HighErrorRate (firing)"',
  );
  await expect(items.getByTestId("rendered-request-body").nth(2)).toContainText(
    '"root": "example-id"',
  );
  await expect(items.first().getByTestId("rendered-header").first()).toHaveText(
    "Authorization: Bearer [redacted]",
  );
  expect(await page.content()).not.toContain("chat-preview-token");
  await shot(page, "destination-preview-chat");

  // Nothing was sent.
  expect(await telegramMessages()).toBe(telegramBefore);
  expect(await chatMessages()).toBe(chatBefore);
  expect(csp).toEqual([]);
});

test("a reader of Russian previews a Telegram Root message at phone width", async ({ browser }) => {
  const admin = await adminApi();
  const { id: userId, token } = await admin.createUser("s048-preview-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "s048-preview-ru-1" });
  await admin.dispose();
  try {
    await russianPreview(browser);
  } finally {
    const cleanup = await adminApi();
    try {
      await cleanup.call("DELETE", `/api/v1/users/${userId}`);
    } finally {
      await cleanup.dispose();
    }
  }
});

async function russianPreview(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "s048-preview-ru", "s048-preview-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto(`/destinations/${ids.telegram}`);
  const panel = page.getByTestId("destination-test-preview");
  await panel.getByRole("tab", { name: "Предпросмотр" }).click();
  await expect(panel.getByRole("heading", { name: "Корневое сообщение" })).toBeVisible();
  const frame = panel.frameLocator('iframe[title="Корневое сообщение в tg-alerts"]');
  await expect(frame.getByText("Раскрывающаяся цитата")).toBeVisible();
  await expect(frame.getByRole("button", { name: "Ack" })).toBeVisible();
  await expect(
    panel.getByText(
      "Что получит это место доставки; секреты скрыты. Ничего не отправляется, ссылки не открываются.",
    ),
  ).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "destination-preview-ru-360");
  expect(csp).toEqual([]);
  await context.close();
}
