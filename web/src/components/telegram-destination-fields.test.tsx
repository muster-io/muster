// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination form with the Telegram fields in a real browser: only the Telegram Connections and the channel, with
// its help text; the channel's username as the name while untouched; the request a save sends; the Destination check's
// refusals next to the channel — comments not enabled, the bot not an admin of the discussion group — and nothing
// saved; the channel and the discussion group found, read-only, after the save; no chat-wide mention; and "Check" with
// the names of the Telegram checks.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DestinationCheckResult,
  DestinationInput,
  MattermostConnection,
  Permission,
  Session,
  TelegramConnection,
  TelegramDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { DestinationCheck } from "./destination-check";
import { DestinationForm } from "./destination-form";
import { defaultMentions } from "./mention-settings";
import { TELEGRAM_KIND, channelName, telegramValues } from "./telegram-destination-fields";

const NO_COMMENTS =
  "Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.";
const NOT_ADMIN =
  "The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed to post messages.";

const TG: TelegramConnection = {
  id: "CN0000000000TG",
  type: "telegram",
  name: "Dev Telegram",
  bot_api_base_url: "http://127.0.0.1:18081",
  update_mode: "long_polling",
  limiter: { limit: 15, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-09T09:30:00Z" },
  bot_username: "muster_dev_bot",
  proxy: { enabled: false },
  destination_count: 0,
  warnings: ["base_url_uses_http"],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"1"',
};

const MM: MattermostConnection = {
  id: "CN0000000000MM",
  type: "mattermost",
  name: "Dev Mattermost",
  server_url: "http://127.0.0.1:18065",
  limiter: { limit: 5, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-09T09:30:00Z" },
  proxy: { enabled: false },
  destination_count: 0,
  callback_url: "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000MM",
  created_at: "2026-10-09T09:30:00Z",
  etag: '"1"',
};

const DESTINATION: TelegramDestination = {
  id: "DS0000000000TG",
  type: "telegram",
  name: "muster_alerts",
  connection_id: TG.id,
  channel_id: "@muster_alerts",
  channel_title: "Muster alerts",
  discussion_group_id: "-1001000000002",
  discussion_group_title: "Muster alerts Chat",
  mentions: defaultMentions(),
  limiter: { limit: 10, per_seconds: 60 },
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"1"',
};

const WRITE: Permission[] = [
  "destinations:read",
  "destinations:write",
  "destinations:test",
  "connections:read",
];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function checkFailed(...details: string[]): ApiError {
  return new ApiError(
    422,
    {
      type: "https://muster-io.github.io/muster/problems/validation-failed",
      code: "validation_failed",
      errors: details.map((detail) => ({
        pointer: "/channel_id",
        code: "destination_check_failed",
        detail,
      })),
    },
    undefined,
  );
}

function mockReads(check?: DestinationCheckResult) {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(json(200, { items: [MM, TG], next_cursor: null }));
    }
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(json(200, { items: [], next_cursor: null }));
    }
    if (check !== undefined && url.endsWith("/checks")) {
      return Promise.resolve(json(200, check));
    }
    return Promise.resolve(json(404, { title: "not mocked" }));
  });
}

function providers(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      method: "local",
      permissions: WRITE,
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

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Telegram type", () => {
  test("takes the Connection and the channel, and names the Destination after the channel", () => {
    expect(telegramValues(undefined)).toEqual({ connection_id: "", channel_id: "" });
    expect(telegramValues(DESTINATION)).toEqual({
      connection_id: TG.id,
      channel_id: "@muster_alerts",
    });
    expect(channelName(" @muster_alerts ")).toBe("muster_alerts");
    expect(channelName("-1001000000001")).toBe("-1001000000001");
    expect(TELEGRAM_KIND.check({ connection_id: "", channel_id: " " })).toEqual({
      "/connection_id": "required",
      "/channel_id": "required",
    });
    expect(TELEGRAM_KIND.everyone).toEqual([]);
    expect(TELEGRAM_KIND.groups).toBe(false);
    expect(TELEGRAM_KIND.defaultLimiter).toEqual({ limit: 10, per_seconds: 60 });
  });
});

describe("DestinationForm with the Telegram fields", () => {
  test("asks only for the Connection and the channel, and saves them with the channel's name", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await render(
      providers(<DestinationForm kind={TELEGRAM_KIND} submitLabel="Save" save={save} />),
    );
    // Only the Telegram Connections.
    await expect.element(page.getByRole("option", { name: "Dev Telegram" })).toBeInTheDocument();
    await expect
      .element(page.getByRole("option", { name: "Dev Mattermost" }))
      .not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Team")).not.toBeInTheDocument();
    await expect
      .element(page.getByLabelText("Channel", { exact: true }))
      .toHaveAttribute("placeholder", "@username or chat id");
    await expect
      .element(
        page.getByText(
          "Enable comments on the channel and make the bot an admin of the channel and of its discussion group.",
        ),
      )
      .toBeVisible();
    // No chat-wide mention: the choice holds nobody only.
    const everyone = document.querySelectorAll<HTMLSelectElement>('select[id$="-everyone"]');
    expect(everyone.length).toBeGreaterThan(0);
    for (const select of everyone) {
      expect([...select.options].map((o) => o.value)).toEqual(["none"]);
    }
    await expect.element(page.getByLabelText("Requests")).toHaveValue("10");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("60");

    await userEvent.selectOptions(
      page.getByLabelText("Connection", { exact: true }),
      "Dev Telegram",
    );
    await userEvent.fill(page.getByLabelText("Channel", { exact: true }), "@muster_alerts");
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveValue("muster_alerts");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "telegram",
      name: "muster_alerts",
      connection_id: TG.id,
      channel_id: "@muster_alerts",
      mentions: defaultMentions(),
      limiter: { limit: 10, per_seconds: 60 },
    });
  });

  test("shows each failing check next to the channel, and nothing is saved", async () => {
    mockReads();
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValueOnce(checkFailed(NO_COMMENTS))
      .mockRejectedValueOnce(checkFailed(NOT_ADMIN));
    await render(
      providers(<DestinationForm kind={TELEGRAM_KIND} submitLabel="Save" save={save} />),
    );
    await userEvent.selectOptions(
      page.getByLabelText("Connection", { exact: true }),
      "Dev Telegram",
    );
    await userEvent.fill(page.getByLabelText("Channel", { exact: true }), "@no_comments");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent(NO_COMMENTS);
    await expect.element(page.getByLabelText("Channel", { exact: true })).toHaveFocus();
    await userEvent.fill(page.getByLabelText("Channel", { exact: true }), "@muster_alerts");
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .not.toBeInTheDocument();
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent(NOT_ADMIN);
  });

  test("shows the channel and the discussion group found, read-only, while they are the saved ones", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={TELEGRAM_KIND}
          destination={DESTINATION}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect
      .element(page.getByTestId("telegram-channel-title"))
      .toHaveTextContent("Channel: Muster alerts");
    await expect
      .element(page.getByTestId("telegram-discussion-group"))
      .toHaveTextContent("Discussion group: Muster alerts Chat (-1001000000002)");
    expect(page.getByTestId("telegram-found").element().querySelector("input")).toBeNull();
    // Another channel is not the one the group was found for.
    await userEvent.fill(page.getByLabelText("Channel", { exact: true }), "@other");
    await expect.element(page.getByTestId("telegram-found")).not.toBeInTheDocument();
  });

  test("shows the fields in Russian", async () => {
    await i18n.changeLanguage("ru");
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={TELEGRAM_KIND}
          destination={DESTINATION}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect
      .element(page.getByLabelText("Канал", { exact: true }))
      .toHaveAttribute("placeholder", "@username или id чата");
    await expect
      .element(page.getByTestId("telegram-discussion-group"))
      .toHaveTextContent("Группа обсуждения: Muster alerts Chat (-1001000000002)");
  });
});

describe("DestinationCheck of a Telegram Destination", () => {
  test("lists the four checks with ok or their messages", async () => {
    mockReads({
      ok: false,
      health: { state: "healthy" },
      checks: [
        { name: "channel_exists", ok: true },
        { name: "discussion_group", ok: true },
        { name: "bot_rights_channel", ok: true },
        { name: "bot_rights_group", ok: false, message: NOT_ADMIN },
      ],
    });
    await render(providers(<DestinationCheck destination={DESTINATION} dirty={false} />));
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-summary"))
      .toHaveTextContent("Check failed");
    await expect
      .poll(() =>
        page
          .getByTestId("destination-check-item")
          .elements()
          .map((e) => e.textContent),
      )
      .toEqual([
        "Channel exists: ok",
        "Discussion group: ok",
        "Bot rights in the channel: ok",
        `Bot rights in the discussion group: failed ${NOT_ADMIN}`,
      ]);
  });
});
