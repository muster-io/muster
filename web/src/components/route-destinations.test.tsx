// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destinations of the Route editor in a real browser, against a stubbed API (D305, D309): each row reads
// "<name> · <messenger> · via <Connection> · <channel>" and keeps its Health, the "Add a Destination" options read the
// same, and one warning per chat names the Destinations that post in it — a Telegram @channel and its numeric chat id,
// one Mattermost server and channel through two Connections — but not one channel id on another server; the warning
// never blocks a change, and reads in Russian with its plurals. While the Connections are still being read, a row shows
// a placeholder after the name and never the shorter label without its Connection.

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
  Route,
  Session,
  TelegramConnection,
  TelegramDestination,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { mattermostConnection, telegramConnection } from "../test/fixtures/connections";
import {
  mattermostDestination,
  telegramDestination,
  webhookDestination,
} from "../test/fixtures/destinations";
import { type Entry, RouteDestinations } from "./route-destinations";
import { destinationsText } from "./route-list";

function mmConnection(id: string, name: string, serverUrl: string): MattermostConnection {
  return mattermostConnection({ id, name, server_url: serverUrl });
}

function tgConnection(id: string, name: string): TelegramConnection {
  return telegramConnection({ id, name, bot_username: "muster_dev_bot" });
}

function mm(id: string, name: string, connectionId: string): MattermostDestination {
  return mattermostDestination({
    id,
    name,
    connection_id: connectionId,
    team_id: "team-dev",
    channel_id: "ch-alerts",
    channel_name: "alerts",
  });
}

function tg(
  id: string,
  name: string,
  connectionId: string,
  extra: Partial<TelegramDestination> = {},
): TelegramDestination {
  return telegramDestination({
    id,
    name,
    connection_id: connectionId,
    channel_id: "@muster_alerts",
    channel_title: "Muster alerts",
    ...extra,
  });
}

const HOOK: WebhookDestination = webhookDestination({
  id: "DS00000000HOOK",
  name: "hook",
  mode: "events",
  events: { url: "http://127.0.0.1:18093/hook/s070", headers: [] },
});

const CONNECTIONS: Connection[] = [
  mmConnection("CN000000000MMA", "mm-a", "http://127.0.0.1:18065"),
  mmConnection("CN000000000MMB", "mm-b", "http://127.0.0.1:18065/"),
  mmConnection("CN000000000MM2", "mm-2", "http://127.0.0.1:18066"),
  tgConnection("CN000000000TGA", "Example Telegram"),
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

/** Entries with any severity for these Destinations. */
function anyOf(...ids: string[]): Entry[] {
  return ids.map((id) => ({ destination_id: id, severity_condition: "any" }));
}

function Editor({ initial, onChange }: { initial: Entry[]; onChange: (next: Entry[]) => void }) {
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

async function renderEditor(initial: Entry[], onChange: (next: Entry[]) => void = () => {}) {
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
      docs_version: "latest",
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
    await renderEditor(anyOf(TG_A.id, HOOK.id));
    await expect
      .element(rows().nth(0).getByTestId("route-destination-label"))
      .toHaveTextContent("tg-a-alerts · Telegram · via Example Telegram · Muster alerts");
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

  test("show a placeholder until the Connections are read, never a label that changes", async () => {
    let releaseConnections: ((value: void) => void) | undefined;
    const connectionsRead = new Promise<void>((resolve) => {
      releaseConnections = resolve;
    });
    vi.spyOn(window, "fetch").mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.startsWith("/api/v1/destinations")) {
        return json({ items: ALL, next_cursor: null });
      }
      if (url.startsWith("/api/v1/connections")) {
        await connectionsRead;
        return json({ items: CONNECTIONS, next_cursor: null });
      }
      return new Response("{}", { status: 404 });
    });
    const seen = new Set<string>();
    const observer = new MutationObserver(() => {
      for (const label of document.querySelectorAll('[data-testid="route-destination-label"]')) {
        seen.add(label.textContent ?? "");
      }
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true });
    try {
      await renderEditor(anyOf(TG_A.id));
      await expect
        .element(rows().nth(0).getByRole("link", { name: "tg-a-alerts", exact: true }))
        .toBeVisible();
      await expect
        .element(rows().nth(0).getByTestId("route-destination-label-pending"))
        .toBeInTheDocument();
      expect(rows().nth(0).getByTestId("route-destination-label").element().textContent).toBe(
        "tg-a-alerts",
      );

      releaseConnections?.();
      await expect
        .element(rows().nth(0).getByTestId("route-destination-label"))
        .toHaveTextContent("tg-a-alerts · Telegram · via Example Telegram · Muster alerts");
      expect(rows().nth(0).getByTestId("route-destination-label-pending").query()).toBeNull();
    } finally {
      observer.disconnect();
    }
    expect([...seen].filter((text) => text !== "")).toEqual([
      "tg-a-alerts",
      "tg-a-alerts · Telegram · via Example Telegram · Muster alerts",
    ]);
  });

  test("offer every other Destination by its full label, a Broken one with its health", async () => {
    mockReads(ALL);
    await renderEditor(anyOf(TG_A.id));
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
    await renderEditor(anyOf(MM_A.id, MM_B.id, MM_2.id, TG_A.id, HOOK.id), onChange);
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
    expect(onChange).toHaveBeenLastCalledWith(
      anyOf(MM_A.id, MM_B.id, MM_2.id, TG_A.id, HOOK.id, TG_B.id),
    );
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
    await renderEditor(anyOf(MM_A.id, MM_B.id, third.id));
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

  test("give each Destination a Severity condition with its hint, and report the choice", async () => {
    mockReads(ALL);
    const onChange = vi.fn();
    await renderEditor(anyOf(MM_A.id, TG_A.id), onChange);
    await expect
      .element(page.getByRole("group", { name: "Route Destinations", exact: true }))
      .toBeVisible();
    await expect
      .element(
        page.getByText(
          "Where the Route sends the messages of its Alert Groups; each Destination may have a Severity condition",
        ),
      )
      .toBeVisible();
    const condition = page.getByTestId(`route-destination-condition-${MM_A.id}`);
    await expect.element(condition).toHaveValue("any");
    for (const name of ["Any severity", "Severity = critical", "Severity < critical"]) {
      await expect.element(condition.getByRole("option", { name })).toBeInTheDocument();
    }
    await condition.selectOptions("critical");
    expect(onChange).toHaveBeenLastCalledWith([
      { destination_id: MM_A.id, severity_condition: "critical" },
      { destination_id: TG_A.id, severity_condition: "any" },
    ]);
    // Only the changed row's condition changes; a Destination added later starts with any severity.
    await expect
      .element(page.getByTestId(`route-destination-condition-${TG_A.id}`))
      .toHaveValue("any");
    await page.getByLabelText("Add a Destination").selectOptions(HOOK.id);
    expect(onChange).toHaveBeenLastCalledWith([
      { destination_id: MM_A.id, severity_condition: "critical" },
      { destination_id: TG_A.id, severity_condition: "any" },
      { destination_id: HOOK.id, severity_condition: "any" },
    ]);
    // Removing the first row keeps the condition of the second.
    await page.getByRole("button", { name: "Remove mm-a-alerts" }).click();
    expect(onChange).toHaveBeenLastCalledWith([
      { destination_id: TG_A.id, severity_condition: "any" },
      { destination_id: HOOK.id, severity_condition: "any" },
    ]);
  });

  test("shows the refusal of a Destination at its row, and reads in Russian", async () => {
    mockReads(ALL);
    await i18n.changeLanguage("ru");
    const rootRoute = createRootRoute({
      component: () => (
        <RouteDestinations
          id="route"
          value={anyOf(MM_A.id)}
          errors={{ [MM_A.id]: "duplicate" }}
          onChange={() => {}}
        />
      ),
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </I18nextProvider>,
    );
    await expect
      .element(rows().nth(0).getByRole("alert"))
      .toHaveTextContent("Это место доставки указано дважды.");
    const condition = page.getByTestId(`route-destination-condition-${MM_A.id}`);
    for (const name of ["любая важность", "важность = critical", "важность < critical"]) {
      await expect.element(condition.getByRole("option", { name })).toBeInTheDocument();
    }
    await expect
      .element(
        page.getByText(
          "Куда маршрут отправляет сообщения своих групп алертов. У каждого места доставки может быть условие по важности.",
        ),
      )
      .toBeVisible();
  });

  test("lists a Route's Destinations with their conditions and the Teams from the label", async () => {
    const refs = [MM_A, TG_A, HOOK].map((d) => ({
      id: d.id,
      name: d.name,
      type: d.type,
      health: d.health,
    }));
    const base = {
      destinations: refs,
      destination_entries: [
        { destination_id: MM_A.id, severity_condition: "critical" },
        { destination_id: TG_A.id, severity_condition: "below_critical" },
        { destination_id: HOOK.id, severity_condition: "any" },
      ],
      deliver_to_teams: true,
    };
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the text reads only these fields of the Route
    const route = (extra: Partial<Route> = {}) => ({ ...base, ...extra }) as Route;
    expect(destinationsText(i18n.t, route())).toBe(
      "hook, mm-a-alerts (critical), tg-a-alerts (< critical) + Teams from the label",
    );
    expect(destinationsText(i18n.t, route({ deliver_to_teams: false }))).toBe(
      "hook, mm-a-alerts (critical), tg-a-alerts (< critical)",
    );
    expect(destinationsText(i18n.t, route({ destination_entries: [], destinations: [] }))).toBe(
      "+ Teams from the label",
    );
    expect(
      destinationsText(
        i18n.t,
        route({ destination_entries: [], destinations: [], deliver_to_teams: false }),
      ),
    ).toBe("No Destinations");
    // A Route read without entries (a 0.1 replica) shows its Destinations with any severity.
    expect(
      destinationsText(
        i18n.t,
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a read of 0.1
        { destinations: refs, destination_ids: [MM_A.id], deliver_to_teams: false } as Route,
      ),
    ).toBe("mm-a-alerts");
    await i18n.changeLanguage("ru");
    expect(destinationsText(i18n.t, route())).toBe(
      "hook, mm-a-alerts (critical), tg-a-alerts (< critical) + командам из метки",
    );
  });
});
