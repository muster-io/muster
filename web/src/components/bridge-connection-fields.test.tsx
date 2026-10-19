// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Bridge Connection in a real browser: the request a save sends — the base URL, the token, the event delivery mode,
// the proxy and the limiter — with the push secret only in push mode; the push secret and the callback address with its
// hint only in push mode, the note that a new Connection gets its address once saved and the note that polling removes
// a stored push secret; the checks a save repeats, the push secret's form and a new base URL that needs the token again
// included; the limiter proposal, half of the bridge's send_per_second; the steps "Bridge info" and "Event delivery" of
// the check, each failing one with the text of its code and the bridge's English message as the detail; and the panel
// of what the bridge offers. English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  BridgeConnection,
  BridgeInfo,
  ConnectionCheckResult,
  ConnectionCheckStep,
  ConnectionInput,
  Permission,
  Session,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import {
  BridgeInfoPanel,
  connectionLimiterProposal,
  halfRate,
  isPushSecret,
} from "./bridge-connection-fields";
import { ConnectionCheck } from "./connection-check";
import { ConnectionForm } from "./connection-form";

const CALLBACK = "http://localhost:8081/api/v1/callbacks/bridge/CN0000000000BR";

/** The info of the fake bridge with its default configuration. */
const INFO: BridgeInfo = {
  protocol_version: "1",
  bridge: { name: "Muster fake bridge", version: "1.0.0" },
  messenger: { name: "Fake chat" },
  capabilities: {
    buttons: {
      max_per_message: 10,
      max_per_row: 5,
      data_max_bytes: 64,
      styles: ["primary", "danger", "neutral", "success"],
    },
    thread_messages: "all",
    direct_messages: { bot_can_start: true },
    private_answers: ["ephemeral", "popup"],
    native_mentions: true,
    quiet_messages: true,
    status_color: true,
    delete: { max_age_seconds: null },
    deletion_events: true,
  },
  limits: { max_body_length: 4000, edit_max_age_seconds: null },
  user_lookup: "login",
  event_delivery: ["poll", "push"],
  link_url_template: null,
};

const BRIDGE: BridgeConnection = {
  id: "CN0000000000BR",
  type: "bridge",
  name: "br",
  base_url: "http://127.0.0.1:18094",
  event_delivery: "poll",
  limiter: { limit: 10, per_seconds: 1 },
  bot_token_status: { set: true, updated_at: "2026-10-17T09:30:00Z" },
  push_secret_status: { set: false },
  callback_url: CALLBACK,
  info: INFO,
  info_read_at: "2026-10-17T09:31:00Z",
  proxy: { enabled: false },
  destination_count: 0,
  created_at: "2026-10-17T09:30:00Z",
  etag: '"1"',
};

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

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the push secret and the limiter proposal", () => {
  test("accepts a push secret as the server does: whsec_ followed by base64 of at least one byte", () => {
    expect(isPushSecret("whsec_c2VjcmV0")).toBe(true);
    expect(isPushSecret("whsec_AA==")).toBe(true);
    for (const bad of [
      "",
      "whsec_",
      "c2VjcmV0",
      "whsec_c2VjcmV",
      "whsec_!!!!",
      "whsec_====",
      "WHSEC_AA==",
    ]) {
      expect(isPushSecret(bad), bad).toBe(false);
    }
  });

  test("proposes half of send_per_second, in whole numbers, never above half", () => {
    expect(connectionLimiterProposal(undefined)).toEqual({ limit: 10, per_seconds: 1 });
    expect(connectionLimiterProposal(null)).toEqual({ limit: 10, per_seconds: 1 });
    expect(connectionLimiterProposal(INFO)).toEqual({ limit: 10, per_seconds: 1 });
    const withRate = (rate: number) =>
      connectionLimiterProposal({ ...INFO, limits: { ...INFO.limits, send_per_second: rate } });
    expect(withRate(30)).toEqual({ limit: 15, per_seconds: 1 });
    expect(withRate(3)).toEqual({ limit: 1, per_seconds: 1 });
    expect(withRate(1)).toEqual({ limit: 1, per_seconds: 2 });
    expect(withRate(0.1)).toEqual({ limit: 1, per_seconds: 20 });
    expect(halfRate(3e9, 1)).toEqual({ limit: 999_999_999, per_seconds: 1 });
  });
});

describe("ConnectionForm of a Bridge Connection", () => {
  test("creates a Bridge Connection with the base URL, the token, polling and the limiter", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(providers(<ConnectionForm type="bridge" submitLabel="Save" save={save} />));
    await expect.element(page.getByRole("heading", { name: "Bridge" })).toBeVisible();
    await expect.element(page.getByLabelText("Server URL")).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Bot API base URL")).not.toBeInTheDocument();
    await expect.element(page.getByLabelText("Requests")).toHaveValue("10");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("1");
    await expect
      .element(
        page.getByText(
          "At most this many requests go to the bridge through this Connection. When the bridge asks Muster to wait, Muster waits as long as it asks.",
        ),
      )
      .toBeVisible();
    // Polling by default, each mode with its line.
    await expect.element(page.getByRole("radio", { name: "Polling" })).toBeChecked();
    await expect
      .element(page.getByRole("radio", { name: "Polling" }))
      .toHaveAccessibleDescription(
        "Muster polls the bridge; the bridge needs no address of Muster",
      );
    await expect
      .element(page.getByRole("radio", { name: "Push" }))
      .toHaveAccessibleDescription("The bridge pushes events to Muster's ingest address");
    await expect.element(page.getByText("Push secret")).not.toBeInTheDocument();
    await expect.element(page.getByTestId("limiter-proposal")).not.toBeInTheDocument();

    await userEvent.fill(page.getByLabelText("Name"), "br");
    await userEvent.fill(page.getByLabelText("Base URL"), "http://127.0.0.1:18094/");
    await page.getByRole("button", { name: "Set a value" }).click();
    await userEvent.fill(
      page.getByLabelText("Token", { exact: true }),
      "muster-dev-bridge-token-b",
    );
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "bridge",
      name: "br",
      base_url: "http://127.0.0.1:18094/",
      event_delivery: "poll",
      bot_token: "muster-dev-bridge-token-b",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 10, per_seconds: 1 },
    });
    expect(document.body.textContent).not.toContain("muster-dev-bridge-token-b");
  });

  test("refuses a base URL the server would refuse, at the field", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(providers(<ConnectionForm type="bridge" submitLabel="Save" save={save} />));
    await userEvent.fill(page.getByLabelText("Name"), "br");
    await userEvent.fill(page.getByLabelText("Base URL"), "https://user:pw@bridge.example.org");
    await page.getByRole("button", { name: "Set a value" }).click();
    await userEvent.fill(page.getByLabelText("Token", { exact: true }), "t");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page.getByText(
          "Enter an absolute http or https URL without user information, query or fragment, such as https://bridge.example.org. A path prefix is allowed.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByLabelText("Base URL")).toHaveFocus();
    expect(save).not.toHaveBeenCalled();
  });

  test("shows the push secret and the callback address only in push mode", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const screen = await render(
      providers(<ConnectionForm type="bridge" submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByTestId("bridge-push")).not.toBeInTheDocument();
    await page.getByRole("radio", { name: "Push" }).click();
    await expect.element(page.getByText("Push secret")).toBeVisible();
    await expect
      .element(page.getByTestId("bridge-callback-after-save"))
      .toHaveTextContent("The callback address appears once the Connection is saved.");
    await expect.element(page.getByTestId("bridge-callback-hint")).not.toBeInTheDocument();

    await userEvent.fill(page.getByLabelText("Name"), "br");
    await userEvent.fill(page.getByLabelText("Base URL"), "http://127.0.0.1:18094");
    await page
      .getByTestId("secret-connection-bot-token")
      .getByRole("button", { name: "Set a value" })
      .click();
    await userEvent.fill(
      page.getByLabelText("Token", { exact: true }),
      "muster-dev-bridge-token-b",
    );
    // Push needs a push secret.
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("secret-connection-push-secret").getByText("Fill in this field."))
      .toBeVisible();
    expect(save).not.toHaveBeenCalled();
    await page
      .getByTestId("secret-connection-push-secret")
      .getByRole("button", { name: "Set a value" })
      .click();
    await userEvent.fill(page.getByLabelText("Push secret"), "whsec_!");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page
          .getByTestId("secret-connection-push-secret")
          .getByText("Enter the push secret as whsec_ followed by base64."),
      )
      .toBeVisible();
    await expect.element(page.getByLabelText("Push secret")).toHaveFocus();
    expect(save).not.toHaveBeenCalled();
    await userEvent.fill(page.getByLabelText("Push secret"), "whsec_c2VjcmV0");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(
      expect.objectContaining({ event_delivery: "push", push_secret: "whsec_c2VjcmV0" }),
    );
    // Back to polling: the push secret is not sent, so the save clears a stored one.
    await page.getByRole("radio", { name: "Polling" }).click();
    await expect.element(page.getByTestId("bridge-push")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ push_secret: expect.anything() }),
    );
    expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ event_delivery: "poll" }));
    await screen.unmount();

    // A saved Connection shows its callback address with a copy button and the hint.
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await render(providers(<ConnectionForm connection={BRIDGE} submitLabel="Save" save={save} />));
    await expect.element(page.getByTestId("bridge-callback-hint")).not.toBeInTheDocument();
    await page.getByRole("radio", { name: "Push" }).click();
    await expect.element(page.getByTestId("bridge-callback-url")).toHaveTextContent(CALLBACK);
    await expect
      .element(page.getByTestId("bridge-callback-hint"))
      .toHaveTextContent(
        `The bridge posts events to ${CALLBACK}, signed with the push secret. Configure the same address and secret in the bridge.`,
      );
    await page.getByRole("button", { name: "Copy" }).click();
    expect(writeText).toHaveBeenCalledWith(CALLBACK);
    await expect
      .element(page.getByTestId("secret-connection-push-secret-status"))
      .toHaveTextContent("Not set");
  });

  test("keeps a stored push secret and the callback address of a Connection in push mode", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    await render(
      providers(
        <ConnectionForm
          connection={{
            ...BRIDGE,
            event_delivery: "push",
            push_secret_status: { set: true, updated_at: "2026-10-17T09:30:00Z" },
          }}
          submitLabel="Save"
          save={save}
        />,
      ),
    );
    await expect.element(page.getByRole("radio", { name: "Push" })).toBeChecked();
    await expect.element(page.getByTestId("bridge-callback-url")).toHaveTextContent(CALLBACK);
    const status = page.getByTestId("secret-connection-push-secret-status");
    await expect.element(status).toBeVisible();
    expect(status.element().textContent).toMatch(/^Set, changed /);
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "bridge",
      name: "br",
      base_url: "http://127.0.0.1:18094",
      event_delivery: "push",
      proxy: { enabled: false, type: "http", username: null },
      limiter: { limit: 10, per_seconds: 1 },
    });
  });

  test("says under the modes that saving in polling removes a stored push secret", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const pushed: BridgeConnection = {
      ...BRIDGE,
      event_delivery: "push",
      push_secret_status: { set: true, updated_at: "2026-10-17T09:30:00Z" },
    };
    const screen = await render(
      providers(<ConnectionForm connection={pushed} submitLabel="Save" save={save} />),
    );
    const dropped = page.getByTestId("bridge-push-secret-dropped");
    await expect.element(page.getByRole("radio", { name: "Push" })).toBeChecked();
    await expect.element(dropped).not.toBeInTheDocument();
    await page.getByRole("radio", { name: "Polling" }).click();
    await expect
      .element(dropped)
      .toHaveTextContent("Saving in polling mode removes the stored push secret.");
    await page.getByRole("radio", { name: "Push" }).click();
    await expect.element(dropped).not.toBeInTheDocument();
    await screen.unmount();

    // Without a stored push secret there is nothing to remove.
    const screen2 = await render(
      providers(<ConnectionForm connection={BRIDGE} submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByRole("radio", { name: "Polling" })).toBeChecked();
    await expect.element(dropped).not.toBeInTheDocument();
    await screen2.unmount();

    await i18n.changeLanguage("ru");
    await render(providers(<ConnectionForm connection={pushed} submitLabel="Save" save={save} />));
    await page.getByRole("radio", { name: "Опрос" }).click();
    await expect
      .element(dropped)
      .toHaveTextContent("Сохранение в режиме опроса удалит сохранённый секрет push-отправки.");
  });

  test("asks for the token again for a new base URL", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const screen = await render(
      providers(<ConnectionForm connection={BRIDGE} submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByLabelText("Base URL")).toHaveValue("http://127.0.0.1:18094");
    await userEvent.fill(page.getByLabelText("Base URL"), "http://127.0.0.1:18095");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page.getByText(
          "Enter the token again: the stored token is sent only to the bridge it was entered for.",
        ),
      )
      .toBeVisible();
    await expect.element(page.getByLabelText("Token", { exact: true })).toHaveFocus();
    expect(save).not.toHaveBeenCalled();
    await screen.unmount();

    // The same address with a trailing slash is the stored one.
    await render(providers(<ConnectionForm connection={BRIDGE} submitLabel="Save" save={save} />));
    await userEvent.fill(page.getByLabelText("Base URL"), "http://127.0.0.1:18094/");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({ base_url: "http://127.0.0.1:18094/" }),
    );
  });

  test("proposes half of send_per_second on the Connection form", async () => {
    const save = vi.fn((_input: ConnectionInput) => Promise.resolve(undefined));
    const info: BridgeInfo = { ...INFO, limits: { ...INFO.limits, send_per_second: 30 } };
    const screen = await render(
      providers(<ConnectionForm connection={BRIDGE} info={info} submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByLabelText("Requests")).toHaveValue("10");
    await expect
      .element(page.getByTestId("limiter-proposal"))
      .toHaveTextContent(
        "The bridge declares a rate of 30 a second; Muster proposes half: 15 per 1 s.",
      );
    // The proposal never changes the limiter by itself.
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(
      expect.objectContaining({ limiter: { limit: 10, per_seconds: 1 } }),
    );
    await page.getByRole("button", { name: "Use the proposal" }).click();
    await expect.element(page.getByLabelText("Requests")).toHaveValue("15");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("1");
    await expect.element(page.getByTestId("limiter-proposal")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenLastCalledWith(
      expect.objectContaining({ limiter: { limit: 15, per_seconds: 1 } }),
    );
    await screen.unmount();

    // Without the rate there is nothing to propose: the default stays.
    await render(
      providers(<ConnectionForm connection={BRIDGE} info={INFO} submitLabel="Save" save={save} />),
    );
    await expect.element(page.getByLabelText("Requests")).toHaveValue("10");
    await expect.element(page.getByTestId("limiter-proposal")).not.toBeInTheDocument();
  });

  test("reads the fields and the proposal in Russian", async () => {
    await i18n.changeLanguage("ru");
    const info: BridgeInfo = { ...INFO, limits: { ...INFO.limits, send_per_second: 0.5 } };
    await render(
      providers(
        <ConnectionForm
          connection={BRIDGE}
          info={info}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Мост" })).toBeVisible();
    await expect.element(page.getByLabelText("Базовый URL")).toHaveValue("http://127.0.0.1:18094");
    await expect.element(page.getByRole("radio", { name: "Опрос" })).toBeChecked();
    await expect
      .element(page.getByRole("radio", { name: "Опрос" }))
      .toHaveAccessibleDescription("Muster сам опрашивает мост; мосту не нужен адрес Muster");
    await expect
      .element(page.getByTestId("limiter-proposal"))
      .toHaveTextContent(
        "Мост заявляет предел 0,5 в секунду; Muster предлагает половину: 1 за 4 с.",
      );
    await page.getByRole("radio", { name: "Push-отправка" }).click();
    await expect
      .element(page.getByTestId("bridge-callback-hint"))
      .toHaveTextContent(
        `Мост отправляет события на ${CALLBACK}, подписывая их секретом push-отправки. Укажите в мосте тот же адрес и секрет.`,
      );
  });
});

function step(
  name: ConnectionCheckStep["name"],
  extra: Partial<ConnectionCheckStep> = {},
): ConnectionCheckStep {
  return { name, ok: true, via: "direct", ...extra };
}

/** Answers the check with the result, which names its run as every result does. */
async function runCheck(
  result: Omit<ConnectionCheckResult, "request_log_run">,
  button = "Check connection",
) {
  const fetch = vi
    .spyOn(window, "fetch")
    .mockResolvedValue(json(200, { ...result, request_log_run: "CR0000000000R1" }));
  await render(providers(<ConnectionCheck connectionId="CN0000000000BR" type="bridge" />));
  await page.getByRole("button", { name: button }).click();
  return fetch;
}

function stepTexts(): string[] {
  return page
    .getByTestId("connection-check-step")
    .elements()
    .map((e) => e.textContent ?? "");
}

/** The failures of the check of a Bridge, with the bridge's messages as reference.md writes them. */
const FAILURES: {
  code: NonNullable<ConnectionCheckStep["code"]>;
  message: string;
  reason: string;
  reasonRu: string;
}[] = [
  {
    code: "token_invalid",
    message: "The bridge refused the token.",
    reason: "The bot token is not valid.",
    reasonRu: "Токен бота недействителен.",
  },
  {
    code: "protocol_version_unknown",
    message: "The bridge speaks protocol version 2; this Muster speaks 1.",
    reason: "The bridge speaks a protocol version this Muster does not.",
    reasonRu: "Мост говорит на версии протокола, которую этот Muster не знает.",
  },
  {
    code: "protocol_mismatch",
    message: "The bridge's answer does not match the protocol: protocol_version is missing",
    reason: "The bridge's answer does not match the protocol.",
    reasonRu: "Ответ моста не соответствует протоколу.",
  },
];

describe("ConnectionCheck of a Bridge Connection", () => {
  test("names the Bridge steps, with their latency and path, and the hint of the check", async () => {
    const fetch = await runCheck({
      ok: true,
      // Event delivery makes no call of its own, so it has no latency, and none shows.
      steps: [step("info", { latency_ms: 3 }), step("event_delivery", { latency_ms: null })],
      warnings: [],
      info: INFO,
    });
    await expect
      .element(page.getByTestId("connection-check-summary"))
      .toHaveTextContent("The check passed.");
    await expect
      .poll(stepTexts)
      .toEqual(["Bridge info: ok · 3 ms · direct", "Event delivery: ok · direct"]);
    await expect
      .element(
        page.getByText(
          "Reads the bridge's info with the token, then checks that the bridge offers the event delivery mode. Before saving, the check uses the values in the form; a saved token is sent only to the saved address through the saved proxy.",
        ),
      )
      .toBeVisible();
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/v1/connections/CN0000000000BR/checks");
  });

  test.each(FAILURES)(
    "names the Bridge steps and translates their failures, the bridge's message as the detail: $code",
    async ({ code, message, reason }) => {
      await runCheck({
        ok: false,
        steps: [
          step("info", { ok: false, latency_ms: 3, code, message }),
          step("event_delivery", { ok: false, skipped: true }),
        ],
        warnings: [],
        info: null,
      });
      await expect
        .element(page.getByTestId("connection-check-summary"))
        .toHaveTextContent("The check failed.");
      await expect
        .poll(stepTexts)
        .toEqual([
          `Bridge info: failed 3 ms · direct${reason}Details: ${message}`,
          "Event delivery: Skipped: an earlier step failed",
        ]);
      await expect
        .element(page.getByTestId("connection-check-step-reason"))
        .toHaveTextContent(reason);
      await expect
        .element(page.getByTestId("connection-check-step-detail"))
        .toHaveTextContent(`Details: ${message}`);
    },
  );

  test("names the event delivery mode the bridge does not offer", async () => {
    await runCheck({
      ok: false,
      steps: [
        step("info", { latency_ms: 4 }),
        step("event_delivery", {
          ok: false,
          code: "event_delivery_not_offered",
          message: "The bridge does not offer push event delivery.",
        }),
      ],
      warnings: [],
      info: { ...INFO, event_delivery: ["poll"] },
    });
    await expect
      .poll(stepTexts)
      .toEqual([
        "Bridge info: ok · 4 ms · direct",
        "Event delivery: failed directThe bridge does not offer this event delivery mode.Details: The bridge does not offer push event delivery.",
      ]);
  });

  test.each(FAILURES)(
    "names the Bridge steps and translates their failures into Russian, the English message as the detail: $code",
    async ({ code, message, reasonRu }) => {
      await i18n.changeLanguage("ru");
      await runCheck(
        {
          ok: false,
          steps: [
            step("info", { ok: false, latency_ms: 3, code, message }),
            step("event_delivery", { ok: false, skipped: true }),
          ],
          warnings: [],
          info: null,
        },
        "Проверить подключение",
      );
      await expect
        .poll(stepTexts)
        .toEqual([
          `Сведения о мосте: ошибка 3 мс · напрямую${reasonRu}Подробности: ${message}`,
          "Доставка событий: Пропущено: предыдущий шаг не прошёл",
        ]);
    },
  );

  test("translates the event delivery mode the bridge does not offer into Russian", async () => {
    await i18n.changeLanguage("ru");
    await runCheck(
      {
        ok: false,
        steps: [
          step("info", { latency_ms: 4 }),
          step("event_delivery", {
            ok: false,
            code: "event_delivery_not_offered",
            message: "The bridge does not offer push event delivery.",
          }),
        ],
        warnings: [],
      },
      "Проверить подключение",
    );
    await expect
      .poll(stepTexts)
      .toEqual([
        "Сведения о мосте: успешно · 4 мс · напрямую",
        "Доставка событий: ошибка напрямуюМост не поддерживает этот способ доставки событий.Подробности: The bridge does not offer push event delivery.",
      ]);
  });
});

function infoLines(): string[] {
  return page
    .getByTestId("bridge-info-lines")
    .getByRole("listitem")
    .elements()
    .map((e) => e.textContent ?? "");
}

function features(): string[] {
  return page
    .getByTestId("bridge-info-features")
    .getByRole("listitem")
    .elements()
    .map((e) => e.textContent ?? "");
}

describe("BridgeInfoPanel", () => {
  test("shows what the bridge offers", async () => {
    const screen = await render(
      providers(<BridgeInfoPanel info={INFO} readAt="2026-10-17T09:31:00Z" />),
    );
    await expect
      .element(page.getByRole("heading", { name: "What the bridge offers" }))
      .toBeVisible();
    await expect
      .poll(infoLines)
      .toEqual([
        "Bridge: Muster fake bridge 1.0.0",
        "Messenger: Fake chat",
        "Protocol version 1",
        "Longest message text: 4000 characters",
        "Edits without a time limit",
        "Finds people by login",
        "Event delivery: Polling, Push",
      ]);
    expect(features()).toEqual([
      "Buttons",
      "Thread messages",
      "Direct messages",
      "Answers only one person sees",
      "Mentions that notify",
      "Messages that notify no one",
      "Status colours",
      "Deleting its messages",
      "Reports of deleted messages",
    ]);
    await expect.element(page.getByRole("list", { name: "Features" })).toBeVisible();
    const readAt = page.getByTestId("bridge-info-read-at");
    await expect.element(readAt).toBeVisible();
    expect(readAt.element().textContent).toMatch(/^Read .*09:31$/);
    await screen.unmount();

    // The rates and the edit window when the bridge declares them; a bridge that only posts and edits.
    const screen2 = await render(
      providers(
        <BridgeInfoPanel
          info={{
            ...INFO,
            capabilities: { thread_messages: "mentions_only" },
            limits: {
              max_body_length: 501,
              edit_max_age_seconds: 172_800,
              send_per_second: 30,
              send_per_second_per_channel: 0.5,
            },
            user_lookup: "none",
            event_delivery: ["push"],
          }}
          readAt="2026-10-17T09:31:00Z"
        />,
      ),
    );
    await expect
      .poll(infoLines)
      .toEqual([
        "Bridge: Muster fake bridge 1.0.0",
        "Messenger: Fake chat",
        "Protocol version 1",
        "Longest message text: 501 characters",
        "Edits for 48 h after posting",
        "Sends per second: 30",
        "Sends per second per channel: 0.5",
        "Finds nobody: accounts link by token only",
        "Event delivery: Push",
      ]);
    expect(features()).toEqual(["Thread messages that mention the bot"]);
    await screen2.unmount();

    await render(providers(<BridgeInfoPanel info={{ ...INFO, capabilities: {} }} />));
    await expect
      .element(page.getByTestId("bridge-info-no-features"))
      .toHaveTextContent("Only posting and editing");
    await expect.element(page.getByTestId("bridge-info-read-at")).not.toBeInTheDocument();
  });

  test("says that nothing was read before the first check", async () => {
    await render(providers(<BridgeInfoPanel info={null} readAt={null} />));
    await expect
      .element(page.getByTestId("bridge-info-none"))
      .toHaveTextContent("Nothing read yet: run the Connection check.");
    await expect.element(page.getByTestId("bridge-info-lines")).not.toBeInTheDocument();
  });

  test("shows what the bridge offers in Russian, with plural forms", async () => {
    await i18n.changeLanguage("ru");
    await render(providers(<BridgeInfoPanel info={INFO} readAt="2026-10-17T09:31:00Z" />));
    await expect.element(page.getByRole("heading", { name: "Что предлагает мост" })).toBeVisible();
    await expect
      .poll(infoLines)
      .toEqual([
        "Мост: Muster fake bridge 1.0.0",
        "Мессенджер: Fake chat",
        "Версия протокола 1",
        "Самый длинный текст сообщения: 4000 символов",
        "Правка без ограничения по времени",
        "Ищет людей по логину",
        "Доставка событий: Опрос, Push-отправка",
      ]);
    expect(features()[0]).toBe("Кнопки");
    const t = i18n.t;
    expect(t("connections.bridge.limits.maxBody", { count: 501 })).toBe(
      "Самый длинный текст сообщения: 501 символ",
    );
    expect(t("connections.bridge.limits.maxBody", { count: 502 })).toBe(
      "Самый длинный текст сообщения: 502 символа",
    );
    expect(t("connections.bridge.limits.maxBody", { count: 4000 })).toBe(
      "Самый длинный текст сообщения: 4000 символов",
    );
  });
});
