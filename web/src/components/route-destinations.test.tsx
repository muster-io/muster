// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destinations of the Route editor in a real browser, against a stubbed API (D305, D309): each row reads
// "<name> · <messenger> · via <Connection> · <channel>" and keeps its Health, the "Add a Destination" options read the
// same, and one warning per chat names the Destinations that post in it — a Telegram @channel and its numeric chat id,
// one Mattermost server and channel through two Connections — but not one channel id on another server; the warning
// never blocks a change, and reads in Russian with its plurals.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  Connection,
  Destination,
  MattermostConnection,
  MattermostDestination,
  Permission,
  Session,
  TelegramConnection,
  TelegramDestination,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { defaultMentions } from "./mention-settings";
import { RouteDestinations } from "./route-destinations";

const READ: Pick<
  MattermostDestination,
  "health" | "routes" | "created_at" | "etag" | "mentions" | "limiter"
> = {
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-13T09:00:00Z",
  etag: '"1"',
  mentions: defaultMentions(),
  limiter: { limit: 10, per_seconds: 1 },
};

function mmConnection(id: string, name: string, serverUrl: string): MattermostConnection {
  return {
    id,
    type: "mattermost",
    name,
    server_url: serverUrl,
    limiter: { limit: 50, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: "2026-10-13T09:00:00Z" },
    proxy: { enabled: false },
    destination_count: 1,
    callback_url: `http://127.0.0.1:8081/api/v1/callbacks/mattermost/${id}`,
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
  };
}

function tgConnection(id: string, name: string): TelegramConnection {
  return {
    id,
    type: "telegram",
    name,
    bot_api_base_url: "http://127.0.0.1:18081",
    update_mode: "long_polling",
    limiter: { limit: 30, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: "2026-10-13T09:00:00Z" },
    bot_username: "muster_dev_bot",
    proxy: { enabled: false },
    destination_count: 1,
    warnings: [],
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
  };
}

function mm(id: string, name: string, connectionId: string): MattermostDestination {
  return {
    ...READ,
    id,
    type: "mattermost",
    name,
    connection_id: connectionId,
    team_id: "team-dev",
    channel_id: "ch-alerts",
    channel_name: "alerts",
  };
}

function tg(
  id: string,
  name: string,
  connectionId: string,
  extra: Partial<TelegramDestination> = {},
): TelegramDestination {
  return {
    ...READ,
    id,
    type: "telegram",
    name,
    connection_id: connectionId,
    channel_id: "@muster_alerts",
    channel_title: "Muster alerts",
    ...extra,
  };
}

const HOOK: WebhookDestination = {
  ...READ,
  id: "DS00000000HOOK",
  type: "webhook",
  name: "hook",
  mode: "events",
  events: { url: "http://127.0.0.1:18093/hook/s070", headers: [] },
  proxy: { enabled: false },
  signing_secret_status: { set: false },
  warnings: [],
};

const CONNECTIONS: Connection[] = [
  mmConnection("CN000000000MMA", "mm-a", "http://127.0.0.1:18065"),
  mmConnection("CN000000000MMB", "mm-b", "http://127.0.0.1:18065/"),
  mmConnection("CN000000000MM2", "mm-2", "http://127.0.0.1:18066"),
  tgConnection("CN000000000TGA", "Dev Telegram"),
  tgConnection("CN000000000TGB", "tg-b"),
];

const MM_A = mm("DS0000000000MA", "mm-a-alerts", "CN000000000MMA");
const MM_B = mm("DS0000000000MB", "mm-b-alerts", "CN000000000MMB");
const MM_2 = mm("DS0000000000M2", "mm-2-alerts", "CN000000000MM2");
// tg-a was checked and knows the numeric chat id; tg-b names that chat by its numeric id, unchecked.
const TG_A = tg("DS0000000000TA", "tg-a-alerts", "CN000000000TGA", {
  channel_chat_id: "-1001000000001",
});
const TG_B = tg("DS0000000000TB", "tg-b-alerts", "CN000000000TGB", {
  channel_id: "-1001000000001",
  channel_title: "Muster alerts",
  health: { state: "broken", since: "2026-10-13T08:00:00Z", reason: "chat not found" },
});
const ALL: Destination[] = [MM_A, MM_B, MM_2, TG_A, TG_B, HOOK];

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function mockReads(destinations: Destination[]) {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/destinations")) {
      return Promise.resolve(json({ items: destinations, next_cursor: null }));
    }
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(json({ items: CONNECTIONS, next_cursor: null }));
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

const PERMISSIONS: Permission[] = ["destinations:read", "connections:read", "routes:write"];

function Editor({ initial, onChange }: { initial: string[]; onChange: (next: string[]) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <RouteDestinations
      id="route"
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange(next);
      }}
    />
  );
}

async function renderEditor(initial: string[], onChange: (next: string[]) => void = () => {}) {
  const rootRoute = createRootRoute({
    component: () => <Editor initial={initial} onChange={onChange} />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the editor reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      method: "local",
      permissions: PERMISSIONS,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const rows = () => page.getByTestId("route-destination");
const warnings = () => page.getByTestId("duplicate-chat-warning");

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Destinations of the Route editor", () => {
  test("label each row with its messenger, Connection and channel and keep its Health", async () => {
    mockReads(ALL);
    await renderEditor([TG_A.id, HOOK.id]);
    await expect
      .element(rows().nth(0).getByTestId("route-destination-label"))
      .toHaveTextContent("tg-a-alerts · Telegram · via Dev Telegram · Muster alerts");
    await expect
      .element(rows().nth(0).getByTestId("destination-health"))
      .toHaveTextContent("Healthy");
    await expect
      .element(rows().nth(0).getByRole("link", { name: "tg-a-alerts", exact: true }))
      .toBeVisible();
    await expect
      .element(rows().nth(1).getByTestId("route-destination-label"))
      .toHaveTextContent("hook · Outgoing webhook · 127.0.0.1:18093");
  });

  test("offer every other Destination by its full label, a Broken one with its health", async () => {
    mockReads(ALL);
    await renderEditor([TG_A.id]);
    const picker = page.getByLabelText("Add a Destination");
    await expect
      .element(
        picker.getByRole("option", { name: "mm-a-alerts · Mattermost · via mm-a · #alerts" }),
      )
      .toBeInTheDocument();
    await expect
      .element(
        picker.getByRole("option", { name: "mm-2-alerts · Mattermost · via mm-2 · #alerts" }),
      )
      .toBeInTheDocument();
    await expect
      .element(
        picker.getByRole("option", {
          name: "tg-b-alerts · Telegram · via tg-b · Muster alerts (Broken)",
        }),
      )
      .toBeInTheDocument();
    await expect
      .element(picker.getByRole("option", { name: "hook · Outgoing webhook · 127.0.0.1:18093" }))
      .toBeInTheDocument();
  });

  test("warn once per chat, not for one channel id on another server, without blocking a change", async () => {
    mockReads(ALL);
    const onChange = vi.fn();
    await renderEditor([MM_A.id, MM_B.id, MM_2.id, TG_A.id, HOOK.id], onChange);
    await expect
      .element(warnings())
      .toHaveTextContent(
        "Two Destinations deliver to the same chat: mm-a-alerts and mm-b-alerts. Each message will appear there twice.",
      );
    expect(warnings().elements()).toHaveLength(1);
    await expect.element(warnings()).toHaveAttribute("role", "status");

    // Adding tg-b, whose numeric chat id is tg-a's @channel, adds the Telegram chat's warning.
    const picker = page.getByLabelText("Add a Destination");
    await expect.element(picker).toBeEnabled();
    await picker.selectOptions(TG_B.id);
    expect(onChange).toHaveBeenLastCalledWith([
      MM_A.id,
      MM_B.id,
      MM_2.id,
      TG_A.id,
      HOOK.id,
      TG_B.id,
    ]);
    await expect
      .element(warnings().nth(1))
      .toHaveTextContent(
        "Two Destinations deliver to the same chat: tg-a-alerts and tg-b-alerts. Each message will appear there twice.",
      );
    expect(warnings().elements()).toHaveLength(2);
    for (const w of warnings().elements()) {
      expect(w.textContent).not.toContain("mm-2-alerts");
    }

    // Removing a Destination of a chat is still allowed and ends its warning.
    await page.getByRole("button", { name: "Remove mm-b-alerts" }).click();
    await expect.poll(() => warnings().elements().length).toBe(1);
    await expect
      .element(warnings())
      .toHaveTextContent(
        "Two Destinations deliver to the same chat: tg-a-alerts and tg-b-alerts. Each message will appear there twice.",
      );
  });

  test("names three Destinations of one chat with Russian plurals", async () => {
    const third = mm("DS0000000000M3", "mm-c-alerts", "CN000000000MMA");
    mockReads([...ALL, third]);
    await i18n.changeLanguage("ru");
    await renderEditor([MM_A.id, MM_B.id, third.id]);
    await expect
      .element(warnings())
      .toHaveTextContent(
        "3 места доставки отправляют сообщения в один чат: mm-a-alerts, mm-b-alerts и mm-c-alerts. Каждое сообщение появится в нём 3 раза.",
      );
    await expect
      .element(rows().nth(0).getByTestId("route-destination-label"))
      .toHaveTextContent("mm-a-alerts · Mattermost · через mm-a · #alerts");
    expect(i18n.t("routes.destinations.sameChat", { count: 5, names: "x" })).toBe(
      "5 мест доставки отправляют сообщения в один чат: x. Каждое сообщение появится в нём 5 раз.",
    );
    expect(i18n.t("routes.destinations.sameChat", { count: 21, names: "x" })).toBe(
      "21 место доставки отправляет сообщения в один чат: x. Каждое сообщение появится в нём 21 раз.",
    );
    expect(i18n.t("routes.destinations.sameChatTwo", { names: "a и b" })).toBe(
      "Два места доставки отправляют сообщения в один чат: a и b. Каждое сообщение появится в нём дважды.",
    );
  });
});
