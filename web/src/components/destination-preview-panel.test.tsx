// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Preview panel of a Destination in a real browser: the Root message of a messenger in a frame with sandbox="",
// built from text — Telegram HTML keeps only the tags of its subset and no attribute, Mattermost Markdown and its
// attachment become elements — with links shown by their address and never as anchors, and the buttons drawn; an
// outgoing webhook shows each request with its method, URL, headers and body, and the error of a failing template.
// Nothing of the output runs: no script, event handler, image or link reaches the frame. A Bridge shows the envelope it
// would post, the item "message" in the format json, as formatted JSON, with the characters that Go escapes (<, > and
// &) as they are, instead of "Preview failed". Both in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  BridgeDestination,
  DestinationPreview,
  DestinationPreviewItem,
  Permission,
  Session,
  SlackDestination,
  TelegramDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import {
  DestinationPreviewPanel,
  frameDocument,
  mattermostNodes,
  slackMessageNodes,
  telegramButtons,
  telegramMessageNodes,
} from "./destination-preview-panel";
import { defaultMentions } from "./mention-settings";
import { EXAMPLE, type PickedSource, TestSourcePicker } from "./test-source-picker";

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

// An old-style post with a non-empty message, kept on purpose: the frame must still draw a message that has text.
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
      docs_version: "latest",
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

  test("draw only the attachment of a Mattermost post whose message is empty, as Muster posts it", () => {
    const body = (MATTERMOST_ITEM.request?.body ?? "").replace(
      '"message":"🔴 #1 HighErrorRate"',
      '"message":""',
    );
    expect(body).toContain('"message":""');
    const item: DestinationPreviewItem = {
      ...MATTERMOST_ITEM,
      text: "",
      request: {
        method: "POST",
        url: "http://127.0.0.1:18065/api/v4/posts",
        headers: [],
        body,
      },
    };
    const frame = frameBody(frameDocument("en", [], (doc) => mattermostNodes(doc, item)));
    expect(frame.children).toHaveLength(1);
    expect(frame.children[0]?.className).toContain("border-l-4");
    expect(frame.querySelector("b")?.textContent).toBe("#1 HighErrorRate");
    expect(frame.textContent).not.toContain("🔴");
  });

  test("read no buttons from a body that is not JSON", () => {
    expect(telegramButtons("not json")).toEqual([]);
    expect(telegramButtons(null)).toEqual([]);
    expect(telegramButtons('{"reply_markup":{"inline_keyboard":"x"}}')).toEqual([]);
  });
});

const SLACK_BODY = {
  channel: "C0FAKEALERTS",
  text: "🔴 #1 HighErrorRate",
  blocks: [
    {
      type: "section",
      block_id: "muster_summary",
      text: { type: "mrkdwn", text: "🔴 #1 HighErrorRate <script>alert(1)</script>" },
    },
  ],
  attachments: [
    {
      color: "#d32f2f",
      fallback: "🔴 #1 HighErrorRate",
      blocks: [
        {
          type: "section",
          block_id: "muster_heading",
          text: {
            type: "mrkdwn",
            text: "*<http://localhost:8080/alert-groups/AG1|#1 HighErrorRate>*",
          },
        },
        {
          type: "section",
          block_id: "muster_body",
          text: {
            type: "mrkdwn",
            text: "cluster: prod\nnote: &lt;!here&gt; <img src=x onerror=alert(1)>",
          },
        },
        {
          type: "context",
          block_id: "muster_context",
          elements: [{ type: "plain_text", text: "Muster v1.2.3" }],
        },
        {
          type: "actions",
          block_id: "muster_actions",
          elements: [
            {
              type: "button",
              action_id: "ack",
              text: { type: "plain_text", text: "Ack" },
              value: "[redacted]",
              style: "primary",
            },
            {
              type: "button",
              action_id: "resolve",
              text: { type: "plain_text", text: "Resolve" },
              value: "[redacted]",
            },
            {
              type: "static_select",
              action_id: "snooze",
              placeholder: { type: "plain_text", text: "Snooze…" },
              options: [{ text: { type: "plain_text", text: "1h" }, value: "[redacted]" }],
            },
          ],
        },
      ],
    },
  ],
  unfurl_links: false,
  unfurl_media: false,
};

const SLACK_ITEM: DestinationPreviewItem = {
  name: "message",
  format: "json",
  text: JSON.stringify(SLACK_BODY, null, 2),
  request: {
    method: "POST",
    url: "https://slack.com/api/chat.postMessage",
    headers: [
      { name: "Authorization", value: "Bearer [redacted]" },
      { name: "Content-Type", value: "application/json; charset=utf-8" },
    ],
    body: JSON.stringify(SLACK_BODY),
  },
};

const SLACK_DESTINATION: SlackDestination = {
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

describe("the frame of a Slack message", () => {
  test("draws the summary, the colour bar, the sections as plain text and the buttons, never as markup", () => {
    const html = frameDocument("en", [], (doc) => slackMessageNodes(doc, SLACK_ITEM));
    expectInert(html);
    const body = frameBody(html);
    expect(body.textContent).toContain("🔴 #1 HighErrorRate <script>alert(1)</script>");
    // mrkdwn is shown as Slack shows it, as text: the link as its label, the escaped Mention literally.
    expect(body.textContent).toContain("#1 HighErrorRate");
    expect(body.textContent).not.toContain("|#1 HighErrorRate>");
    expect(body.textContent).toContain("note: <!here> <img src=x onerror=alert(1)>");
    expect(body.textContent).toContain("Muster v1.2.3");
    const bar = body.querySelector(".border-l-4");
    expect(bar?.className).toContain("border-l-[#d32f2f]");
    const buttons = Array.from(body.querySelectorAll("button"));
    expect(buttons.map((b) => b.textContent)).toEqual(["Ack", "Resolve", "Snooze… ▾"]);
    expect(buttons.every((b) => b.disabled)).toBe(true);
    // Slack draws primary green.
    expect(buttons[0]?.className).toContain("bg-[#007a5a]");
    expect(buttons[1]?.className).not.toContain("bg-[#007a5a]");
  });

  test("shows the mrkdwn of a Root message as Slack does: links as labels, no bold markers, alert data literal", () => {
    // The shape of internal/slack/testdata/layout/root_firing_loud.json: the heading linked to the Alert Group, the body
    // with escaped alert data, whose `*` are followed by a zero-width space, and the line of links.
    const fixture = {
      ...SLACK_ITEM,
      text: JSON.stringify({
        text: "🔴 #12 DiskFull",
        blocks: [{ type: "section", text: { type: "mrkdwn", text: "🔴 #12 DiskFull" } }],
        attachments: [
          {
            color: "#d32f2f",
            blocks: [
              {
                type: "section",
                text: {
                  type: "mrkdwn",
                  text: "*<http://localhost:8080/alert-groups/AG0000000000A1|#12 DiskFull>*",
                },
              },
              {
                type: "section",
                text: {
                  type: "mrkdwn",
                  text: "note: @\u200bchannel &lt;!here&gt; &lt;b&gt;x&lt;/b&gt; *\u200by*\u200b &lt;https://evil.example|z&gt; &amp;\n<http://localhost:8080/alert-groups/AG0000000000A1|Open in Muster> · <https://runbooks.example.org/disk?a=1&amp;b=2|Runbook> · <https://example.org/plain>",
                },
              },
              {
                type: "context",
                elements: [{ type: "plain_text", text: "Muster *v1* &amp; <b>" }],
              },
            ],
          },
        ],
      }),
    };
    const html = frameDocument("en", [], (doc) => slackMessageNodes(doc, fixture));
    expectInert(html);
    const body = frameBody(html);
    const sections = Array.from(body.querySelectorAll(".border-l-4 > div")).map(
      (d) => d.textContent,
    );
    expect(sections[0]).toBe("#12 DiskFull");
    expect(sections[1]).toBe(
      "note: @\u200bchannel <!here> <b>x</b> *\u200by*\u200b <https://evil.example|z> &Open in Muster · Runbook · https://example.org/plain",
    );
    // A plain_text object is shown as it is.
    expect(sections[2]).toBe("Muster *v1* &amp; <b>");
    // Everything is text: the escaped Mention and markup are characters, never elements.
    expect(body.querySelectorAll("b, a, strong")).toHaveLength(0);
  });

  test("gives another colour a neutral bar, and shows the text of a body without blocks", () => {
    const other = { ...SLACK_ITEM, text: JSON.stringify({ text: "plain", attachments: [] }) };
    expect(
      frameBody(frameDocument("en", [], (doc) => slackMessageNodes(doc, other))).textContent,
    ).toBe("plain");
    const odd = {
      ...SLACK_ITEM,
      text: JSON.stringify({ attachments: [{ color: "#123456", blocks: [] }] }),
    };
    const body = frameBody(frameDocument("en", [], (doc) => slackMessageNodes(doc, odd)));
    expect(body.querySelector(".border-l-4")?.className).toContain("border-l-muted-foreground");
  });

  test("shows a Slack Destination's message in the sandboxed frame, with its request", async () => {
    fakeApi({ items: [SLACK_ITEM] });
    await render(
      providers(
        <DestinationPreviewPanel
          destination={SLACK_DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    const frame = page.getByTitle("Root message in slack-alerts");
    await expect.element(frame).toBeVisible();
    const el = frame.element();
    expect(el.getAttribute("sandbox")).toBe("");
    expectInert(el.getAttribute("srcdoc") ?? "");
    await expect.element(page.getByText("Request", { exact: true })).toBeVisible();
  });
});

/** The panel with its source selector and a gone Alert Group chosen, as the Destination page shows them. */
function WithPicker() {
  const [source, setSource] = useState<PickedSource>({
    kind: "alert_group",
    id: "AG0000000000K1",
    number: 12,
    title: "DiskFull",
  });
  const [gone, setGone] = useState(false);
  return (
    <>
      <TestSourcePicker
        routeIds={[]}
        value={source}
        onChange={(picked) => {
          setSource(picked);
          setGone(false);
        }}
        gone={gone}
      />
      <DestinationPreviewPanel
        destination={DESTINATION}
        source={source}
        onSourceGone={() => {
          setSource(EXAMPLE);
          setGone(true);
        }}
      />
    </>
  );
}

const BRIDGE_DESTINATION: BridgeDestination = {
  id: ID,
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

/** The envelope as the server sends it: indented JSON, with Secrets masked. */
const ENVELOPE = JSON.stringify(
  {
    kind: "test",
    status: "firing",
    color: "#d32f2f",
    emoji: "🔴",
    title: "#0 🧪 Test message",
    link: "http://localhost:8080/alert-groups/AG0",
    body: "<script>alert(1)</script> **Severity:** critical",
    buttons: [{ label: "Ack", style: "primary", data: "[masked]" }],
    notify: true,
  },
  null,
  2,
);

describe("DestinationPreviewPanel", () => {
  test("shows a Bridge envelope as formatted JSON", async () => {
    const bodies = fakeApi({ items: [{ name: "message", format: "json", text: ENVELOPE }] });
    await render(
      providers(
        <DestinationPreviewPanel
          destination={BRIDGE_DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Root message" })).toBeVisible();
    const envelope = page.getByTestId("preview-envelope");
    await expect.element(envelope).toBeVisible();
    expect(envelope.element().textContent).toBe(ENVELOPE);
    expect(envelope.element().textContent).toContain('"kind": "test"');
    // Text only: the body's markup is never parsed.
    expect(envelope.element().querySelector("script")).toBeNull();
    await expect
      .element(
        page.getByText(
          "The envelope Muster would post to the bridge, as JSON; the bridge renders it in its messenger's markup.",
        ),
      )
      .toBeVisible();
    expect(document.querySelector('[data-testid="preview-item-error"]')).toBeNull();
    expect(document.querySelector('[data-testid="preview-frame"]')).toBeNull();
    expect(document.body.textContent).not.toContain("The template of this request failed:");
    expect(JSON.parse(bodies[0] ?? "")).toEqual({ source: { kind: "example" } });
  });

  test("shows the characters Go's indented JSON escapes in a Bridge envelope as they are", async () => {
    // json.MarshalIndent writes <, > and & as <, > and &.
    const escaped = [
      "{",
      '  "kind": "test",',
      '  "title": "#0 cpu \\u003e 90% \\u0026 rising",',
      '  "body": "\\u003cb\\u003eload\\u003c/b\\u003e"',
      "}",
    ].join("\n");
    fakeApi({ items: [{ name: "message", format: "json", text: escaped }] });
    await render(
      providers(
        <DestinationPreviewPanel
          destination={BRIDGE_DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    const envelope = page.getByTestId("preview-envelope");
    await expect.element(envelope).toBeVisible();
    expect(envelope.element().textContent).toBe(
      '{\n  "kind": "test",\n  "title": "#0 cpu > 90% & rising",\n  "body": "<b>load</b>"\n}',
    );
    expect(envelope.element().querySelector("b")).toBeNull();
  });

  test("shows a compact Bridge envelope indented, and its hint in Russian", async () => {
    await i18n.changeLanguage("ru");
    fakeApi({
      items: [{ name: "message", format: "json", text: '{"kind":"test","title":"#0 x"}' }],
    });
    await render(
      providers(
        <DestinationPreviewPanel
          destination={BRIDGE_DESTINATION}
          source={EXAMPLE}
          onSourceGone={() => undefined}
        />,
      ),
    );
    await expect.element(page.getByRole("heading", { name: "Корневое сообщение" })).toBeVisible();
    await expect.element(page.getByTestId("preview-envelope")).toBeVisible();
    expect(page.getByTestId("preview-envelope").element().textContent).toBe(
      '{\n  "kind": "test",\n  "title": "#0 x"\n}',
    );
    await expect
      .element(
        page.getByText(
          "Конверт, который Muster отправил бы мосту, в формате JSON; мост сам переводит его в разметку своего мессенджера.",
        ),
      )
      .toBeVisible();
  });

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

  test("reads in Russian, and returns to the example and says why when the Alert Group is gone", async () => {
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
    const sources: unknown[] = [];
    vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url === `/api/v1/destinations/${ID}/previews` && init?.method === "POST") {
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the body the panel sent
        const { source } = JSON.parse(typeof init.body === "string" ? init.body : "{}") as {
          source: { kind: string };
        };
        sources.push(source);
        return source.kind === "example"
          ? json(200, WEBHOOK_PREVIEW)
          : json(422, {
              title: "Validation failed",
              status: 422,
              errors: [{ pointer: "/source/alert_group_id", code: "unknown_id" }],
            });
      }
      return json(404, { title: "not mocked" });
    });
    await render(providers(<WithPicker />));
    const gone = page.getByRole("alert");
    await expect
      .element(gone)
      .toHaveTextContent(
        "This Alert Group is no longer one of the recent Alert Groups of this Destination's Routes. Choose another one.",
      );
    const select = page.getByLabelText("Based on");
    await expect.element(select).toHaveDisplayValue("Example");
    // The example is rendered, and the selector still says why it returned to it; the panel does not repeat it.
    await expect.element(page.getByRole("heading", { name: "Reply in thread" })).toBeVisible();
    expect(sources).toEqual([
      { kind: "alert_group", alert_group_id: "AG0000000000K1" },
      { kind: "example" },
    ]);
    await expect.element(gone).toBeVisible();
    expect(document.querySelector('[data-testid="preview-error"]')).toBeNull();
    expect(document.querySelectorAll('[data-testid="test-source-gone"]')).toHaveLength(1);
  });
});
