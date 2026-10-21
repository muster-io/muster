// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link through {Connection}" for a Bridge in a real browser, against a stubbed API (C-22.FR-15, AC-18): the token with
// "Copy", the instruction, its expiry and the bridge's link when it has a safe one; the account-links hint that closes
// the dialog only for a Bridge link of this Connection; the code tab only where the bridge offers a code, its field
// labelled by the kind of handle; the attempts left from the code request and from each wrong code, "Request a new
// code"; and every refusal at the field it concerns, in English and Russian with the plural forms.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AccountLink, LinkableConnection, Session } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { applyHint } from "../lib/live";
// The dialog is laid out by its classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { LinkBridgeDialog, bridgeLinkProblem, handleKindLabel } from "./link-bridge-dialog";

const START = "/api/v1/me/account-links/bridge";
const CODES = "/api/v1/me/account-links/bridge/codes";
const CONFIRM = "/api/v1/me/account-links/bridge/confirmation";
const LIST = "/api/v1/me/account-links";
const TOKEN = "tok_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnop";

const BR: LinkableConnection = {
  id: "CN0000000000BR",
  name: "br",
  messenger: "bridge",
  link_methods: ["token"],
  handle_kind: null,
};
const DEVB: LinkableConnection = {
  id: "CN0000000000DB",
  name: "Example Bridge",
  messenger: "bridge",
  link_methods: ["token", "code"],
  handle_kind: "email",
};

const BRIDGE_LINK: AccountLink = {
  id: "AK0000000000B1",
  messenger: "bridge",
  connection: { id: BR.id, name: "br" },
  external_id: "u-bob",
  username: "bob",
  display_name: "Bob Brown",
  created_at: "2026-10-17T10:00:00Z",
};

interface Answer {
  status: number;
  body: unknown;
  headers?: Record<string, string>;
}

let requests: { method: string; path: string; body: string }[] = [];
let answer: (path: string) => Answer;
let links: AccountLink[] = [];
let linkUrl: string | null = null;
let codesStarted = 0;
let listed = 0;

function respond({ status, body, headers = {} }: Answer): Response {
  return new Response(JSON.stringify(body), {
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

function fieldProblem(pointer: string, code: string, extra: Record<string, unknown> = {}): Answer {
  return problem(422, { errors: [{ pointer, code, detail: code }], ...extra });
}

function wrongCode(left: number): Answer {
  return fieldProblem("/code", "link_code_invalid", { attempts_remaining: left });
}

function defaultAnswer(path: string): Answer {
  switch (path) {
    case START:
      return {
        status: 201,
        body: {
          token: TOKEN,
          link_url: linkUrl,
          expires_at: new Date(Date.now() + 600_000).toISOString(),
        },
      };
    case CODES:
      codesStarted += 1;
      return {
        status: 202,
        body: {
          request_id: `LR00000000000${codesStarted}`,
          expires_at: new Date(Date.now() + 600_000).toISOString(),
          attempts_remaining: 5,
        },
      };
    case CONFIRM:
      return { status: 201, body: BRIDGE_LINK };
    case LIST:
      listed += 1;
      return { status: 200, body: { items: links } };
    default:
      return problem(404);
  }
}

const SESSION: SessionRead = {
  session: {
    state: "active",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the dialog reads only the time zone
    user: { id: "US0000000000ME", name: "Me", time_zone: "UTC" } as Session["user"],
    csrf_token: "csrf",
    expires_at: "2026-10-18T09:00:00Z",
    idle_expires_at: "2026-10-18T09:00:00Z",
    docs_version: "latest",
    method: "local",
    permissions: [],
  },
  ended: false,
};

beforeEach(async () => {
  requests = [];
  links = [];
  linkUrl = null;
  codesStarted = 0;
  listed = 0;
  answer = defaultAnswer;
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    requests.push({
      method: (init?.method ?? "GET").toUpperCase(),
      path: url.pathname,
      body: typeof init?.body === "string" ? init.body : "",
    });
    return respond(answer(url.pathname));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function openDialog(connection: LinkableConnection) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <LinkBridgeDialog connection={connection} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await page
    .getByRole("button", {
      name: i18n.t("accountLinks.bridge.start", { connection: connection.name }),
    })
    .click();
  return { queryClient, screen };
}

function posts(path: string) {
  return requests.filter((r) => r.method === "POST" && r.path === path);
}

const dialog = () => page.getByRole("dialog");
const handleField = (label = "Email") => dialog().getByLabelText(label, { exact: true });
const codeField = () => dialog().getByLabelText("Enter the code the bot sent you");

/** Opens Example Bridge on its code tab and sends a code request for the handle. */
async function requestCode(handle = "bob@example.org") {
  const opened = await openDialog(DEVB);
  await dialog().getByRole("tab", { name: "Code" }).click();
  await handleField().fill(handle);
  await dialog().getByRole("button", { name: "Send code" }).click();
  return opened;
}

async function enter(code: string) {
  await codeField().fill(code);
  await dialog().getByRole("button", { name: "Link", exact: true }).click();
}

/** A 422 refusal of one field. */
function field(pointer: string, code: string): ApiError {
  return new ApiError(422, { errors: [{ pointer, code, detail: code }] }, undefined);
}

describe("LinkBridgeDialog", () => {
  test("shows the token with a copy button, the instruction and its expiry, without a link or tabs", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await openDialog(BR);
    await expect.element(page.getByTestId("link-bridge-token")).toHaveTextContent(TOKEN);
    await expect
      .element(dialog().getByText("Send this token to the bot in a direct message."))
      .toBeVisible();
    await expect
      .element(page.getByTestId("link-bridge-expires"))
      .toMatchTextContent(/^This token works once and expires at \d{1,2}:\d{2}\.$/);
    expect(dialog().getByRole("link", { name: "Open the chat with the bot" }).query()).toBeNull();
    expect(dialog().getByRole("tablist").query()).toBeNull();
    expect(JSON.parse(posts(START)[0]?.body ?? "")).toEqual({ connection_id: BR.id });

    await dialog().getByRole("button", { name: "Copy" }).click();
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(TOKEN));
  });

  test("offers the bridge's link when it has a safe one", async () => {
    linkUrl = `https://chat.example.org/bot?start=${TOKEN}`;
    const { screen } = await openDialog(BR);
    const link = dialog().getByRole("link", { name: "Open the chat with the bot" });
    await expect.element(link).toHaveAttribute("href", linkUrl);
    await expect.element(link).toHaveAttribute("target", "_blank");
    await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
    await screen.unmount();

    linkUrl = "javascript:alert(1)";
    await openDialog(BR);
    await expect.element(page.getByTestId("link-bridge-token")).toHaveTextContent(TOKEN);
    expect(dialog().getByRole("link").query()).toBeNull();
  });

  test("closes when the hint names the Bridge link of this Connection", async () => {
    const { queryClient } = await openDialog(BR);
    await expect.element(page.getByTestId("link-bridge-token")).toBeVisible();

    // A Telegram link, a Bridge link of another Connection, and a hint without a link.
    links = [
      { ...BRIDGE_LINK, id: "AK0000000000T1", messenger: "telegram" },
      { ...BRIDGE_LINK, id: "AK0000000000B2", connection: { id: DEVB.id, name: DEVB.name } },
    ];
    applyHint(queryClient, { type: "account-links", id: "AK0000000000T1" });
    applyHint(queryClient, { type: "account-links", id: "AK0000000000B2" });
    applyHint(queryClient, { type: "account-links", id: null });
    await vi.waitFor(() => expect(listed).toBeGreaterThanOrEqual(1));
    await new Promise((resolve) => setTimeout(resolve, 100));
    await expect.element(page.getByTestId("link-bridge-token")).toBeVisible();

    links = [...links, BRIDGE_LINK];
    applyHint(queryClient, { type: "account-links", id: BRIDGE_LINK.id });
    await vi.waitFor(() => expect(dialog().query()).toBeNull());
  });

  test("offers the code only when the bridge does, its handle labelled by its kind", async () => {
    // Without a kind of handle there is no code, whatever the methods say.
    let opened = await openDialog({ ...DEVB, handle_kind: null });
    await expect.element(page.getByTestId("link-bridge-token")).toBeVisible();
    expect(dialog().getByRole("tablist").query()).toBeNull();
    await opened.screen.unmount();

    const kinds = [
      ["email", "Email"],
      ["login", "Login"],
      ["username", "Username"],
      ["user_id", "User id"],
    ] as const;
    for (const [kind, label] of kinds) {
      opened = await openDialog({ ...DEVB, handle_kind: kind });
      const tabs = dialog().getByRole("tab");
      await expect.element(tabs.nth(0)).toHaveTextContent("Token");
      await expect.element(tabs.nth(1)).toHaveTextContent("Code");
      await dialog().getByRole("tab", { name: "Code" }).click();
      await expect.element(handleField(label)).toBeVisible();
      await expect
        .element(dialog().getByText("The bot sends a code to this account in a direct message."))
        .toBeVisible();
      await opened.screen.unmount();
    }
  });

  test("shows the attempts left after the code request and after a wrong code, and links with the right one", async () => {
    answer = (path) => {
      if (path === CONFIRM) {
        return posts(CONFIRM).at(-1)?.body.includes('"code":"ABCDEFGH"') === true
          ? defaultAnswer(path)
          : wrongCode(4);
      }
      return defaultAnswer(path);
    };
    await requestCode(" bob@example.org ");
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("5 attempts left");
    expect(JSON.parse(posts(CODES)[0]?.body ?? "")).toEqual({
      connection_id: DEVB.id,
      handle: "bob@example.org",
    });

    await enter("22222222");
    await expect
      .element(page.getByTestId("link-bridge-code-error"))
      .toHaveTextContent("The code is wrong or no longer valid. Request a new code.");
    await expect.element(codeField()).toHaveAttribute("aria-invalid", "true");
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("4 attempts left");
    await expect.element(codeField()).toHaveValue("");

    await enter(" ABCDEFGH ");
    await vi.waitFor(() => expect(dialog().query()).toBeNull());
    expect(JSON.parse(posts(CONFIRM)[1]?.body ?? "")).toEqual({
      request_id: "LR000000000001",
      code: "ABCDEFGH",
    });
  });

  test("Request a new code asks again for the same handle and restores the attempts", async () => {
    answer = (path) => (path === CONFIRM ? wrongCode(0) : defaultAnswer(path));
    await requestCode();
    await enter("22222222");
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("0 attempts left");
    expect(codeField().query()).toBeNull();
    await expect
      .element(page.getByTestId("link-bridge-code-error"))
      .toHaveTextContent("The code is wrong or no longer valid. Request a new code.");

    await dialog().getByRole("button", { name: "Request a new code" }).click();
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("5 attempts left");
    expect(posts(CODES)).toHaveLength(2);
    expect(JSON.parse(posts(CODES)[1]?.body ?? "")).toEqual({
      connection_id: DEVB.id,
      handle: "bob@example.org",
    });
    await expect.element(codeField()).toBeVisible();
    expect(page.getByTestId("link-bridge-code-error").query()).toBeNull();
  });

  test("shows handle_not_found, direct_message_refused, code_not_offered and too many codes at the handle field", async () => {
    const refusals: [Answer, string][] = [
      [fieldProblem("/handle", "handle_not_found"), "No account with this email was found."],
      [
        fieldProblem("/handle", "direct_message_refused"),
        "The bot cannot send you a direct message. Send it the token instead.",
      ],
      [
        problem(409, { code: "code_not_offered" }),
        "This bridge sends no codes: send the token to the bot instead.",
      ],
      [
        problem(409, { code: "account_linked_elsewhere" }),
        "This account is linked to another user. Its owner or an admin can unlink it.",
      ],
      [
        problem(429, { code: "link_code_requests_exceeded", retry_after_seconds: 3600 }),
        "Too many codes were requested. Try again in 60 minutes.",
      ],
      [
        {
          ...problem(429, { code: "link_code_requests_exceeded" }),
          headers: { "Retry-After": "30" },
        },
        "Too many codes were requested. Try again in 1 minute.",
      ],
    ];
    let refusal: Answer = problem(500);
    answer = (path) => (path === CODES ? refusal : defaultAnswer(path));
    await requestCode();
    for (const [next, text] of refusals) {
      refusal = next;
      await dialog().getByRole("button", { name: "Send code" }).click();
      await expect.element(page.getByTestId("link-bridge-handle-error")).toHaveTextContent(text);
      await expect.element(handleField()).toHaveAttribute("aria-invalid", "true");
      await expect
        .element(handleField())
        .toHaveAccessibleDescription(
          `The bot sends a code to this account in a direct message. ${text}`,
        );
    }

    // A busy messenger is about the dialog, not the handle.
    refusal = problem(503);
    await dialog().getByRole("button", { name: "Send code" }).click();
    await expect
      .element(dialog().getByText("The messenger is busy. Try again in a few seconds."))
      .toBeVisible();
    expect(page.getByTestId("link-bridge-handle-error").query()).toBeNull();
  });

  test("an empty handle is refused before any request", async () => {
    await openDialog(DEVB);
    await dialog().getByRole("tab", { name: "Code" }).click();
    await handleField().fill("   ");
    await dialog().getByRole("button", { name: "Send code" }).click();
    await expect
      .element(page.getByTestId("link-bridge-handle-error"))
      .toHaveTextContent(i18n.t("fieldErrors.required"));
    expect(posts(CODES)).toHaveLength(0);
  });

  test("counts the handle in code points, as the API does", async () => {
    await openDialog(DEVB);
    await dialog().getByRole("tab", { name: "Code" }).click();
    await handleField().fill("a".repeat(321));
    await dialog().getByRole("button", { name: "Send code" }).click();
    await expect
      .element(page.getByTestId("link-bridge-handle-error"))
      .toHaveTextContent(i18n.t("fieldErrors.tooLong"));
    expect(posts(CODES)).toHaveLength(0);
    // 320 emoji are 640 UTF-16 units but 320 code points: within the limit.
    await handleField().fill("😀".repeat(320));
    await dialog().getByRole("button", { name: "Send code" }).click();
    await expect.poll(() => posts(CODES).length).toBe(1);
    expect(page.getByTestId("link-bridge-handle-error").query()).toBeNull();
  });

  test("names the kind and the refusals in Russian, with the plural forms", async () => {
    await i18n.changeLanguage("ru");
    const t = i18n.t;
    expect(handleKindLabel(t, "email")).toBe("Почта");
    expect(handleKindLabel(t, "login")).toBe("Логин");
    expect(handleKindLabel(t, "username")).toBe("Имя пользователя");
    expect(handleKindLabel(t, "user_id")).toBe("ID пользователя");
    expect(bridgeLinkProblem(t, field("/handle", "handle_not_found"), "email")).toEqual({
      at: "handle",
      text: "Аккаунт с такой почтой не найден.",
    });
    expect(bridgeLinkProblem(t, field("/handle", "handle_not_found"), "login").text).toBe(
      "Аккаунт с таким логином не найден.",
    );
    expect(bridgeLinkProblem(t, field("/handle", "direct_message_refused"), "email")).toEqual({
      at: "handle",
      text: "Бот не может написать вам в личные сообщения. Отправьте ему токен.",
    });
    expect(bridgeLinkProblem(t, field("/code", "link_code_invalid"), "email")).toEqual({
      at: "code",
      text: "Код неверный или больше не действует. Запросите новый код.",
    });
    expect(
      bridgeLinkProblem(t, new ApiError(409, { code: "code_not_offered" }, undefined), "email"),
    ).toEqual({ at: "handle", text: "Этот мост не присылает коды: отправьте боту токен." });
    const exceeded = (seconds: number) =>
      bridgeLinkProblem(
        t,
        new ApiError(429, { code: "link_code_requests_exceeded" }, seconds),
        "email",
      ).text;
    expect(exceeded(60)).toBe("Запрошено слишком много кодов. Попробуйте снова через 1 минуту.");
    expect(exceeded(120)).toBe("Запрошено слишком много кодов. Попробуйте снова через 2 минуты.");
    expect(exceeded(300)).toBe("Запрошено слишком много кодов. Попробуйте снова через 5 минут.");
    expect(t("accountLinks.bridge.attemptsLeft", { count: 1 })).toBe("Осталась 1 попытка");
    expect(t("accountLinks.bridge.attemptsLeft", { count: 3 })).toBe("Осталось 3 попытки");
    expect(t("accountLinks.bridge.attemptsLeft", { count: 5 })).toBe("Осталось 5 попыток");

    answer = (path) => (path === CONFIRM ? wrongCode(4) : defaultAnswer(path));
    await openDialog(DEVB);
    await expect
      .element(dialog().getByText("Отправьте этот токен боту в личном сообщении."))
      .toBeVisible();
    await dialog().getByRole("tab", { name: "Код" }).click();
    await handleField("Почта").fill("bob@example.org");
    await dialog().getByRole("button", { name: "Отправить код" }).click();
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("Осталось 5 попыток");
    await dialog().getByLabelText("Введите код, который прислал бот").fill("22222222");
    await dialog().getByRole("button", { name: "Привязать", exact: true }).click();
    await expect
      .element(page.getByTestId("link-bridge-attempts"))
      .toHaveTextContent("Осталось 4 попытки");
    await expect
      .element(page.getByTestId("link-bridge-code-error"))
      .toHaveTextContent("Код неверный или больше не действует. Запросите новый код.");
  });
});
