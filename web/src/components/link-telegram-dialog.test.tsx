// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Telegram" in a real browser, against a stubbed API: the account-links hint closes the dialog only when it names
// a Telegram link of the list read again; a hint for a Mattermost link, for a link that went away or without a link
// leaves the deep link open.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AccountLink, LinkableConnection, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { applyHint } from "../lib/live";
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { LinkTelegramDialog } from "./link-telegram-dialog";

const START = "/api/v1/me/account-links/telegram";
const CONNECTIONS: LinkableConnection[] = [
  { id: "CN0000000000TG", name: "Dev Telegram", messenger: "telegram" },
];
const TELEGRAM: AccountLink = {
  id: "AK0000000000T1",
  messenger: "telegram",
  external_id: "7001",
  username: "alice_t",
  created_at: "2026-10-15T10:00:00Z",
};
const MATTERMOST: AccountLink = {
  id: "AK0000000000M1",
  messenger: "mattermost",
  external_id: "u-alice",
  username: "alice",
  created_at: "2026-10-15T10:00:00Z",
};

let links: AccountLink[] = [];
let listed = 0;

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the dialog reads nothing of the user
    user: { id: "US0000000000ME", name: "Me" } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-16T09:00:00Z",
    idle_expires_at: "2026-10-16T09:00:00Z",
    method: "local",
    permissions: [],
  },
  ended: false,
};

beforeEach(async () => {
  links = [];
  listed = 0;
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation(async (input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    if (url.pathname === START) {
      const started = {
        deep_link: "https://t.me/muster_dev_bot?start=token",
        expires_at: new Date(Date.now() + 600_000).toISOString(),
      };
      return new Response(JSON.stringify(started), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      });
    }
    listed += 1;
    return new Response(JSON.stringify({ items: links }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

async function openDialog(): Promise<QueryClient> {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <LinkTelegramDialog connections={CONNECTIONS} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await page.getByRole("button", { name: "Link Telegram" }).click();
  await expect.element(page.getByRole("link", { name: "Open Telegram" })).toBeVisible();
  return queryClient;
}

describe("LinkTelegramDialog", () => {
  test("stays open for a hint that names another link and closes for its Telegram link", async () => {
    const queryClient = await openDialog();

    // A Mattermost link made in another tab.
    links = [MATTERMOST];
    applyHint(queryClient, { type: "account-links", id: MATTERMOST.id });
    await vi.waitFor(() => expect(listed).toBe(1));
    await new Promise((resolve) => setTimeout(resolve, 100));
    await expect.element(page.getByRole("link", { name: "Open Telegram" })).toBeVisible();

    // A link that went away, and a hint without a link, as after a reconnect.
    links = [];
    applyHint(queryClient, { type: "account-links", id: "AK0000000000GO" });
    applyHint(queryClient, { type: "account-links", id: null });
    await vi.waitFor(() => expect(listed).toBe(2));
    await new Promise((resolve) => setTimeout(resolve, 100));
    await expect.element(page.getByRole("link", { name: "Open Telegram" })).toBeVisible();

    // The bot linked the Telegram account.
    links = [MATTERMOST, TELEGRAM];
    applyHint(queryClient, { type: "account-links", id: TELEGRAM.id });
    await vi.waitFor(() => expect(page.getByRole("dialog").query()).toBeNull());
  });
});
