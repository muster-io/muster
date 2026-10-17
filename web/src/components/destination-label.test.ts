// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The label of a Destination (D309) and the chat it posts in (D305): "#<channel name>" or the channel id for
// Mattermost, the channel title or the channel as entered for Telegram, the literal host of an outgoing webhook's URL
// and nothing for a host from a template or with user information only, the full label in English and Russian, and the
// duplicate chats — a Telegram @channel and its numeric chat id are one chat, one Mattermost channel id on two servers
// is two. A Bridge Destination reads its messenger from the info of its Connection, "Bridge" until a check read it,
// and two Bridge Destinations of one channel through Connections to the same base URL are one chat. The Connections
// list names a Connection by its messenger and the host it calls (C-22.FR-1).

import { afterEach, beforeEach, describe, expect, test } from "vitest";

import type {
  BridgeConnection,
  BridgeDestination,
  Connection,
  Destination,
  MattermostConnection,
  MattermostDestination,
  RequestTemplate,
  TelegramConnection,
  TelegramDestination,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import {
  chatKey,
  connectionAccount,
  connectionMessengerName,
  connectionPlace,
  destinationPlace,
  duplicateChatGroups,
  fullLabel,
  normalizedServer,
  webhookHost,
} from "./destination-label";
import { defaultMentions } from "./mention-settings";

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
    bot_username: null,
    proxy: { enabled: false },
    destination_count: 1,
    warnings: [],
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
  };
}

function brConnection(
  id: string,
  name: string,
  baseUrl: string,
  messenger: string | null = "Fake chat",
): BridgeConnection {
  return {
    id,
    type: "bridge",
    name,
    base_url: baseUrl,
    event_delivery: "poll",
    limiter: { limit: 10, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: "2026-10-17T09:00:00Z" },
    push_secret_status: { set: false },
    callback_url: `http://127.0.0.1:8081/api/v1/callbacks/bridge/${id}`,
    info:
      messenger === null
        ? null
        : {
            protocol_version: "1",
            bridge: { name: "Muster fake bridge", version: "1.0.0" },
            messenger: { name: messenger },
            capabilities: {},
            limits: { max_body_length: 4000, edit_max_age_seconds: null },
            user_lookup: "login",
            event_delivery: ["poll", "push"],
          },
    info_read_at: messenger === null ? null : "2026-10-17T09:00:00Z",
    proxy: { enabled: false },
    destination_count: 1,
    created_at: "2026-10-17T09:00:00Z",
    etag: '"1"',
  };
}

function br(
  id: string,
  name: string,
  connectionId: string,
  extra: Partial<BridgeDestination> = {},
): BridgeDestination {
  return {
    ...READ,
    id,
    type: "bridge",
    name,
    connection_id: connectionId,
    channel_id: "ch-alerts",
    channel_name: "alerts",
    ...extra,
  };
}

function mm(
  id: string,
  name: string,
  connectionId: string,
  extra: Partial<MattermostDestination> = {},
): MattermostDestination {
  return {
    ...READ,
    id,
    type: "mattermost",
    name,
    connection_id: connectionId,
    team_id: "team-dev",
    channel_id: "ch-alerts",
    channel_name: "alerts",
    ...extra,
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

function hook(url: string, mode: WebhookDestination["mode"] = "events"): WebhookDestination {
  const request: RequestTemplate = { method: "POST", url, headers: [] };
  return {
    ...READ,
    id: "DS0000000000WH",
    type: "webhook",
    name: "hook",
    mode,
    events: mode === "template" ? undefined : { url, headers: [] },
    template:
      mode === "events"
        ? undefined
        : { create: request, update: { ...request, url: "http://other.example.org/u" } },
    proxy: { enabled: false },
    signing_secret_status: { set: false },
    warnings: [],
  };
}

const CONNECTIONS = new Map<string, Connection>(
  [
    mmConnection("CN000000000MMA", "mm-a", "http://127.0.0.1:18065"),
    mmConnection("CN000000000MMB", "mm-b", "HTTP://127.0.0.1:18065/"),
    mmConnection("CN000000000MM2", "mm-2", "http://127.0.0.1:18066"),
    tgConnection("CN000000000TGA", "Dev Telegram"),
    tgConnection("CN000000000TGB", "tg-b"),
    brConnection("CN000000000BRA", "br", "http://127.0.0.1:18094"),
    brConnection("CN000000000BRB", "br-b", "HTTP://127.0.0.1:18094/"),
    brConnection("CN000000000BR2", "br-2", "http://127.0.0.1:18095", "Other chat"),
    brConnection("CN000000000BRU", "br-unread", "http://127.0.0.1:18096", null),
  ].map((c) => [c.id, c]),
);

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("webhookHost", () => {
  test("shows the literal host and port, never user information, path or query", () => {
    expect(webhookHost(hook("http://127.0.0.1:18093/hook/s069"))).toBe("127.0.0.1:18093");
    expect(webhookHost(hook("https://user:pw@hooks.example.org/x?token=1"))).toBe(
      "hooks.example.org",
    );
    expect(webhookHost(hook("https://hooks.example.org/{{ .AlertGroup.ID }}"))).toBe(
      "hooks.example.org",
    );
    expect(webhookHost(hook("https://hooks.example.org?x={{ secret `k` }}"))).toBe(
      "hooks.example.org",
    );
    expect(webhookHost(hook("  https://hooks.example.org#{{x}}"))).toBe("hooks.example.org");
  });

  test("shows nothing when the host is not complete literal text or not http(s)", () => {
    expect(webhookHost(hook("https://{{ secret `host` }}/x"))).toBeUndefined();
    expect(webhookHost(hook("https://hooks.{{ .Env }}.example.org/x"))).toBeUndefined();
    expect(webhookHost(hook("https://user:{{ secret `pw` }}@hooks.example.org/x"))).toBeUndefined();
    expect(webhookHost(hook("{{ secret `url` }}"))).toBeUndefined();
    expect(webhookHost(hook("ftp://files.example.org/x"))).toBeUndefined();
    expect(webhookHost(hook("not a url"))).toBeUndefined();
    expect(webhookHost(hook(""))).toBeUndefined();
  });

  test("takes the create request of the mode template and the events URL otherwise", () => {
    expect(webhookHost(hook("http://create.example.org/c", "template"))).toBe("create.example.org");
    expect(webhookHost(hook("http://events.example.org/e", "both"))).toBe("events.example.org");
  });
});

describe("the label", () => {
  test("names the channel of each messenger", () => {
    expect(destinationPlace(mm("DS1", "a", "CN000000000MMA"))).toBe("#alerts");
    expect(destinationPlace(mm("DS1", "a", "CN000000000MMA", { channel_name: null }))).toBe(
      "ch-alerts",
    );
    expect(destinationPlace(tg("DS2", "b", "CN000000000TGA"))).toBe("Muster alerts");
    expect(destinationPlace(tg("DS2", "b", "CN000000000TGA", { channel_title: null }))).toBe(
      "@muster_alerts",
    );
  });

  test("reads Connection · channel in the Destinations list", () => {
    expect(connectionPlace(mm("DS1", "mm-a-alerts", "CN000000000MMA"), CONNECTIONS)).toBe(
      "mm-a · #alerts",
    );
    expect(connectionPlace(tg("DS2", "tg-b-alerts", "CN000000000TGB"), CONNECTIONS)).toBe(
      "tg-b · Muster alerts",
    );
    expect(connectionPlace(hook("http://127.0.0.1:18093/hook"), CONNECTIONS)).toBe(
      "127.0.0.1:18093",
    );
    expect(connectionPlace(hook("https://{{ secret `h` }}/x"), CONNECTIONS)).toBeUndefined();
    // Without connections:read the Connection is unknown and the channel stands alone.
    expect(connectionPlace(mm("DS1", "x", "CN000000000MMA"), new Map())).toBe("#alerts");
  });

  test("reads name · messenger · via Connection · channel in English and Russian", async () => {
    const d: Destination = tg("DS2", "tg-b-alerts", "CN000000000TGB");
    expect(fullLabel(i18n.t, d, CONNECTIONS)).toBe(
      "tg-b-alerts · Telegram · via tg-b · Muster alerts",
    );
    expect(fullLabel(i18n.t, mm("DS1", "mm-a-alerts", "CN000000000MMA"), CONNECTIONS)).toBe(
      "mm-a-alerts · Mattermost · via mm-a · #alerts",
    );
    expect(fullLabel(i18n.t, hook("http://127.0.0.1:18093/hook"), CONNECTIONS)).toBe(
      "hook · Outgoing webhook · 127.0.0.1:18093",
    );
    expect(fullLabel(i18n.t, hook("{{ secret `u` }}"), CONNECTIONS)).toBe(
      "hook · Outgoing webhook",
    );
    expect(fullLabel(i18n.t, d, new Map())).toBe("tg-b-alerts · Telegram · Muster alerts");
    await i18n.changeLanguage("ru");
    expect(fullLabel(i18n.t, d, CONNECTIONS)).toBe(
      "tg-b-alerts · Telegram · через tg-b · Muster alerts",
    );
    expect(fullLabel(i18n.t, hook("http://127.0.0.1:18093/hook"), CONNECTIONS)).toBe(
      "hook · Исходящий вебхук · 127.0.0.1:18093",
    );
  });

  test("reads a Bridge Destination with the messenger of its Connection's info, else Bridge", async () => {
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BRA"), CONNECTIONS)).toBe(
      "alerts · Fake chat · via br · alerts",
    );
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BR2"), CONNECTIONS)).toBe(
      "alerts · Other chat · via br-2 · alerts",
    );
    // No info read yet, or the Connection unknown: the type's name.
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BRU"), CONNECTIONS)).toBe(
      "alerts · Bridge · via br-unread · alerts",
    );
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BRA"), new Map())).toBe(
      "alerts · Bridge · alerts",
    );
    // Without the channel's name, its id.
    const unnamed = br("DS6", "alerts", "CN000000000BRA", { channel_name: null });
    expect(destinationPlace(unnamed)).toBe("ch-alerts");
    expect(fullLabel(i18n.t, unnamed, CONNECTIONS)).toBe("alerts · Fake chat · via br · ch-alerts");
    expect(connectionPlace(br("DS6", "alerts", "CN000000000BRA"), CONNECTIONS)).toBe("br · alerts");
    await i18n.changeLanguage("ru");
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BRU"), CONNECTIONS)).toBe(
      "alerts · Мост · через br-unread · alerts",
    );
    expect(fullLabel(i18n.t, br("DS6", "alerts", "CN000000000BRA"), CONNECTIONS)).toBe(
      "alerts · Fake chat · через br · alerts",
    );
  });
});

function byId(id: string): Connection {
  const c = CONNECTIONS.get(id);
  if (c === undefined) {
    throw new Error(id);
  }
  return c;
}

describe("the Connections list", () => {
  test("names a Bridge by the messenger from its info and the host of its base URL", async () => {
    expect(connectionMessengerName(i18n.t, byId("CN000000000BRA"))).toBe("Fake chat");
    expect(connectionMessengerName(i18n.t, byId("CN000000000BRU"))).toBe("Bridge");
    expect(connectionMessengerName(i18n.t, byId("CN000000000MMA"))).toBe("Mattermost");
    expect(connectionMessengerName(i18n.t, byId("CN000000000TGA"))).toBe("Telegram");
    expect(connectionAccount(byId("CN000000000BRA"))).toBe("127.0.0.1:18094");
    expect(connectionAccount(byId("CN000000000BRU"))).toBe("127.0.0.1:18096");
    expect(
      connectionAccount(brConnection("CN000000000BRX", "x", "https://bridge.example.org/prefix")),
    ).toBe("bridge.example.org");
    expect(connectionAccount(byId("CN000000000MMA"))).toBe("127.0.0.1:18065");
    expect(connectionAccount(byId("CN000000000TGA"))).toBeUndefined();
    expect(
      connectionAccount({ ...tgConnection("CN000000000TGC", "tg"), bot_username: "muster_bot" }),
    ).toBe("@muster_bot");
    await i18n.changeLanguage("ru");
    expect(connectionMessengerName(i18n.t, byId("CN000000000BRU"))).toBe("Мост");
  });
});

describe("the chat of a Destination", () => {
  test("normalises the Mattermost server", () => {
    expect(normalizedServer("HTTP://Chat.Example.org/")).toBe("http://chat.example.org");
    expect(normalizedServer("https://chat.example.org/mm/")).toBe("https://chat.example.org/mm");
    expect(normalizedServer("https://Chat.Example.org/MM/")).toBe("https://chat.example.org/MM");
    expect(normalizedServer("http://127.0.0.1:18065")).toBe("http://127.0.0.1:18065");
  });

  test("is the numeric chat id of a checked Telegram channel, else the channel lower-cased", () => {
    expect(
      chatKey(tg("DS2", "a", "CN000000000TGA", { channel_chat_id: "-1001" }), CONNECTIONS),
    ).toBe("tg:-1001");
    expect(
      chatKey(tg("DS2", "a", "CN000000000TGA", { channel_id: "@Muster_Alerts" }), CONNECTIONS),
    ).toBe("tg:@muster_alerts");
    expect(chatKey(hook("http://x.example.org/"), CONNECTIONS)).toBeUndefined();
  });

  test("groups the Destinations of one chat and keeps two servers apart", () => {
    const destinations: Destination[] = [
      mm("DS1", "mm-a-alerts", "CN000000000MMA"),
      mm("DS2", "mm-b-alerts", "CN000000000MMB"),
      mm("DS3", "mm-2-alerts", "CN000000000MM2"),
      tg("DS4", "tg-a-alerts", "CN000000000TGA", { channel_chat_id: "-1001000000001" }),
      tg("DS5", "tg-b-alerts", "CN000000000TGB", {
        channel_id: "-1001000000001",
        channel_chat_id: null,
      }),
      hook("http://127.0.0.1:18093/hook"),
    ];
    expect(duplicateChatGroups(destinations, CONNECTIONS)).toEqual([
      ["mm-a-alerts", "mm-b-alerts"],
      ["tg-a-alerts", "tg-b-alerts"],
    ]);
  });

  test("keys a Bridge chat by the normalized base URL and the channel id", () => {
    expect(chatKey(br("DS6", "a", "CN000000000BRA"), CONNECTIONS)).toBe(
      "br:http://127.0.0.1:18094:ch-alerts",
    );
    expect(chatKey(br("DS6", "a", "CN000000000BRB"), CONNECTIONS)).toBe(
      "br:http://127.0.0.1:18094:ch-alerts",
    );
    // The channel id is opaque: its case is kept.
    expect(
      chatKey(br("DS6", "a", "CN000000000BRA", { channel_id: "CH-Alerts" }), CONNECTIONS),
    ).toBe("br:http://127.0.0.1:18094:CH-Alerts");
    expect(chatKey(br("DS6", "a", "CN000000000BRA"), new Map())).toBe(
      "br:connection:CN000000000BRA:ch-alerts",
    );
  });

  test("warns about two Bridge Destinations of one channel through Connections to the same base URL", () => {
    expect(
      duplicateChatGroups(
        [
          br("DS1", "br-alerts", "CN000000000BRA"),
          br("DS2", "br-b-alerts", "CN000000000BRB"),
          br("DS3", "br-other-channel", "CN000000000BRA", { channel_id: "ch-alerts-prod" }),
          br("DS4", "br-other-bridge", "CN000000000BR2"),
          mm("DS5", "mm-alerts", "CN000000000MMA"),
        ],
        CONNECTIONS,
      ),
    ).toEqual([["br-alerts", "br-b-alerts"]]);
    // Connections the reader cannot see: only the same Connection is known to be the same bridge.
    expect(
      duplicateChatGroups(
        [br("DS1", "one", "CN000000000BRA"), br("DS2", "two", "CN000000000BRB")],
        new Map(),
      ),
    ).toEqual([]);
    expect(
      duplicateChatGroups(
        [br("DS1", "one", "CN000000000BRA"), br("DS2", "two", "CN000000000BRA")],
        new Map(),
      ),
    ).toEqual([["one", "two"]]);
  });

  test("warns about two Destinations of one Connection in one chat, and not about other channels", () => {
    expect(
      duplicateChatGroups(
        [
          mm("DS1", "one", "CN000000000MMA"),
          mm("DS2", "two", "CN000000000MMA"),
          mm("DS3", "three", "CN000000000MMA", { channel_id: "ch-other" }),
          tg("DS4", "four", "CN000000000TGA"),
          tg("DS5", "five", "CN000000000TGA", { channel_id: "@other" }),
        ],
        CONNECTIONS,
      ),
    ).toEqual([["one", "two"]]);
    // A Connection the reader cannot see: only the same Connection is known to be the same server.
    expect(
      duplicateChatGroups(
        [mm("DS1", "one", "CN000000000MMA"), mm("DS2", "two", "CN000000000MMB")],
        new Map(),
      ),
    ).toEqual([]);
  });
});
