// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of the form's values in a real browser (C-13.FR-14, AC-17; C-14.AC-20; C-22.AC-26): the create
// form sends its draft to POST /connections/checks once the address is filled in, and an edit form sends its draft with
// the saved Connection while it holds changes, or checks the saved Connection without a body otherwise. The skipped
// steps say why, read from the result: a draft without a token asks for it, a changed address or proxy asks for it
// again or for a save, a failed dry probe keeps "an earlier step failed"; after a dry probe alone the summary names what
// the address answers as. A changed draft hides an earlier result; a value the server would refuse is named before
// anything is sent; refusals and a busy messenger have their texts. A result that names its run offers "Requests of
// this check", which lists the requests of that run alone, the check before saving included. A Slack check names its
// steps and translates its four codes; the scopes a bot token lacks are named from missing_scopes, never read from the
// English message, and without them the text names none (D396). "Slack refused the token." stays the detail of the
// generic text of token_invalid. A Slack check that passes with another_client_connected warns that another program
// shares the app (C-24.FR-2). English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  Connection,
  ConnectionCheckResult,
  ConnectionCheckStep,
  ConnectionDraft,
} from "../api/gen/model";
import i18n from "../i18n";
import {
  ConnectionCheck,
  type ConnectionCheckProps,
  checkCodeText,
  checkDetail,
} from "./connection-check";

const SAVED_ID = "CN0000000000AA";

const NEW_MATTERMOST: ConnectionDraft = {
  type: "mattermost",
  server_url: "http://127.0.0.1:18066",
  proxy: { enabled: false, type: "http", username: null },
};

const EDIT_MATTERMOST: ConnectionDraft = { ...NEW_MATTERMOST, connection_id: SAVED_ID };

const NEW_DRAFTS: Record<ConnectionDraft["type"], ConnectionDraft> = {
  mattermost: NEW_MATTERMOST,
  telegram: {
    type: "telegram",
    bot_api_base_url: "http://127.0.0.1:18081",
    update_mode: "long_polling",
    proxy: { enabled: false, type: "http", username: null },
  },
  bridge: {
    type: "bridge",
    base_url: "http://127.0.0.1:18094",
    event_delivery: "poll",
    proxy: { enabled: false, type: "http", username: null },
  },
  slack: {
    type: "slack",
    event_mode: "socket",
    proxy: { enabled: false, type: "http", username: null },
  },
};

/** The steps of each type after its dry probe, which only a token runs. */
const TOKEN_STEPS: Record<Connection["type"], ConnectionCheckStep["name"][]> = {
  mattermost: ["token"],
  telegram: ["get_me", "get_webhook_info"],
  bridge: ["info", "event_delivery"],
  slack: ["token", "scopes", "app_token"],
};

function step(
  name: ConnectionCheckStep["name"],
  extra: Partial<ConnectionCheckStep> = {},
): ConnectionCheckStep {
  return { name, ok: true, via: "direct", ...extra };
}

/** The run of every check result; each result names one. */
const RUN = "CR0000000000R1";

/** The result of a draft whose dry probe ran alone, passed or failed, with the type's other steps skipped. */
function probeOnly(type: Connection["type"], probe: Partial<ConnectionCheckStep> = {}) {
  const dry = step("dry_probe", { latency_ms: 2, ...probe });
  return {
    ok: dry.ok,
    steps: [dry, ...TOKEN_STEPS[type].map((name) => step(name, { ok: false, skipped: true }))],
    warnings: [],
    request_log_run: RUN,
  } satisfies ConnectionCheckResult;
}

const CONNECTED: ConnectionCheckResult = {
  ok: true,
  steps: [step("token", { latency_ms: 12 })],
  warnings: [],
  bot_name: "muster-dev-bot-b",
  request_log_run: RUN,
};

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function problem(status: number, errors: { pointer: string; code: string }[]): Response {
  return new Response(
    JSON.stringify({
      type: "https://muster-io.github.io/muster/problems/validation-failed",
      title: "Unprocessable",
      status,
      code: "validation_failed",
      errors,
    }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}

/** Answers every request with a fresh copy of the response. */
function answer(response: () => Response) {
  return vi.spyOn(window, "fetch").mockImplementation(() => Promise.resolve(response()));
}

/** The JSON body a request sent, parsed. */
function sent(init: RequestInit | undefined): unknown {
  return typeof init?.body === "string" ? JSON.parse(init.body) : init?.body;
}

function stepTexts(): string[] {
  return page
    .getByTestId("connection-check-step")
    .elements()
    .map((e) => e.textContent ?? "");
}

/** Renders the panel; rerender keeps its QueryClient, so a mutation's result outlives a new draft. */
async function renderCheck(props: ConnectionCheckProps) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrap = (children: ReactNode) => (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
  const screen = await render(wrap(<ConnectionCheck {...props} />));
  return {
    rerender: (next: ConnectionCheckProps) => screen.rerender(wrap(<ConnectionCheck {...next} />)),
  };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the check of a new Connection", () => {
  test("sends the draft and shows the bot of the typed token", async () => {
    const draft = { ...NEW_MATTERMOST, bot_token: "muster-dev-mattermost-token-b" };
    const fetch = answer(() => json(200, CONNECTED));
    await renderCheck({ type: "mattermost", draft });
    await expect
      .element(page.getByTestId("connection-check-draft"))
      .toHaveTextContent("The check uses the values in the form and saves nothing.");
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot-b");
    await expect.poll(stepTexts).toEqual(["Token: ok · 12 ms · direct"]);
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(url).toBe("/api/v1/connections/checks");
    expect(init?.method).toBe("POST");
    expect(sent(init)).toEqual(draft);
    // The token is sent, never shown.
    expect(document.body.textContent).not.toContain("muster-dev-mattermost-token-b");
  });

  test("is enabled once the address is filled in", async () => {
    const fetch = answer(() => json(200, CONNECTED));
    const screen = await renderCheck({
      type: "mattermost",
      draft: { ...NEW_MATTERMOST, server_url: "" },
    });
    await expect.element(page.getByRole("button", { name: "Check connection" })).toBeDisabled();
    await screen.rerender({ type: "mattermost", draft: NEW_MATTERMOST });
    await expect.element(page.getByRole("button", { name: "Check connection" })).toBeEnabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  test.each([
    ["mattermost", "The address answers as a Mattermost server.", ["Token"]],
    ["telegram", "The address answers as a Bot API.", ["getMe", "getWebhookInfo"]],
    ["bridge", "The address answers as a bridge.", ["Bridge info", "Event delivery"]],
  ] as const)(
    "asks for the token after the dry probe of a %s draft without one",
    async (type, summary, names) => {
      answer(() => json(200, probeOnly(type)));
      await renderCheck({ type, draft: NEW_DRAFTS[type] });
      await page.getByRole("button", { name: "Check connection" }).click();
      await expect.element(page.getByTestId("connection-check-summary")).toHaveTextContent(summary);
      await expect
        .poll(stepTexts)
        .toEqual([
          "Dry probe without the token: ok · 2 ms · direct",
          ...names.map((name) => `${name}: Skipped: type the bot token to check it`),
        ]);
    },
  );

  test("keeps 'an earlier step failed' after a failed dry probe, with the text of its code", async () => {
    answer(() =>
      json(
        200,
        probeOnly("bridge", {
          ok: false,
          code: "not_bridge",
          message:
            "The address answered 200 text/html, not a problem: it is not a bridge, or the path prefix is wrong.",
        }),
      ),
    );
    await renderCheck({ type: "bridge", draft: NEW_DRAFTS.bridge });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The check failed.");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Dry probe without the token: failed 2 ms · directThe address answers, but not as a bridge: a bridge answers a request without its token with 401 and a problem.Details: The address answered 200 text/html, not a problem: it is not a bridge, or the path prefix is wrong.",
        "Bridge info: Skipped: an earlier step failed",
        "Event delivery: Skipped: an earlier step failed",
      ]);
  });

  test("names an address that does not answer as a Mattermost server", async () => {
    answer(() =>
      json(200, probeOnly("mattermost", { ok: false, code: "not_mattermost", message: null })),
    );
    await renderCheck({ type: "mattermost", draft: NEW_MATTERMOST });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("The address answers, but not as a Mattermost server.");
    await expect.poll(() => stepTexts()[1]).toBe("Token: Skipped: an earlier step failed");
  });

  test("names a value the server would refuse, and sends nothing", async () => {
    const fetch = answer(() => json(200, CONNECTED));
    await renderCheck({
      type: "mattermost",
      draft: { ...NEW_MATTERMOST, server_url: "chat" },
      invalid: "Server URL: This value has the wrong format.",
    });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("Server URL: This value has the wrong format.");
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("the check of a saved Connection", () => {
  test("checks the saved Connection without a body while the form holds no changes", async () => {
    const fetch = answer(() => json(200, { ...CONNECTED, bot_name: "muster-dev-bot" }));
    await renderCheck({ connectionId: SAVED_ID, type: "mattermost" });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot");
    await expect.element(page.getByTestId("connection-check-draft")).not.toBeInTheDocument();
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(url).toBe(`/api/v1/connections/${SAVED_ID}/checks`);
    expect(init?.body ?? null).toBeNull();
  });

  test("checks the draft with the saved Connection, and asks for the token again or a save", async () => {
    const fetch = answer(() => json(200, probeOnly("mattermost")));
    await renderCheck({ connectionId: SAVED_ID, type: "mattermost", draft: EDIT_MATTERMOST });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The address answers as a Mattermost server.");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Dry probe without the token: ok · 2 ms · direct",
        "Token: Skipped: type the bot token again to check the new address or proxy, or save first",
      ]);
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(url).toBe("/api/v1/connections/checks");
    expect(sent(init)).toEqual(EDIT_MATTERMOST);
  });

  test("hides an earlier result once the form changes", async () => {
    const fetch = answer(() => json(200, probeOnly("mattermost")));
    const screen = await renderCheck({
      connectionId: SAVED_ID,
      type: "mattermost",
      draft: EDIT_MATTERMOST,
    });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect.element(page.getByTestId("connection-check-result")).toBeVisible();

    const changed = { ...EDIT_MATTERMOST, server_url: "http://127.0.0.1:18067" };
    await screen.rerender({ connectionId: SAVED_ID, type: "mattermost", draft: changed });
    await expect.element(page.getByTestId("connection-check-result")).not.toBeInTheDocument();
    // Back to the values the result was made for: it stays cleared.
    await screen.rerender({ connectionId: SAVED_ID, type: "mattermost", draft: EDIT_MATTERMOST });
    await expect.element(page.getByTestId("connection-check-result")).not.toBeInTheDocument();
    // The form without changes checks the saved Connection again.
    await screen.rerender({ connectionId: SAVED_ID, type: "mattermost" });
    await expect.element(page.getByTestId("connection-check-draft")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect.element(page.getByTestId("connection-check-result")).toBeVisible();
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/connections/checks",
      `/api/v1/connections/${SAVED_ID}/checks`,
    ]);
  });

  test("asks for a reload when the Connection is gone, and names a refused field", async () => {
    answer(() => problem(422, [{ pointer: "/connection_id", code: "unknown_id" }]));
    const screen = await renderCheck({
      connectionId: SAVED_ID,
      type: "mattermost",
      draft: EDIT_MATTERMOST,
    });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("This Connection no longer exists. Reload the page.");

    vi.restoreAllMocks();
    answer(() => problem(422, [{ pointer: "/proxy/address", code: "invalid_format" }]));
    const proxied: ConnectionDraft = {
      ...EDIT_MATTERMOST,
      proxy: { enabled: true, type: "http", address: "proxy", username: null },
    };
    await screen.rerender({ connectionId: SAVED_ID, type: "mattermost", draft: proxied });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("Proxy: This value has the wrong format.");
  });

  test("names the wait of a busy messenger", async () => {
    answer(() =>
      json(
        503,
        { type: "about:blank", title: "Service Unavailable", status: 503 },
        { "Retry-After": "4" },
      ),
    );
    await renderCheck({ connectionId: SAVED_ID, type: "mattermost", draft: EDIT_MATTERMOST });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("The messenger is busy; try again in 4 s.");
  });
});

describe("the check in Russian", () => {
  test("asks for the token of a new Connection and names what the address answers as", async () => {
    await i18n.changeLanguage("ru");
    answer(() => json(200, probeOnly("bridge")));
    await renderCheck({ type: "bridge", draft: NEW_DRAFTS.bridge });
    await expect
      .element(page.getByTestId("connection-check-draft"))
      .toHaveTextContent("Проверка берёт значения из формы и ничего не сохраняет.");
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("По этому адресу отвечает мост.");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Пробный запрос без токена: успешно · 2 мс · напрямую",
        "Сведения о мосте: Пропущено: введите токен бота, чтобы проверить его",
        "Доставка событий: Пропущено: введите токен бота, чтобы проверить его",
      ]);
  });

  test("asks for the token again or a save, and names a Connection that is gone", async () => {
    await i18n.changeLanguage("ru");
    answer(() => json(200, probeOnly("mattermost")));
    const screen = await renderCheck({
      connectionId: SAVED_ID,
      type: "mattermost",
      draft: EDIT_MATTERMOST,
    });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("По этому адресу отвечает сервер Mattermost.");
    await expect
      .poll(() => stepTexts()[1])
      .toBe(
        "Токен: Пропущено: введите токен бота заново, чтобы проверить новый адрес или прокси, или сохраните изменения",
      );

    vi.restoreAllMocks();
    answer(() => problem(422, [{ pointer: "/connection_id", code: "unknown_id" }]));
    await screen.rerender({
      connectionId: SAVED_ID,
      type: "mattermost",
      draft: { ...EDIT_MATTERMOST, server_url: "http://127.0.0.1:18067" },
    });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("Этого подключения больше нет. Обновите страницу.");
  });

  test("shows the English message as the detail only when it says more than the text of its code", async () => {
    await i18n.changeLanguage("ru");
    const tokenRefused = {
      ok: false,
      steps: [
        step("token", {
          ok: false,
          latency_ms: 4,
          code: "token_invalid",
          message: "The bot token is not valid.",
        }),
      ],
      warnings: [],
      request_log_run: RUN,
    } satisfies ConnectionCheckResult;
    answer(() => json(200, tokenRefused));
    const screen = await renderCheck({ connectionId: SAVED_ID, type: "mattermost" });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("Токен бота недействителен.");
    // The message is the English text of its code: no English repeats the Russian text.
    await expect
      .poll(stepTexts)
      .toEqual(["Токен: ошибка 4 мс · напрямуюТокен бота недействителен."]);
    expect(page.getByTestId("connection-check-step-detail").elements()).toHaveLength(0);

    vi.restoreAllMocks();
    answer(() =>
      json(
        200,
        probeOnly("mattermost", {
          ok: false,
          code: "dns_failed",
          message: "DNS lookup failed: mm.example.invalid",
        }),
      ),
    );
    await screen.rerender({
      type: "mattermost",
      draft: { ...NEW_MATTERMOST, server_url: "http://mm.example.invalid" },
    });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("Имя хоста не найдено.");
    // A message that names the host says more, and stays as the detail, small and muted.
    await expect
      .element(page.getByTestId("connection-check-step-detail"))
      .toHaveTextContent("Подробности: DNS lookup failed: mm.example.invalid");
    expect(page.getByTestId("connection-check-step-detail").element().className).toContain(
      "text-muted-foreground",
    );
  });

  test("names an address that is not a Mattermost server", async () => {
    await i18n.changeLanguage("ru");
    answer(() =>
      json(200, probeOnly("mattermost", { ok: false, code: "not_mattermost", message: null })),
    );
    await renderCheck({ type: "mattermost", draft: NEW_MATTERMOST });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("Адрес отвечает, но не как сервер Mattermost.");
    await expect.poll(() => stepTexts()[1]).toBe("Токен: Пропущено: предыдущий шаг не прошёл");
  });
});

/** The English message of the step scopes for a bot token without users:read.email, as the server writes it. */
const MISSING_EMAIL = "The bot token lacks the scopes users:read.email.";

/** The scopes of the step scopes for a bot token without users:read.email, as the server lists them. */
const EMAIL_SCOPE = ["users:read.email"];

/**
 * A Slack check whose step failed with the code, message and the scopes the bot token lacks; the steps after it are
 * skipped.
 */
function slackFailed(
  failing: "token" | "scopes" | "app_token",
  code: NonNullable<ConnectionCheckStep["code"]>,
  message: string,
  missingScopes?: string[],
): ConnectionCheckResult {
  const names = TOKEN_STEPS.slack;
  const at = names.indexOf(failing);
  return {
    ok: false,
    steps: names.map((name, index) =>
      index < at
        ? step(name, name === "scopes" ? {} : { latency_ms: 12 })
        : index === at
          ? step(name, {
              ok: false,
              code,
              message,
              ...(missingScopes === undefined ? {} : { missing_scopes: missingScopes }),
              ...(name === "scopes" ? {} : { latency_ms: 4 }),
            })
          : step(name, { ok: false, skipped: true }),
    ),
    warnings: [],
    bot_name: failing === "token" ? null : "muster-dev-slack-bot",
    request_log_run: RUN,
  };
}

describe("the check of a Slack Connection", () => {
  test("names its steps and the bot it is connected as", async () => {
    answer(() =>
      json(200, {
        ok: true,
        steps: [
          step("token", { latency_ms: 12 }),
          step("scopes"),
          step("app_token", { latency_ms: 7 }),
        ],
        warnings: [],
        bot_name: "muster-dev-slack-bot",
        request_log_run: RUN,
      } satisfies ConnectionCheckResult),
    );
    await renderCheck({ connectionId: SAVED_ID, type: "slack" });
    await expect
      .element(
        page.getByText(
          "Checks the bot token with auth.test and the scopes it grants, and in Socket Mode that the app-level token belongs to the same Slack app.",
        ),
      )
      .toBeVisible();
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-slack-bot");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Token: ok · 12 ms · direct",
        "Scopes: ok · direct",
        "App-level token: ok · 7 ms · direct",
      ]);
  });

  test.each([
    [
      "token",
      "wrong_token_type",
      "This is not a bot token.",
      undefined,
      [
        "Token: failed 4 ms · directThis is not a bot token.",
        "Scopes: Skipped: an earlier step failed",
        "App-level token: Skipped: an earlier step failed",
      ],
    ],
    [
      "scopes",
      "missing_scope",
      MISSING_EMAIL,
      EMAIL_SCOPE,
      [
        "Token: ok · 12 ms · direct",
        "Scopes: failed directThe bot token lacks the scopes users:read.email.",
        "App-level token: Skipped: an earlier step failed",
      ],
    ],
    [
      "app_token",
      "app_mismatch",
      "The app-level token belongs to another Slack app than the bot token.",
      undefined,
      [
        "Token: ok · 12 ms · direct",
        "Scopes: ok · direct",
        "App-level token: failed 4 ms · directThe app-level token belongs to another Slack app than the bot token.",
      ],
    ],
  ] as const)(
    "translates a failed step %s with %s, without repeating its message",
    async (failing, code, message, scopes, texts) => {
      answer(() => json(200, slackFailed(failing, code, message, scopes)));
      await renderCheck({ connectionId: SAVED_ID, type: "slack" });
      await page.getByRole("button", { name: "Check connection" }).click();
      await expect
        .element(page.getByTestId("connection-check-summary"))
        .toHaveTextContent("The check failed.");
      await expect.poll(stepTexts).toEqual(texts);
      expect(page.getByTestId("connection-check-step-detail").elements()).toHaveLength(0);
    },
  );

  test("keeps the generic text of a refused token, with Slack's refusal as its detail", async () => {
    answer(() => json(200, slackFailed("token", "token_invalid", "Slack refused the token.")));
    await renderCheck({ connectionId: SAVED_ID, type: "slack" });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("The bot token is not valid.");
    await expect
      .element(page.getByTestId("connection-check-step-detail"))
      .toHaveTextContent("Details: Slack refused the token.");
  });

  test("reads in Russian, the scopes the bot token lacks included", async () => {
    await i18n.changeLanguage("ru");
    answer(() => json(200, slackFailed("scopes", "missing_scope", MISSING_EMAIL, EMAIL_SCOPE)));
    await renderCheck({ connectionId: SAVED_ID, type: "slack" });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await expect
      .poll(stepTexts)
      .toEqual([
        "Токен: успешно · 12 мс · напрямую",
        "Права: ошибка напрямуюУ токена бота нет прав users:read.email.",
        "Токен уровня приложения: Пропущено: предыдущий шаг не прошёл",
      ]);
    expect(page.getByTestId("connection-check-step-detail").elements()).toHaveLength(0);
  });

  test.each([
    [
      "en",
      "Check connection",
      "The bot token lacks the scopes chat:write, users:read.email.",
      "Details: Slack wants more scopes.",
    ],
    [
      "ru",
      "Проверить подключение",
      "У токена бота нет прав chat:write, users:read.email.",
      "Подробности: Slack wants more scopes.",
    ],
  ] as const)(
    "names the scopes a check misses from missing_scopes, in English and Russian (%s)",
    async (language, start, reason, detail) => {
      await i18n.changeLanguage(language);
      // The message names no scope: the text takes them from missing_scopes alone.
      answer(() =>
        json(
          200,
          slackFailed("scopes", "missing_scope", "Slack wants more scopes.", [
            "chat:write",
            "users:read.email",
          ]),
        ),
      );
      await renderCheck({ connectionId: SAVED_ID, type: "slack" });
      await page.getByRole("button", { name: start }).click();
      await expect
        .element(page.getByTestId("connection-check-step-reason"))
        .toHaveTextContent(reason);
      await expect
        .element(page.getByTestId("connection-check-step-detail"))
        .toHaveTextContent(detail);
    },
  );

  test("shows the unnamed text without missing_scopes", async () => {
    // The English message names a scope, but the text never reads it from there.
    answer(() => json(200, slackFailed("scopes", "missing_scope", MISSING_EMAIL)));
    await renderCheck({ connectionId: SAVED_ID, type: "slack" });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-step-reason"))
      .toHaveTextContent("The bot token lacks scopes that Muster needs.");
    await expect
      .element(page.getByTestId("connection-check-step-detail"))
      .toHaveTextContent("Details: The bot token lacks the scopes users:read.email.");
  });

  test.each([
    [
      "en",
      "Check connection",
      "Connected as muster-dev-slack-bot",
      "Another program is connected to this Slack app through Socket Mode: presses are shared between them at random. Use one Slack app for each Connection and share it with no other Connection or installation.",
    ],
    [
      "ru",
      "Проверить подключение",
      "Подключено как muster-dev-slack-bot",
      "К этому приложению Slack через Socket Mode подключена другая программа: нажатия случайным образом делятся между ними. Используйте отдельное приложение Slack для каждого подключения и не используйте его в других подключениях и установках.",
    ],
  ] as const)(
    "passes with the warning that another program shares the Slack app (%s)",
    async (language, start, summary, warning) => {
      await i18n.changeLanguage(language);
      answer(() =>
        json(200, {
          ok: true,
          steps: [
            step("token", { latency_ms: 12 }),
            step("scopes"),
            step("app_token", { latency_ms: 7 }),
          ],
          warnings: ["another_client_connected"],
          bot_name: "muster-dev-slack-bot",
          request_log_run: RUN,
        } satisfies ConnectionCheckResult),
      );
      await renderCheck({ connectionId: SAVED_ID, type: "slack" });
      await page.getByRole("button", { name: start }).click();
      await expect.element(page.getByTestId("connection-check-summary")).toHaveTextContent(summary);
      await expect.element(page.getByTestId("connection-check-warning")).toHaveTextContent(warning);
      expect(page.getByTestId("connection-check-warning").elements()).toHaveLength(1);
    },
  );

  test("translates the four codes of a Slack check in English and Russian", () => {
    const en = i18n.getFixedT("en");
    const ru = i18n.getFixedT("ru");
    const scopes = ["chat:write", "users:read.email"];
    const twoScopes = "The bot token lacks the scopes chat:write, users:read.email.";
    expect(checkCodeText(en, "wrong_token_type")).toBe("This is not a bot token.");
    expect(checkCodeText(ru, "wrong_token_type")).toBe("Это не токен бота.");
    expect(checkCodeText(en, "missing_scope", scopes)).toBe(twoScopes);
    expect(checkCodeText(ru, "missing_scope", scopes)).toBe(
      "У токена бота нет прав chat:write, users:read.email.",
    );
    // Without missing_scopes, such as in a list of codes, the text names no scope.
    expect(checkCodeText(en, "missing_scope")).toBe(
      "The bot token lacks scopes that Muster needs.",
    );
    expect(checkCodeText(ru, "missing_scope", null)).toBe(
      "У токена бота нет прав, которые нужны Muster.",
    );
    expect(checkCodeText(en, "missing_scope", [])).toBe(
      "The bot token lacks scopes that Muster needs.",
    );
    expect(checkCodeText(en, "app_mismatch")).toBe(
      "The app-level token belongs to another Slack app than the bot token.",
    );
    expect(checkCodeText(ru, "app_mismatch")).toBe(
      "Токен уровня приложения и токен бота принадлежат разным приложениям Slack.",
    );
    expect(checkCodeText(en, "not_slack")).toBe("The address answered, but not as Slack's API.");
    expect(checkCodeText(ru, "not_slack")).toBe("Адрес ответил, но не как API Slack.");
    // The server's English text of a code is no detail; Slack's refusal under token_invalid is.
    expect(checkDetail("missing_scope", twoScopes, scopes)).toBeNull();
    expect(
      checkDetail("missing_scope", "The bot token lacks scopes that Muster needs."),
    ).toBeNull();
    // The English message alone names no scope: without missing_scopes it is the detail of the unnamed text.
    expect(checkDetail("missing_scope", twoScopes)).toBe(twoScopes);
    expect(checkDetail("wrong_token_type", "This is not a bot token.")).toBeNull();
    expect(checkDetail("token_invalid", "Slack refused the token.")).toBe(
      "Slack refused the token.",
    );
  });
});

/** Answers the check with the result and the Request log with the record of its run; returns the list's queries. */
function serveRun(result: ConnectionCheckResult): Record<string, string>[] {
  const queries: Record<string, string>[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const [path = "", search = ""] = url.split("?");
    if (path === "/api/v1/request-log") {
      queries.push(Object.fromEntries(new URLSearchParams(search)));
      return json(200, {
        items: [
          {
            id: "RQ0000000000AA",
            at: "2026-10-18T06:00:12Z",
            direction: "outbound",
            operation: "getInfo",
            method: "GET",
            url: "http://127.0.0.1:18094/v1/info",
            status: 200,
            latency_ms: 3,
            via: "direct",
            attempt: 1,
            error_class: null,
            error_code: null,
            check: "draft_check",
            check_run: RUN,
            connection_id: null,
            destination: null,
            alert_group: null,
            request_body_truncated: false,
            response_body_truncated: false,
          },
        ],
        next_cursor: null,
      });
    }
    if (path === "/api/v1/sessions/current") {
      return json(401, { title: "Unauthorized", status: 401 });
    }
    return json(200, result);
  });
  return queries;
}

describe("the requests of a check", () => {
  test("opens the requests of the run of a check before saving", async () => {
    const queries = serveRun(probeOnly("bridge"));
    await renderCheck({
      type: "bridge",
      draft: { ...NEW_DRAFTS.bridge, bot_token: "muster-dev-bridge-token-b" },
    });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The address answers as a bridge.");
    await page.getByRole("button", { name: "Requests of this check" }).click();
    const list = page.getByRole("dialog", { name: "Requests of this check" });
    await expect.element(list.getByRole("button", { name: "getInfo" })).toBeVisible();
    await expect
      .element(list.getByTestId("request-check"))
      .toHaveTextContent("Check before saving");
    // The run alone: a new Connection has no owner yet.
    expect(queries).toEqual([{ run: RUN }]);
    expect(document.body.textContent).not.toContain("muster-dev-bridge-token-b");
  });

  test("opens the requests of the run of a saved Connection's check", async () => {
    const queries = serveRun(CONNECTED);
    await renderCheck({ connectionId: SAVED_ID, type: "mattermost" });
    await page.getByRole("button", { name: "Check connection" }).click();
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot-b");
    await page.getByRole("button", { name: "Requests of this check" }).click();
    await expect
      .element(
        page
          .getByRole("dialog", { name: "Requests of this check" })
          .getByTestId("request-operation"),
      )
      .toHaveTextContent("getInfo");
    expect(queries).toEqual([{ run: RUN }]);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    serveRun(probeOnly("bridge"));
    await renderCheck({ type: "bridge", draft: NEW_DRAFTS.bridge });
    await page.getByRole("button", { name: "Проверить подключение" }).click();
    await page.getByRole("button", { name: "Запросы этой проверки" }).click();
    await expect
      .element(
        page.getByRole("dialog", { name: "Запросы этой проверки" }).getByTestId("request-check"),
      )
      .toHaveTextContent("Проверка перед сохранением");
  });
});
