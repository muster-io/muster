// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination form with the Bridge fields in a real browser: only the Bridge Connections and the channel id, with
// "As the messenger names it." as its hint; the limiters proposed from half of the bridge's rates (C-22.FR-18), and the
// defaults without them; the checks a save repeats and the request it sends; nobody and chosen users only in the
// Mention settings; the channel the bridge forbids refused at the channel field with the bridge's text; and the
// channel's name the check found. English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  BridgeConnection,
  BridgeDestination,
  BridgeInfo,
  DestinationInput,
  MattermostConnection,
  Permission,
  Session,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { connectionLimiterProposal } from "./bridge-connection-fields";
import { BRIDGE_KIND, bridgeValues, destinationLimiterProposal } from "./bridge-destination-fields";
import { DestinationForm } from "./destination-form";
import { defaultMentions } from "./mention-settings";

const FORBIDDEN_TEXT =
  "The bridge refused the channel: it does not exist, or the bot cannot use it. Details: The bot is not a member of this channel.";

const INFO: BridgeInfo = {
  protocol_version: "1",
  bridge: { name: "Muster fake bridge", version: "1.0.0" },
  messenger: { name: "Fake chat" },
  capabilities: {
    buttons: { max_per_message: 10, max_per_row: 5, data_max_bytes: 64, styles: [] },
  },
  limits: { max_body_length: 4000, edit_max_age_seconds: null },
  user_lookup: "login",
  event_delivery: ["poll", "push"],
};

function withRates(rates: { send_per_second?: number; send_per_second_per_channel?: number }) {
  return { ...INFO, limits: { ...INFO.limits, ...rates } };
}

function bridgeConnection(id: string, name: string, info: BridgeInfo | null): BridgeConnection {
  return {
    id,
    type: "bridge",
    name,
    base_url: "http://127.0.0.1:18094",
    event_delivery: "poll",
    limiter: { limit: 10, per_seconds: 1 },
    bot_token_status: { set: true, updated_at: "2026-10-17T09:30:00Z" },
    push_secret_status: { set: false },
    callback_url: `http://localhost:8081/api/v1/callbacks/bridge/${id}`,
    info,
    info_read_at: info === null ? null : "2026-10-17T09:31:00Z",
    proxy: { enabled: false },
    destination_count: 0,
    created_at: "2026-10-17T09:30:00Z",
    etag: '"1"',
  };
}

const BR = bridgeConnection("CN0000000000BR", "br", withRates({ send_per_second_per_channel: 2 }));
const PLAIN = bridgeConnection("CN0000000000BP", "br-plain", INFO);
const UNREAD = bridgeConnection("CN0000000000BU", "br-unread", null);

const MM: MattermostConnection = {
  id: "CN0000000000MM",
  type: "mattermost",
  name: "Dev Mattermost",
  server_url: "http://127.0.0.1:18065",
  limiter: { limit: 5, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-17T09:30:00Z" },
  proxy: { enabled: false },
  destination_count: 0,
  callback_url: "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000MM",
  created_at: "2026-10-17T09:30:00Z",
  etag: '"1"',
};

const DESTINATION: BridgeDestination = {
  id: "DS0000000000BR",
  type: "bridge",
  name: "alerts",
  connection_id: BR.id,
  channel_id: "ch-alerts",
  channel_name: "alerts",
  mentions: defaultMentions(),
  limiter: { limit: 30, per_seconds: 60 },
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-17T09:30:00Z",
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

function mockReads() {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(json(200, { items: [MM, BR, PLAIN, UNREAD], next_cursor: null }));
    }
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(json(200, { items: [], next_cursor: null }));
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
      expires_at: "2026-10-17T09:00:00Z",
      idle_expires_at: "2026-10-17T09:00:00Z",
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

async function renderNew(save: (input: DestinationInput) => Promise<undefined>) {
  mockReads();
  return render(providers(<DestinationForm kind={BRIDGE_KIND} submitLabel="Save" save={save} />));
}

function inputValue(label: string): string {
  const input = page.getByLabelText(label).element();
  return input instanceof HTMLInputElement ? input.value : "";
}

/** The limiter as the form shows it: requests, and the period in seconds. */
function limiter(): [string, string] {
  return [inputValue("Requests"), inputValue("Period in seconds")];
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the Bridge type", () => {
  test("proposes half of the bridge's rates as limiters, and the defaults without them", () => {
    // The Connection: half of limits.send_per_second, per second; connection.bridge.limiter without it.
    expect(connectionLimiterProposal(withRates({ send_per_second: 30 }))).toEqual({
      limit: 15,
      per_seconds: 1,
    });
    expect(connectionLimiterProposal(withRates({ send_per_second: 1 }))).toEqual({
      limit: 1,
      per_seconds: 2,
    });
    expect(connectionLimiterProposal(withRates({ send_per_second: 0.1 }))).toEqual({
      limit: 1,
      per_seconds: 20,
    });
    expect(connectionLimiterProposal(INFO)).toEqual({ limit: 10, per_seconds: 1 });
    expect(connectionLimiterProposal(null)).toEqual({ limit: 10, per_seconds: 1 });
    // The Destination: half of limits.send_per_second_per_channel, per minute; destination.bridge.limiter without it.
    expect(destinationLimiterProposal(withRates({ send_per_second_per_channel: 1 }))).toEqual({
      limit: 30,
      per_seconds: 60,
    });
    expect(destinationLimiterProposal(withRates({ send_per_second_per_channel: 2 }))).toEqual({
      limit: 60,
      per_seconds: 60,
    });
    expect(destinationLimiterProposal(withRates({ send_per_second_per_channel: 0.01 }))).toEqual({
      limit: 1,
      per_seconds: 200,
    });
    // The rate of the whole bot is not the rate per channel.
    expect(destinationLimiterProposal(withRates({ send_per_second: 30 }))).toEqual({
      limit: 30,
      per_seconds: 60,
    });
    expect(destinationLimiterProposal(null)).toEqual({ limit: 30, per_seconds: 60 });
    expect(destinationLimiterProposal(undefined)).toEqual({ limit: 30, per_seconds: 60 });
    expect(BRIDGE_KIND.defaultLimiter).toEqual({ limit: 30, per_seconds: 60 });
  });

  test("takes the Connection and the channel id, and checks them as the server does", () => {
    expect(bridgeValues(undefined)).toEqual({ connection_id: "", channel_id: "" });
    expect(bridgeValues(DESTINATION)).toEqual({ connection_id: BR.id, channel_id: "ch-alerts" });
    expect(BRIDGE_KIND.check({ connection_id: "", channel_id: " " })).toEqual({
      "/connection_id": "required",
      "/channel_id": "required",
    });
    expect(BRIDGE_KIND.check({ connection_id: BR.id, channel_id: "x".repeat(512) })).toEqual({});
    expect(BRIDGE_KIND.check({ connection_id: BR.id, channel_id: "x".repeat(513) })).toEqual({
      "/channel_id": "too_long",
    });
    // Characters, not UTF-16 units.
    expect(BRIDGE_KIND.check({ connection_id: BR.id, channel_id: "😀".repeat(512) })).toEqual({});
    expect(BRIDGE_KIND.everyone).toEqual([]);
    expect(BRIDGE_KIND.groups).toBe(false);
  });
});

describe("DestinationForm with the Bridge fields", () => {
  test("a new Destination takes the proposal of the chosen Connection", async () => {
    await renderNew(() => Promise.resolve(undefined));
    // Only the Bridge Connections.
    await expect.element(page.getByRole("option", { name: "br", exact: true })).toBeInTheDocument();
    await expect
      .element(page.getByRole("option", { name: "Dev Mattermost" }))
      .not.toBeInTheDocument();
    expect(limiter()).toEqual(["30", "60"]);
    const connection = page.getByLabelText("Connection", { exact: true });
    await userEvent.selectOptions(connection, "br");
    await expect.poll(limiter).toEqual(["60", "60"]);
    await userEvent.selectOptions(connection, "br-plain");
    await expect.poll(limiter).toEqual(["30", "60"]);
    await userEvent.selectOptions(connection, "br");
    await expect.poll(limiter).toEqual(["60", "60"]);
    await userEvent.selectOptions(connection, "br-unread");
    await expect.poll(limiter).toEqual(["30", "60"]);
    // A limiter the user typed is kept.
    await userEvent.fill(page.getByLabelText("Requests"), "5");
    await userEvent.selectOptions(connection, "br");
    await expect.poll(limiter).toEqual(["5", "60"]);
  });

  test("sends the Connection and the trimmed channel id, and checks them first", async () => {
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await renderNew(save);
    await expect
      .element(page.getByText("As the messenger names it.", { exact: true }))
      .toBeVisible();
    await expect
      .element(page.getByLabelText("Channel id"))
      .toHaveAccessibleDescription("As the messenger names it.");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-connection-error-text"))
      .toHaveTextContent("Fill in this field.");
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent("Fill in this field.");
    expect(save).not.toHaveBeenCalled();

    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "br");
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "alerts");
    await userEvent.fill(page.getByLabelText("Channel id"), "c".repeat(513));
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent("This value is too long.");
    await expect.element(page.getByLabelText("Channel id")).toHaveFocus();
    expect(save).not.toHaveBeenCalled();

    await userEvent.fill(page.getByLabelText("Channel id"), "  ch-alerts ");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "bridge",
      name: "alerts",
      connection_id: BR.id,
      channel_id: "ch-alerts",
      mentions: defaultMentions(),
      limiter: { limit: 60, per_seconds: 60 },
    });
  });

  test("offers nobody and chosen users only", async () => {
    await renderNew(() => Promise.resolve(undefined));
    const everyone = document.querySelectorAll<HTMLSelectElement>('select[id$="-everyone"]');
    expect(everyone.length).toBe(6);
    for (const select of everyone) {
      expect([...select.options].map((o) => o.textContent)).toEqual(["Nobody"]);
      expect(select.disabled).toBe(true);
    }
    await expect
      .element(page.getByLabelText("New Alert Group", { exact: true }))
      .toHaveAccessibleDescription(
        "A Bridge has no mention of the whole chat and no groups: only the chosen users are mentioned, through their accounts linked to this Connection.",
      );
    await expect.element(page.getByRole("button", { name: "Add group" })).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("@channel");
  });

  test("shows the forbidden channel at the channel field with the bridge's text", async () => {
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValueOnce(
        new ApiError(
          422,
          {
            type: "https://muster-io.github.io/muster/problems/validation-failed",
            code: "validation_failed",
            errors: [
              {
                pointer: "/channel_id",
                code: "destination_check_failed",
                check_code: "channel_forbidden",
                detail: "The bot is not a member of this channel.",
              },
            ],
          },
          undefined,
        ),
      );
    await renderNew(save);
    await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "br");
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "alerts");
    await userEvent.fill(page.getByLabelText("Channel id"), "ch-nobot");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent(FORBIDDEN_TEXT);
    await expect.element(page.getByLabelText("Channel id")).toHaveFocus();
    await expect.element(page.getByLabelText("Channel id")).toHaveAttribute("aria-invalid", "true");
    // Another channel takes the refusal away.
    await userEvent.fill(page.getByLabelText("Channel id"), "ch-alerts");
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .not.toBeInTheDocument();
  });

  test("shows the channel's name the check found, in Russian too", async () => {
    mockReads();
    const screen = await render(
      providers(
        <DestinationForm
          kind={BRIDGE_KIND}
          destination={DESTINATION}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByTestId("bridge-found")).toHaveTextContent("Channel: alerts");
    await expect.element(page.getByTestId("bridge-channel-name")).toHaveTextContent("alerts");
    await expect.element(page.getByLabelText("Connection", { exact: true })).toHaveValue(BR.id);
    // An edit keeps its stored limiter.
    expect(limiter()).toEqual(["30", "60"]);
    // Another channel is not the one the name was found for.
    await userEvent.fill(page.getByLabelText("Channel id"), "ch-other");
    await expect.element(page.getByTestId("bridge-found")).not.toBeInTheDocument();
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(
      providers(
        <DestinationForm
          kind={BRIDGE_KIND}
          destination={DESTINATION}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByTestId("bridge-found")).toHaveTextContent("Канал: alerts");
    await expect.element(page.getByLabelText("Подключение", { exact: true })).toHaveValue(BR.id);
    await expect
      .element(page.getByLabelText("ID канала"))
      .toHaveAccessibleDescription("Так, как его называет мессенджер.");
    await expect
      .element(page.getByLabelText("Новая группа алертов", { exact: true }))
      .toHaveAccessibleDescription(
        "У моста нет упоминания всего чата и групп: упоминаются только выбранные пользователи — через их аккаунты, привязанные к этому подключению.",
      );
  });
});
