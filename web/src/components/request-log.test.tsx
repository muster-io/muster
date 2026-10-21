// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Request log in a real browser (C-23.FR-6, AC-6): the records newest first with the time in the user's time zone,
// the arrow named "Sent" or "Received" before the operation in a monospace font with the kind of check below it, the
// status or what failed with the latency below it, in three columns that fit 360 px; "Load more" with the cursor; the
// filters by direction, status class and operation, which change the request and the caller's filters; the run of one
// check as a filter that can be cleared; the empty list; the dialog of "Requests" with exactly its scope, and a record
// opened over it; and the tabs of the pages, which keep the settings mounted behind the Request log. English and
// Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { RequestLogList, RequestRecordSummary, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
// The dialogs are laid out by their classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { RequestLog, RequestLogButton, type RequestLogScope, RequestLogTabs } from "./request-log";
import type { RequestLogFilters, RequestLogSearch } from "./request-log-search";

const CONNECTION = "CN0000000000MM";

function summary(extra: Partial<RequestRecordSummary>): RequestRecordSummary {
  return {
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
    check: null,
    check_run: null,
    connection_id: CONNECTION,
    destination: null,
    alert_group: null,
    request_body_truncated: false,
    response_body_truncated: false,
    ...extra,
  };
}

const ROWS: RequestRecordSummary[] = [
  summary({}),
  summary({
    id: "RQ0000000000AB",
    at: "2026-10-18T05:59:00Z",
    direction: "inbound",
    operation: "press",
    status: 200,
    latency_ms: 3,
  }),
  summary({
    id: "RQ0000000000AC",
    at: "2026-10-18T05:58:00Z",
    operation: "updatePost",
    status: null,
    error_class: "transient",
    error_code: "timeout",
    latency_ms: 10000,
  }),
];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * Answers the list of the Request log by its query and the read of a record; returns the queries the list was read
 * with, each as its parameters.
 */
function serve(
  pages: (query: URLSearchParams) => RequestLogList,
  records: Record<string, unknown> = {},
): Record<string, string>[] {
  const queries: Record<string, string>[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const [path = "", search = ""] = url.split("?");
    if (path === "/api/v1/request-log") {
      const query = new URLSearchParams(search);
      queries.push(Object.fromEntries(query));
      return json(200, pages(query));
    }
    const record = records[path.replace("/api/v1/request-log/", "")];
    if (path.startsWith("/api/v1/request-log/") && record !== undefined) {
      return json(200, record);
    }
    return json(404, { title: "not mocked", status: 404 });
  });
  return queries;
}

async function renderIn(node: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the list reads only the time zone
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "Asia/Tokyo" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-19T09:00:00Z",
      idle_expires_at: "2026-10-19T09:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions: ["connections:read", "destinations:read", "alert-groups:read"],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const rootRoute = createRootRoute({ component: () => node });
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

/** The list as a page holds it: its filters in state, each change also reported. */
function Stateful({
  scope,
  initial = {},
  onChange,
}: {
  scope: RequestLogScope;
  initial?: RequestLogFilters;
  onChange?: (patch: Partial<RequestLogFilters>) => void;
}) {
  const [filters, setFilters] = useState(initial);
  return (
    <RequestLog
      scope={scope}
      filters={filters}
      onFiltersChange={(patch) => {
        onChange?.(patch);
        setFilters((f) => ({ ...f, ...patch }));
      }}
    />
  );
}

const TABLE = 'table[aria-label="Request log"], table[aria-label="Журнал запросов"]';

/** Each row of the table as its time, direction, operation, status and latency, as text. */
function rows(): string[][] {
  return Array.from(document.querySelectorAll(`:is(${TABLE}) tbody tr`)).map((tr) =>
    [
      tr.querySelector("time"),
      tr.querySelector('[data-testid="request-direction"]'),
      tr.querySelector('[data-testid="request-operation"]'),
      tr.querySelector('[data-testid="request-status"]'),
      tr.querySelector('[data-testid="request-latency"]'),
    ].map((cell) => cell?.textContent ?? ""),
  );
}

/** The headers of the table's columns. */
function headers(): string[] {
  return Array.from(document.querySelectorAll(`:is(${TABLE}) thead th`)).map(
    (th) => th.textContent ?? "",
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

describe("the Request log", () => {
  test("lists the records newest first with their time, direction, operation, status and latency", async () => {
    const queries = serve(() => ({ items: ROWS, next_cursor: null }));
    await renderIn(<Stateful scope={{ connection: CONNECTION }} />);
    await expect.element(page.getByRole("table", { name: "Request log" })).toBeVisible();
    await expect.poll(rows).toEqual([
      // The time in the user's time zone, Tokyo, with its seconds.
      ["Oct 18, 2026, 15:00:12", "→", "createPost", "201", "12 ms"],
      ["Oct 18, 2026, 14:59:00", "←", "press", "200", "3 ms"],
      // Without an answer, what failed, translated as the checks name it.
      ["Oct 18, 2026, 14:58:00", "→", "updatePost", "No answer in time.", "10000 ms"],
    ]);
    // The arrow stands before the operation, the latency under the status.
    expect(headers()).toEqual(["Time", "Operation", "Status"]);
    const arrow = page.getByTestId("request-direction").first().element().getBoundingClientRect();
    const name = page.getByTestId("request-operation").first().element().getBoundingClientRect();
    expect(arrow.right).toBeLessThanOrEqual(name.left);
    const status = page.getByTestId("request-status").first().element().getBoundingClientRect();
    const latency = page.getByTestId("request-latency").first().element().getBoundingClientRect();
    expect(latency.top).toBeGreaterThanOrEqual(status.bottom);
    const arrows = page.getByTestId("request-direction");
    await expect.element(arrows.nth(0)).toHaveAccessibleName("Sent");
    await expect.element(arrows.nth(1)).toHaveAccessibleName("Received");
    await expect.element(page.getByRole("img", { name: "Sent" }).first()).toBeVisible();
    const operation = page.getByRole("button", { name: "createPost" });
    await expect.element(operation).toHaveClass(/font-mono/);
    await expect.element(page.getByTestId("request-status").nth(2)).toHaveClass(/text-destructive/);
    await expect
      .element(page.getByTestId("request-status").nth(0))
      .not.toHaveClass(/text-destructive/);
    expect(queries).toEqual([{ connection: CONNECTION }]);
  });

  test("loads the next page with its cursor and adds it", async () => {
    const queries = serve((query) =>
      query.get("cursor") === "c2"
        ? { items: ROWS.slice(2), next_cursor: null }
        : { items: ROWS.slice(0, 2), next_cursor: "c2" },
    );
    await renderIn(<Stateful scope={{ connection: CONNECTION }} />);
    await expect.poll(() => rows().length).toBe(2);
    await page.getByRole("button", { name: "Load more" }).click();
    await expect.poll(() => rows().map((r) => r[2])).toEqual(["createPost", "press", "updatePost"]);
    expect(queries).toEqual([{ connection: CONNECTION }, { connection: CONNECTION, cursor: "c2" }]);
    await expect.element(page.getByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  test("filters by direction, status class and operation, which change the request and the filters", async () => {
    const changes: Partial<RequestLogFilters>[] = [];
    const queries = serve(() => ({ items: ROWS.slice(0, 1), next_cursor: null }));
    await renderIn(
      <Stateful scope={{ connection: CONNECTION }} onChange={(patch) => changes.push(patch)} />,
    );
    await expect.poll(() => rows().length).toBe(1);
    const filters = page.getByRole("search", { name: "Request log filters" });
    await userEvent.selectOptions(filters.getByLabelText("Direction"), "Received");
    await expect
      .poll(() => queries.at(-1))
      .toEqual({ connection: CONNECTION, direction: "inbound" });
    await userEvent.selectOptions(filters.getByLabelText("Status"), "2xx");
    await expect
      .poll(() => queries.at(-1))
      .toEqual({ connection: CONNECTION, direction: "inbound", status_class: "2xx" });
    await filters.getByLabelText("Operation").fill("  createPost ");
    await filters.getByRole("button", { name: "Apply" }).click();
    await expect
      .poll(() => queries.at(-1))
      .toEqual({
        connection: CONNECTION,
        direction: "inbound",
        status_class: "2xx",
        operation: "createPost",
      });
    expect(changes).toEqual([
      { direction: "inbound" },
      { status_class: "2xx" },
      { operation: "createPost" },
    ]);
    // "No answer" is the status class none.
    await userEvent.selectOptions(filters.getByLabelText("Status"), "No answer");
    await expect.poll(() => queries.at(-1)?.status_class).toBe("none");

    await filters.getByRole("button", { name: "Clear filters" }).click();
    await expect.poll(() => queries.at(-1)).toEqual({ connection: CONNECTION });
    await expect.element(filters.getByLabelText("Operation")).toHaveValue("");
    await expect.element(filters.getByLabelText("Direction")).toHaveValue("");
    await expect
      .element(filters.getByRole("button", { name: "Clear filters" }))
      .not.toBeInTheDocument();
  });

  test("says when there are no requests", async () => {
    serve(() => ({ items: [], next_cursor: null }));
    await renderIn(<Stateful scope={{ connection: CONNECTION }} />);
    await expect.element(page.getByText("No requests in the last 7 days.")).toBeVisible();
  });

  test("narrows to one check's run, which can be cleared, and names the kind of check", async () => {
    const queries = serve(() => ({
      items: [summary({ operation: "getInfo", check: "draft_check", check_run: "CR0000000000R1" })],
      next_cursor: null,
    }));
    await renderIn(
      <Stateful scope={{ connection: CONNECTION }} initial={{ run: "CR0000000000R1" }} />,
    );
    await expect
      .element(page.getByTestId("request-check"))
      .toHaveTextContent("Check before saving");
    await expect
      .element(page.getByTestId("request-log-run"))
      .toHaveTextContent("Requests of one check");
    expect(queries[0]).toEqual({ connection: CONNECTION, run: "CR0000000000R1" });
    await page.getByRole("button", { name: "Show the requests of every check" }).click();
    await expect.poll(() => queries.at(-1)).toEqual({ connection: CONNECTION });
    await expect.element(page.getByTestId("request-log-run")).not.toBeInTheDocument();
  });

  test("opens the records of its scope in a dialog, and a record over it", async () => {
    const queries = serve(() => ({ items: ROWS.slice(0, 1), next_cursor: null }), {
      RQ0000000000AA: {
        ...ROWS[0],
        request_headers: {},
        request_body: '{"channel_id":"ch-alerts"}',
        request_body_bytes: 26,
        response_headers: {},
        response_body: "{}",
        response_body_bytes: 2,
        error: null,
      },
    });
    await renderIn(
      <RequestLogButton
        scope={{ destination: "DS0000000000AA", alert_group: "AG0000000000AA" }}
        label="Requests"
        title="Requests: alerts"
      />,
    );
    expect(queries).toEqual([]);
    await page.getByRole("button", { name: "Requests" }).click();
    const list = page.getByRole("dialog", { name: "Requests: alerts" });
    await expect.element(list.getByRole("button", { name: "createPost" })).toBeVisible();
    expect(queries).toEqual([{ destination: "DS0000000000AA", alert_group: "AG0000000000AA" }]);

    await list.getByRole("button", { name: "createPost" }).click();
    const record = page.getByTestId("request-record");
    await expect.element(record.getByTestId("record-status")).toHaveTextContent("201");
    // Escape closes the record and keeps the list.
    await userEvent.keyboard("{Escape}");
    await expect.element(record).not.toBeInTheDocument();
    await expect.element(list).toBeVisible();
    await userEvent.keyboard("{Escape}");
    await expect.element(list).not.toBeInTheDocument();
  });

  test("keeps the settings mounted behind the tab of the Request log", async () => {
    serve(() => ({ items: ROWS.slice(0, 1), next_cursor: null }));
    const searches: Partial<RequestLogSearch>[] = [];
    function Page() {
      const [search, setSearch] = useState<RequestLogSearch>({});
      return (
        <RequestLogTabs
          search={search}
          onSearch={(patch) => {
            searches.push(patch);
            setSearch((s) => ({ ...s, ...patch }));
          }}
          scope={{ connection: CONNECTION }}
        >
          <input aria-label="Name" defaultValue="" />
        </RequestLogTabs>
      );
    }
    await renderIn(<Page />);
    const settings = page.getByRole("tab", { name: "Settings" });
    const log = page.getByRole("tab", { name: "Request log" });
    await expect.element(settings).toHaveAttribute("aria-selected", "true");
    await page.getByLabelText("Name").fill("unsaved");
    expect(page.getByRole("table").elements()).toHaveLength(0);

    await log.click();
    await expect.element(page.getByRole("table", { name: "Request log" })).toBeVisible();
    await expect.element(log).toHaveAttribute("aria-selected", "true");
    await expect
      .element(page.getByRole("tabpanel"))
      .toHaveAttribute("aria-labelledby", log.element().id);
    // Hidden, and still mounted with its unsaved value.
    await expect.element(page.getByLabelText("Name")).not.toBeVisible();
    await userEvent.selectOptions(page.getByLabelText("Direction"), "Sent");

    await settings.click();
    await expect.element(page.getByLabelText("Name")).toHaveValue("unsaved");
    expect(searches).toEqual([
      { tab: "request-log" },
      { direction: "outbound" },
      {
        tab: undefined,
        direction: undefined,
        status_class: undefined,
        operation: undefined,
        run: undefined,
      },
    ]);
  });

  test("fits 360 px in Russian: the status and the latency stay in view, nothing scrolls sideways", async () => {
    await i18n.changeLanguage("ru");
    await page.viewport(360, 740);
    serve(() => ({
      items: [
        summary({ operation: "createPostEphemeral", check: "connection_check" }),
        summary({
          id: "RQ0000000000AC",
          operation: "createPostEphemeral",
          status: null,
          error_class: "transient",
          error_code: "timeout",
          latency_ms: 10000,
        }),
      ],
      next_cursor: null,
    }));
    await renderIn(<Stateful scope={{ connection: CONNECTION }} />);
    const table = page.getByRole("table", { name: "Журнал запросов" });
    await expect.poll(() => rows().length).toBe(2);
    expect(rows()).toEqual([
      // The date and the time of day stand on two lines and read as one time.
      ["18 окт. 2026 г., 15:00:12", "→", "createPostEphemeral", "201", "12 мс"],
      [
        "18 окт. 2026 г., 15:00:12",
        "→",
        "createPostEphemeral",
        "Нет ответа за отведённое время.",
        "10000 мс",
      ],
    ]);
    for (const id of ["request-status", "request-latency"]) {
      for (const cell of table.getByTestId(id).elements()) {
        expect(cell.getBoundingClientRect().right, id).toBeLessThanOrEqual(360);
      }
    }
    // The table's own box does not scroll sideways either.
    const box = table.element().parentElement;
    expect(box).not.toBeNull();
    expect(box?.scrollWidth).toBeLessThanOrEqual(box?.clientWidth ?? 0);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(360);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    serve((query) =>
      query.has("direction")
        ? { items: [], next_cursor: null }
        : { items: ROWS.slice(0, 1), next_cursor: "c2" },
    );
    await renderIn(<Stateful scope={{ connection: CONNECTION }} />);
    await expect.element(page.getByRole("table", { name: "Журнал запросов" })).toBeVisible();
    await expect.element(page.getByRole("img", { name: "Отправлен" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Показать ещё" })).toBeVisible();
    await expect
      .poll(rows)
      .toEqual([["18 окт. 2026 г., 15:00:12", "→", "createPost", "201", "12 мс"]]);
    await userEvent.selectOptions(page.getByLabelText("Направление"), "Получен");
    await expect.element(page.getByText("Запросов за последние 7 дней нет.")).toBeVisible();
  });
});
