// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Slack" in a real browser, against a stubbed API (C-24.FR-12, AC-18): the Connection and the Slack email or name,
// the code step with the attempts left that the server gives after a wrong code, and the text of every refusal of the
// code request and the confirmation — handle_not_found, handle_ambiguous, direct_message_refused, an app whose Messages
// tab is off, link_code_invalid, account_linked_elsewhere, 429 link_code_requests_exceeded and 503 — in English and
// Russian, and no horizontal scroll in the dialog at 360 px.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { LinkableConnection, Session } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
// The dialog is laid out by its classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { LinkSlackDialog, slackLinkProblemText } from "./link-slack-dialog";

const START = "/api/v1/me/account-links/slack";
const CONFIRM = "/api/v1/me/account-links/slack/confirmation";
const CONNECTIONS: LinkableConnection[] = [
  { id: "CN0000000000SA", name: "Dev Slack", messenger: "slack" },
  { id: "CN0000000000SL", name: "ui", messenger: "slack" },
];

interface Answer {
  status: number;
  body: unknown;
  headers?: Record<string, string>;
}

let requests: { method: string; path: string; body: string }[] = [];
let answer: (path: string) => Answer;
let started = 0;

function respond({ status, body, headers = {} }: Answer): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status < 300 ? "application/json" : "application/problem+json",
      ...headers,
    },
  });
}

function problem(status: number, extra: Record<string, unknown> = {}): Answer {
  return { status, body: { type: "about:blank", title: "Refused", status, ...extra } };
}

function fieldProblem(pointer: string, code: string): Answer {
  return problem(422, { code: "validation_failed", errors: [{ pointer, code, detail: code }] });
}

function wrongCode(left: number): Answer {
  return problem(422, {
    errors: [{ pointer: "/code", code: "link_code_invalid", detail: "wrong" }],
    attempts_remaining: left,
  });
}

function startedAnswer(): Answer {
  started += 1;
  return {
    status: 202,
    body: {
      request_id: `LR00000000000${started}`,
      expires_at: "2026-10-20T10:10:00Z",
      attempts_remaining: 5,
    },
  };
}

beforeEach(async () => {
  requests = [];
  started = 0;
  answer = (path) =>
    path === START ? startedAnswer() : { status: 201, body: { id: "AL0000000000AA" } };
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    const method = (init?.method ?? "GET").toUpperCase();
    requests.push({
      method,
      path: url.pathname,
      body: typeof init?.body === "string" ? init.body : "",
    });
    return respond(answer(url.pathname));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await page.viewport(1280, 800);
});

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the dialog reads nothing of the user
    user: { id: "US0000000000ME", name: "Me" } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-21T09:00:00Z",
    idle_expires_at: "2026-10-21T09:00:00Z",
    method: "local",
    permissions: [],
  },
  ended: false,
};

async function open(handle = "alice@example.org") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <LinkSlackDialog connections={CONNECTIONS} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await page.getByRole("button", { name: i18n.t("accountLinks.slack.start") }).click();
  await page.getByLabelText(i18n.t("accountLinks.slack.handle")).fill(handle);
  return screen;
}

async function sendCode() {
  await page.getByRole("button", { name: i18n.t("accountLinks.slack.send") }).click();
}

function codeField() {
  return page.getByLabelText("Enter the code the bot sent you in Slack");
}

function posts(path: string) {
  return requests.filter((r) => r.method === "POST" && r.path === path);
}

describe("LinkSlackDialog", () => {
  test("sends the email or name to the chosen Connection and links with the code, the attempts from the server", async () => {
    answer = (path) => {
      if (path === START) {
        return startedAnswer();
      }
      return posts(CONFIRM).at(-1)?.body.includes('"code":"ABCDEFGH"') === true
        ? { status: 201, body: { id: "AL0000000000AA" } }
        : wrongCode(3);
    };
    await open(" alice@example.org ");
    await expect
      .element(page.getByLabelText("Your Slack email or name"))
      .toHaveAccessibleDescription(
        "An email finds your account for sure; a name is compared with the name and the display name of each account.",
      );
    await page.getByLabelText("Connection").selectOptions("ui");
    await sendCode();
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
    expect(JSON.parse(posts(START)[0]?.body ?? "")).toEqual({
      connection_id: "CN0000000000SL",
      handle: "alice@example.org",
    });
    await codeField().fill("22222222");
    await page.getByRole("button", { name: "Link", exact: true }).click();
    await expect
      .element(page.getByText("The code is wrong or no longer valid. Request a new code."))
      .toBeVisible();
    await expect.element(page.getByText("3 attempts left")).toBeVisible();
    await codeField().fill(" ABCDEFGH ");
    await page.getByRole("button", { name: "Link", exact: true }).click();
    await vi.waitFor(() => expect(page.getByRole("dialog").query()).toBeNull());
    expect(JSON.parse(posts(CONFIRM)[1]?.body ?? "")).toEqual({
      request_id: "LR000000000001",
      code: "ABCDEFGH",
    });
  });

  test("says why a code request is refused, each refusal of S-102 with its text", async () => {
    const cases: [Answer, string][] = [
      [
        fieldProblem("/handle", "handle_not_found"),
        "No Slack account with this email or name was found.",
      ],
      [
        fieldProblem("/handle", "handle_ambiguous"),
        "Several Slack accounts have this name. Enter the email instead.",
      ],
      [
        fieldProblem("/handle", "direct_message_refused"),
        "The bot cannot send a direct message to this account. Enter the email or name of a person's account.",
      ],
      [
        fieldProblem("/handle", "messages_tab_disabled"),
        "The Messages tab of the Slack app is turned off, so the bot cannot send you the code. Ask the admin of the Slack app to turn on the Messages tab under App Home, then request a new code.",
      ],
      [
        problem(409, { code: "account_linked_elsewhere" }),
        "This account is linked to another user. Its owner or an admin can unlink it.",
      ],
      [
        {
          ...problem(429, { code: "link_code_requests_exceeded" }),
          headers: { "Retry-After": "600" },
        },
        "Too many codes were requested. Try again in 10 minutes.",
      ],
      [problem(503), "The messenger is busy. Try again in a few seconds."],
    ];
    await open("nobody");
    for (const [refusal, text] of cases) {
      answer = () => refusal;
      await sendCode();
      await expect.element(page.getByTestId("link-slack-error")).toHaveTextContent(text);
    }
    expect(posts(CONFIRM)).toHaveLength(0);
  });

  test("reads in Russian, with the plural forms, and fits 360 px", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t;
    expect(t("accountLinks.slack.attemptsLeft", { count: 1 })).toBe("Осталась 1 попытка");
    expect(t("accountLinks.slack.attemptsLeft", { count: 3 })).toBe("Осталось 3 попытки");
    expect(t("accountLinks.slack.attemptsLeft", { count: 5 })).toBe("Осталось 5 попыток");
    expect(
      slackLinkProblemText(
        t,
        new ApiError(
          422,
          { errors: [{ pointer: "/handle", code: "handle_not_found" }] },
          undefined,
        ),
      ),
    ).toBe("Аккаунт Slack с такой почтой или именем не найден.");
    expect(
      slackLinkProblemText(t, new ApiError(429, { code: "link_code_requests_exceeded" }, 120)),
    ).toBe("Запрошено слишком много кодов. Попробуйте снова через 2 минуты.");

    await page.viewport(360, 740);
    await open("alice");
    await expect.element(page.getByRole("heading", { name: "Привязать Slack" })).toBeVisible();
    await expect.element(page.getByLabelText("Ваша почта или имя в Slack")).toHaveValue("alice");
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
    await page.getByRole("button", { name: "Отправить код" }).click();
    await expect.element(page.getByText("Осталось 5 попыток")).toBeVisible();
    await expect
      .element(page.getByLabelText("Введите код, который бот прислал вам в Slack"))
      .toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });
});
