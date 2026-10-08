// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination form in a real browser, with the Mattermost fields: the Connection, team and channel pickers loaded
// through the bot; the name of the channel and the limiter of the Connection taken while untouched; the request a save
// sends; the checks it repeats; the refusals it puts on its fields — destination_check_failed next to the channel with
// the check's message, unknown_id at the Connection, name_taken, a busy messenger and a stale version; the read-only
// form and the stored names without connections:write; the Broken banner in the user's time zone; and "Check".

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DestinationCheckResult,
  DestinationInput,
  MattermostChannel,
  MattermostConnection,
  MattermostDestination,
  Permission,
  Session,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { channelLabel, teamChannels, teamsOf } from "./channel-picker";
import { DestinationCheck } from "./destination-check";
import {
  DestinationDeleteDialog,
  DestinationForm,
  destinationErrorText,
  serverErrors,
} from "./destination-form";
import { BrokenBanner, brokenText } from "./destination-health";
import { MATTERMOST_KIND } from "./mattermost-destination-fields";
import { defaultMentions } from "./mention-settings";

const CONNECTION: MattermostConnection = {
  id: "CN0000000000AA",
  type: "mattermost",
  name: "mm",
  server_url: "http://127.0.0.1:18065",
  limiter: { limit: 7, per_seconds: 2 },
  bot_token_status: { set: true, updated_at: "2026-10-08T09:30:00Z" },
  bot_username: "muster-dev-bot",
  proxy: { enabled: false },
  destination_count: 0,
  callback_url: "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000AA",
  created_at: "2026-10-08T09:30:00Z",
  etag: '"1"',
};

function channel(
  id: string,
  name: string,
  display: string,
  extra: Partial<MattermostChannel> = {},
) {
  return {
    id,
    name,
    display_name: display,
    team_id: "team-dev",
    team_name: "dev",
    type: "open",
    ...extra,
  } satisfies MattermostChannel;
}

const CHANNELS: MattermostChannel[] = [
  channel("ch-alerts", "alerts", "Alerts"),
  channel("ch-nobot", "no-bot", "No bot"),
  channel("ch-old", "old", "Old", { archived: true }),
  channel("ch-dm", "dm", "dm", { type: "direct" }),
];

const DESTINATION: MattermostDestination = {
  id: "DS0000000000AA",
  type: "mattermost",
  name: "alerts",
  connection_id: CONNECTION.id,
  team_id: "team-dev",
  channel_id: "ch-alerts",
  team_name: "dev",
  channel_name: "alerts",
  mentions: defaultMentions(),
  limiter: { limit: 5, per_seconds: 1 },
  health: { state: "healthy" },
  routes: [{ id: "RT0000000000AA", name: "db" }],
  created_at: "2026-10-08T09:30:00Z",
  etag: '"3"',
};

const WRITE: Permission[] = [
  "destinations:read",
  "destinations:write",
  "destinations:test",
  "connections:read",
  "connections:write",
  "alert-groups:read",
];

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function refusal(status: number, problem: Record<string, unknown>, retryAfter?: number): ApiError {
  return new ApiError(
    status,
    { type: "https://muster-io.github.io/muster/problems/x", ...problem },
    retryAfter,
  );
}

/** Answers the reads of the pickers; anything else is a test's own. */
function mockReads() {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/connections/CN0000000000AA/channels")) {
      return Promise.resolve(json(200, { items: CHANNELS }));
    }
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(json(200, { items: [CONNECTION], next_cursor: null }));
    }
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(
        json(200, {
          items: [{ id: "US0000000000AA", name: "Alice", login: "alice", deactivated: false }],
          next_cursor: null,
        }),
      );
    }
    return Promise.resolve(json(404, { title: "not mocked" }));
  });
}

function providers(children: ReactNode, permissions: Permission[], timeZone = "Europe/Berlin") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads only the Permissions and the zone
      user: { id: "SR0000000000AA", name: "Admin", time_zone: timeZone } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
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

async function renderForm(
  save: (input: DestinationInput) => Promise<MattermostDestination | undefined>,
  options: {
    destination?: MattermostDestination;
    readOnly?: boolean;
    permissions?: Permission[];
  } = {},
) {
  return render(
    providers(
      <DestinationForm
        kind={MATTERMOST_KIND}
        destination={options.destination}
        readOnly={options.readOnly}
        submitLabel="Save"
        save={save}
        onReload={() => {}}
      />,
      options.permissions ?? WRITE,
    ),
  );
}

/** Picks the Connection, the team and a channel as a person does. */
async function pick(channelLabelText: string) {
  await userEvent.selectOptions(page.getByLabelText("Connection", { exact: true }), "mm");
  await expect.element(page.getByRole("option", { name: "dev" })).toBeInTheDocument();
  await userEvent.selectOptions(page.getByLabelText("Team", { exact: true }), "dev");
  await userEvent.selectOptions(page.getByLabelText("Channel", { exact: true }), channelLabelText);
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the channel pickers", () => {
  test("offer the public and private channels of the team that are not archived, by name", () => {
    expect(teamsOf(CHANNELS)).toEqual([{ id: "team-dev", name: "dev" }]);
    expect(teamChannels(CHANNELS, "team-dev").map((c) => c.id)).toEqual(["ch-alerts", "ch-nobot"]);
    expect(channelLabel(i18n.t, CHANNELS[0]!)).toBe("alerts");
    expect(channelLabel(i18n.t, CHANNELS[1]!)).toBe("No bot (no-bot)");
    expect(channelLabel(i18n.t, CHANNELS[2]!)).toBe("old (archived)");
  });
});

describe("a new Mattermost Destination", () => {
  test("takes the channel's name and the Connection's limiter and sends every field", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await renderForm(save);
    await pick("alerts");
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveValue("alerts");
    await expect.element(page.getByLabelText("Requests")).toHaveValue("7");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("2");
    await userEvent.selectOptions(page.getByLabelText("New Alerts", { exact: true }), "@channel");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    const mentions = defaultMentions();
    mentions.new_alerts = { everyone: "channel", user_ids: [], groups: [] };
    expect(save.mock.calls[0]?.[0]).toEqual({
      type: "mattermost",
      name: "alerts",
      connection_id: CONNECTION.id,
      team_id: "team-dev",
      channel_id: "ch-alerts",
      mentions,
      limiter: { limit: 7, per_seconds: 2 },
    });
  });

  test("keeps a name the user typed when the channel changes", async () => {
    mockReads();
    await renderForm(() => Promise.resolve(undefined));
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "pager");
    await pick("No bot (no-bot)");
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveValue("pager");
  });

  test("repeats the server's checks and marks the missing fields", async () => {
    mockReads();
    const save = vi.fn(() => Promise.resolve(undefined));
    await renderForm(save);
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByLabelText("Connection", { exact: true }))
      .toHaveAttribute("aria-invalid", "true");
    await expect.element(page.getByLabelText("Connection", { exact: true })).toHaveFocus();
    await expect
      .element(page.getByLabelText("Name", { exact: true }))
      .toHaveAttribute("aria-invalid", "true");
    expect(save).not.toHaveBeenCalled();
  });

  test("shows a failing Destination check next to the channel with the check's message", async () => {
    mockReads();
    await renderForm(() =>
      Promise.reject(
        refusal(422, {
          code: "validation-failed",
          errors: [
            {
              pointer: "/channel_id",
              code: "destination_check_failed",
              detail: "The bot is not a member of this channel.",
            },
          ],
        }),
      ),
    );
    await pick("No bot (no-bot)");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .toHaveTextContent("The bot is not a member of this channel.");
    await expect.element(page.getByLabelText("Channel", { exact: true })).toHaveFocus();
    // Another channel clears the refusal.
    await userEvent.selectOptions(page.getByLabelText("Channel", { exact: true }), "alerts");
    await expect
      .element(page.getByTestId("destination-type-channel-error-text"))
      .not.toBeInTheDocument();
  });

  test("names a busy messenger with its wait", async () => {
    mockReads();
    await renderForm(() =>
      Promise.reject(refusal(503, { code: "limited", retry_after_seconds: 4 })),
    );
    await pick("alerts");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-error"))
      .toHaveTextContent("The messenger is busy; try again in 4 s.");
  });
});

describe("the refusals of a save", () => {
  test("land at their pointers", () => {
    expect(serverErrors(refusal(409, { code: "name_taken" }))).toEqual({
      "/name": { code: "name_taken" },
    });
    expect(
      serverErrors(
        refusal(422, {
          errors: [
            { pointer: "/connection_id", code: "unknown_id" },
            { pointer: "/limiter", code: "invalid_format" },
            { pointer: "/mentions/reopen/user_ids/0", code: "unknown_id" },
          ],
        }),
      ),
    ).toEqual({
      "/connection_id": { code: "unknown_id" },
      "/limiter/limit": { code: "invalid_format" },
      "/limiter/per_seconds": { code: "invalid_format" },
      "/mentions/reopen/user_ids/0": { code: "unknown_id" },
    });
    // Several failing checks at one field show every message.
    expect(
      serverErrors(
        refusal(422, {
          errors: [
            { pointer: "/channel_id", code: "destination_check_failed", detail: "First." },
            { pointer: "/channel_id", code: "destination_check_failed", detail: "Second." },
          ],
        }),
      ),
    ).toEqual({ "/channel_id": { code: "destination_check_failed", detail: "First. Second." } });
    expect(destinationErrorText(i18n.t, "/connection_id", { code: "unknown_id" })).toBe(
      "This Connection no longer exists. Choose another one.",
    );
    expect(destinationErrorText(i18n.t, "/name", { code: "name_taken" })).toBe(
      "Another Destination has this name.",
    );
    expect(
      destinationErrorText(i18n.t, "/mentions/reopen/user_ids/0", { code: "unknown_id" }),
    ).toBe("A chosen user no longer exists.");
    expect(destinationErrorText(i18n.t, "/channel_id", { code: "destination_check_failed" })).toBe(
      "The Destination check failed.",
    );
  });

  test("puts an unknown Connection at the Connection and a refused Mention at its kind", async () => {
    mockReads();
    await renderForm(
      () =>
        Promise.reject(
          refusal(422, {
            errors: [
              { pointer: "/connection_id", code: "unknown_id" },
              { pointer: "/mentions/reopen/everyone", code: "unsupported" },
            ],
          }),
        ),
      { destination: DESTINATION },
    );
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-connection-error-text"))
      .toHaveTextContent("This Connection no longer exists. Choose another one.");
    await expect
      .element(
        page
          .getByTestId("mention-kind")
          .nth(2)
          .getByText("This messenger does not support this mention.", { exact: true }),
      )
      .toBeVisible();
  });

  test("offers to reload a version that changed elsewhere (412) and a missing one (428)", async () => {
    for (const status of [412, 428]) {
      mockReads();
      const screen = await renderForm(() => Promise.reject(refusal(status, {})), {
        destination: DESTINATION,
      });
      await page.getByRole("button", { name: "Save" }).click();
      await expect
        .element(
          page
            .getByTestId("destination-stale")
            .getByText("Someone else changed this Destination. Reload to see the changes.", {
              exact: true,
            }),
        )
        .toBeVisible();
      await screen.unmount();
      vi.restoreAllMocks();
    }
  });

  test("names a taken name at the name", async () => {
    mockReads();
    await renderForm(() => Promise.reject(refusal(409, { code: "name_taken" })), {
      destination: DESTINATION,
    });
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByLabelText("Name", { exact: true })).toHaveFocus();
    await expect.element(page.getByText("Another Destination has this name.")).toBeVisible();
  });
});

describe("a stored Destination", () => {
  test("shows its values and says when it was saved", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(DESTINATION));
    await renderForm(save, { destination: DESTINATION });
    await expect.element(page.getByLabelText("Channel", { exact: true })).toHaveValue("ch-alerts");
    await userEvent.fill(page.getByLabelText("Requests"), "3");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toMatchObject({ limiter: { limit: 3, per_seconds: 1 } });
  });

  test("is read-only without destinations:write and shows the stored team and channel", async () => {
    mockReads();
    await renderForm(() => Promise.resolve(undefined), {
      destination: DESTINATION,
      readOnly: true,
      permissions: ["destinations:read", "connections:read", "alert-groups:read"],
    });
    await expect
      .element(page.getByTestId("destination-read-only"))
      .toHaveTextContent("You can view this Destination but not change it.");
    await expect.element(page.getByLabelText("Name", { exact: true })).toBeDisabled();
    await expect.element(page.getByTestId("stored-team")).toHaveTextContent("dev");
    await expect.element(page.getByTestId("stored-channel")).toHaveTextContent("alerts");
    await expect.element(page.getByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });

  test("shows the stored names without connections:write, which the channel list needs", async () => {
    const fetch = mockReads();
    await renderForm(() => Promise.resolve(undefined), {
      destination: DESTINATION,
      permissions: ["destinations:read", "destinations:write", "connections:read"],
    });
    await expect.element(page.getByTestId("stored-channel")).toHaveTextContent("alerts");
    await expect
      .element(
        page.getByText(
          /^Only someone who may change Connections can pick the team and the channel/,
        ),
      )
      .toBeVisible();
    expect(fetch.mock.calls.some(([u]) => typeof u === "string" && u.includes("/channels"))).toBe(
      false,
    );
  });

  test("names the fields in Russian", async () => {
    await i18n.changeLanguage("ru");
    mockReads();
    await renderForm(() => Promise.resolve(undefined), { destination: DESTINATION });
    await expect.element(page.getByLabelText("Подключение", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("Канал", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("Новые алерты", { exact: true })).toBeVisible();
  });
});

describe("the Broken banner", () => {
  test("says since when in the user's time zone, the reason and the probe interval", async () => {
    const health = {
      state: "broken" as const,
      since: "2026-10-09T08:05:00Z",
      reason: "403 Forbidden: the bot is not a member of the channel.",
    };
    expect(brokenText(i18n.t, health, () => "10:05")).toBe(
      "Broken since 10:05: 403 Forbidden: the bot is not a member of the channel. Muster tries again every 5 min.",
    );
    await render(providers(<BrokenBanner health={health} />, WRITE, "Europe/Berlin"));
    await expect
      .element(page.getByTestId("broken-banner"))
      .toHaveTextContent(
        "Broken since 10:05: 403 Forbidden: the bot is not a member of the channel. Muster tries again every 5 min.",
      );
  });

  test("shows nothing for a healthy Destination and reads in Russian", async () => {
    const screen = await render(providers(<BrokenBanner health={{ state: "healthy" }} />, WRITE));
    await expect.element(page.getByTestId("broken-banner")).not.toBeInTheDocument();
    await screen.unmount();
    await i18n.changeLanguage("ru");
    expect(
      brokenText(
        i18n.t,
        { state: "broken", since: "2026-10-09T08:05:00Z", reason: "403" },
        () => "11:05",
      ),
    ).toBe("Сломано с 11:05: 403. Muster пробует снова каждые 5 мин.");
  });
});

const PASSED: DestinationCheckResult = {
  ok: true,
  checks: [
    { name: "token", ok: true },
    { name: "bot_in_channel", ok: true },
  ],
  health: { state: "healthy" },
};

async function runCheck(response: Response) {
  const fetch = vi.spyOn(window, "fetch").mockResolvedValue(response);
  await render(
    providers(
      <DestinationCheck
        destination={{ ...DESTINATION, health: { state: "broken", since: "2026-10-09T08:05:00Z" } }}
        dirty={false}
      />,
      WRITE,
    ),
  );
  await page.getByRole("button", { name: "Check", exact: true }).click();
  return fetch;
}

describe("Check", () => {
  test("shows each check and Check passed", async () => {
    const fetch = await runCheck(json(200, PASSED));
    await expect
      .element(page.getByTestId("destination-check-summary"))
      .toHaveTextContent("Check passed");
    await expect
      .element(page.getByTestId("destination-check-item").nth(1))
      .toHaveTextContent("Bot in the channel: ok");
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/v1/destinations/DS0000000000AA/checks");
  });

  test("shows the message of a failing check and Check failed", async () => {
    await runCheck(
      json(200, {
        ok: false,
        checks: [
          { name: "token", ok: true },
          {
            name: "bot_in_channel",
            ok: false,
            message: "The bot is not a member of this channel.",
          },
        ],
        health: { state: "broken", since: "2026-10-09T08:05:00Z", reason: "403" },
      }),
    );
    await expect
      .element(page.getByTestId("destination-check-summary"))
      .toHaveTextContent("Check failed");
    await expect
      .element(page.getByTestId("destination-check-item").nth(1))
      .toHaveTextContent("Bot in the channel: failed The bot is not a member of this channel.");
  });

  test("names a busy messenger with its wait", async () => {
    await runCheck(json(503, { title: "busy", code: "limited" }, { "Retry-After": "3" }));
    await expect
      .element(page.getByTestId("destination-check-error"))
      .toHaveTextContent("The messenger is busy; try again in 3 s.");
  });
});

describe("Delete", () => {
  test("says what deleting does and sends the version read last", async () => {
    const fetch = vi.spyOn(window, "fetch").mockResolvedValue(new Response(null, { status: 412 }));
    const rootRoute = createRootRoute({
      component: () => <DestinationDeleteDialog destination={DESTINATION} />,
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/destinations/DS0000000000AA"] }),
    });
    await render(providers(<RouterProvider router={router} />, WRITE));
    await page.getByRole("button", { name: "Delete" }).click();
    await expect
      .element(page.getByText(/^Muster stops sending here/))
      .toHaveTextContent(
        'Muster stops sending here, and the Destination leaves every Route. Open Root messages get one final edit, "No longer updated here". This cannot be undone.',
      );
    // The pointer that opened the dialog is still over the inert layer of the modal; the keyboard confirms.
    const confirm = page.getByRole("dialog").getByRole("button", { name: "Delete" }).element();
    if (confirm instanceof HTMLElement) {
      confirm.focus();
    }
    await userEvent.keyboard("{Enter}");
    await expect
      .element(
        page
          .getByTestId("destination-delete-error")
          .getByText("Someone else changed this Destination. Reload to see the changes.", {
            exact: true,
          }),
      )
      .toBeVisible();
    const init = fetch.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get("If-Match")).toBe('"3"');
  });
});
