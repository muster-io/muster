// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Slack Connection in a real browser: the request a save sends — the bot token, the event mode with only the
// secret it needs, the proxy and the limiter — and no address field; the app-level token only in Socket Mode, the Slack
// signing secret and the callback address with its hint only in HTTP mode, the note that a new Connection gets its
// address once saved; the checks a save repeats, the tokens' prefixes and the secret of the mode included; the draft
// that "Check connection" sends (C-13.FR-14, C-24.FR-2); the steps of the check with the bot, the workspace and the app,
// a refused app-level token named as such, and the step "App-level token" a draft skips with what to type; the line of
// what the last check learned and the warning of a Slack app another program shares. English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  ConnectionCheckResult,
  ConnectionCheckStep,
  ConnectionInput,
  Permission,
  Session,
  SlackConnection,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { slackConnection } from "../test/fixtures/connections";
import { ConnectionCheck } from "./connection-check";
import {
  ConnectionForm,
  connectionDraft,
  connectionValues,
  draftErrorText,
} from "./connection-form";
import { SlackConnectionInfo, SlackSharedAppWarning, sharesApp } from "./slack-connection-fields";

const CALLBACK = "http://localhost:8081/api/v1/callbacks/slack/CN0000000000SL";
const BOT = "xoxb-muster-dev-slack-token-b";
const APP = "xapp-muster-dev-slack-token-b";
const SET = { set: true, updated_at: "2026-10-20T09:30:00Z" };

const SLACK: SlackConnection = slackConnection({
  id: "CN0000000000SL",
  name: "ui",
  event_mode: "socket",
  limiter: { limit: 50, per_seconds: 60 },
  bot_token_status: SET,
  app_token_status: SET,
  callback_url: CALLBACK,
  bot_name: "muster-dev-bot-b",
  bot_user_id: "U0MUSTERBOTB",
  team_id: "T0MUSTERDEVB",
  team_name: "Example workspace B",
  app_id: "A0MUSTERDEVB",
  socket: null,
  created_at: "2026-10-20T09:30:00Z",
  etag: '"1"',
});

const NO_PROXY = { enabled: false, type: "http", username: null } as const;

const WRITE: Permission[] = ["connections:read", "connections:write"];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function providers(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads Permissions and time zone
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-20T09:00:00Z",
      idle_expires_at: "2026-10-20T09:00:00Z",
      docs_version: "latest",
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

async function typeSecret(testId: string, label: string, value: string, button = "Set a value") {
  await page.getByTestId(testId).getByRole("button", { name: button }).click();
  await userEvent.fill(page.getByLabelText(label, { exact: true }), value);
}

describe("ConnectionForm of a Slack Connection", () => {
  test("creates a Slack Connection in Socket Mode with the tokens, the proxy and the limiter", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(providers(<ConnectionForm type="slack" submitLabel="Save" save={save} />));
    await expect.element(page.getByRole("heading", { name: "Slack" })).toBeVisible();
    // Slack's address is fixed: no address field.
    await expect.element(page.getByLabelText("Server URL")).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Base URL")).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Requests")).toHaveValue("50");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("60");
    await expect.element(page.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
    await expect
      .element(page.getByRole("radio", { name: "Socket Mode" }))
      .toHaveAccessibleDescription(
        "The Leader holds the Socket Mode connection, so Slack needs no address of Muster. Presses made while the Leader changes are lost; use HTTP if that matters.",
      );
    await expect.element(page.getByText("App-level token", { exact: true })).toBeVisible();
    await expect.element(page.getByText("Slack signing secret")).not.toBeInTheDocument();

    await userEvent.fill(page.getByLabelText("Name"), "ui");
    await typeSecret("secret-connection-bot-token", "Bot token", BOT);
    // Socket Mode needs the app-level token.
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("secret-connection-app-token").getByText("Fill in this field."))
      .toBeVisible();
    expect(save).not.toHaveBeenCalled();
    await typeSecret("secret-connection-app-token", "App-level token", APP);
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "slack",
      name: "ui",
      event_mode: "socket",
      bot_token: BOT,
      app_token: APP,
      proxy: NO_PROXY,
      limiter: { limit: 50, per_seconds: 60 },
    });
    expect(document.body.textContent).not.toContain(BOT);
    expect(document.body.textContent).not.toContain(APP);
  });

  test("refuses tokens without their prefixes, at their fields", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(providers(<ConnectionForm type="slack" submitLabel="Save" save={save} />));
    await userEvent.fill(page.getByLabelText("Name"), "ui");
    await typeSecret("secret-connection-bot-token", "Bot token", APP);
    await typeSecret("secret-connection-app-token", "App-level token", BOT);
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByText("Enter the bot token of the Slack app, which starts with xoxb-."))
      .toBeVisible();
    await expect
      .element(
        page.getByText("Enter the app-level token of the Slack app, which starts with xapp-."),
      )
      .toBeVisible();
    expect(save).not.toHaveBeenCalled();
  });

  test("shows the Slack signing secret and the callback address only in HTTP mode", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const screen = await render(
      providers(<ConnectionForm type="slack" submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByTestId("slack-http")).not.toBeInTheDocument();
    await page.getByRole("radio", { name: "HTTP" }).click();
    await expect.element(page.getByTestId("slack-socket")).not.toBeInTheDocument();
    await expect.element(page.getByText("Slack signing secret", { exact: true })).toBeVisible();
    await expect
      .element(page.getByTestId("slack-callback-after-save"))
      .toHaveTextContent("The callback address appears once the Connection is saved.");
    await userEvent.fill(page.getByLabelText("Name"), "ui");
    await typeSecret("secret-connection-bot-token", "Bot token", BOT);
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page.getByTestId("secret-connection-slack-signing-secret").getByText("Fill in this field."),
      )
      .toBeVisible();
    expect(save).not.toHaveBeenCalled();
    await typeSecret("secret-connection-slack-signing-secret", "Slack signing secret", "s3cret");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith({
      type: "slack",
      name: "ui",
      event_mode: "http",
      bot_token: BOT,
      slack_signing_secret: "s3cret",
      proxy: NO_PROXY,
      limiter: { limit: 50, per_seconds: 60 },
    });
    await screen.unmount();

    // A saved Connection shows its callback address with a copy button and the hint of reference.md.
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await render(providers(<ConnectionForm connection={SLACK} submitLabel="Save" save={save} />));
    await expect.element(page.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
    await page.getByRole("radio", { name: "HTTP" }).click();
    await expect.element(page.getByTestId("slack-callback-url")).toHaveTextContent(CALLBACK);
    await expect
      .element(page.getByTestId("slack-callback-hint"))
      .toHaveTextContent(
        `Slack sends button presses to ${CALLBACK}. Enter it as the Interactivity Request URL of the Slack app, and the app's Slack signing secret here. A press on a test message checks both.`,
      );
    await page.getByRole("button", { name: "Copy" }).click();
    expect(writeText).toHaveBeenCalledWith(CALLBACK);
    // HTTP needs the Slack signing secret, which this Connection has not stored.
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledTimes(1);
  });

  test("keeps the stored secrets: a save sends no token and only the event mode", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(
      providers(
        <ConnectionForm
          connection={{ ...SLACK, slack_signing_secret_status: SET }}
          submitLabel="Save"
          save={save}
        />,
      ),
    );
    expect(page.getByTestId("secret-connection-app-token-status").element().textContent).toMatch(
      /^Set, changed /,
    );
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith({
      type: "slack",
      name: "ui",
      event_mode: "socket",
      proxy: NO_PROXY,
      limiter: { limit: 50, per_seconds: 60 },
    });
    // A typed app-level token does not go in HTTP mode: the server keeps the stored one.
    await typeSecret("secret-connection-app-token", "App-level token", APP, "Replace");
    await page.getByRole("radio", { name: "HTTP" }).click();
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith({
      type: "slack",
      name: "ui",
      event_mode: "http",
      proxy: NO_PROXY,
      limiter: { limit: 50, per_seconds: 60 },
    });
  });

  test("reads the event mode and its fields in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(
      providers(
        <ConnectionForm
          connection={SLACK}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByText("Режим событий")).toBeVisible();
    await expect.element(page.getByRole("radio", { name: "Socket Mode" })).toBeChecked();
    await expect.element(page.getByText("Токен уровня приложения", { exact: true })).toBeVisible();
    await page.getByRole("radio", { name: "HTTP" }).click();
    await expect.element(page.getByText("Секрет подписи Slack", { exact: true })).toBeVisible();
    await expect
      .element(page.getByTestId("slack-callback-hint"))
      .toHaveTextContent(
        `Slack отправляет нажатия кнопок на ${CALLBACK}. Укажите этот адрес как Interactivity Request URL приложения Slack, а секрет подписи Slack приложения — здесь. Нажатие кнопки тестового сообщения проверяет и то и другое.`,
      );
  });
});

describe("the draft of a Slack Connection", () => {
  test("sends the event mode, the typed tokens, the proxy and the saved Connection", () => {
    const fresh = connectionValues(undefined, "slack");
    expect(connectionDraft(fresh, undefined)).toEqual({
      type: "slack",
      event_mode: "socket",
      proxy: NO_PROXY,
    });
    const typed = {
      ...fresh,
      bot_token: { mode: "replace", value: BOT },
      app_token: { mode: "replace", value: APP },
    } as const;
    expect(connectionDraft(typed, undefined)).toEqual({
      type: "slack",
      event_mode: "socket",
      bot_token: BOT,
      app_token: APP,
      proxy: NO_PROXY,
    });
    // HTTP mode has no step app_token: the app-level token is not sent.
    expect(connectionDraft({ ...typed, event_mode: "http" }, undefined)).toEqual({
      type: "slack",
      event_mode: "http",
      bot_token: BOT,
      proxy: NO_PROXY,
    });
    // An edit with only the proxy changed sends no token, and names the saved Connection.
    const proxied = {
      ...connectionValues(SLACK),
      proxy: { ...connectionValues(SLACK).proxy, enabled: true, address: "127.0.0.1:18098" },
    };
    expect(connectionDraft(proxied, SLACK)).toEqual({
      type: "slack",
      event_mode: "socket",
      proxy: { enabled: true, type: "http", address: "127.0.0.1:18098", username: null },
      connection_id: SLACK.id,
    });
  });

  test("names a typed token without its prefix before anything is sent", () => {
    const v = {
      ...connectionValues(undefined, "slack"),
      bot_token: { mode: "replace", value: "nope" },
    } as const;
    expect(draftErrorText(i18n.t, v, undefined)).toBe(
      "Bot token: Enter the bot token of the Slack app, which starts with xoxb-.",
    );
    // A missing app-level token is the save's to refuse: the check skips its step.
    expect(draftErrorText(i18n.t, connectionValues(undefined, "slack"), undefined)).toBeUndefined();
  });
});

function step(
  name: ConnectionCheckStep["name"],
  extra: Partial<ConnectionCheckStep> = {},
): ConnectionCheckStep {
  return { name, ok: true, via: "direct", latency_ms: 3, ...extra };
}

const RUN = "CR0000000000R1";

const PASSED: ConnectionCheckResult = {
  ok: true,
  steps: [step("token"), step("scopes", { latency_ms: null }), step("app_token")],
  warnings: [],
  bot_name: "muster-dev-bot-b",
  slack: {
    team_id: "T0MUSTERDEVB",
    team_name: "Example workspace B",
    bot_user_id: "U0MUSTERBOTB",
    app_id: "A0MUSTERDEVB",
  },
  request_log_run: RUN,
};

/** A check whose step app_token failed with the code and the server's message. */
function appTokenFailed(
  code: "token_invalid" | "wrong_token_type",
  message: string,
): ConnectionCheckResult {
  return {
    ...PASSED,
    ok: false,
    steps: [
      step("token"),
      step("scopes", { latency_ms: null }),
      step("app_token", { ok: false, code, message }),
    ],
    slack: { ...PASSED.slack!, app_id: null },
  };
}

function stepTexts(): string[] {
  return page
    .getByTestId("connection-check-step")
    .elements()
    .map((e) => e.textContent ?? "");
}

describe("the check of a Slack Connection", () => {
  test("names the bot, the workspace and the app of a draft's steps", async () => {
    const fetch = vi
      .spyOn(window, "fetch")
      .mockImplementation(() => Promise.resolve(json(200, PASSED)));
    const draft = {
      type: "slack",
      event_mode: "socket",
      bot_token: BOT,
      app_token: APP,
      proxy: NO_PROXY,
    } as const;
    await render(providers(<ConnectionCheck type="slack" draft={draft} />));
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot-b");
    expect(stepTexts()).toEqual([
      "Token: ok · 3 ms · direct · Bot: muster-dev-bot-b · Workspace: Example workspace B",
      "Scopes: ok · direct",
      "App-level token: ok · 3 ms · direct · App: A0MUSTERDEVB",
    ]);
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(typeof url === "string" ? url : "").toContain("/api/v1/connections/checks");
    expect(JSON.parse(typeof init?.body === "string" ? init.body : "")).toEqual(draft);
  });

  test("names a refused or wrong app-level token as the app-level token, not the bot's", async () => {
    const fetch = vi
      .spyOn(window, "fetch")
      .mockImplementationOnce(() =>
        Promise.resolve(json(200, appTokenFailed("token_invalid", "Slack refused the token."))),
      )
      .mockImplementationOnce(() =>
        Promise.resolve(
          json(200, appTokenFailed("wrong_token_type", "This is not an app-level token.")),
        ),
      );
    await render(providers(<ConnectionCheck connectionId={SLACK.id} type="slack" />));
    const reason = page.getByTestId("connection-check-step-reason");
    const detail = page.getByTestId("connection-check-step-detail");
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect.element(reason).toHaveTextContent("The app-level token is not valid.");
    await expect.element(detail).toHaveTextContent("Details: Slack refused the token.");
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect.element(reason).toHaveTextContent("This is not an app-level token.");
    // The server's message says no more than the text.
    await expect.element(detail).not.toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  test("says why a draft skipped the app-level token: none typed, or the proxy changed", async () => {
    const skipped: ConnectionCheckResult = {
      ...PASSED,
      steps: [
        step("token", { via: "proxy" }),
        step("scopes", { latency_ms: null, via: "proxy" }),
        step("app_token", { ok: false, skipped: true, latency_ms: null, via: "proxy" }),
      ],
      slack: { ...PASSED.slack!, app_id: null },
    };
    vi.spyOn(window, "fetch").mockImplementation(() => Promise.resolve(json(200, skipped)));
    const draft = {
      type: "slack",
      event_mode: "socket",
      bot_token: BOT,
      proxy: NO_PROXY,
    } as const;
    const screen = await render(providers(<ConnectionCheck type="slack" draft={draft} />));
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step").nth(2))
      .toHaveTextContent("App-level token: Skipped: type the app-level token to check it");
    await screen.unmount();

    // An edit whose Connection stores an app-level token: the stored one does not go with this draft.
    const stored = await render(
      providers(
        <ConnectionCheck
          connectionId={SLACK.id}
          type="slack"
          draft={{ ...draft, connection_id: SLACK.id }}
          appTokenStored
        />,
      ),
    );
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step").nth(2))
      .toHaveTextContent(
        "App-level token: Skipped: type the app-level token too, or save first: the stored one goes to Slack only with the stored bot token through the saved proxy",
      );
    // The bot token passed, so the check names the bot, not what the address answers as.
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot-b");
    await stored.unmount();

    // An HTTP Connection without a stored app-level token, switched to Socket Mode: there is none to send.
    await render(
      providers(
        <ConnectionCheck
          connectionId={SLACK.id}
          type="slack"
          draft={{ type: "slack", event_mode: "socket", proxy: NO_PROXY, connection_id: SLACK.id }}
          appTokenStored={false}
        />,
      ),
    );
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step").nth(2))
      .toHaveTextContent("App-level token: Skipped: type the app-level token to check it");
  });

  test("shows only the dry probe of an edit whose proxy changed", async () => {
    const probe: ConnectionCheckResult = {
      ok: true,
      steps: [
        step("dry_probe", { via: "proxy" }),
        step("token", { ok: false, skipped: true, latency_ms: null }),
        step("scopes", { ok: false, skipped: true, latency_ms: null }),
        step("app_token", { ok: false, skipped: true, latency_ms: null }),
      ],
      warnings: [],
      request_log_run: RUN,
    };
    vi.spyOn(window, "fetch").mockImplementation(() => Promise.resolve(json(200, probe)));
    await i18n.changeLanguage("ru");
    await render(
      providers(
        <ConnectionCheck
          connectionId={SLACK.id}
          type="slack"
          draft={{ type: "slack", event_mode: "socket", proxy: NO_PROXY, connection_id: SLACK.id }}
        />,
      ),
    );
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("По этому адресу отвечает Web API Slack.");
    expect(stepTexts()[0]).toBe("Пробный запрос без токена: успешно · 3 мс · через прокси");
    expect(stepTexts()[3]).toBe(
      "Токен уровня приложения: Пропущено: введите токен бота заново, чтобы проверить новый адрес или прокси, или сохраните изменения",
    );
  });
});

describe("the page of a Slack Connection", () => {
  test("names the bot, the workspace and the app the last check found", async () => {
    const screen = await render(providers(<SlackConnectionInfo connection={SLACK} />));
    await expect
      .element(page.getByTestId("connection-bot"))
      .toHaveTextContent(
        "Bot: muster-dev-bot-b · Workspace: Example workspace B · App: A0MUSTERDEVB",
      );
    await screen.unmount();
    await i18n.changeLanguage("ru");
    await render(
      providers(
        <SlackConnectionInfo
          connection={{ ...SLACK, bot_name: null, team_name: null, app_id: null }}
        />,
      ),
    );
    await expect
      .element(page.getByTestId("connection-bot"))
      .toHaveTextContent("Бот: станет известен после первой успешной проверки.");
  });

  test("warns while the last hello counted another program's connection, in Socket Mode only", async () => {
    const shared: SlackConnection = {
      ...SLACK,
      socket: { hello_at: "2026-10-20T09:40:00Z", other_connections: 1 },
    };
    expect(sharesApp(SLACK)).toBe(false);
    expect(sharesApp(shared)).toBe(true);
    expect(sharesApp({ ...shared, event_mode: "http" })).toBe(false);
    expect(sharesApp({ ...shared, socket: { ...shared.socket!, other_connections: 0 } })).toBe(
      false,
    );
    const screen = await render(providers(<SlackSharedAppWarning connection={shared} />));
    await expect
      .element(page.getByTestId("slack-shared-app"))
      .toHaveTextContent(
        "Another program is connected to this Slack app through Socket Mode: presses are shared between them at random. Use one Slack app for each Connection and share it with no other Connection or installation. How to fix",
      );
    await expect
      .element(
        page.getByRole("link", {
          name: "How to fix: Another program uses this Slack app (opens in a new tab)",
        }),
      )
      .toHaveAttribute(
        "href",
        "https://muster-io.github.io/muster/latest/messengers/slack/#one-slack-app-per-connection",
      );
    await screen.unmount();
    await render(providers(<SlackSharedAppWarning connection={SLACK} />));
    await expect.element(page.getByTestId("slack-shared-app")).not.toBeInTheDocument();
  });
});
