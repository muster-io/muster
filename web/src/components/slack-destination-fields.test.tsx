// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination form with the Slack fields in a real browser: only the Slack Connections; the channel picked from the
// bot's channels, read once and filtered by name in the browser with a leading # dropped — a filter never asks the
// server again —, with the note when Slack had more channels;
// the channel id typed by hand and checked as the server does; destination.slack.limiter; @channel and @here without
// groups in the Mention settings; a channel without the bot refused at the channel field with "Invite the bot to the
// channel: /invite @{bot}"; and "#" with the channel's name the check found. The Destination check names the scopes a
// bot token lacks from missing_scopes (D396) and a Slack channel without the bot as the save does. English and Russian.

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
  NullableDestinationCheckCode,
  Permission,
  Session,
  SlackChannelList,
  SlackConnection,
  SlackDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { DestinationCheck } from "./destination-check";
import { DestinationForm } from "./destination-form";
import { defaultMentions } from "./mention-settings";
import {
  SLACK_KIND,
  channelQuery,
  filterChannels,
  isSlackChannelId,
  slackValues,
} from "./slack-destination-fields";

const SET = { set: true, updated_at: "2026-10-20T09:30:00Z" };

function slackConnection(id: string, name: string, bot: string | null): SlackConnection {
  return {
    id,
    type: "slack",
    name,
    event_mode: "socket",
    limiter: { limit: 50, per_seconds: 60 },
    bot_token_status: SET,
    app_token_status: SET,
    slack_signing_secret_status: { set: false },
    callback_url: `http://localhost:8081/api/v1/callbacks/slack/${id}`,
    bot_name: bot,
    bot_user_id: bot === null ? null : "U0MUSTERBOTB",
    team_id: bot === null ? null : "T0MUSTERDEVB",
    team_name: bot === null ? null : "Fake workspace B",
    app_id: null,
    proxy: { enabled: false },
    destination_count: 0,
    created_at: "2026-10-20T09:30:00Z",
    etag: '"1"',
  };
}

const UI = slackConnection("CN0000000000SL", "ui", "muster-dev-bot-b");
const UNCHECKED = slackConnection("CN0000000000SU", "ui-unchecked", null);

const MM: MattermostConnection = {
  id: "CN0000000000MM",
  type: "mattermost",
  name: "Dev Mattermost",
  server_url: "http://127.0.0.1:18065",
  limiter: { limit: 5, per_seconds: 1 },
  bot_token_status: SET,
  proxy: { enabled: false },
  destination_count: 0,
  callback_url: "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000MM",
  created_at: "2026-10-20T09:30:00Z",
  etag: '"1"',
};

const CHANNELS: SlackChannelList = {
  items: [
    { id: "C0FAKEALERTS", name: "alerts", is_private: false },
    { id: "G0FAKEOPS", name: "ops", is_private: true },
  ],
  truncated: false,
};

const DESTINATION: SlackDestination = {
  id: "DS0000000000SL",
  type: "slack",
  name: "alerts",
  connection_id: UI.id,
  channel_id: "C0FAKEALERTS",
  channel_name: "alerts",
  mentions: defaultMentions(),
  limiter: { limit: 30, per_seconds: 60 },
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-20T09:30:00Z",
  etag: '"1"',
};

const WRITE: Permission[] = [
  "destinations:read",
  "destinations:write",
  "destinations:test",
  "connections:read",
  "connections:write",
];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function urlOf(input: RequestInfo | URL): string {
  return typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
}

/** Answers the Connections, the bot's channels and the user directory. */
function mockReads(list: SlackChannelList = CHANNELS) {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = urlOf(input);
    if (url.includes("/channels")) {
      return Promise.resolve(json(200, list));
    }
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(json(200, { items: [MM, UI, UNCHECKED], next_cursor: null }));
    }
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(json(200, { items: [], next_cursor: null }));
    }
    return Promise.resolve(json(404, { title: "not mocked" }));
  });
}

function providers(children: ReactNode, permissions: Permission[] = WRITE) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-20T09:00:00Z",
      idle_expires_at: "2026-10-20T09:00:00Z",
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

function refusal(checkCode: NonNullable<NullableDestinationCheckCode>, detail: string): ApiError {
  return new ApiError(
    422,
    {
      type: "https://muster-io.github.io/muster/problems/validation-failed",
      code: "validation_failed",
      errors: [
        {
          pointer: "/channel_id",
          code: "destination_check_failed",
          check_code: checkCode,
          detail,
        },
      ],
    },
    undefined,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Slack type", () => {
  test("takes the Connection and the channel id, C… or G…, as the server does", () => {
    expect(slackValues(undefined)).toEqual({ connection_id: "", channel_id: "" });
    expect(slackValues(DESTINATION)).toEqual({ connection_id: UI.id, channel_id: "C0FAKEALERTS" });
    expect(SLACK_KIND.check({ connection_id: "", channel_id: " " })).toEqual({
      "/connection_id": "required",
      "/channel_id": "required",
    });
    for (const bad of ["#alerts", "c0fakealerts", "D0FAKEDM", "U0FAKEALICE", "C1"]) {
      expect(isSlackChannelId(bad), bad).toBe(false);
    }
    expect(isSlackChannelId(" G0FAKEOPS ")).toBe(true);
    expect(SLACK_KIND.check({ connection_id: UI.id, channel_id: "#alerts" })).toEqual({
      "/channel_id": "invalid_format",
    });
    expect(SLACK_KIND.defaultLimiter).toEqual({ limit: 30, per_seconds: 60 });
    expect(SLACK_KIND.everyone).toEqual(["channel", "here"]);
    expect(SLACK_KIND.groups).toBe(false);
  });

  test("drops a leading # of the filter, as Slack names channels without it", () => {
    expect(filterChannels(CHANNELS.items, "#Alerts").map((c) => c.id)).toEqual(["C0FAKEALERTS"]);
    expect(filterChannels(CHANNELS.items, " ").map((c) => c.id)).toEqual([
      "C0FAKEALERTS",
      "G0FAKEOPS",
    ]);
    expect(filterChannels(CHANNELS.items, "nothing")).toEqual([]);
    expect(channelQuery("#alerts")).toBe("alerts");
    expect(channelQuery("  ##al ")).toBe("al");
    expect(channelQuery("ops#1")).toBe("ops#1");
    expect(channelQuery("#")).toBe("");
  });
});

describe("DestinationForm with the Slack fields", () => {
  test("picks a channel from the bot's channels, filtered by name, and saves it", async () => {
    const fetch = mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await render(providers(<DestinationForm kind={SLACK_KIND} submitLabel="Save" save={save} />));
    // Only the Slack Connections.
    await expect.element(page.getByRole("option", { name: "ui", exact: true })).toBeInTheDocument();
    await expect
      .element(page.getByRole("option", { name: "Dev Mattermost" }))
      .not.toBeInTheDocument();
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "ui");
    const picker = page.getByLabelText("Channel", { exact: true });
    await expect.element(page.getByRole("option", { name: "#alerts" })).toBeInTheDocument();
    await expect.element(page.getByRole("option", { name: "#ops (private)" })).toBeInTheDocument();
    const listReads = () =>
      fetch.mock.calls.map(([input]) => urlOf(input)).filter((url) => url.includes("/channels"));
    expect(listReads()).toEqual([`/api/v1/connections/${UI.id}/channels`]);
    // The filter, a leading # dropped, works on the list read once: typing asks the server for nothing.
    await userEvent.fill(page.getByLabelText("Filter channels"), "#ALE");
    await expect
      .element(page.getByRole("option", { name: "#ops (private)" }))
      .not.toBeInTheDocument();
    await expect.element(page.getByRole("option", { name: "#alerts" })).toBeInTheDocument();
    await userEvent.fill(page.getByLabelText("Filter channels"), "o");
    await expect.element(page.getByRole("option", { name: "#ops (private)" })).toBeInTheDocument();
    await userEvent.fill(page.getByLabelText("Filter channels"), "#al");
    await expect.element(page.getByRole("option", { name: "#alerts" })).toBeInTheDocument();
    expect(listReads()).toEqual([`/api/v1/connections/${UI.id}/channels`]);
    await userEvent.selectOptions(picker, "C0FAKEALERTS");
    // The pick fills the id and, while the user has not typed one, the name.
    await expect.element(page.getByLabelText("Channel id")).toHaveValue("C0FAKEALERTS");
    // A filter that leaves the picked channel out keeps it as an option.
    await userEvent.fill(page.getByLabelText("Filter channels"), "ops");
    await expect.element(picker).toHaveValue("C0FAKEALERTS");
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveValue("alerts");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "slack",
      name: "alerts",
      connection_id: UI.id,
      channel_id: "C0FAKEALERTS",
      mentions: defaultMentions(),
      limiter: { limit: 30, per_seconds: 60 },
    });
  });

  test("says when Slack had more channels than the list shows, and that no channel matches", async () => {
    mockReads({ ...CHANNELS, truncated: true });
    await render(
      providers(
        <DestinationForm
          kind={SLACK_KIND}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "ui");
    await expect
      .element(page.getByTestId("slack-channels-truncated"))
      .toHaveTextContent(
        "Slack has more channels than the list shows: enter the channel id of a channel that is not listed.",
      );
    await userEvent.fill(page.getByLabelText("Filter channels"), "nothing");
    await expect.element(page.getByText("No channel of the bot matches “nothing”.")).toBeVisible();
  });

  test("takes a channel id typed by hand, and refuses one in another form", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await render(providers(<DestinationForm kind={SLACK_KIND} submitLabel="Save" save={save} />));
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "ui");
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "other");
    await userEvent.fill(page.getByLabelText("Channel id"), "#other");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent(
        "Enter the channel id as Slack shows it in the channel's details: C or G, then upper-case letters and digits, such as C0123456789.",
      );
    expect(save).not.toHaveBeenCalled();
    await userEvent.fill(page.getByLabelText("Channel id"), " C0FAKEOTHER ");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith(expect.objectContaining({ channel_id: "C0FAKEOTHER" }));
  });

  test("offers @channel and @here, and no groups", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={SLACK_KIND}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    const everyone = document.querySelectorAll<HTMLSelectElement>('select[id$="-everyone"]');
    expect(everyone.length).toBe(6);
    for (const select of everyone) {
      expect([...select.options].map((o) => o.textContent)).toEqual([
        "Nobody",
        "@channel",
        "@here",
      ]);
    }
    await expect.element(page.getByRole("button", { name: "Add group" })).not.toBeInTheDocument();
  });

  test("asks to invite the bot to a channel without it, naming the bot, in English and Russian", async () => {
    mockReads();
    const message = "Invite the bot to the channel: /invite @muster-dev-bot-b";
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValue(refusal("bot_not_in_channel", message));
    const screen = await render(
      providers(<DestinationForm kind={SLACK_KIND} submitLabel="Save" save={save} />),
    );
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "ui");
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "other");
    await userEvent.fill(page.getByLabelText("Channel id"), "C0FAKEOTHER");
    await page.getByRole("button", { name: "Save" }).click();
    // The text is the server's: no detail repeats it.
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent(message);
    await expect.element(page.getByLabelText("Channel id")).toHaveFocus();
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(
      providers(<DestinationForm kind={SLACK_KIND} submitLabel="Сохранить" save={save} />),
    );
    await userEvent.selectOptions(page.getByLabelText("Подключение", { exact: true }), "ui");
    await userEvent.fill(page.getByLabelText("Название", { exact: true }), "other");
    await userEvent.fill(page.getByLabelText("ID канала"), "C0FAKEOTHER");
    await page.getByRole("button", { name: "Сохранить" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent("Пригласите бота в канал: /invite @muster-dev-bot-b");
  });

  test("names no bot that no check found, with the server's text as the detail", async () => {
    mockReads();
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValue(refusal("bot_not_in_channel", "Invite the bot to the channel."));
    await render(providers(<DestinationForm kind={SLACK_KIND} submitLabel="Save" save={save} />));
    await userEvent.selectOptions(
      page.getByLabelText("Connection", { exact: true }),
      "ui-unchecked",
    );
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "other");
    await userEvent.fill(page.getByLabelText("Channel id"), "C0FAKEOTHER");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent("Invite the bot to the channel.");
  });

  test("shows # and the channel's name the check found, in Russian too", async () => {
    mockReads();
    const screen = await render(
      providers(
        <DestinationForm
          kind={SLACK_KIND}
          destination={DESTINATION}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByTestId("slack-found")).toHaveTextContent("Channel: #alerts");
    await expect.element(page.getByLabelText("Channel id")).toHaveValue("C0FAKEALERTS");
    await userEvent.fill(page.getByLabelText("Channel id"), "C0FAKEOTHER");
    await expect.element(page.getByTestId("slack-found")).not.toBeInTheDocument();
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(
      providers(
        <DestinationForm
          kind={SLACK_KIND}
          destination={DESTINATION}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByTestId("slack-found")).toHaveTextContent("Канал: #alerts");
    await expect
      .element(page.getByLabelText("Новая группа алертов", { exact: true }))
      .toHaveAccessibleDescription(
        "Кого в канале упоминать целиком: никого, @channel или @here; @everyone и группы Slack не предлагаются. Выбранные пользователи упоминаются через свои аккаунты Slack, привязанные к этому подключению.",
      );
  });

  test("takes only a typed id without connections:write, which the list needs", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={SLACK_KIND}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
        ["destinations:read", "destinations:write", "connections:read"],
      ),
    );
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "ui");
    await expect.element(page.getByTestId("slack-channel-picker")).not.toBeInTheDocument();
    await expect
      .element(
        page.getByText(
          "Only someone who may change Connections can pick from the bot's channels: the list comes from Slack through the bot. Enter the channel id instead.",
        ),
      )
      .toBeVisible();
  });
});

function answer(result: DestinationCheckResult) {
  return vi.spyOn(window, "fetch").mockImplementation(() => Promise.resolve(json(200, result)));
}

describe("the Destination check of a Slack Destination", () => {
  test("names the scopes Slack named from missing_scopes, in the reader's language", async () => {
    answer({
      ok: false,
      checks: [
        {
          name: "channel",
          ok: false,
          code: "missing_scope",
          message: "The bot token lacks the scopes groups:read.",
          missing_scopes: ["groups:read"],
        },
      ],
      health: { state: "healthy" },
      request_log_run: "CR0000000000R1",
    });
    const screen = await render(
      providers(<DestinationCheck destination={DESTINATION} dirty={false} slackBot="b" />),
    );
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-item-reason"))
      .toHaveTextContent("failed The bot token lacks the scopes groups:read.");
    // The server's message says no more than the text.
    await expect.element(page.getByTestId("destination-check-item-detail")).not.toBeInTheDocument();
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(
      providers(<DestinationCheck destination={DESTINATION} dirty={false} slackBot="b" />),
    );
    await page.getByRole("button", { name: "Проверить", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-item-reason"))
      .toHaveTextContent("ошибка У токена бота нет прав groups:read.");
    await expect.element(page.getByTestId("destination-check-item-detail")).not.toBeInTheDocument();
  });

  test("passes with the item channel, and asks to invite the bot as the save does", async () => {
    const fetch = answer({
      ok: true,
      checks: [{ name: "channel", ok: true }],
      health: { state: "healthy" },
      request_log_run: "CR0000000000R1",
    });
    fetch.mockImplementationOnce(() =>
      Promise.resolve(
        json(200, {
          ok: false,
          checks: [
            {
              name: "channel",
              ok: false,
              code: "bot_not_in_channel",
              message: "Invite the bot to the channel: /invite @muster-dev-bot-b",
            },
          ],
          health: { state: "broken", reason: "bot_not_in_channel" },
          request_log_run: "CR0000000000R1",
        }),
      ),
    );
    await render(
      providers(
        <DestinationCheck destination={DESTINATION} dirty={false} slackBot="muster-dev-bot-b" />,
      ),
    );
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-item-reason"))
      .toHaveTextContent("failed Invite the bot to the channel: /invite @muster-dev-bot-b");
    await expect.element(page.getByTestId("destination-check-item-detail")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-item"))
      .toHaveTextContent("Channel: ok");
    await expect
      .element(page.getByTestId("destination-check-summary"))
      .toHaveTextContent("Check passed");
  });
});
