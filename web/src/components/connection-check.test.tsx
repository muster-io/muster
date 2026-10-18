// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of the form's values in a real browser (C-13.FR-14, AC-17; C-14.AC-20; C-22.AC-26): the create
// form sends its draft to POST /connections/checks once the address is filled in, and an edit form sends its draft with
// the saved Connection while it holds changes, or checks the saved Connection without a body otherwise. The skipped
// steps say why, read from the result: a draft without a token asks for it, a changed address or proxy asks for it
// again or for a save, a failed dry probe keeps "an earlier step failed"; after a dry probe alone the summary names what
// the address answers as. A changed draft hides an earlier result; a value the server would refuse is named before
// anything is sent; refusals and a busy messenger have their texts. English and Russian.

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
import { ConnectionCheck, type ConnectionCheckProps } from "./connection-check";

const SAVED_ID = "CN0000000000AA";

const NEW_MATTERMOST: ConnectionDraft = {
  type: "mattermost",
  server_url: "http://127.0.0.1:18066",
  proxy: { enabled: false, type: "http", username: null },
};

const EDIT_MATTERMOST: ConnectionDraft = { ...NEW_MATTERMOST, connection_id: SAVED_ID };

const NEW_DRAFTS: Record<Connection["type"], ConnectionDraft> = {
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
};

/** The steps of each type after its dry probe, which only a token runs. */
const TOKEN_STEPS: Record<Connection["type"], ConnectionCheckStep["name"][]> = {
  mattermost: ["token"],
  telegram: ["get_me", "get_webhook_info"],
  bridge: ["info", "event_delivery"],
};

function step(
  name: ConnectionCheckStep["name"],
  extra: Partial<ConnectionCheckStep> = {},
): ConnectionCheckStep {
  return { name, ok: true, via: "direct", ...extra };
}

/** The result of a draft whose dry probe ran alone, passed or failed, with the type's other steps skipped. */
function probeOnly(type: Connection["type"], probe: Partial<ConnectionCheckStep> = {}) {
  const dry = step("dry_probe", { latency_ms: 2, ...probe });
  return {
    ok: dry.ok,
    steps: [dry, ...TOKEN_STEPS[type].map((name) => step(name, { ok: false, skipped: true }))],
    warnings: [],
  } satisfies ConnectionCheckResult;
}

const CONNECTED: ConnectionCheckResult = {
  ok: true,
  steps: [step("token", { latency_ms: 12 })],
  warnings: [],
  bot_name: "muster-dev-bot-b",
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
        "Token: Skipped: type the bot token again to check the new address, or save first",
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
        "Токен: Пропущено: введите токен бота заново, чтобы проверить новый адрес, или сохраните изменения",
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
    await expect.poll(() => stepTexts()[1]).toBe("Токен: Пропущен: предыдущий шаг не прошёл");
  });
});
