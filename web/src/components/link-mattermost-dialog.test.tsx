// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Mattermost" in a real browser, against a stubbed API: the code step with the attempts counted down after a
// wrong code, a used-up request that offers only a new code, and the text of every refusal of the code request and the
// confirmation (link_code_invalid, messenger_user_not_found, account_linked_elsewhere, 429 in minutes from Retry-After
// or the body, 503), in English and Russian.

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
import { LinkMattermostDialog, linkProblemText } from "./link-mattermost-dialog";

const START = "/api/v1/me/account-links/mattermost";
const CONFIRM = "/api/v1/me/account-links/mattermost/confirmation";
const CONNECTIONS: LinkableConnection[] = [
  { id: "CN0000000000MM", name: "mm", messenger: "mattermost" },
  { id: "CN0000000000M2", name: "mm-2", messenger: "mattermost" },
];

interface Answer {
  status: number;
  body: unknown;
  headers?: Record<string, string>;
}

let requests: { method: string; path: string; body: string }[] = [];
let answer: (path: string) => Answer;
let started = 0;
// While set, a confirmation waits for it before the stub answers.
let hold: Promise<void> | null = null;
let release: () => void = () => {};

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
  return problem(422, { errors: [{ pointer, code, detail: code }] });
}

function startedAnswer(): Answer {
  started += 1;
  return {
    status: 201,
    body: {
      request_id: `LR00000000000${started}`,
      expires_at: "2026-10-12T10:10:00Z",
      attempts_remaining: 5,
    },
  };
}

beforeEach(async () => {
  requests = [];
  started = 0;
  hold = null;
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
    if (hold !== null && url.pathname === CONFIRM) {
      await hold;
    }
    return respond(answer(url.pathname));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the dialog reads nothing of the user
    user: { id: "US0000000000ME", name: "Me" } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-13T09:00:00Z",
    idle_expires_at: "2026-10-13T09:00:00Z",
    method: "local",
    permissions: [],
  },
  ended: false,
};

async function open(username = "@alice") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <LinkMattermostDialog connections={CONNECTIONS} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await page.getByRole("button", { name: i18n.t("accountLinks.mattermost.start") }).click();
  await page.getByLabelText(i18n.t("accountLinks.mattermost.username")).fill(username);
  return screen;
}

async function sendCode() {
  await page.getByRole("button", { name: i18n.t("accountLinks.mattermost.send") }).click();
}

function codeField() {
  return page.getByLabelText("Enter the code the bot sent you in Mattermost");
}

async function enter(code: string) {
  await codeField().fill(code);
  await page.getByRole("button", { name: "Link", exact: true }).click();
}

function posts(path: string) {
  return requests.filter((r) => r.method === "POST" && r.path === path);
}

describe("LinkMattermostDialog", () => {
  test("counts down the attempts after a wrong code and links with the right one", async () => {
    answer = (path) => {
      if (path === START) {
        return startedAnswer();
      }
      return posts(CONFIRM).at(-1)?.body.includes('"code":"ABCDEFGH"') === true
        ? { status: 201, body: { id: "AL0000000000AA" } }
        : fieldProblem("/code", "link_code_invalid");
    };
    await open();
    await page.getByLabelText("Connection").selectOptions("mm-2");
    await sendCode();
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
    expect(JSON.parse(posts(START)[0]?.body ?? "")).toEqual({
      connection_id: "CN0000000000M2",
      username: "@alice",
    });

    await enter("22222222");
    await expect
      .element(page.getByText("The code is wrong or no longer valid. Request a new code."))
      .toBeVisible();
    await expect.element(page.getByText("4 attempts left")).toBeVisible();
    await expect.element(codeField()).toHaveValue("");
    expect(JSON.parse(posts(CONFIRM)[0]?.body ?? "")).toEqual({
      request_id: "LR000000000001",
      code: "22222222",
    });

    await enter(" ABCDEFGH ");
    await vi.waitFor(() => expect(page.getByRole("dialog").query()).toBeNull());
    expect(JSON.parse(posts(CONFIRM)[1]?.body ?? "")).toEqual({
      request_id: "LR000000000001",
      code: "ABCDEFGH",
    });
  });

  test("a confirmation still pending when the dialog closes leaves the reopened dialog alone", async () => {
    answer = (path) =>
      path === START ? startedAnswer() : fieldProblem("/code", "link_code_invalid");
    hold = new Promise((resolve) => {
      release = resolve;
    });
    await open();
    await sendCode();
    await enter("22222222");
    await vi.waitFor(() => expect(posts(CONFIRM)).toHaveLength(1));

    await page
      .getByRole("button", { name: i18n.t("common.close") })
      .first()
      .click();
    await vi.waitFor(() => expect(page.getByRole("dialog").query()).toBeNull());
    await page.getByRole("button", { name: i18n.t("accountLinks.mattermost.start") }).click();
    await page.getByLabelText(i18n.t("accountLinks.mattermost.username")).fill("@alice");
    await sendCode();
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
    await codeField().fill("ABCDEFGH");

    // The first round's wrong code is answered now; the new round neither counts it nor clears the code.
    release();
    await new Promise((resolve) => setTimeout(resolve, 100));
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
    await expect.element(codeField()).toHaveValue("ABCDEFGH");
    expect(
      page.getByText("The code is wrong or no longer valid. Request a new code.").query(),
    ).toBeNull();
  });

  test("a confirmation of a request that is gone says so", async () => {
    answer = (path) => (path === START ? startedAnswer() : problem(404));
    await open();
    await sendCode();
    await enter("ABCDEFGH");
    await expect.element(page.getByText("It no longer exists. Reload the page.")).toBeVisible();
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
  });

  test("after the last wrong code only a new code is offered, which restores the attempts", async () => {
    answer = (path) =>
      path === START ? startedAnswer() : fieldProblem("/code", "link_code_invalid");
    await open();
    await sendCode();
    for (let i = 0; i < 5; i++) {
      await enter("22222222");
      await expect.element(page.getByText(`${4 - i} attempt`, { exact: false })).toBeVisible();
    }
    await expect.element(page.getByText("0 attempts left")).toBeVisible();
    expect(codeField().query()).toBeNull();
    expect(page.getByRole("button", { name: "Link", exact: true }).query()).toBeNull();
    await expect
      .element(page.getByText("The code is wrong or no longer valid. Request a new code."))
      .toBeVisible();

    await page.getByRole("button", { name: "Request a new code" }).click();
    await expect.element(page.getByText("5 attempts left")).toBeVisible();
    expect(posts(START)).toHaveLength(2);
    expect(JSON.parse(posts(START)[1]?.body ?? "")).toEqual({
      connection_id: "CN0000000000MM",
      username: "@alice",
    });
    await expect.element(codeField()).toBeVisible();
    expect(
      page.getByText("The code is wrong or no longer valid. Request a new code.").query(),
    ).toBeNull();
  });

  test("an account linked to another user is refused with its text", async () => {
    answer = () => problem(409, { code: "account_linked_elsewhere" });
    await open("@bob");
    await sendCode();
    await expect
      .element(
        page.getByText(
          "This account is linked to another user. Its owner or an admin can unlink it.",
        ),
      )
      .toBeVisible();
  });

  test("an unknown username is named once, with one @", async () => {
    answer = () => fieldProblem("/username", "messenger_user_not_found");
    await open("@bob");
    await sendCode();
    await expect.element(page.getByText("No user @bob on this Mattermost server.")).toBeVisible();
  });

  test("too many code requests name the wait in minutes", async () => {
    answer = () => ({ ...problem(429), headers: { "Retry-After": "1800" } });
    await open();
    await sendCode();
    await expect
      .element(page.getByText("Too many codes were requested. Try again in 30 minutes."))
      .toBeVisible();

    answer = () => problem(429, { retry_after_seconds: 30 });
    await sendCode();
    await expect
      .element(page.getByText("Too many codes were requested. Try again in 1 minute."))
      .toBeVisible();
  });

  test("a busy messenger asks to try again shortly", async () => {
    answer = () => problem(503);
    await open();
    await sendCode();
    await expect
      .element(page.getByText("The messenger is busy. Try again in a few seconds."))
      .toBeVisible();
  });

  test("the Russian texts with their plural forms", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t;
    expect(t("accountLinks.mattermost.attemptsLeft", { count: 4 })).toBe("Осталось 4 попытки");
    expect(t("accountLinks.mattermost.attemptsLeft", { count: 1 })).toBe("Осталась 1 попытка");
    expect(t("accountLinks.mattermost.attemptsLeft", { count: 5 })).toBe("Осталось 5 попыток");
    expect(linkProblemText(t, new ApiError(429, {}, 300), "x")).toBe(
      "Запрошено слишком много кодов. Попробуйте снова через 5 минут.",
    );
    expect(linkProblemText(t, new ApiError(429, {}, 120), "x")).toBe(
      "Запрошено слишком много кодов. Попробуйте снова через 2 минуты.",
    );
    expect(linkProblemText(t, new ApiError(429, {}, 60), "x")).toBe(
      "Запрошено слишком много кодов. Попробуйте снова через 1 минуту.",
    );
    expect(
      linkProblemText(
        t,
        new ApiError(
          422,
          { errors: [{ pointer: "/username", code: "messenger_user_not_found" }] },
          undefined,
        ),
        "@bob",
      ),
    ).toBe("На этом сервере Mattermost нет пользователя @bob.");

    answer = (path) =>
      path === START ? startedAnswer() : fieldProblem("/code", "link_code_invalid");
    await open();
    await page.getByRole("button", { name: "Отправить код" }).click();
    await expect.element(page.getByText("Осталось 5 попыток")).toBeVisible();
    await page.getByLabelText("Введите код, который бот прислал вам в Mattermost").fill("22222222");
    await page.getByRole("button", { name: "Привязать", exact: true }).click();
    await expect.element(page.getByText("Осталось 4 попытки")).toBeVisible();
    await expect
      .element(page.getByText("Код неверный или больше не действует. Запросите новый код."))
      .toBeVisible();
  });
});
