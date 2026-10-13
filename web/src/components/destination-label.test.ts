// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The label of a Destination (D309) and the chat it posts in (D305): "#<channel name>" or the channel id for
// Mattermost, the channel title or the channel as entered for Telegram, the literal host of an outgoing webhook's URL
// and nothing for a host from a template or with user information only, the full label in English and Russian, and the
// duplicate chats — a Telegram @channel and its numeric chat id are one chat, one Mattermost channel id on two servers
// is two.

import { afterEach, beforeEach, describe, expect, test } from "vitest";

import type {
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
