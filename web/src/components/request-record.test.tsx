// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A record of the Request log in a real browser (C-23.FR-6, AC-6): the request and the answer with their headers as two
// columns and their bodies, a JSON body indented — also one whose characters are escapes — and any other as it is, a
// cut body with its full size rounded up, an inbound body that was not kept, the kind of check, latency, path and
// attempt, the links to the Destination, the Alert Group and the requests of its check, the copy buttons, also on a
// plain-http installation, and the time in the user's time zone. Headers, bodies and URLs are text: markup in them is
// shown, never rendered, and [redacted] stays as it is. A record of a Connection that the reader may not open says why.
// The request and the answer stand side by side from 1024 px. English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, RequestRecord, RequestRecordSummary, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
// The dialog is laid out by its classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { RequestRecordDialog } from "./request-record";

const ALL: Permission[] = ["connections:read", "destinations:read", "alert-groups:read"];

const SUMMARY: RequestRecordSummary = {
  id: "RQ0000000000AA",
  at: "2026-10-18T06:00:12Z",
  direction: "outbound",
  operation: "createPost",
  method: "POST",
  url: "http://127.0.0.1:18065/api/v4/posts",
  status: 201,
  latency_ms: 12,
  via: "direct",
  attempt: 1,
  error_class: null,
  error_code: null,
  check: "connection_check",
  check_run: "CR0000000000R1",
  connection_id: "CN0000000000MM",
  destination: {
    id: "DS0000000000AA",
    name: "alerts",
    type: "mattermost",
    health: { state: "healthy" },
  },
  alert_group: { id: "AG0000000000AA", number: 42 },
  request_body_truncated: false,
  response_body_truncated: false,
};

const BODY = '{"channel_id":"ch-alerts","message":"Disk full"}';

const RECORD: RequestRecord = {
  ...SUMMARY,
  request_headers: {
    Authorization: ["[redacted]"],
    "Content-Type": ["application/json"],
  },
  request_body: BODY,
  request_body_bytes: BODY.length,
  response_headers: { "Content-Type": ["text/plain"] },
  response_body: "created",
  response_body_bytes: 7,
  error: null,
};

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status < 400 ? "application/json" : "application/problem+json",
    },
  });
}

/** Answers the read of the record, and counts the reads. */
function serve(answer: () => Response): { reads: () => number } {
  let reads = 0;
  vi.spyOn(window, "fetch").mockImplementation(async (input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/request-log/")) {
      reads++;
      return answer();
    }
    return json(404, { title: "not mocked", status: 404 });
  });
  return { reads: () => reads };
}

function Harness({ summary, currentRun }: { summary: RequestRecordSummary; currentRun?: string }) {
  const [open, setOpen] = useState<RequestRecordSummary | null>(summary);
  return (
    <RequestRecordDialog summary={open} onClose={() => setOpen(null)} currentRun={currentRun} />
  );
}

async function renderRecord(
  summary: RequestRecordSummary = SUMMARY,
  { permissions = ALL, currentRun }: { permissions?: Permission[]; currentRun?: string } = {},
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the record reads only the time zone
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "Asia/Tokyo" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-19T09:00:00Z",
      idle_expires_at: "2026-10-19T09:00:00Z",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const rootRoute = createRootRoute({
    component: () => <Harness summary={summary} currentRun={currentRun} />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

function dialog() {
  return page.getByTestId("request-record");
}

/** The facts of the record, each label with its value. */
function facts(): string[][] {
  const list = page.getByTestId("record-facts").element();
  return Array.from(list.children).map((row) =>
    Array.from(row.children).map((c) => c.textContent ?? ""),
  );
}

function headerRows(): string[] {
  return page
    .getByTestId("record-header")
    .elements()
    .map((row) =>
      Array.from(row.children)
        .map((c) => c.textContent ?? "")
        .join(" | "),
    );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await page.viewport(414, 896);
  await i18n.changeLanguage("en");
});

describe("the record of a request", () => {
  test("shows the request and the answer, the JSON body indented and plain text as it is", async () => {
    serve(() => json(200, RECORD));
    await renderRecord();
    await expect.element(dialog().getByRole("img", { name: "Sent" })).toHaveTextContent("→");
    await expect
      .element(dialog().getByRole("heading", { level: 2 }))
      .toHaveTextContent("→createPost");
    // The time in the user's time zone, Tokyo, with its seconds.
    await expect.element(dialog().getByText("Oct 18, 2026, 15:00:12")).toBeVisible();
    await expect
      .element(page.getByTestId("record-request-line"))
      .toHaveTextContent("POST http://127.0.0.1:18065/api/v4/posts");
    expect(headerRows()).toEqual([
      "Authorization | [redacted]",
      "Content-Type | application/json",
      "Content-Type | text/plain",
    ]);
    // The headers are two columns: each name beside its value.
    const row = page.getByTestId("record-header").first().element();
    const [name, value] = Array.from(row.children).map((c) => c.getBoundingClientRect());
    expect(value?.left).toBeGreaterThan(name?.right ?? Number.POSITIVE_INFINITY);
    expect(value?.top).toBe(name?.top);
    expect(page.getByTestId("record-request-body").element().textContent).toBe(
      '{\n  "channel_id": "ch-alerts",\n  "message": "Disk full"\n}',
    );
    await expect.element(page.getByTestId("record-status")).toHaveTextContent("201");
    expect(page.getByTestId("record-response-body").element().textContent).toBe("created");
    expect(facts()).toEqual([
      ["Direction", "Sent"],
      ["Latency", "12 ms"],
      ["Path", "Direct"],
      ["Attempt", "1"],
      ["Kind of check", "Connection checkRequests of this check"],
      ["Destination", "alerts"],
      ["Alert Group", "#42"],
    ]);
    await expect.element(page.getByTestId("record-check")).toHaveTextContent("Connection check");
  });

  test("indents JSON whose characters are escapes, keeps an escaped backslash, and leaves changed numbers as they are", async () => {
    // A messenger may escape every character beyond ASCII.
    const telegram = String.raw`{"ok":true,"result":{"text":"\u041f\u0440\u0438\u0432\u0435\u0442 \ud83d\ude00","url":"https:\/\/t.me\/x"}}`;
    let record: RequestRecord = { ...RECORD, response_body: telegram };
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    serve(() => json(200, record));
    const screen = await renderRecord();
    const answer = page.getByRole("region", { name: "Answer", exact: true });
    await expect.element(answer.getByTestId("record-response-body")).toBeVisible();
    expect(page.getByTestId("record-response-body").element().textContent).toBe(
      '{\n  "ok": true,\n  "result": {\n    "text": "Привет 😀",\n    "url": "https://t.me/x"\n  }\n}',
    );
    await expect.element(answer.getByText("Привет", { exact: false })).toBeVisible();
    // Copying copies the body as it was recorded.
    await answer.getByRole("button", { name: "Copy body" }).click();
    expect(writeText).toHaveBeenLastCalledWith(telegram);
    await screen.unmount();

    // An escaped backslash before u0041 is a backslash and the text u0041, not the letter A.
    record = { ...RECORD, response_body: String.raw`{"a":"\\u0041","b":"\u00e9"}` };
    const backslash = await renderRecord();
    await expect.element(page.getByTestId("record-response-body")).toBeVisible();
    expect(page.getByTestId("record-response-body").element().textContent).toBe(
      String.raw`{
  "a": "\\u0041",
  "b": "é"
}`,
    );
    await backslash.unmount();

    // Numbers that reading would change, and characters that cannot be shown, leave the body as it is.
    for (const body of [
      '{"id":12345678901234567890}',
      String.raw`{"id":12345678901234567890,"text":"\u00e9"}`,
      '{"value":1.0}',
      String.raw`{"text":"\u202e"}`,
    ]) {
      record = { ...RECORD, response_body: body };
      const raw = await renderRecord();
      await expect.element(page.getByTestId("record-response-body")).toBeVisible();
      expect(page.getByTestId("record-response-body").element().textContent).toBe(body);
      await raw.unmount();
    }
  });

  test("copies the body as it was recorded on a plain-http installation", async () => {
    const secure = Object.getOwnPropertyDescriptor(window, "isSecureContext");
    Object.defineProperty(window, "isSecureContext", { value: false, configurable: true });
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    // What the browser's copy command would copy: the selection of the focused field.
    const copied: string[] = [];
    vi.spyOn(document, "execCommand").mockImplementation((command) => {
      const field = document.activeElement;
      copied.push(
        command === "copy" && field instanceof HTMLTextAreaElement
          ? field.value.slice(field.selectionStart, field.selectionEnd)
          : "",
      );
      return true;
    });
    try {
      const body = String.raw`{"text":"\u041f\u0440\u0438\u0432\u0435\u0442"}`;
      serve(() => json(200, { ...RECORD, request_body: body }));
      await renderRecord();
      const request = page.getByRole("region", { name: "Request", exact: true });
      await expect.element(request.getByTestId("record-request-body")).toBeVisible();
      expect(request.getByTestId("record-request-body").element().textContent).toBe(
        '{\n  "text": "Привет"\n}',
      );
      await request.getByRole("button", { name: "Copy body" }).click();
      await expect.element(request.getByRole("button", { name: "Copied" })).toBeVisible();
      expect(copied).toEqual([body]);
      await request.getByRole("button", { name: "Copy URL" }).click();
      expect(copied).toEqual([body, "http://127.0.0.1:18065/api/v4/posts"]);
      expect(writeText).not.toHaveBeenCalled();
      // The hidden field is gone.
      expect(document.querySelectorAll("body > textarea")).toHaveLength(0);
    } finally {
      if (secure === undefined) {
        Reflect.deleteProperty(window, "isSecureContext");
      } else {
        Object.defineProperty(window, "isSecureContext", secure);
      }
    }
  });

  test("marks a cut body with its full size, an inbound body that was not kept, and a request without an answer", async () => {
    const large = "x".repeat(64 * 1024);
    let record: RequestRecord = {
      ...RECORD,
      request_body: large,
      request_body_bytes: 40 * 1024,
      request_body_truncated: true,
    };
    serve(() => json(200, record));
    const screen = await renderRecord({ ...SUMMARY, request_body_truncated: true });
    await expect
      .element(page.getByTestId("record-request-body-cut"))
      .toHaveTextContent("Cut at 16 KB of 40 KB");
    // A large body is one text node in a block that scrolls, never a node per line.
    const code = page.getByTestId("record-request-body").element().querySelector("code");
    expect(code?.childNodes).toHaveLength(1);
    expect(code?.textContent).toHaveLength(large.length);
    const pre = page.getByTestId("record-request-body").element();
    expect(getComputedStyle(pre).overflowY).toBe("auto");
    await screen.unmount();

    // The size is rounded up, so that a body a little over 16 KB never reads as 16 KB.
    record = { ...record, request_body: "x".repeat(16 * 1024), request_body_bytes: 16_400 };
    const over = await renderRecord({ ...SUMMARY, request_body_truncated: true });
    await expect
      .element(page.getByTestId("record-request-body-cut"))
      .toHaveTextContent("Cut at 16 KB of 16.1 KB");
    await over.unmount();

    record = {
      ...RECORD,
      id: "RQ0000000000AB",
      direction: "inbound",
      operation: "press",
      method: "POST",
      url: "http://127.0.0.1:8081/callbacks/mattermost/[redacted]",
      request_body: "",
      request_body_bytes: 300 * 1024,
      request_body_truncated: true,
    };
    const inbound = await renderRecord({ ...record, check: null, check_run: null });
    await expect.element(dialog().getByRole("img", { name: "Received" })).toHaveTextContent("←");
    await expect
      .element(page.getByTestId("record-request-body"))
      .toHaveTextContent("Not kept: 300 KB, too large or not read to the end");
    await inbound.unmount();

    record = {
      ...RECORD,
      id: "RQ0000000000AC",
      status: null,
      error_class: "transient",
      error_code: "timeout",
      response_headers: null,
      response_body: null,
      response_body_bytes: 0,
      error: "Post http://127.0.0.1:18065/api/v4/posts: context deadline exceeded",
    };
    await renderRecord({ ...SUMMARY, ...record });
    await expect.element(page.getByTestId("record-status")).toHaveTextContent("No answer.");
    await expect.poll(facts).toContainEqual(["Error class", "No answer or a server error"]);
    expect(facts()).toContainEqual(["What failed", "No answer in time."]);
    await expect
      .element(page.getByTestId("record-error-text"))
      .toHaveTextContent("Post http://127.0.0.1:18065/api/v4/posts: context deadline exceeded");
  });

  test("shows headers, bodies and URLs as text, never as markup", async () => {
    serve(() =>
      json(200, {
        ...RECORD,
        url: "http://example.org/<img src=x onerror=alert(1)>",
        request_headers: { "X-Test": ["<b>bold</b>"], Authorization: ["[redacted]"] },
        request_body: "**not markdown** <img src=x onerror=alert(2)>",
        response_headers: { "X-Answer": ["<i>it</i>"] },
        response_body: "<script>alert(3)</script><strong>no</strong>",
        error: "<em>error</em>",
      }),
    );
    await renderRecord();
    await expect
      .element(page.getByTestId("record-request-line"))
      .toHaveTextContent("POST http://example.org/<img src=x onerror=alert(1)>");
    expect(headerRows()).toContain("X-Test | <b>bold</b>");
    expect(headerRows()).toContain("Authorization | [redacted]");
    expect(headerRows()).toContain("X-Answer | <i>it</i>");
    await expect
      .element(page.getByTestId("record-request-body"))
      .toHaveTextContent("**not markdown** <img src=x onerror=alert(2)>");
    await expect
      .element(page.getByTestId("record-response-body"))
      .toHaveTextContent("<script>alert(3)</script><strong>no</strong>");
    await expect.element(page.getByTestId("record-error-text")).toHaveTextContent("<em>error</em>");
    for (const tag of ["b", "i", "img", "script", "strong", "em"]) {
      expect(document.querySelector(`[data-testid="request-record"] ${tag}`)).toBeNull();
    }
  });

  test("copies the URL, the headers and each body as they are, and says Copied", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    serve(() => json(200, RECORD));
    await renderRecord();
    const request = page.getByRole("region", { name: "Request", exact: true });
    const answer = page.getByRole("region", { name: "Answer", exact: true });
    await request.getByRole("button", { name: "Copy body" }).click();
    expect(writeText).toHaveBeenLastCalledWith(BODY);
    await expect.element(request.getByRole("button", { name: "Copied" })).toBeVisible();
    await request.getByRole("button", { name: "Copy URL" }).click();
    expect(writeText).toHaveBeenLastCalledWith("http://127.0.0.1:18065/api/v4/posts");
    await request.getByRole("button", { name: "Copy headers" }).click();
    expect(writeText).toHaveBeenLastCalledWith(
      "Authorization: [redacted]\nContent-Type: application/json",
    );
    await answer.getByRole("button", { name: "Copy body" }).click();
    expect(writeText).toHaveBeenLastCalledWith("created");
    await answer.getByRole("button", { name: "Copy headers" }).click();
    expect(writeText).toHaveBeenLastCalledWith("Content-Type: text/plain");
  });

  test("links to the Destination, the Alert Group and the requests of its check", async () => {
    serve(() => json(200, RECORD));
    const screen = await renderRecord();
    await expect
      .element(dialog().getByRole("link", { name: "alerts" }))
      .toHaveAttribute("href", "/destinations/DS0000000000AA");
    await expect
      .element(dialog().getByRole("link", { name: "#42" }))
      .toHaveAttribute("href", "/alert-groups/AG0000000000AA");
    await expect
      .element(dialog().getByRole("link", { name: "Requests of this check" }))
      .toHaveAttribute("href", "/connections/CN0000000000MM?tab=request-log&run=CR0000000000R1");
    await screen.unmount();

    // Opened from the list of its own check: no link to the same list.
    const again = await renderRecord(SUMMARY, { currentRun: "CR0000000000R1" });
    await expect.element(page.getByTestId("record-check")).toHaveTextContent("Connection check");
    expect(page.getByRole("link", { name: "Requests of this check" }).elements()).toHaveLength(0);
    await again.unmount();

    // An outgoing webhook's test lists its requests on the Destination's tab.
    const webhook: RequestRecordSummary = {
      ...SUMMARY,
      check: "test",
      connection_id: null,
      destination: {
        id: "DS0000000000WH",
        name: "hook",
        type: "webhook",
        health: { state: "healthy" },
      },
      alert_group: null,
    };
    const hook = await renderRecord(webhook);
    await expect
      .element(page.getByRole("link", { name: "Requests of this check" }))
      .toHaveAttribute("href", "/destinations/DS0000000000WH?tab=request-log&run=CR0000000000R1");
    await expect.element(page.getByTestId("record-check")).toHaveTextContent("Test message");
    await hook.unmount();

    // A check before saving of a new Connection has no owner page.
    const draft = await renderRecord({
      ...SUMMARY,
      check: "draft_check",
      connection_id: null,
      destination: null,
      alert_group: null,
    });
    await expect.element(page.getByTestId("record-check")).toHaveTextContent("Check before saving");
    expect(page.getByRole("link").elements()).toHaveLength(0);
    await draft.unmount();

    // Without the permissions to read them, the Destination and the Alert Group are named, not linked.
    await renderRecord(SUMMARY, { permissions: ["connections:read"] });
    await expect.element(page.getByTestId("record-destination")).toHaveTextContent("alerts");
    await expect.element(page.getByTestId("record-alert-group")).toHaveTextContent("#42");
    expect(page.getByRole("link", { name: "alerts" }).elements()).toHaveLength(0);
    expect(page.getByRole("link", { name: "#42" }).elements()).toHaveLength(0);
  });

  test("says why a record of a Connection cannot be opened, and when it is gone", async () => {
    let status = 403;
    const { reads } = serve(() =>
      json(status, {
        type: "https://muster-io.github.io/muster/problems/forbidden",
        title: "Forbidden",
        status,
        detail: "This needs the Permission connections:read.",
      }),
    );
    const screen = await renderRecord(SUMMARY, { permissions: ["destinations:read"] });
    await expect
      .element(page.getByRole("alert"))
      .toHaveTextContent(
        "Opening this request needs the permission to read Connections: it belongs to the Connection of this Destination.",
      );
    // The dialog still names the request it was opened for, and offers no Connection's tab it could not open.
    await expect
      .element(dialog().getByRole("heading", { level: 2 }))
      .toHaveTextContent("→createPost");
    await expect.element(page.getByTestId("record-check")).toHaveTextContent("Connection check");
    expect(page.getByRole("link", { name: "Requests of this check" }).elements()).toHaveLength(0);
    expect(reads()).toBe(1);
    await screen.unmount();

    status = 404;
    await renderRecord();
    await expect
      .element(page.getByRole("alert"))
      .toHaveTextContent("It no longer exists. Reload the page.");
  });

  test("puts the request and the answer side by side from 1024 px, one under the other below", async () => {
    serve(() => json(200, RECORD));
    await page.viewport(1280, 900);
    await renderRecord();
    const request = page.getByRole("region", { name: "Request", exact: true });
    const answer = page.getByRole("region", { name: "Answer", exact: true });
    await expect.element(answer).toBeVisible();
    let a = request.element().getBoundingClientRect();
    let b = answer.element().getBoundingClientRect();
    expect(b.top).toBe(a.top);
    expect(b.left).toBeGreaterThan(a.right - 1);

    await page.viewport(800, 900);
    await expect.poll(() => answer.element().getBoundingClientRect().top).toBeGreaterThan(a.top);
    a = request.element().getBoundingClientRect();
    b = answer.element().getBoundingClientRect();
    expect(b.top).toBeGreaterThanOrEqual(a.bottom);
    // Nothing makes the dialog scroll sideways.
    const content = dialog().element();
    expect(content.scrollWidth).toBeLessThanOrEqual(content.clientWidth);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    serve(() => json(200, { ...RECORD, request_body_truncated: true, request_body_bytes: 40960 }));
    await renderRecord();
    await expect.element(dialog().getByRole("img", { name: "Отправлен" })).toBeVisible();
    const request = page.getByRole("region", { name: "Запрос", exact: true });
    await expect.element(request).toBeVisible();
    await expect.element(page.getByRole("region", { name: "Ответ", exact: true })).toBeVisible();
    await expect
      .element(page.getByTestId("record-request-body-cut"))
      .toHaveTextContent("Обрезано до 16 КБ из 40 КБ");
    await expect
      .element(page.getByTestId("record-check"))
      .toHaveTextContent("Проверка подключения");
    await expect
      .element(dialog().getByRole("link", { name: "Запросы этой проверки" }))
      .toBeVisible();
    await request.getByRole("button", { name: "Копировать тело" }).click();
    expect(writeText).toHaveBeenLastCalledWith(BODY);
    await expect.element(request.getByRole("button", { name: "Скопировано" })).toBeVisible();
    await userEvent.keyboard("{Escape}");
    await expect.element(dialog()).not.toBeInTheDocument();
  });
});
