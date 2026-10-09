// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Preview panel of a Destination in a real browser: the Root message of a messenger in a frame with sandbox="",
// built from text — Telegram HTML keeps only the tags of its subset and no attribute, Mattermost Markdown and its
// attachment become elements — with links shown by their address and never as anchors, and the buttons drawn; an
// outgoing webhook shows each request with its method, URL, headers and body, and the error of a failing template.
// Nothing of the output runs: no script, event handler, image or link reaches the frame. Both in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DestinationPreview,
  DestinationPreviewItem,
  Permission,
  Session,
  TelegramDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import {
  DestinationPreviewPanel,
  frameDocument,
  mattermostNodes,
  telegramButtons,
  telegramMessageNodes,
} from "./destination-preview-panel";
import { defaultMentions } from "./mention-settings";
import { EXAMPLE } from "./test-source-picker";

const ID = "DS0000000000TG";

const DESTINATION: TelegramDestination = {
  id: ID,
  type: "telegram",
  name: "tg-alerts",
  connection_id: "CN0000000000TG",
  channel_id: "-1001000000001",
  mentions: defaultMentions(),
  limiter: { limit: 30, per_seconds: 1 },
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"2"',
};

const TELEGRAM_TEXT =
  '🔴 <b><a href="http://localhost:8080/alert-groups/AG1">#1 HighErrorRate</a></b>\n' +
  "prod · Started 2026-10-09 22:40 UTC\n" +
  "<blockquote expandable>cluster: prod\nnamespace: shop</blockquote>\n" +
  '<i>Error rate above 5%</i><script>alert(1)</script><img src="x" onerror="alert(2)">' +
  '<b onclick="alert(3)">x</b><a href="javascript:alert(4)">bad</a><tg-spoiler>secret</tg-spoiler>';

const TELEGRAM_BODY = JSON.stringify({
  chat_id: -1001000000001,
  text: TELEGRAM_TEXT,
  reply_markup: {
    inline_keyboard: [
      [{ text: "Ack" }, { text: "Resolve" }],
      [{ text: "Snooze 1 h" }, { text: "Snooze 4 h" }],
    ],
  },
});

const TELEGRAM_ITEM: DestinationPreviewItem = {
  name: "message",
  format: "html",
  text: TELEGRAM_TEXT,
  request: {
    method: "POST",
    url: "http://127.0.0.1:18081/bot[redacted]/sendMessage",
    headers: [{ name: "Content-Type", value: "application/json" }],
    body: TELEGRAM_BODY,
  },
};

const MATTERMOST_ITEM: DestinationPreviewItem = {
  name: "message",
  format: "markdown",
  text: "🔴 #1 HighErrorRate <script>alert(1)</script>",
  request: {
    method: "POST",
    url: "http://127.0.0.1:18065/api/v4/posts",
    headers: [{ name: "Authorization", value: "Bearer [redacted]" }],
    body: JSON.stringify({
      message: "🔴 #1 HighErrorRate",
      props: {
        attachments: [
          {
            title: "#1 HighErrorRate",
            title_link: "http://localhost:8080/alert-groups/AG1",
            text: "prod · **Started**\n- pod: checkout-1\n[Runbook](https://runbooks.example.org/x) <img src=x onerror=alert(1)>",
            fields: [{ title: "cluster", value: "`prod`", short: true }],
            footer: "Muster",
            footer_icon: "http://localhost:8080/muster-mark-256.png",
            actions: [
              { id: "ack", name: "Ack" },
              { id: "resolve", name: "Resolve" },
            ],
          },
        ],
      },
    }),
  },
};

const WEBHOOK_PREVIEW: DestinationPreview = {
  items: [
    {
      name: "create",
      format: "json",
      request: {
        method: "POST",
        url: "http://127.0.0.1:18093/chat/s048/messages",
        headers: [{ name: "Authorization", value: "Bearer [redacted]" }],
        body: '{"text":"#1 HighErrorRate (firing)"}',
      },
    },
    {
      name: "update",
      format: "json",
      request: {
        method: "PUT",
        url: "http://127.0.0.1:18093/chat/s048/messages/example-id",
        headers: [],
        body: '{"text":"#1 HighErrorRate (firing)"}',
      },
    },
    {
      name: "open_thread",
      format: "plain",
      text: 'template: open_thread:1: function "nope" not defined',
    },
    {
      name: "reply_in_thread",
      format: "json",
      request: {
        method: "POST",
        url: "http://127.0.0.1:18093/chat/s048/threads/example-thread/messages",
        headers: [],
        body: null,
      },
    },
  ],
};

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
  const permissions: Permission[] = ["destinations:read", "destinations:test"];
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the panel reads only the Permissions
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
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function fakeApi(preview: DestinationPreview | Response): string[] {
  const bodies: string[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url === `/api/v1/destinations/${ID}/previews` && init?.method === "POST") {
      bodies.push(typeof init.body === "string" ? init.body : "");
      return preview instanceof Response ? preview : json(200, preview);
    }
    return json(404, { title: "not mocked" });
  });
  return bodies;
}

/** The body of a frame document, parsed only to read it. */
function frameBody(html: string): HTMLElement {
  return new DOMParser().parseFromString(html, "text/html").body;
}

/** What no frame may hold: anything that runs, loads or navigates. */
function expectInert(html: string): void {
  const body = frameBody(html);
  expect(body.querySelectorAll("script, img, a, iframe, object, embed, style, link")).toHaveLength(
    0,
  );
  for (const el of body.querySelectorAll("*")) {
    for (const attr of el.getAttributeNames()) {
      expect(["class", "type", "disabled", "open"]).toContain(attr);
    }
  }
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("frame documents", () => {
  test("keep the Telegram subset without attributes, show links by their address and draw the buttons", () => {
    const html = frameDocument("en", ["/assets/index.css"], (doc) =>
      telegramMessageNodes(doc, TELEGRAM_ITEM, { expandable: "Expandable quote" }),
    );
    expectInert(html);
    expect(html).toContain('<link rel="stylesheet" href="/assets/index.css">');
    const body = frameBody(html);
    expect(body.querySelector("b")?.textContent).toBe(
      "#1 HighErrorRate (http://localhost:8080/alert-groups/AG1)",
    );
    expect(body.querySelector("i")?.textContent).toBe("Error rate above 5%");
    const details = body.querySelector("details");
    expect(details?.open).toBe(true);
    expect(details?.querySelector("summary")?.textContent).toBe("Expandable quote");
    expect(details?.querySelector("blockquote")?.innerHTML).toBe(
      "cluster: prod<br>namespace: shop",
    );
    expect(body.textContent).toContain("bad (javascript:alert(4))");
    expect(body.textContent).not.toContain("alert(1)");
    expect(body.textContent).toContain("secret");
    expect(Array.from(body.querySelectorAll("button")).map((b) => b.textContent)).toEqual([
      "Ack",
      "Resolve",
      "Snooze 1 h",
      "Snooze 4 h",
    ]);
    expect(Array.from(body.querySelectorAll("button")).every((b) => b.disabled)).toBe(true);
  });

  test("render Mattermost Markdown and the attachment as elements, never as markup", () => {
    const html = frameDocument("en", [], (doc) => mattermostNodes(doc, MATTERMOST_ITEM));
    expectInert(html);
    const body = frameBody(html);
    expect(body.textContent).toContain("🔴 #1 HighErrorRate <script>alert(1)</script>");
    expect(body.textContent).toContain("#1 HighErrorRate (http://localhost:8080/alert-groups/AG1)");
    expect(body.querySelector("strong")?.textContent).toBe("Started");
    expect(body.querySelector("li")?.textContent).toBe("pod: checkout-1");
    expect(body.textContent).toContain(
      "Runbook (https://runbooks.example.org/x) <img src=x onerror=alert(1)>",
    );
    expect(body.querySelector("dt")?.textContent).toBe("cluster");
    expect(body.querySelector("dd code")?.textContent).toBe("prod");
    expect(Array.from(body.querySelectorAll("button")).map((b) => b.textContent)).toEqual([
      "Ack",
      "Resolve",
    ]);
    expect(body.textContent).toContain("Muster");
  });

  test("read no buttons from a body that is not JSON", () => {
    expect(telegramButtons("not json")).toEqual([]);
    expect(telegramButtons(null)).toEqual([]);
    expect(telegramButtons('{"reply_markup":{"inline_keyboard":"x"}}')).toEqual([]);
  });
});

describe("DestinationPreviewPanel", () => {
  test('renders the Root message in a frame with sandbox="" without sending anything', async () => {
    const bodies = fakeApi({ items: [TELEGRAM_ITEM] });
    await render(
      providers(
        <DestinationPreviewPanel
          destination={DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    const frame = page.getByTitle("Root message in tg-alerts");
    await expect.element(frame).toBeVisible();
    await expect.element(page.getByRole("heading", { name: "Root message" })).toBeVisible();
    const el = frame.element();
    expect(el.getAttribute("sandbox")).toBe("");
    const srcdoc = el.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toContain("HighErrorRate");
    expectInert(srcdoc);
    expect(JSON.parse(bodies[0] ?? "")).toEqual({ source: { kind: "example" } });
    await expect
      .element(
        page.getByText(
          "What this Destination would receive, with Secrets masked. Nothing is sent, and links are not followed.",
        ),
      )
      .toBeVisible();
  });

  test("shows each webhook request with its method, URL, headers and body, and a failing template", async () => {
    fakeApi(WEBHOOK_PREVIEW);
    await render(
      providers(
        <DestinationPreviewPanel
          destination={DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Reply in thread" })).toBeVisible();
    expect(
      Array.from(document.querySelectorAll('[data-testid="preview-item"] h3')).map(
        (h) => h.textContent,
      ),
    ).toEqual(["Create", "Update", "Open thread", "Reply in thread"]);
    expect(
      Array.from(document.querySelectorAll('[data-testid="rendered-request-line"]')).map(
        (p) => p.textContent,
      ),
    ).toEqual([
      "POST http://127.0.0.1:18093/chat/s048/messages",
      "PUT http://127.0.0.1:18093/chat/s048/messages/example-id",
      "POST http://127.0.0.1:18093/chat/s048/threads/example-thread/messages",
    ]);
    await expect
      .element(page.getByTestId("rendered-header"))
      .toHaveTextContent("Authorization: Bearer [redacted]");
    await expect.element(page.getByText("No body.")).toBeVisible();
    const failed = page.getByTestId("preview-item-error");
    await expect.element(failed.getByText("The template of this request failed:")).toBeVisible();
    await expect
      .element(failed.getByText('template: open_thread:1: function "nope" not defined'))
      .toBeVisible();
    expect(document.querySelector("iframe")).toBeNull();
  });

  test("reads in Russian, and returns to the example when the Alert Group is gone", async () => {
    await i18n.changeLanguage("ru");
    fakeApi(WEBHOOK_PREVIEW);
    const screen = await render(
      providers(
        <DestinationPreviewPanel
          destination={DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Ответ в треде" })).toBeVisible();
    expect(
      Array.from(document.querySelectorAll('[data-testid="preview-item"] h3')).map(
        (h) => h.textContent,
      ),
    ).toEqual(["Создание", "Обновление", "Открытие треда", "Ответ в треде"]);
    await screen.unmount();
    vi.restoreAllMocks();
    await i18n.changeLanguage("en");
    fakeApi(
      json(422, {
        title: "Validation failed",
        status: 422,
        errors: [{ pointer: "/source/alert_group_id", code: "unknown_id" }],
      }),
    );
    const gone = vi.fn();
    await render(
      providers(
        <DestinationPreviewPanel
          destination={DESTINATION}
          source={{ kind: "alert_group", id: "AG1", number: 1, title: "x" }}
          onSourceGone={gone}
        />,
      ),
    );
    await expect
      .element(page.getByTestId("preview-error"))
      .toHaveTextContent(
        "This Alert Group is no longer one of the recent Alert Groups of this Destination's Routes. Choose another one.",
      );
    expect(gone).toHaveBeenCalled();
  });
});
