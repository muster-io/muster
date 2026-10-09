// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Telegram Connection in a real browser: the base URL with its hint and the warning of an http address, from the
// server's warnings for the saved address and at once for a typed one; the update mode with its hints; the request a
// save sends and the checks it repeats; the refusals at /bot_api_base_url and /update_mode; and the step-by-step check
// with the bot, the pending updates, the webhook warning in the long-polling mode, the unsaved address that gets only
// the dry probe with the other steps skipped, the steps skipped after a failed dry probe, and a busy messenger.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  ConnectionCheckResult,
  ConnectionInput,
  Permission,
  Session,
  TelegramConnection,
  TelegramUpdateMode,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { ConnectionCheck, checkErrorText } from "./connection-check";
import {
  BASE_URL_FORMAT,
  ConnectionForm,
  TOKEN_FOR_NEW_SERVER,
  addressChanged,
  connectionErrors,
  connectionInput,
  connectionValues,
  serverDetails,
  serverErrors,
} from "./connection-form";
import {
  TelegramConnectionFields,
  isBaseUrl,
  normalizeBaseUrl,
  usesHttp,
} from "./telegram-connection-fields";

const HINT =
  "A self-hosted Bot API server keeps the bot on that server only. Do not call api.telegram.org with this token from anywhere else: even getMe moves the bot back to Telegram's cloud, and presses and comments start to go missing without an error.";
const HTTP_WARNING =
  "The bot token travels in clear text over http. Use https unless this path is a private network or a tunnel.";
const BASE_URL_TEXT =
  "Enter an absolute http or https URL without user information, query or fragment, such as https://api.telegram.org. A path prefix is allowed.";
const WEBHOOK_SET = "A webhook is set: long polling fails with 409 while it stays.";

const CONNECTION: TelegramConnection = {
  id: "CN0000000000TG",
  type: "telegram",
  name: "tg",
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

function refusal(status: number, problem: Record<string, unknown>): ApiError {
  return new ApiError(
    status,
    { type: "https://muster-io.github.io/muster/problems/x", ...problem },
    undefined,
  );
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

const WRITE: Permission[] = ["connections:read", "connections:write"];

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

describe("the base URL", () => {
  test("is normalized and checked as the server does", () => {
    expect(normalizeBaseUrl(" http://127.0.0.1:18081/other/ ")).toBe(
      "http://127.0.0.1:18081/other",
    );
    expect(normalizeBaseUrl("https://api.telegram.org/")).toBe("https://api.telegram.org");
    expect(normalizeBaseUrl("HTTPS://api.telegram.org")).toBe("https://api.telegram.org");
    expect(isBaseUrl("https://api.telegram.org")).toBe(true);
    expect(isBaseUrl("http://10.0.0.5:8081/k3x9/")).toBe(true);
    for (const bad of [
      "",
      "api.telegram.org",
      "ftp://api.telegram.org",
      "https://user:pw@api.telegram.org",
      "https://api.telegram.org/?x=1",
      "https://api.telegram.org/#x",
      "https:api.telegram.org",
    ]) {
      expect(isBaseUrl(bad), bad).toBe(false);
    }
    expect(usesHttp(" HTTP://127.0.0.1:18081")).toBe(true);
    expect(usesHttp("https://api.telegram.org")).toBe(false);
  });
});

describe("the request of a save", () => {
  test("sends the base URL and the update mode, and the bot token only when it was replaced", () => {
    const fresh = connectionValues(undefined, "telegram");
    expect(fresh.bot_api_base_url).toBe("https://api.telegram.org");
    expect(fresh.update_mode).toBe("long_polling");
    expect(fresh.limiter).toEqual({ limit: "15", per_seconds: "1" });
    const stored = connectionValues(CONNECTION);
    expect(connectionInput(stored)).toEqual({
      type: "telegram",
      name: "tg",
      bot_api_base_url: "http://127.0.0.1:18081",
      update_mode: "long_polling",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 15, per_seconds: 1 },
    });
    expect(
      connectionInput({ ...stored, bot_token: { mode: "replace", value: "777010:ui-token" } }),
    ).toEqual(expect.objectContaining({ bot_token: "777010:ui-token" }));
  });

  test("checks the base URL and asks for the token again for a new one", () => {
    const fresh = connectionValues(undefined, "telegram");
    expect(connectionErrors({ ...fresh, name: "tg", bot_api_base_url: "" }, undefined)).toEqual({
      "/bot_api_base_url": "required",
      "/bot_token": "required",
    });
    expect(
      connectionErrors({ ...fresh, name: "tg", bot_api_base_url: "telegram" }, undefined)[
        "/bot_api_base_url"
      ],
    ).toBe(BASE_URL_FORMAT);
    const stored = connectionValues(CONNECTION);
    expect(connectionErrors(stored, CONNECTION)).toEqual({});
    // A trailing slash is the same address.
    expect(
      connectionErrors({ ...stored, bot_api_base_url: "http://127.0.0.1:18081/" }, CONNECTION),
    ).toEqual({});
    const moved = { ...stored, bot_api_base_url: "http://127.0.0.1:18081/other" };
    expect(addressChanged(moved, CONNECTION)).toBe(true);
    expect(connectionErrors(moved, CONNECTION)).toEqual({ "/bot_token": TOKEN_FOR_NEW_SERVER });
  });

  test("maps the refusals of the base URL and of a webhook call to their fields", () => {
    expect(
      serverErrors(
        refusal(422, {
          code: "validation_failed",
          errors: [{ pointer: "/bot_api_base_url", code: "invalid_format", detail: "x" }],
        }),
        false,
      ),
    ).toEqual({ "/bot_api_base_url": BASE_URL_FORMAT });
    const webhook = refusal(422, {
      code: "validation_failed",
      errors: [
        {
          pointer: "/update_mode",
          code: "webhook_call_failed",
          detail: "setWebhook failed: bad webhook: HTTPS url must be provided for webhook",
        },
      ],
    });
    expect(serverErrors(webhook, false)).toEqual({ "/update_mode": "webhook_call_failed" });
    expect(serverDetails(webhook)).toEqual({
      "/update_mode": "setWebhook failed: bad webhook: HTTPS url must be provided for webhook",
    });
    expect(serverDetails(new Error("network"))).toEqual({});
  });
});

function Fields({
  baseUrl,
  mode = "long_polling",
  saved,
}: {
  baseUrl: string;
  mode?: TelegramUpdateMode;
  saved?: { baseUrl: string; warnings: "base_url_uses_http"[] };
}) {
  return (
    <TelegramConnectionFields
      id="t"
      baseUrl={baseUrl}
      onBaseUrlChange={() => {}}
      updateMode={mode}
      onUpdateModeChange={() => {}}
      errors={{}}
      disabled={false}
      saved={saved}
    />
  );
}

describe("TelegramConnectionFields", () => {
  test("shows the hint under the base URL, and the http warning only for http", async () => {
    const screen = await render(providers(<Fields baseUrl="https://api.telegram.org" />));
    await expect.element(page.getByTestId("telegram-base-url-hint")).toHaveTextContent(HINT);
    await expect
      .element(page.getByLabelText("Bot API base URL"))
      .toHaveAttribute("aria-describedby", "t-bot-api-base-url-hint");
    await expect.element(page.getByTestId("telegram-http-warning")).not.toBeInTheDocument();
    await screen.rerender(providers(<Fields baseUrl="http://127.0.0.1:18081" />));
    await expect.element(page.getByTestId("telegram-http-warning")).toHaveTextContent(HTTP_WARNING);
    await expect
      .element(page.getByLabelText("Bot API base URL"))
      .toHaveAttribute("aria-describedby", "t-bot-api-base-url-hint t-bot-api-base-url-warning");
  });

  test("takes the warning of the saved address from the server", async () => {
    const screen = await render(
      providers(
        <Fields
          baseUrl="http://127.0.0.1:18081/"
          saved={{ baseUrl: "http://127.0.0.1:18081", warnings: [] }}
        />,
      ),
    );
    await expect.element(page.getByTestId("telegram-http-warning")).not.toBeInTheDocument();
    await screen.rerender(
      providers(
        <Fields
          baseUrl="http://127.0.0.1:18081"
          saved={{ baseUrl: "http://127.0.0.1:18081", warnings: ["base_url_uses_http"] }}
        />,
      ),
    );
    await expect.element(page.getByTestId("telegram-http-warning")).toBeVisible();
  });

  test("explains each update mode", async () => {
    const screen = await render(providers(<Fields baseUrl="https://api.telegram.org" />));
    await expect.element(page.getByLabelText("Update mode")).toHaveValue("long_polling");
    await expect
      .element(page.getByTestId("telegram-update-mode-hint"))
      .toHaveTextContent(
        "The Leader asks Telegram for updates; Muster needs no public address. Only one process may poll a bot: give each installation its own bot.",
      );
    await screen.rerender(providers(<Fields baseUrl="https://api.telegram.org" mode="webhook" />));
    await expect
      .element(page.getByTestId("telegram-update-mode-hint"))
      .toHaveTextContent(
        "Telegram posts each update to Muster's Ingestion URL (MUSTER_INGEST_URL), which it must reach over HTTPS. Saving in this mode sets the webhook; leaving the mode or deleting the Connection removes it.",
      );
  });

  test("shows the hint and the warning in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(providers(<Fields baseUrl="http://127.0.0.1:18081" mode="webhook" />));
    await expect
      .element(page.getByTestId("telegram-base-url-hint"))
      .toHaveTextContent(
        "Собственный сервер Bot API держит бота только на этом сервере. Не обращайтесь к api.telegram.org с этим токеном ни из какого другого места: даже getMe возвращает бота в облако Telegram, и нажатия кнопок и комментарии начинают пропадать без всякой ошибки.",
      );
    await expect
      .element(page.getByTestId("telegram-http-warning"))
      .toHaveTextContent(
        "По http токен бота передаётся открытым текстом. Используйте https, если только этот путь не проходит по частной сети или туннелю.",
      );
    await expect.element(page.getByLabelText("Режим получения обновлений")).toHaveValue("webhook");
    await expect.element(page.getByRole("option", { name: "Вебхук" })).toBeInTheDocument();
  });
});

describe("ConnectionForm of a Telegram Connection", () => {
  test("creates a Connection with the default base URL, the token, the update mode and the limiter", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const baseUrls: string[] = [];
    await render(
      providers(
        <ConnectionForm
          type="telegram"
          submitLabel="Save"
          save={save}
          onBaseUrlChange={(v) => baseUrls.push(v)}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Telegram" })).toBeVisible();
    await expect
      .element(page.getByLabelText("Bot API base URL"))
      .toHaveValue("https://api.telegram.org");
    await expect.element(page.getByLabelText("Server URL")).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Requests")).toHaveValue("15");
    await userEvent.fill(page.getByLabelText("Name"), "tg");
    await page.getByRole("button", { name: "Set a value" }).click();
    await userEvent.fill(page.getByLabelText("Bot token"), "777010:ui-token");
    await userEvent.fill(page.getByLabelText("Bot API base URL"), "http://127.0.0.1:18081");
    await expect.element(page.getByTestId("telegram-http-warning")).toHaveTextContent(HTTP_WARNING);
    await userEvent.selectOptions(page.getByLabelText("Update mode"), "webhook");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "telegram",
      name: "tg",
      bot_api_base_url: "http://127.0.0.1:18081",
      update_mode: "webhook",
      bot_token: "777010:ui-token",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 15, per_seconds: 1 },
    });
    expect(baseUrls.at(-1)).toBe("http://127.0.0.1:18081");
    expect(document.body.textContent).not.toContain("777010:ui-token");
  });

  test("shows the refusals at the base URL and at the update mode next to them", async () => {
    const save = vi
      .fn((_input: ConnectionInput) => Promise.resolve(undefined))
      .mockRejectedValueOnce(
        refusal(422, {
          code: "validation_failed",
          errors: [{ pointer: "/bot_api_base_url", code: "invalid_format", detail: "x" }],
        }),
      )
      .mockRejectedValueOnce(
        refusal(422, {
          code: "validation_failed",
          errors: [
            {
              pointer: "/update_mode",
              code: "webhook_call_failed",
              detail: "setWebhook failed: Bad Request: bad webhook",
            },
          ],
        }),
      );
    await render(
      providers(<ConnectionForm connection={CONNECTION} submitLabel="Save" save={save} />),
    );
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText(BASE_URL_TEXT)).toBeVisible();
    await expect.element(page.getByLabelText("Bot API base URL")).toHaveFocus();
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page.getByText(
          "Telegram refused the webhook change, and nothing was saved: setWebhook failed: Bad Request: bad webhook",
        ),
      )
      .toBeVisible();
    await expect
      .element(page.getByLabelText("Update mode"))
      .toHaveAttribute("aria-invalid", "true");
    await expect.element(page.getByLabelText("Update mode")).toHaveFocus();
  });

  test("offers to reload when the Connection changed elsewhere", async () => {
    const save = vi.fn(() =>
      Promise.reject(
        new ApiError(
          412,
          { type: "about:blank", title: "Precondition Failed", status: 412 },
          undefined,
        ),
      ),
    );
    const onReload = vi.fn();
    await render(
      providers(
        <ConnectionForm
          connection={CONNECTION}
          submitLabel="Save"
          save={save}
          onReload={onReload}
        />,
      ),
    );
    await userEvent.fill(page.getByLabelText("Name"), "tg-renamed");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("connection-stale"))
      .toHaveTextContent("Someone else changed this Connection. Reload to see the changes.Reload");
    await page.getByTestId("connection-stale").getByRole("button", { name: "Reload" }).click();
    expect(onReload).toHaveBeenCalledOnce();
  });
});

const PASSED: ConnectionCheckResult = {
  ok: true,
  steps: [
    { name: "dry_probe", ok: true, latency_ms: 3, via: "direct", message: null },
    { name: "get_me", ok: true, latency_ms: 4, via: "direct", message: null },
    { name: "get_webhook_info", ok: true, latency_ms: 5, via: "direct", message: null },
  ],
  warnings: [],
  bot_name: "muster_dev_bot",
  webhook_set: false,
  pending_updates: 0,
};

async function runCheck(
  response: Response,
  props: { unsavedBaseUrl?: string; updateMode?: TelegramUpdateMode } = {},
  button = "Check connection",
) {
  const fetch = vi.spyOn(window, "fetch").mockResolvedValue(response);
  await render(
    providers(
      <ConnectionCheck
        connectionId="CN0000000000TG"
        type="telegram"
        dirty={props.unsavedBaseUrl !== undefined}
        unsavedBaseUrl={props.unsavedBaseUrl}
        updateMode={props.updateMode ?? "long_polling"}
      />,
    ),
  );
  await page.getByRole("button", { name: button }).click();
  return fetch;
}

function stepTexts(): string[] {
  return page
    .getByTestId("connection-check-step")
    .elements()
    .map((e) => e.textContent ?? "");
}

describe("ConnectionCheck of a Telegram Connection", () => {
  test("shows the three steps with their latency and path, the bot and the pending updates", async () => {
    const fetch = await runCheck(json(200, PASSED));
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as @muster_dev_bot");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Dry probe without the token: ok · 3 ms · direct",
        "getMe: ok · 4 ms · direct · Bot: @muster_dev_bot",
        "getWebhookInfo: ok · 5 ms · direct · 0 pending updates",
      ]);
    await expect.element(page.getByTestId("connection-check-warning")).not.toBeInTheDocument();
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(url).toBe("/api/v1/connections/CN0000000000TG/checks");
    expect(init?.body ?? null).toBeNull();
  });

  test("warns of a set webhook in the long-polling mode only, with its host", async () => {
    const hooked = {
      ...PASSED,
      steps: [
        PASSED.steps[0],
        PASSED.steps[1],
        { ...PASSED.steps[2], message: "A webhook is set at hooks.example.org." },
      ],
      webhook_set: true,
      pending_updates: 1,
    };
    await runCheck(json(200, hooked));
    await expect
      .element(page.getByTestId("connection-check-warning"))
      .toHaveTextContent(WEBHOOK_SET);
    await expect
      .poll(() => stepTexts()[2])
      .toBe(
        "getWebhookInfo: ok · 5 ms · direct · 1 pending updateA webhook is set at hooks.example.org.",
      );
  });

  test("shows no webhook warning in the webhook mode", async () => {
    await runCheck(json(200, { ...PASSED, webhook_set: true }), { updateMode: "webhook" });
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as @muster_dev_bot");
    await expect.element(page.getByTestId("connection-check-warning")).not.toBeInTheDocument();
  });

  test("sends an unsaved address alone and shows the other steps as skipped", async () => {
    const fetch = await runCheck(
      json(200, {
        ok: false,
        steps: [
          {
            name: "dry_probe",
            ok: false,
            latency_ms: 2,
            via: "direct",
            message: "Wrong path prefix: the server answered 404.",
          },
          { name: "get_me", ok: false, skipped: true, via: "direct" },
          { name: "get_webhook_info", ok: false, skipped: true, via: "direct" },
        ],
        warnings: [],
      }),
      { unsavedBaseUrl: " http://127.0.0.1:18081/other/ " },
    );
    await expect
      .element(page.getByTestId("connection-check-unsaved"))
      .toHaveTextContent(
        "The address is not saved: the check makes only the dry probe against it, without the token.",
      );
    await expect
      .poll(stepTexts)
      .toEqual([
        "Dry probe without the token: failed 2 ms · directWrong path prefix: the server answered 404.",
        "getMe: Skipped: save the address first",
        "getWebhookInfo: Skipped: save the address first",
      ]);
    const init = fetch.mock.calls[0]?.[1];
    expect(init?.body).toBe(JSON.stringify({ base_url: "http://127.0.0.1:18081/other/" }));
    // A step that never ran does not look like one that passed.
    const icons = page
      .getByTestId("connection-check-step")
      .elements()
      .map((e) => e.querySelector("svg")?.getAttribute("data-icon") ?? "");
    expect(icons).toEqual(["", "skipped", "skipped"]);
  });

  test("says that a dry probe of an unsaved address passed", async () => {
    await runCheck(
      json(200, {
        ok: true,
        steps: [
          { name: "dry_probe", ok: true, latency_ms: 2, via: "proxy" },
          { name: "get_me", ok: false, skipped: true, via: "proxy" },
          { name: "get_webhook_info", ok: false, skipped: true, via: "proxy" },
        ],
        warnings: [],
      }),
      { unsavedBaseUrl: "https://bot-api.example.org" },
    );
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The address answers as a Bot API. Save it to check the bot token there.");
    await expect
      .poll(() => stepTexts()[0])
      .toBe("Dry probe without the token: ok · 2 ms · through the proxy");
  });

  test("skips the token's steps after a failed dry probe of the saved address", async () => {
    await runCheck(
      json(200, {
        ok: false,
        steps: [
          {
            name: "dry_probe",
            ok: false,
            latency_ms: 1,
            via: "direct",
            message: "dial tcp 127.0.0.1:1: connect: connection refused",
          },
          { name: "get_me", ok: false, skipped: true, via: "direct" },
          { name: "get_webhook_info", ok: false, skipped: true, via: "direct" },
        ],
        warnings: [],
      }),
    );
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The check failed.");
    await expect
      .poll(stepTexts)
      .toEqual([
        "Dry probe without the token: failed 1 ms · directdial tcp 127.0.0.1:1: connect: connection refused",
        "getMe: Skipped: an earlier step failed",
        "getWebhookInfo: Skipped: an earlier step failed",
      ]);
  });

  test("refuses an unsaved address that is not a URL before sending anything", async () => {
    const fetch = await runCheck(json(200, PASSED), { unsavedBaseUrl: "telegram" });
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent(BASE_URL_TEXT);
    expect(fetch).not.toHaveBeenCalled();
  });

  test("names the refusal of an unsaved address and the wait of a busy messenger", async () => {
    expect(
      checkErrorText(
        i18n.t,
        refusal(422, {
          code: "validation_failed",
          errors: [{ pointer: "/base_url", code: "invalid_format" }],
        }),
      ),
    ).toBe(BASE_URL_TEXT);
    await runCheck(
      json(
        503,
        { type: "about:blank", title: "Service Unavailable", status: 503 },
        { "Retry-After": "9" },
      ),
    );
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("The messenger is busy; try again in 9 s.");
  });

  test("shows the steps in Russian", async () => {
    await i18n.changeLanguage("ru");
    await runCheck(
      json(200, {
        ok: true,
        steps: [
          { name: "dry_probe", ok: true, latency_ms: 2, via: "direct" },
          { name: "get_me", ok: false, skipped: true, via: "direct" },
          { name: "get_webhook_info", ok: false, skipped: true, via: "direct" },
        ],
        warnings: [],
      }),
      { unsavedBaseUrl: "http://127.0.0.1:18081/other/" },
      "Проверить подключение",
    );
    await expect
      .poll(stepTexts)
      .toEqual([
        "Пробный запрос без токена: успешно · 2 мс · напрямую",
        "getMe: Пропущен: сначала сохраните адрес",
        "getWebhookInfo: Пропущен: сначала сохраните адрес",
      ]);
  });

  test("writes the pending updates with Russian plurals", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t;
    expect(t("connections.check.pendingUpdates", { count: 1 })).toBe("1 обновление в очереди");
    expect(t("connections.check.pendingUpdates", { count: 3 })).toBe("3 обновления в очереди");
    expect(t("connections.check.pendingUpdates", { count: 5 })).toBe("5 обновлений в очереди");
    expect(t("connections.check.pendingUpdates", { count: 21 })).toBe("21 обновление в очереди");
  });
});
