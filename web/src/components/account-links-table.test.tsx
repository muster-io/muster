// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Account links in a real browser, against a stubbed API (D305, D309): a Telegram link reads "Telegram ·
// @<username> · for all Telegram bots" with "linked via <Connection>" and its last use on the secondary line, a
// Mattermost link keeps one row per Connection, a Bridge link reads "<messenger> · <display name> · via <Connection>"
// with the messenger its Connection's bridge names ("Bridge" without connections:read), and the profile offers "Relink
// Telegram" once a Telegram link exists, says that Mattermost links are per Connection and offers "Link through
// <Connection>" for each Bridge Connection — in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  AccountLink,
  BridgeConnection,
  LinkableConnection,
  Permission,
  Session,
} from "../api/gen/model";
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

const BRIDGE: AccountLink = {
  id: "AL00000000BR01",
  messenger: "bridge",
  connection: { id: "CN000000000BRB", name: "br" },
  external_id: "u-bob",
  username: "bob@example.org",
  display_name: "Bob Brown",
  last_used_at: null,
  created_at: "2026-10-17T09:00:00Z",
};

/** The Bridge Connection of BRIDGE, whose info names the messenger. */
const BR_CONNECTION: BridgeConnection = {
  id: "CN000000000BRB",
  type: "bridge",
  name: "br",
  base_url: "http://127.0.0.1:18094",
  event_delivery: "poll",
  limiter: { limit: 10, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-17T09:00:00Z" },
  push_secret_status: { set: false },
  callback_url: "http://127.0.0.1:8081/api/v1/callbacks/bridge/CN000000000BRB",
  info: {
    protocol_version: "1",
    bridge: { name: "Example bridge", version: "1.0.0" },
    messenger: { name: "Example chat" },
    capabilities: {},
    limits: { max_body_length: 4000, edit_max_age_seconds: null },
    user_lookup: "login",
    event_delivery: ["poll", "push"],
  },
  info_read_at: "2026-10-17T09:00:00Z",
  proxy: { enabled: false },
  destination_count: 0,
  created_at: "2026-10-17T09:00:00Z",
  etag: '"1"',
};

const CONNECTIONS: LinkableConnection[] = [
  { id: "CN000000000TGA", name: "Example Telegram", messenger: "telegram" },
  { id: "CN000000000TGB", name: "tg-b", messenger: "telegram" },
  { id: "CN000000000MMA", name: "Example Mattermost", messenger: "mattermost" },
  {
    id: "CN000000000BRB",
    name: "br",
    messenger: "bridge",
    link_methods: ["token"],
    handle_kind: null,
  },
];

function providers(children: ReactNode, permissions: Permission[] = []) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the list reads only the time zone
      user: { id: "US0000000000ME", name: "Me", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
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
    const items = url.startsWith("/api/v1/me/account-links/connections")
      ? CONNECTIONS
      : url.startsWith("/api/v1/connections")
        ? [BR_CONNECTION]
        : links;
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

  test("read a Bridge link as messenger · display name · via Connection, the messenger from its info", async () => {
    mockProfile([]);
    await render(
      providers(
        <AccountLinksTable
          links={[BRIDGE]}
          loading={false}
          error={null}
          empty="none"
          label="Account links"
        />,
        ["connections:read"],
      ),
    );
    await expect
      .element(page.getByTestId("account-link-summary"))
      .toHaveTextContent("Example chat · Bob Brown · via br");
    await expect.element(page.getByTestId("account-link-via")).toHaveTextContent("Never");
  });

  test("read a Bridge link as Bridge without connections:read, and in Russian as Мост", async () => {
    const fetch = vi.spyOn(window, "fetch");
    const table = (
      <AccountLinksTable
        links={[BRIDGE]}
        loading={false}
        error={null}
        empty="none"
        label="Account links"
      />
    );
    const screen = await render(providers(table));
    await expect
      .element(page.getByTestId("account-link-summary"))
      .toHaveTextContent("Bridge · Bob Brown · via br");
    expect(fetch).not.toHaveBeenCalled();
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(providers(table));
    await expect
      .element(page.getByTestId("account-link-summary"))
      .toHaveTextContent("Мост · Bob Brown · через br");
    await expect.element(page.getByTestId("account-link-via")).toHaveTextContent("Никогда");
  });

  test("the profile offers Link through each Bridge Connection and unlinks a Bridge account by its name", async () => {
    mockProfile([BRIDGE]);
    await render(providers(<ProfileAccountLinks />, ["connections:read"]));
    await expect.element(page.getByRole("button", { name: "Link through br" })).toBeVisible();
    await expect
      .element(page.getByTestId("account-link-summary"))
      .toHaveTextContent("Example chat · Bob Brown · via br");
    await expect.element(page.getByRole("button", { name: "Unlink Bob Brown" })).toBeVisible();
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
