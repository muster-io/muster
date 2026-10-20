// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Test panel of a Destination in a real browser: one block per step with its name and outcome for every error
// class, the button press block reached and not reached — translated by its error code, the server's text below —, the
// error, duration, request with its masked headers, response
// status and body as plain text and the extracted values; why a failed test leaves a Broken Destination Broken, the
// button press included; "Send test message" with the chosen source, disabled while it runs, the health of the result
// taken by the page at once, and the refusal of an Alert Group that is no longer offered. The source selector offers
// the example and the recent Alert Groups of the Destination's Routes. The hint of a Bridge Destination says what its
// test sends instead of the outgoing webhook's. A test or a Destination check whose result names its run offers
// "Requests of this check", which lists the requests of that run beside the Destination. Both in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import { getGetDestinationQueryKey } from "../api/gen/endpoints/destinations/destinations";
import type {
  BridgeDestination,
  DestinationCheckResult,
  MattermostDestination,
  DeliveryErrorClass,
  Destination,
  DestinationTestResult,
  Permission,
  Session,
  SlackDestination,
  TestStep,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { DestinationCheck } from "./destination-check";
import { DestinationTestPanel, TestResult } from "./destination-test-panel";
import { readableBody } from "./rendered-request";
import { defaultMentions } from "./mention-settings";
import { EXAMPLE, type PickedSource, TestSourcePicker } from "./test-source-picker";

const ID = "DS0000000000WH";
/** The run of every test and check result; each result names one. */
const RUN = "CR0000000000R1";
const PRESS_ERROR =
  "The button press did not reach Muster. Add the host of http://localhost:8081 to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.";

const DESTINATION: WebhookDestination = {
  id: ID,
  type: "webhook",
  name: "chat",
  mode: "template",
  mentions: defaultMentions(),
  limiter: { limit: 5, per_seconds: 1 },
  proxy: { enabled: false },
  signing_secret_status: { set: true },
  warnings: [],
  health: { state: "broken", since: "2026-10-09T09:30:00Z", reason: "Refused" },
  routes: [{ id: "RT0000000000DB", name: "db" }],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"4"',
};

const ALL: Permission[] = ["destinations:read", "destinations:test", "alert-groups:read"];

function step(
  name: TestStep["name"],
  errorClass: DeliveryErrorClass,
  extra: Partial<TestStep> = {},
): TestStep {
  return { name, error_class: errorClass, duration_ms: 12, ...extra };
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function providers(children: ReactNode, permissions: Permission[] = ALL) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the panels read only the Permissions
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  queryClient.setQueryData(getGetDestinationQueryKey(ID), DESTINATION);
  return {
    queryClient,
    ui: (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </I18nextProvider>
    ),
  };
}

function titles(): string[] {
  return Array.from(document.querySelectorAll('[data-testid="test-step-title"]')).map(
    (e) => e.textContent ?? "",
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("TestResult", () => {
  test("names every step and the outcome of every error class", async () => {
    const classes: DeliveryErrorClass[] = [
      "none",
      "retry_after",
      "transient",
      "fatal",
      "unknown",
      "template_error",
      "blocked",
      "limited",
    ];
    const result: DestinationTestResult = {
      steps: [
        ...classes.map((c) => step("message", c)),
        step("event", "limited"),
        step("create", "limited"),
        step("create", "fatal", { error: "Mattermost answered 403: <b>no</b>" }),
      ],
      health: { state: "healthy" },
      request_log_run: RUN,
    };
    const { ui } = providers(<TestResult result={result} />);
    await render(ui);
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Test failed");
    expect(titles()).toEqual([
      "Message: Sent",
      "Message: Asked to wait and try again later",
      "Message: No answer or a temporary error",
      "Message: Refused",
      "Message: Unexpected answer",
      "Message: Template error",
      "Message: Blocked by the outbound address policy",
      "Message: The messenger is busy; nothing was sent. Try again in a few seconds.",
      // An outgoing webhook's limit guards its endpoint, not a messenger.
      "Event: The endpoint's rate limit is reached; nothing was sent. Try again in a few seconds.",
      "Create: The endpoint's rate limit is reached; nothing was sent. Try again in a few seconds.",
      "Create: Refused",
    ]);
    // The error is untrusted text: shown as it is, never as markup.
    await expect
      .element(page.getByTestId("test-step-error"))
      .toHaveTextContent("Mattermost answered 403: <b>no</b>");
    expect(document.querySelector('[data-testid="test-result"] b')).toBeNull();
    await expect.element(page.getByTestId("test-still-broken")).not.toBeInTheDocument();
  });

  test("shows that button presses reach Muster, or the press error by its code with the server's text", async () => {
    const reached: DestinationTestResult = {
      steps: [step("message", "none"), step("press", "none")],
      health: { state: "healthy" },
      request_log_run: RUN,
    };
    const { ui } = providers(<TestResult result={reached} />);
    const screen = await render(ui);
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Test passed");
    expect(titles()).toEqual(["Message: Sent", "Button press: Button presses reach Muster."]);

    await screen.rerender(
      providers(
        <TestResult
          result={{
            steps: [
              step("message", "none"),
              step("press", "unknown", { error: PRESS_ERROR, error_code: "press_not_reached" }),
            ],
            health: { state: "healthy" },
            request_log_run: RUN,
          }}
        />,
      ).ui,
    );
    expect(titles()).toEqual([
      "Message: Sent",
      "Button press: The button press did not reach Muster. Add the host of MUSTER_INGEST_URL to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server.",
    ]);
    // The server's text, with the address, is the detail.
    await expect.element(page.getByTestId("test-step-error")).toHaveTextContent(PRESS_ERROR);

    await screen.rerender(
      providers(
        <TestResult
          result={{
            steps: [
              step("message", "none"),
              step("press", "unknown", {
                error: "Muster does not know the bot of this Connection yet.",
                error_code: "bot_unknown",
              }),
            ],
            health: { state: "healthy" },
            request_log_run: RUN,
          }}
        />,
      ).ui,
    );
    expect(titles()).toEqual([
      "Message: Sent",
      "Button press: Muster does not know the bot of this Connection yet, so it did not press the button. Run the Connection check, then test again.",
    ]);

    // An error without a code reads as its class, the server's text below.
    await screen.rerender(
      providers(
        <TestResult
          result={{
            steps: [step("message", "none"), step("press", "unknown", { error: "boom" })],
            health: { state: "healthy" },
            request_log_run: RUN,
          }}
        />,
      ).ui,
    );
    expect(titles()).toEqual(["Message: Sent", "Button press: Unexpected answer"]);
    await expect.element(page.getByTestId("test-step-error")).toHaveTextContent("boom");

    // An empty error falls back to the text of its class.
    await screen.rerender(
      providers(
        <TestResult
          result={{
            steps: [step("message", "none"), step("press", "fatal", { error: "" })],
            health: { state: "healthy" },
            request_log_run: RUN,
          }}
        />,
      ).ui,
    );
    expect(titles()).toEqual(["Message: Sent", "Button press: Refused"]);

    await screen.rerender(
      providers(
        <TestResult
          result={{
            steps: [step("message", "none"), step("press", "limited")],
            health: { state: "healthy" },
            request_log_run: RUN,
          }}
        />,
      ).ui,
    );
    expect(titles()).toEqual([
      "Message: Sent",
      "Button press: The messenger is busy; the button was not pressed. Try again in a few seconds.",
    ]);
  });

  test("explains why a Broken Destination stays Broken, the button press included", async () => {
    const press: DestinationTestResult = {
      steps: [
        step("message", "none"),
        step("press", "unknown", { error: PRESS_ERROR, error_code: "press_not_reached" }),
      ],
      health: { state: "broken", since: "2026-10-09T09:30:00Z", reason: "Refused" },
      request_log_run: RUN,
    };
    const { ui } = providers(<TestResult result={press} />);
    const screen = await render(ui);
    await expect
      .element(page.getByTestId("test-still-broken"))
      .toHaveTextContent(
        "The test message was posted, but the Destination stays Broken: a test ends the Broken state only when every step succeeds, the button press included.",
      );
    await screen.rerender(
      providers(
        <TestResult
          result={{ steps: [step("message", "fatal")], health: press.health, request_log_run: RUN }}
        />,
      ).ui,
    );
    await expect
      .element(page.getByTestId("test-still-broken"))
      .toHaveTextContent(
        "The Destination stays Broken: a test ends the Broken state only when every step succeeds.",
      );
  });

  test("shows the request with masked headers, the status, the body as text and the extracted values", async () => {
    const result: DestinationTestResult = {
      steps: [
        step("create", "none", {
          duration_ms: 37,
          request: {
            method: "POST",
            url: "http://127.0.0.1:18093/chat/ops/messages",
            headers: [
              { name: "Authorization", value: "Bearer [redacted]" },
              { name: "Content-Type", value: "application/json" },
            ],
            body: '{"text":"#1 HighErrorRate (firing)"}',
          },
          response_status: 200,
          response_body: '<img src=x onerror="alert(1)">',
          extracted: { id: "m1" },
        }),
      ],
      health: { state: "healthy" },
      request_log_run: RUN,
    };
    const { ui } = providers(<TestResult result={result} />);
    await render(ui);
    expect(titles()).toEqual(["Create: Sent"]);
    await expect.element(page.getByTestId("test-step-duration")).toHaveTextContent("Took 37 ms");
    await expect
      .element(page.getByTestId("test-step-status"))
      .toHaveTextContent("Response status: 200");
    await expect
      .element(page.getByTestId("rendered-request-line"))
      .toHaveTextContent("POST http://127.0.0.1:18093/chat/ops/messages");
    await expect
      .element(page.getByTestId("rendered-header").first())
      .toHaveTextContent("Authorization: Bearer [redacted]");
    await expect
      .element(page.getByTestId("rendered-request-body"))
      .toHaveTextContent('{ "text": "#1 HighErrorRate (firing)" }');
    await expect
      .element(page.getByTestId("test-step-response"))
      .toHaveTextContent('<img src=x onerror="alert(1)">');
    expect(document.querySelector('[data-testid="test-result"] img')).toBeNull();
    const extracted = page.getByTestId("test-step-extracted");
    await expect.element(extracted.getByText("Extracted values")).toBeVisible();
    await expect.element(extracted.getByRole("rowheader", { name: "id" })).toBeVisible();
    await expect.element(extracted.getByRole("cell", { name: "m1" })).toBeVisible();
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    const { ui } = providers(
      <TestResult
        result={{
          steps: [
            step("message", "none"),
            step("press", "none"),
            step("event", "limited"),
            step("press", "unknown", { error: PRESS_ERROR, error_code: "press_not_reached" }),
            step("press", "unknown", { error: "bot", error_code: "bot_unknown" }),
          ],
          health: { state: "healthy" },
          request_log_run: RUN,
        }}
      />,
    );
    await render(ui);
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Тест не пройден");
    expect(titles()).toEqual([
      "Сообщение: Отправлено",
      "Нажатие кнопки: Нажатия кнопок доходят до Muster.",
      "Событие: Лимит запросов к адресу исчерпан, ничего не отправлено. Повторите через несколько секунд.",
      "Нажатие кнопки: Нажатие кнопки не дошло до Muster. Добавьте хост из MUSTER_INGEST_URL в ServiceSettings.AllowedUntrustedInternalConnections на сервере Mattermost.",
      "Нажатие кнопки: Muster ещё не знает бота этого подключения, поэтому не нажал кнопку. Запустите проверку подключения и повторите тест.",
    ]);
  });
});

/** The panel with its source selector, as the Destination page shows them. */
function Panel({ dirty = false }: { dirty?: boolean }) {
  const [source, setSource] = useState<PickedSource>(EXAMPLE);
  const [gone, setGone] = useState(false);
  return (
    <>
      <TestSourcePicker
        routeIds={["RT0000000000DB"]}
        value={source}
        onChange={(picked) => {
          setSource(picked);
          setGone(false);
        }}
        gone={gone}
      />
      <DestinationTestPanel
        destination={DESTINATION}
        source={source}
        dirty={dirty}
        onSourceGone={() => {
          setSource(EXAMPLE);
          setGone(true);
        }}
      />
    </>
  );
}

interface Call {
  method: string;
  url: string;
  body: string;
}

function fakeApi(answer: () => Promise<Response>): Call[] {
  const calls: Call[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({ method, url, body: typeof init?.body === "string" ? init.body : "" });
    if (url.startsWith("/api/v1/alert-groups")) {
      return json(200, {
        items: [
          { id: "AG0000000000K1", number: 12, title: "DiskFull" },
          { id: "AG0000000000K2", number: 11, title: "QueueStuck" },
        ],
        next_cursor: null,
      });
    }
    if (url === `/api/v1/destinations/${ID}/tests` && method === "POST") {
      return answer();
    }
    if (url === `/api/v1/destinations/${ID}`) {
      // Never answers: the health on the page can only come from the result.
      return new Promise<Response>(() => undefined);
    }
    return json(404, { title: "not mocked" });
  });
  return calls;
}

describe("DestinationTestPanel", () => {
  test("sends the test from the chosen Alert Group and takes the health of the result at once", async () => {
    let release: ((r: Response) => void) | undefined;
    const calls = fakeApi(
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    );
    const { queryClient, ui } = providers(<Panel dirty />);
    await render(ui);
    await expect
      .element(page.getByText("The test uses the saved settings: save your changes first."))
      .toBeVisible();
    const select = page.getByLabelText("Based on");
    await expect.element(select).toHaveValue("example");
    await expect
      .poll(() => Array.from(document.querySelectorAll("option")).map((o) => o.textContent ?? ""))
      .toEqual(["Example", "#12 DiskFull", "#11 QueueStuck"]);
    const list = calls.find((c) => c.url.startsWith("/api/v1/alert-groups"));
    expect(list?.url).toContain("route=RT0000000000DB");
    await userEvent.selectOptions(select, "AG0000000000K1");

    await page.getByRole("button", { name: "Send test message" }).click();
    await expect.element(page.getByRole("button", { name: "Sending…" })).toBeDisabled();
    const sent = calls.find((c) => c.method === "POST");
    expect(JSON.parse(sent?.body ?? "")).toEqual({
      source: { kind: "alert_group", alert_group_id: "AG0000000000K1" },
    });
    release?.(
      json(200, {
        steps: [step("create", "none", { extracted: { id: "m1" } })],
        health: { state: "healthy" },
      }),
    );
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Test passed");
    await expect.element(page.getByRole("button", { name: "Send test message" })).toBeEnabled();
    expect(queryClient.getQueryData<Destination>(getGetDestinationQueryKey(ID))?.health.state).toBe(
      "healthy",
    );

    // The result belongs to its source: another source starts empty.
    await userEvent.selectOptions(select, "example");
    await expect.element(page.getByTestId("test-result")).not.toBeInTheDocument();
  });

  test("sends the example by default and maps the refusals", async () => {
    let answer = json(429, { title: "Too many requests", status: 429, retry_after_seconds: 3 });
    const calls = fakeApi(() => Promise.resolve(answer));
    const { ui } = providers(<Panel />);
    await render(ui);
    const send = page.getByRole("button", { name: "Send test message" });
    await send.click();
    await expect
      .element(page.getByTestId("test-error"))
      .toHaveTextContent("Too many attempts. Try again in 3 seconds.");
    expect(JSON.parse(calls.find((c) => c.method === "POST")?.body ?? "")).toEqual({
      source: { kind: "example" },
    });
    for (const [status, text] of [
      [403, "You do not have permission to do this."],
      [404, "It no longer exists. Reload the page."],
      [500, "Something went wrong. Try again."],
    ] as const) {
      answer = json(status, { title: "Refused", status });
      await send.click();
      await expect.element(page.getByTestId("test-error")).toHaveTextContent(text);
    }

    // An Alert Group that is no longer offered: the selector returns to the example.
    const select = page.getByLabelText("Based on");
    await expect.poll(() => document.querySelectorAll("option").length).toBe(3);
    await userEvent.selectOptions(select, "AG0000000000K2");
    answer = json(422, {
      title: "Validation failed",
      status: 422,
      errors: [{ pointer: "/source/alert_group_id", code: "unknown_id" }],
    });
    // The refusal of the earlier source went with it.
    await expect.element(page.getByTestId("test-error")).not.toBeInTheDocument();
    const lists = calls.filter((c) => c.url.startsWith("/api/v1/alert-groups")).length;
    await send.click();
    const gone = page.getByRole("alert");
    await expect
      .element(gone)
      .toHaveTextContent(
        "This Alert Group is no longer one of the recent Alert Groups of this Destination's Routes. Choose another one.",
      );
    expect(JSON.parse(calls.filter((c) => c.method === "POST").at(-1)?.body ?? "")).toEqual({
      source: { kind: "alert_group", alert_group_id: "AG0000000000K2" },
    });
    await expect.element(select).toHaveValue("example");
    await expect.element(select).toHaveDisplayValue("Example");
    // The selector read its list again, and still says why it returned to the example; the panel does not repeat it.
    await expect
      .poll(() => calls.filter((c) => c.url.startsWith("/api/v1/alert-groups")).length)
      .toBeGreaterThan(lists);
    await expect.element(gone).toBeVisible();
    expect(document.querySelector('[data-testid="test-error"]')).toBeNull();
    expect(document.querySelectorAll('[data-testid="test-source-gone"]')).toHaveLength(1);

    // Choosing another source clears it.
    await userEvent.selectOptions(select, "AG0000000000K1");
    await expect.element(page.getByTestId("test-source-gone")).not.toBeInTheDocument();
  });

  test("explains the test of a Bridge Destination", async () => {
    const bridge: BridgeDestination = {
      id: "DS0000000000BR",
      type: "bridge",
      name: "alerts",
      connection_id: "CN0000000000BR",
      channel_id: "ch-alerts",
      channel_name: "alerts",
      mentions: defaultMentions(),
      limiter: { limit: 30, per_seconds: 60 },
      health: { state: "healthy" },
      routes: [],
      created_at: "2026-10-17T09:30:00Z",
      etag: '"1"',
    };
    const calls = fakeApi(() => Promise.resolve(json(500, {})));
    const panel = () => (
      <DestinationTestPanel
        destination={bridge}
        source={EXAMPLE}
        dirty={false}
        onSourceGone={() => undefined}
      />
    );
    const screen = await render(providers(panel()).ui);
    const hint = page.getByTestId("destination-test").getByText(/^Sends one test message/);
    await expect
      .element(hint)
      .toHaveTextContent(
        "Sends one test message, marked 🧪, through the bridge with the saved settings. No Alert Group changes; when a person presses a button of a test message, nothing changes and Muster answers that it is a test message. A test that succeeds in every step ends the Broken state.",
      );
    // Not the outgoing webhook's hint, and no language named: a Bridge answers in the linked User's language.
    expect(document.body.textContent).not.toContain("the “Create” request");
    expect(document.body.textContent).not.toContain("in English");
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(providers(panel()).ui);
    await expect
      .element(page.getByTestId("destination-test").getByText(/^Отправляет через мост/))
      .toHaveTextContent(
        "Отправляет через мост одно тестовое сообщение с пометкой 🧪 по сохранённым настройкам. Группы алертов не меняются; если человек нажмёт кнопку тестового сообщения, ничего не изменится, а Muster ответит, что это тестовое сообщение. Тест, успешный на каждом шаге, снимает состояние «сломано».",
      );
    expect(document.body.textContent).not.toContain("по-английски");
  });

  test("says beside a Slack Destination's one step that Slack does not let a bot press its own button", async () => {
    const slack: SlackDestination = {
      id: ID,
      type: "slack",
      name: "slack-alerts",
      connection_id: "CN0000000000SL",
      channel_id: "C0FAKEALERTS",
      channel_name: "alerts",
      mentions: defaultMentions(),
      limiter: { limit: 30, per_seconds: 60 },
      health: { state: "healthy" },
      routes: [],
      created_at: "2026-10-20T09:30:00Z",
      etag: '"1"',
    };
    const result: DestinationTestResult = {
      steps: [step("message", "none", { response_status: 200 })],
      health: { state: "healthy" },
      request_log_run: RUN,
    };
    fakeApi(() => Promise.resolve(json(200, result)));
    const panel = () => (
      <DestinationTestPanel
        destination={slack}
        source={EXAMPLE}
        dirty={false}
        onSourceGone={() => undefined}
      />
    );
    const screen = await render(providers(panel()).ui);
    await expect.element(page.getByTestId("test-note")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Send test message" }).click();
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Test passed");
    expect(titles()).toEqual(["Message: Sent"]);
    await expect
      .element(page.getByTestId("test-note"))
      .toHaveTextContent(
        "Slack does not let a bot press its own button, so this test does not check presses. Press a button on the test message: once the press reaches Muster, it answers that nothing was changed.",
      );
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(providers(panel()).ui);
    await page.getByRole("button", { name: "Отправить тестовое сообщение" }).click();
    await expect
      .element(page.getByTestId("test-note"))
      .toHaveTextContent(
        "Slack не даёт боту нажимать собственные кнопки, поэтому этот тест не проверяет нажатия. Нажмите кнопку тестового сообщения: когда нажатие дойдёт до Muster, он ответит, что ничего не изменилось.",
      );
  });

  test("offers only the example without alert-groups:read", async () => {
    const calls = fakeApi(() => Promise.resolve(json(500, {})));
    const { ui } = providers(<Panel />, ["destinations:read", "destinations:test"]);
    await render(ui);
    await expect
      .element(page.getByText("Only the example is offered: you may not read Alert Groups."))
      .toBeVisible();
    expect(Array.from(document.querySelectorAll("option")).map((o) => o.textContent)).toEqual([
      "Example",
    ]);
    expect(calls.some((c) => c.url.startsWith("/api/v1/alert-groups"))).toBe(false);
    expect(document.querySelector('input[type="search"]')).toBeNull();
  });
});

/** Answers the test or the check with the result and the Request log with one record of the run. */
function serveRun(result: unknown, record: Record<string, unknown>): Record<string, string>[] {
  const queries: Record<string, string>[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
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
            method: "POST",
            url: "http://127.0.0.1:18093/hook/rl",
            latency_ms: 4,
            via: "direct",
            attempt: 1,
            error_class: null,
            error_code: null,
            check_run: RUN,
            connection_id: null,
            destination: null,
            alert_group: null,
            request_body_truncated: false,
            response_body_truncated: false,
            ...record,
          },
        ],
        next_cursor: null,
      });
    }
    if ((init?.method ?? "GET").toUpperCase() === "POST") {
      return json(200, result);
    }
    if (url.startsWith("/api/v1/alert-groups")) {
      return json(200, { items: [], next_cursor: null });
    }
    // Never answers: nothing else is read.
    return new Promise<Response>(() => undefined);
  });
  return queries;
}

describe("the requests of a test or a check", () => {
  test("opens the requests of the test's run beside the Destination", async () => {
    const queries = serveRun(
      {
        steps: [step("event", "fatal", { response_status: 500 })],
        health: DESTINATION.health,
        request_log_run: RUN,
      },
      { operation: "event", status: 500, check: "test" },
    );
    const { ui } = providers(<Panel />);
    await render(ui);
    await page.getByRole("button", { name: "Send test message" }).click();
    await expect.element(page.getByTestId("test-summary")).toHaveTextContent("Test failed");
    await page.getByRole("button", { name: "Requests of this check" }).click();
    const list = page.getByRole("dialog", { name: "Requests of this check" });
    await expect.element(list.getByRole("button", { name: "event" })).toBeVisible();
    await expect.element(list.getByTestId("request-status")).toHaveTextContent("500");
    await expect.element(list.getByTestId("request-check")).toHaveTextContent("Test message");
    expect(queries).toEqual([{ destination: ID, run: RUN }]);
  });

  test("opens the requests of a Destination check's run, and in Russian", async () => {
    const mattermost: MattermostDestination = {
      id: "DS0000000000MM",
      type: "mattermost",
      name: "alerts",
      connection_id: "CN0000000000MM",
      team_id: "team-dev",
      channel_id: "ch-alerts",
      channel_name: "alerts",
      mentions: defaultMentions(),
      limiter: { limit: 50, per_seconds: 1 },
      health: { state: "healthy" },
      routes: [],
      created_at: "2026-10-17T09:30:00Z",
      etag: '"1"',
    };
    const result: DestinationCheckResult = {
      ok: true,
      checks: [],
      health: { state: "healthy" },
      request_log_run: RUN,
    };
    const queries = serveRun(result, {
      operation: "getChannel",
      method: "GET",
      status: 200,
      check: "destination_check",
      connection_id: "CN0000000000MM",
    });
    const { ui } = providers(<DestinationCheck destination={mattermost} dirty={false} />);
    const screen = await render(ui);
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect
      .element(page.getByTestId("destination-check-summary"))
      .toHaveTextContent("Check passed");
    await page.getByRole("button", { name: "Requests of this check" }).click();
    const list = page.getByRole("dialog", { name: "Requests of this check" });
    await expect.element(list.getByTestId("request-check")).toHaveTextContent("Destination check");
    expect(queries).toEqual([{ destination: "DS0000000000MM", run: RUN }]);
    await screen.unmount();

    await i18n.changeLanguage("ru");
    await render(providers(<DestinationCheck destination={mattermost} dirty={false} />).ui);
    await page.getByRole("button", { name: "Проверить", exact: true }).click();
    await page.getByRole("button", { name: "Запросы этой проверки" }).click();
    await expect
      .element(
        page.getByRole("dialog", { name: "Запросы этой проверки" }).getByTestId("request-check"),
      )
      .toHaveTextContent("Проверка места доставки");
  });
});

describe("readableBody", () => {
  test("indents JSON only when nothing but the spacing changes", () => {
    expect(readableBody('{"a":1,"b":[true]}')).toBe('{\n  "a": 1,\n  "b": [\n    true\n  ]\n}');
    // Go escapes <, > and & in strings; the text is the same.
    expect(readableBody('{"text":"\\u003cb\\u003e"}')).toBe('{\n  "text": "<b>"\n}');
    for (const raw of [
      '{"id":1234567890123456789}',
      '{"a":1,"a":2}',
      '{"a":"\\u00e9"}',
      '{"cut":',
      "plain text",
    ]) {
      expect(readableBody(raw)).toBe(raw);
    }
  });
});
