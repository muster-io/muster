// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Account links in a real browser, against a stubbed API (D305, D309): a Telegram link reads "Telegram ·
// @<username> · for all Telegram bots" with "linked via <Connection>" and its last use on the secondary line, a
// Mattermost link keeps one row per Connection, and the profile offers "Relink Telegram" once a Telegram link exists
// and says that Mattermost links are per Connection — in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AccountLink, LinkableConnection, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { AccountLinksTable } from "./account-links-table";
import { ProfileAccountLinks } from "./profile-account-links";

const TELEGRAM: AccountLink = {
  id: "AL00000000TG01",
  messenger: "telegram",
  connection: { id: "CN000000000TGB", name: "tg-b" },
  external_id: "6901",
  username: "s069_t",
  last_used_at: null,
  created_at: "2026-10-12T09:00:00Z",
};

const MATTERMOST: AccountLink = {
  id: "AL00000000MM01",
  messenger: "mattermost",
  connection: { id: "CN000000000MMB", name: "mm-b" },
  external_id: "u-dana",
  username: "dana",
  last_used_at: null,
  created_at: "2026-10-12T09:00:00Z",
};

const CONNECTIONS: LinkableConnection[] = [
  { id: "CN000000000TGA", name: "Dev Telegram", messenger: "telegram" },
  { id: "CN000000000TGB", name: "tg-b", messenger: "telegram" },
  { id: "CN000000000MMA", name: "Dev Mattermost", messenger: "mattermost" },
];

function providers(children: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the list reads only the time zone
      user: { id: "US0000000000ME", name: "Me", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      method: "local",
      permissions: [],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function mockProfile(links: AccountLink[]) {
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const items = url.startsWith("/api/v1/me/account-links/connections") ? CONNECTIONS : links;
    return Promise.resolve(
      new Response(JSON.stringify({ items, next_cursor: null }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
}

const rows = () => page.getByTestId("account-link-row");

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Account links", () => {
  test("read a Telegram link as valid for all Telegram bots, a Mattermost link per Connection", async () => {
    await render(
      providers(
        <AccountLinksTable
          links={[MATTERMOST, TELEGRAM]}
          loading={false}
          error={null}
          empty="none"
          label="Account links"
        />,
      ),
    );
    await expect
      .element(rows().nth(0).getByTestId("account-link-summary"))
      .toHaveTextContent("Mattermost · mm-b · @dana · Never");
    await expect.element(rows().nth(0).getByTestId("account-link-via")).not.toBeInTheDocument();
    await expect
      .element(rows().nth(1).getByTestId("account-link-summary"))
      .toHaveTextContent("Telegram · @s069_t · for all Telegram bots");
    await expect
      .element(rows().nth(1).getByTestId("account-link-via"))
      .toHaveTextContent("linked via tg-b · Never");
  });

  test("read in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(
      providers(
        <AccountLinksTable
          links={[TELEGRAM]}
          loading={false}
          error={null}
          empty="none"
          label="Account links"
        />,
      ),
    );
    await expect
      .element(page.getByTestId("account-link-summary"))
      .toHaveTextContent("Telegram · @s069_t · для всех Telegram-ботов");
    await expect
      .element(page.getByTestId("account-link-via"))
      .toHaveTextContent("привязан через tg-b · Никогда");
  });

  test("the profile offers Relink Telegram with a Telegram link and explains Mattermost links", async () => {
    mockProfile([MATTERMOST, TELEGRAM]);
    await render(providers(<ProfileAccountLinks />));
    await expect.element(page.getByRole("button", { name: "Relink Telegram" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Link Mattermost" })).toBeVisible();
    await expect
      .element(page.getByTestId("mattermost-links-hint"))
      .toHaveTextContent(
        "Mattermost links are per Connection: two bots on one server need two links.",
      );
    await page.getByRole("button", { name: "Relink Telegram" }).click();
    await expect
      .element(page.getByRole("dialog").getByRole("heading", { name: "Relink Telegram" }))
      .toBeVisible();
  });

  test("the profile offers Link Telegram without one", async () => {
    mockProfile([MATTERMOST]);
    await i18n.changeLanguage("ru");
    await render(providers(<ProfileAccountLinks />));
    await expect.element(page.getByRole("button", { name: "Привязать Telegram" })).toBeVisible();
    await expect
      .element(page.getByTestId("mattermost-links-hint"))
      .toHaveTextContent(
        "Привязки Mattermost — для каждого подключения отдельно: двум ботам на одном сервере нужны две привязки.",
      );
  });
});
