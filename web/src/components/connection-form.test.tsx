// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mattermost Connection form in a real browser: the request a save sends, with the bot token only when it was
// replaced and never shown; the checks it repeats, a new server URL that needs the token again included; the refusals
// it puts on its fields; the read-only form; the limiter; the connection check with its steps, path, latency, warning
// and busy messenger; the callback address with its hint; and the in_use refusal of "Delete" in its plural forms.

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
  ConnectionCheckResult,
  MattermostConnection,
  MattermostConnectionInput,
  Permission,
  Session,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { CallbackHint } from "./callback-hint";
import { ConnectionCheck, checkErrorText } from "./connection-check";
import {
  ConnectionDeleteDialog,
  ConnectionForm,
  TOKEN_FOR_NEW_SERVER,
  connectionErrors,
  connectionInput,
  connectionValues,
  inUseText,
  isServerUrl,
  serverErrors,
} from "./connection-form";
import { limiterErrors, limiterInput } from "./limiter-field";

const CALLBACK = "http://localhost:8081/api/v1/callbacks/mattermost/CN0000000000AA";

const CONNECTION: MattermostConnection = {
  id: "CN0000000000AA",
  type: "mattermost",
  name: "mm",
  server_url: "http://127.0.0.1:18065",
  limiter: { limit: 5, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-08T09:30:00Z" },
  bot_username: "muster-dev-bot",
  proxy: { enabled: false },
  destination_count: 1,
  callback_url: CALLBACK,
  created_at: "2026-10-08T09:30:00Z",
  etag: '"1"',
};

const WARNING =
  "The bot may not make ephemeral messages, so answers to button presses show in the Thread of the Root message. Give the bot the create_post_ephemeral permission, for example the system admin role, to show them in the channel.";

function refusal(status: number, problem: Record<string, unknown>, retryAfter?: number): ApiError {
  return new ApiError(
    status,
    { type: "https://muster-io.github.io/muster/problems/x", ...problem },
    retryAfter,
  );
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function providers(children: ReactNode, permissions: Permission[]) {
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

const WRITE: Permission[] = ["connections:read", "connections:write"];

async function renderForm(
  save: (input: MattermostConnectionInput) => Promise<MattermostConnection | undefined>,
  options: { connection?: MattermostConnection; readOnly?: boolean } = {},
) {
  return render(
    providers(
      <ConnectionForm
        connection={options.connection}
        readOnly={options.readOnly}
        submitLabel="Save"
        save={save}
        onReload={() => {}}
      />,
      options.readOnly === true ? ["connections:read"] : WRITE,
    ),
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the request of a save", () => {
  test("keeps the stored bot token unless it was replaced", () => {
    const values = connectionValues(CONNECTION);
    expect(connectionInput(values)).toEqual({
      type: "mattermost",
      name: "mm",
      server_url: "http://127.0.0.1:18065",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 5, per_seconds: 1 },
    });
    expect("bot_token" in connectionInput(values)).toBe(false);
    const replaced = connectionInput({
      ...values,
      bot_token: { mode: "replace", value: "mm-dev-token" },
    });
    expect(replaced.bot_token).toBe("mm-dev-token");
  });

  test("starts a new Connection with the limiter of connection.mattermost.limiter", () => {
    expect(connectionValues(undefined).limiter).toEqual({ limit: "5", per_seconds: "1" });
  });
});

describe("the checks before a save", () => {
  test("accepts only absolute http and https URLs without user information, query or fragment", () => {
    expect(isServerUrl("http://127.0.0.1:18065")).toBe(true);
    expect(isServerUrl(" https://mattermost.example.org/sub ")).toBe(true);
    for (const bad of [
      "mattermost.example.org",
      "ftp://mattermost.example.org",
      "https://user:pw@mattermost.example.org",
      "https://mattermost.example.org/?x=1",
      "https://mattermost.example.org/#top",
      "",
    ]) {
      expect(isServerUrl(bad), bad).toBe(false);
    }
  });

  test("needs a bot token for a new Connection and again for a new server URL", () => {
    const fresh = connectionValues(undefined);
    expect(connectionErrors({ ...fresh, name: " ", server_url: "x" }, undefined)).toEqual({
      "/name": "required",
      "/server_url": "invalid_format",
      "/bot_token": "required",
    });
    const stored = connectionValues(CONNECTION);
    expect(connectionErrors(stored, CONNECTION)).toEqual({});
    expect(
      connectionErrors({ ...stored, server_url: "https://other.example.org" }, CONNECTION),
    ).toEqual({ "/bot_token": TOKEN_FOR_NEW_SERVER });
    expect(
      connectionErrors(
        {
          ...stored,
          server_url: "https://other.example.org",
          bot_token: { mode: "replace", value: "new" },
        },
        CONNECTION,
      ),
    ).toEqual({});
  });

  test("needs whole limiter numbers of at least 1", () => {
    expect(limiterErrors({ limit: "", per_seconds: "0" })).toEqual({
      limit: "required",
      per_seconds: "invalid_format",
    });
    expect(limiterErrors({ limit: "2.5", per_seconds: "-1" })).toEqual({
      limit: "invalid_format",
      per_seconds: "invalid_format",
    });
    expect(limiterErrors({ limit: " 10 ", per_seconds: "60" })).toEqual({});
    expect(limiterInput({ limit: " 10 ", per_seconds: "60" })).toEqual({
      limit: 10,
      per_seconds: 60,
    });
  });

  test("puts the refusals of the server on their fields", () => {
    expect(serverErrors(refusal(409, { code: "name_taken" }), false)).toEqual({
      "/name": "name_taken",
    });
    expect(
      serverErrors(
        refusal(422, {
          errors: [
            { pointer: "/limiter", code: "invalid_format" },
            { pointer: "/proxy/address", code: "required" },
          ],
        }),
        false,
      ),
    ).toEqual({
      "/limiter/limit": "invalid_format",
      "/limiter/per_seconds": "invalid_format",
      "/proxy/address": "required",
    });
    const tokenAgain = refusal(422, { errors: [{ pointer: "/bot_token", code: "required" }] });
    expect(serverErrors(tokenAgain, true)).toEqual({ "/bot_token": TOKEN_FOR_NEW_SERVER });
    expect(serverErrors(tokenAgain, false)).toEqual({ "/bot_token": "required" });
    expect(serverErrors(new Error("network"), false)).toEqual({});
  });
});

describe("ConnectionForm", () => {
  test("shows the bot token only as set, and sends it only after Replace", async () => {
    const save = vi.fn((_input: MattermostConnectionInput) => Promise.resolve(undefined));
    await renderForm(save, { connection: CONNECTION });
    const status = page.getByTestId("secret-connection-bot-token-status");
    await expect.element(status).toBeVisible();
    expect(status.element().textContent).toMatch(/^Set, changed /);
    expect(document.querySelectorAll('input[type="password"]')).toHaveLength(0);
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ bot_token: expect.anything() }),
    );

    await page.getByRole("button", { name: "Replace" }).click();
    await userEvent.fill(page.getByLabelText("Bot token"), "mm-new-token");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ bot_token: "mm-new-token" }));
    expect(document.body.textContent).not.toContain("mm-new-token");
  });

  test("creates a Connection with the name, the server URL, the bot token and the limiter", async () => {
    const save = vi.fn((_input: MattermostConnectionInput) => Promise.resolve(undefined));
    await renderForm(save);
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByLabelText("Name")).toHaveFocus();
    expect(save).not.toHaveBeenCalled();

    await userEvent.fill(page.getByLabelText("Name"), "mm");
    await userEvent.fill(page.getByLabelText("Server URL"), "http://127.0.0.1:18065");
    await page.getByRole("button", { name: "Set a value" }).click();
    await userEvent.fill(page.getByLabelText("Bot token"), "mm-dev-token");
    await userEvent.fill(page.getByLabelText("Requests"), "3");
    await userEvent.fill(page.getByLabelText("Period in seconds"), "2");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "mattermost",
      name: "mm",
      server_url: "http://127.0.0.1:18065",
      bot_token: "mm-dev-token",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 3, per_seconds: 2 },
    });
  });

  test("asks for the bot token again when the server URL changes", async () => {
    const save = vi.fn(() => Promise.resolve(undefined));
    await renderForm(save, { connection: CONNECTION });
    await userEvent.fill(page.getByLabelText("Server URL"), "https://mattermost.example.org");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page.getByText(
          "Enter the bot token again: the stored token is sent only to the server it was entered for.",
        ),
      )
      .toBeVisible();
    // The token's input opens and takes the focus, so that the refusal is found by keyboard.
    await expect.element(page.getByLabelText("Bot token")).toHaveFocus();
    await expect.element(page.getByLabelText("Bot token")).toHaveAttribute("aria-invalid", "true");
    expect(save).not.toHaveBeenCalled();
  });

  test("keeps the focus on Save and shows the saved Connection after a save", async () => {
    const saved: MattermostConnection = {
      ...CONNECTION,
      name: "mm2",
      bot_token_status: { set: true, updated_at: "2026-10-09T08:00:00Z" },
      etag: '"2"',
    };
    await renderForm(() => Promise.resolve(saved), { connection: CONNECTION });
    await page.getByRole("button", { name: "Replace" }).click();
    await userEvent.fill(page.getByLabelText("Bot token"), "mm-new-token");
    await userEvent.fill(page.getByLabelText("Name"), "mm2");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByRole("button", { name: "Replace" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Save" })).toHaveFocus();
    await expect.element(page.getByLabelText("Name")).toHaveValue("mm2");
    expect(document.querySelectorAll('input[type="password"]')).toHaveLength(0);
  });

  test("marks the field of a refusal and offers a reload for a newer version", async () => {
    let answer: unknown = refusal(409, { code: "name_taken" });
    await renderForm(() => Promise.reject(answer), { connection: CONNECTION });
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Another Connection has this name.")).toBeVisible();
    await expect.element(page.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");

    // Once the name is edited, the refusal is not shown again as a whole.
    await userEvent.fill(page.getByLabelText("Name"), "mm2");
    await expect
      .element(page.getByText("Another Connection has this name."))
      .not.toBeInTheDocument();
    await expect.element(page.getByText("Another user has this login.")).not.toBeInTheDocument();
    await expect
      .element(page.getByText("Something went wrong. Try again."))
      .not.toBeInTheDocument();

    answer = refusal(422, { errors: [{ pointer: "/limiter", code: "invalid_format" }] });
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByLabelText("Requests")).toHaveAttribute("aria-invalid", "true");

    for (const status of [412, 428]) {
      answer = refusal(status, {});
      await page.getByRole("button", { name: "Save" }).click();
      await expect
        .element(page.getByTestId("connection-stale"))
        .toHaveTextContent(
          "Someone else changed this Connection. Reload to see the changes.Reload",
        );
    }
  });

  test("is read-only without connections:write", async () => {
    await renderForm(() => Promise.resolve(undefined), { connection: CONNECTION, readOnly: true });
    await expect.element(page.getByTestId("connection-read-only")).toBeVisible();
    await expect.element(page.getByLabelText("Name")).toBeDisabled();
    for (const name of ["Save", "Replace"]) {
      await expect.element(page.getByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  test("shows the field texts in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderForm(() => Promise.resolve(undefined), { connection: CONNECTION });
    await expect.element(page.getByLabelText("URL сервера")).toHaveValue("http://127.0.0.1:18065");
    await expect.element(page.getByText("Ограничение частоты: запросов за период")).toBeVisible();
  });
});

describe("the callback address", () => {
  test("shows the address with a copy button and the hint", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await render(providers(<CallbackHint callbackUrl={CALLBACK} />, WRITE));
    await expect.element(page.getByTestId("connection-callback-url")).toHaveTextContent(CALLBACK);
    await expect
      .element(page.getByTestId("connection-callback-hint"))
      .toHaveTextContent(
        `Mattermost calls ${CALLBACK} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it.`,
      );
    await page.getByRole("button", { name: "Copy" }).click();
    expect(writeText).toHaveBeenCalledWith(CALLBACK);
  });

  test("shows the hint in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(providers(<CallbackHint callbackUrl={CALLBACK} />, WRITE));
    await expect
      .element(page.getByTestId("connection-callback-hint"))
      .toHaveTextContent(
        `Mattermost обращается к ${CALLBACK}, когда кто-то нажимает кнопку. Если этот адрес внутренний, добавьте его хост в AllowedUntrustedInternalConnections в настройках сервера Mattermost, иначе нажатия завершатся ошибкой «Action integration error». Проверить это можно тестовым сообщением в место доставки.`,
      );
  });
});

const PASSED: ConnectionCheckResult = {
  ok: true,
  steps: [{ name: "token", ok: true, latency_ms: 12, via: "direct", message: null }],
  warnings: ["press_answers_in_thread"],
  bot_name: "muster-dev-bot",
};

async function runCheck(response: Response, button = "Check connection") {
  const fetch = vi.spyOn(window, "fetch").mockResolvedValue(response);
  await render(providers(<ConnectionCheck connectionId="CN0000000000AA" dirty={false} />, WRITE));
  await page.getByRole("button", { name: button }).click();
  return fetch;
}

describe("ConnectionCheck", () => {
  test("shows the bot, each step with its latency and path, and the warning with its hint", async () => {
    const fetch = await runCheck(json(200, PASSED));
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Connected as muster-dev-bot");
    await expect
      .element(page.getByTestId("connection-check-step"))
      .toHaveTextContent("Token: ok · 12 ms · direct");
    await expect.element(page.getByTestId("connection-check-warning")).toHaveTextContent(WARNING);
    // The permission is Mattermost's identifier and shows as code.
    expect(
      page.getByTestId("connection-check-warning").element().querySelector("code")?.textContent,
    ).toBe("create_post_ephemeral");
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/v1/connections/CN0000000000AA/checks");
  });

  test("shows no hint without the warning, and the path through the proxy", async () => {
    await runCheck(
      json(200, {
        ...PASSED,
        warnings: [],
        steps: [{ name: "token", ok: true, latency_ms: 40, via: "proxy" }],
      }),
    );
    await expect
      .element(page.getByTestId("connection-check-step"))
      .toHaveTextContent("Token: ok · 40 ms · through the proxy");
    await expect.element(page.getByTestId("connection-check-warning")).not.toBeInTheDocument();
  });

  test("shows the message of the failing step", async () => {
    await runCheck(
      json(200, {
        ok: false,
        steps: [
          {
            name: "token",
            ok: false,
            latency_ms: 8,
            via: "direct",
            message: "The bot token is not valid.",
          },
        ],
        warnings: [],
        bot_name: null,
      }),
    );
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The check failed.");
    await expect
      .element(page.getByTestId("connection-check-step"))
      .toHaveTextContent("Token: failed 8 ms · directThe bot token is not valid.");
  });

  test("names the wait of a busy messenger", async () => {
    await runCheck(
      json(
        503,
        { type: "about:blank", title: "Service Unavailable", status: 503 },
        { "Retry-After": "7" },
      ),
    );
    await expect
      .element(page.getByTestId("connection-check-error"))
      .toHaveTextContent("The messenger is busy; try again in 7 s.");
  });

  test("shows the result and the warning in Russian", async () => {
    await i18n.changeLanguage("ru");
    await runCheck(json(200, PASSED), "Проверить подключение");
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("Подключено как muster-dev-bot");
    await expect
      .element(page.getByTestId("connection-check-step"))
      .toHaveTextContent("Токен: успешно · 12 мс · напрямую");
    await expect
      .element(page.getByTestId("connection-check-warning"))
      .toHaveTextContent(
        "Боту нельзя создавать эфемерные сообщения, поэтому ответы на нажатия кнопок появляются в треде корневого сообщения. Чтобы они показывались в канале, дайте боту право create_post_ephemeral, например роль системного администратора.",
      );
    expect(checkErrorText(i18n.t, refusal(503, {}, 3))).toBe(
      "Мессенджер занят, повторите через 3 с.",
    );
  });
});

describe("deleting a Connection", () => {
  test("names the Destinations that use it, in their plural forms", async () => {
    expect(inUseText(i18n.t, 1)).toBe("This Connection is used by 1 Destination. Delete it first.");
    expect(inUseText(i18n.t, 3)).toBe(
      "This Connection is used by 3 Destinations. Delete them first.",
    );
    await i18n.changeLanguage("ru");
    expect(inUseText(i18n.t, 1)).toBe(
      "Это подключение используется в 1 месте доставки. Сначала удалите его.",
    );
    expect(inUseText(i18n.t, 2)).toBe(
      "Это подключение используется в 2 местах доставки. Сначала удалите их.",
    );
    expect(inUseText(i18n.t, 5)).toBe(
      "Это подключение используется в 5 местах доставки. Сначала удалите их.",
    );
    expect(inUseText(i18n.t, 21)).toBe(
      "Это подключение используется в 21 месте доставки. Сначала удалите их.",
    );
  });

  test("refuses a Connection that a Destination uses (in_use)", async () => {
    // The first request is the deletion, refused; the page then reads the Connection again.
    const fetch = vi
      .spyOn(window, "fetch")
      .mockResolvedValueOnce(
        json(409, {
          type: "https://muster-io.github.io/muster/problems/conflict",
          title: "Conflict",
          status: 409,
          code: "in_use",
        }),
      )
      .mockResolvedValue(json(200, CONNECTION));
    const rootRoute = createRootRoute({
      component: () => <ConnectionDeleteDialog connection={CONNECTION} />,
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    await render(providers(<RouterProvider router={router} />, WRITE));
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = page.getByRole("dialog", { name: "Delete Connection mm?" });
    await expect.element(dialog).toBeVisible();
    // The pointer that opened the dialog is still over the inert layer of the modal; the keyboard confirms.
    const confirm = dialog.getByRole("button", { name: "Delete" });
    const button = confirm.element();
    if (button instanceof HTMLElement) {
      button.focus();
    }
    await userEvent.keyboard("{Enter}");
    await expect
      .element(page.getByTestId("connection-delete-error"))
      .toHaveTextContent("This Connection is used by 1 Destination. Delete it first.");
    const [url, init] = fetch.mock.calls[0] ?? [];
    expect(url).toBe("/api/v1/connections/CN0000000000AA");
    expect(init?.method).toBe("DELETE");
    expect(new Headers(init?.headers).get("If-Match")).toBe('"1"');
    await expect.element(dialog.getByRole("button", { name: "Delete" })).toBeDisabled();
  });
});
