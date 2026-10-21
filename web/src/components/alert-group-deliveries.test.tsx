// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Delivery section of the Alert Group page in a real browser: the text of every delivery state and mark (a Bridge
// message past its bridge's edit window among them), a message link that opens only http and https addresses in a new
// tab without the opener, the error of the messenger as text, a section that follows the alert-group hint instead of
// polling, and a Destination's name whose tooltip and accessible description are its full label with destinations:read
// (a deleted Destination keeps its name alone). With destinations:read each row has "Requests", which opens the
// requests of that Destination for that Alert Group in a dialog, and the row and the dialog's list, its status and
// latency included, keep their layout at 360 px.

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
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AlertGroupDelivery, Permission, Session } from "../api/gen/model";
import i18n from "../i18n";
import { getListAlertGroupDeliveriesQueryKey } from "../api/gen/endpoints/alert-groups/alert-groups";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { applyHint } from "../lib/live";
// The row and the dialog are laid out by their classes, as in the application.
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { AlertGroupDeliveries, deliveryStateText, safeMessageUrl } from "./alert-group-deliveries";

function delivery(extra: Partial<AlertGroupDelivery>): AlertGroupDelivery {
  return {
    destination: {
      id: "DS0000000000AA",
      name: "alerts",
      type: "mattermost",
      health: { state: "healthy" },
    },
    state: "delivered",
    thread_not_attached: false,
    possible_duplicate: false,
    past_edit_window: false,
    ...extra,
  };
}

function providers(
  children: ReactNode,
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
  permissions: Permission[] = ["alert-groups:read"],
) {
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
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

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await page.viewport(414, 896);
  await i18n.changeLanguage("en");
});

/** The section with the Destinations and Connections lists, in a router for the Destinations' links. */
async function renderLabelled(permissions: Permission[], items: AlertGroupDelivery[]) {
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const body = url.startsWith("/api/v1/destinations")
      ? {
          items: [
            {
              id: "DS0000000000AA",
              type: "mattermost",
              name: "alerts",
              connection_id: "CN000000000MMB",
              team_id: "team-dev",
              channel_id: "ch-alerts",
              channel_name: "alerts",
              health: { state: "healthy" },
              routes: [],
            },
          ],
          next_cursor: null,
        }
      : url.startsWith("/api/v1/connections")
        ? {
            items: [
              { id: "CN000000000MMB", type: "mattermost", name: "mm-b", server_url: "http://mm" },
            ],
            next_cursor: null,
          }
        : { items };
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  const rootRoute = createRootRoute({
    component: () => <AlertGroupDeliveries alertGroupId="AG0000000000AA" />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(providers(<RouterProvider router={router} />, queryClient, permissions));
}

function text(extra: Partial<AlertGroupDelivery>): string {
  return deliveryStateText(i18n.t, delivery(extra));
}

describe("the Delivery section", () => {
  test("names every state as the reference does", () => {
    expect(text({ state: "delivered" })).toBe("Delivered");
    expect(text({ state: "pending" })).toBe("Pending");
    expect(text({ state: "not_delivered", error: "answered 400" })).toBe(
      "Not delivered: answered 400",
    );
    expect(text({ state: "waiting_for_broken_destination" })).toBe("Waiting: alerts is Broken");
    expect(text({ state: "deleted_in_messenger" })).toBe("Deleted in the messenger");
    expect(text({ state: "withheld" })).toBe(
      "Not posted: resolved during a Storm or while the Destination was Broken",
    );
    expect(text({ state: "retired" })).toBe("No longer updated here");
  });

  test("links only http and https addresses", () => {
    expect(safeMessageUrl("http://127.0.0.1:18065/dev/pl/abc")).toBe(
      "http://127.0.0.1:18065/dev/pl/abc",
    );
    expect(safeMessageUrl("https://t.me/c/1/2")).toBe("https://t.me/c/1/2");
    expect(safeMessageUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeMessageUrl(" JavaScript:alert(1)")).toBeUndefined();
    expect(safeMessageUrl("data:text/html,x")).toBeUndefined();
    expect(safeMessageUrl("/relative")).toBeUndefined();
    expect(safeMessageUrl(null)).toBeUndefined();
  });

  test("shows the rows with their marks, a safe link and the error as text", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            delivery({
              message_url: "http://127.0.0.1:18065/dev/pl/abc",
              thread_not_attached: true,
              possible_duplicate: true,
            }),
            delivery({
              destination: {
                id: "DS0000000000AB",
                name: "prod",
                type: "mattermost",
                health: { state: "broken", since: "2026-10-09T08:00:00Z", reason: "403" },
              },
              state: "not_delivered",
              error: "<b>answered 400</b>",
              message_url: "javascript:alert(1)",
            }),
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    await render(providers(<AlertGroupDeliveries alertGroupId="AG0000000000AA" />));
    const link = page.getByRole("link", { name: "Delivered (opens in a new tab)" });
    await expect.element(link).toHaveAttribute("href", "http://127.0.0.1:18065/dev/pl/abc");
    await expect.element(link).toHaveAttribute("target", "_blank");
    await expect.element(link).toHaveAttribute("rel", "noopener noreferrer");
    await expect
      .element(page.getByTestId("delivery-thread-not-attached"))
      .toHaveTextContent("Thread not attached to the Root message");
    await expect
      .element(page.getByTestId("delivery-possible-duplicate"))
      .toHaveTextContent("Possible duplicate");
    await expect
      .element(page.getByTestId("delivery-state").nth(1))
      .toHaveTextContent("Not delivered: <b>answered 400</b>");
    expect(
      page.getByTestId("delivery-row").nth(1).element().querySelector("a[href^='javascript']"),
    ).toBeNull();
    await expect.element(page.getByTestId("destination-health").nth(1)).toHaveTextContent("Broken");
  });

  test("marks a delivery past the bridge's edit window beside its state, and nothing when it is not", async () => {
    const bridge = {
      id: "DS0000000000BR",
      name: "alerts-br",
      type: "bridge",
      health: { state: "healthy" },
    } as const;
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            delivery({ destination: bridge, past_edit_window: true }),
            delivery({
              destination: { ...bridge, id: "DS0000000000B2", name: "alerts-br-2" },
              past_edit_window: false,
            }),
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    await render(providers(<AlertGroupDeliveries alertGroupId="AG0000000000AA" />));
    const first = page.getByTestId("delivery-row").nth(0);
    await expect.element(first.getByTestId("delivery-state")).toHaveTextContent("Delivered");
    await expect
      .element(first.getByTestId("delivery-past-edit-window"))
      .toHaveTextContent("Past the edit window: updated on the next change of status");
    // Beside the state: in the same line of the row, right after it.
    expect(
      first.getByTestId("delivery-state").element().nextElementSibling?.getAttribute("data-testid"),
    ).toBe("delivery-past-edit-window");
    await expect
      .element(page.getByTestId("delivery-row").nth(1).getByTestId("delivery-state"))
      .toHaveTextContent("Delivered");
    expect(page.getByTestId("delivery-past-edit-window").elements()).toHaveLength(1);
  });

  test("does not poll a pending delivery and reads it again on the alert-group hint", async () => {
    const answers = [
      [delivery({ state: "pending" })],
      [delivery({ state: "not_delivered", error: "answered 400" })],
    ];
    let reads = 0;
    const fetch = vi.spyOn(window, "fetch").mockImplementation(async () => {
      const items = answers[Math.min(reads, answers.length - 1)];
      reads++;
      return new Response(JSON.stringify({ items }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await render(providers(<AlertGroupDeliveries alertGroupId="AG0000000000AA" />, queryClient));
    await expect.element(page.getByTestId("delivery-state")).toHaveTextContent("Pending");
    const query = queryClient
      .getQueryCache()
      .find({ queryKey: getListAlertGroupDeliveriesQueryKey("AG0000000000AA") });
    expect(query?.observers.map((o) => o.options.refetchInterval)).toEqual([undefined]);
    expect(fetch).toHaveBeenCalledTimes(1);

    applyHint(queryClient, { type: "alert-group", id: "AG0000000000AA" });
    await expect
      .element(page.getByTestId("delivery-state"))
      .toHaveTextContent("Not delivered: answered 400");
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    expect(deliveryStateText(i18n.t, delivery({ state: "waiting_for_broken_destination" }))).toBe(
      "Ожидает: место доставки alerts сломано",
    );
    expect(deliveryStateText(i18n.t, delivery({ state: "retired" }))).toBe(
      "Здесь больше не обновляется",
    );
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ items: [delivery({ past_edit_window: true })] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await render(providers(<AlertGroupDeliveries alertGroupId="AG0000000000AA" />));
    await expect
      .element(page.getByTestId("delivery-past-edit-window"))
      .toHaveTextContent("Окно правки истекло: обновится при следующей смене статуса");
  });
  test("gives the name its full label as tooltip and description, a deleted Destination none", async () => {
    await renderLabelled(
      ["alert-groups:read", "destinations:read", "connections:read"],
      [
        delivery({}),
        delivery({
          destination: {
            id: "DS00000000GONE",
            name: "gone",
            type: "telegram",
            health: { state: "healthy" },
          },
          state: "retired",
        }),
      ],
    );
    const label = "alerts · Mattermost · via mm-b · #alerts";
    const link = page.getByRole("link", { name: "alerts", exact: true });
    await expect.element(link).toHaveAttribute("href", "/destinations/DS0000000000AA");
    await expect.element(link).toHaveAttribute("title", label);
    await expect.element(link).toHaveAccessibleDescription(label);
    const gone = page.getByRole("link", { name: "gone", exact: true });
    await expect.element(gone).toBeVisible();
    await expect.element(gone).not.toHaveAttribute("title");
    await expect.element(gone).not.toHaveAttribute("aria-describedby");
  });

  test("shows the name alone without destinations:read, and no requests", async () => {
    await renderLabelled(["alert-groups:read"], [delivery({})]);
    const name = page.getByTestId("delivery-destination");
    await expect.element(name).toHaveTextContent("alerts");
    await expect.element(name).not.toHaveAttribute("title");
    expect(page.getByRole("link", { name: "alerts", exact: true }).elements()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Requests" }).elements()).toHaveLength(0);
  });
});

/** The section in a router, the Request log answering with one record; returns the queries of the Request log. */
async function renderWithRequests(items: AlertGroupDelivery[]): Promise<Record<string, string>[]> {
  const queries: Record<string, string>[] = [];
  vi.spyOn(window, "fetch").mockImplementation(async (input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const [path = "", search = ""] = url.split("?");
    const body =
      path === "/api/v1/request-log"
        ? {
            items: [
              {
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
                connection_id: "CN000000000MMB",
                destination: items[0]?.destination ?? null,
                alert_group: { id: "AG0000000000AA", number: 42 },
                request_body_truncated: false,
                response_body_truncated: false,
              },
            ],
            next_cursor: null,
          }
        : path === "/api/v1/destinations" || path === "/api/v1/connections"
          ? { items: [], next_cursor: null }
          : { items };
    if (path === "/api/v1/request-log") {
      queries.push(Object.fromEntries(new URLSearchParams(search)));
    }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  const rootRoute = createRootRoute({
    component: () => <AlertGroupDeliveries alertGroupId="AG0000000000AA" />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    providers(<RouterProvider router={router} />, queryClient, [
      "alert-groups:read",
      "destinations:read",
    ]),
  );
  return queries;
}

/** Whether the page scrolls sideways. */
function scrollsSideways(): boolean {
  return document.documentElement.scrollWidth > document.documentElement.clientWidth;
}

describe("the requests of a delivery", () => {
  test("opens the requests of each Destination for this Alert Group", async () => {
    const queries = await renderWithRequests([
      delivery({}),
      delivery({
        destination: {
          id: "DS0000000000AB",
          name: "prod",
          type: "webhook",
          health: { state: "healthy" },
        },
      }),
    ]);
    // Each Destination's row has its own.
    await expect
      .element(page.getByTestId("delivery-row").nth(1).getByRole("button", { name: "Requests" }))
      .toBeVisible();
    const row = page.getByTestId("delivery-row").nth(0);
    await row.getByRole("button", { name: "Requests", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Requests: alerts" });
    await expect.element(dialog.getByRole("button", { name: "createPost" })).toBeVisible();
    await expect.element(dialog.getByTestId("request-status")).toHaveTextContent("201");
    expect(queries).toEqual([{ destination: "DS0000000000AA", alert_group: "AG0000000000AA" }]);
  });

  test("keeps the row without horizontal scrolling at 360 px in Russian, the dialog open or not", async () => {
    await i18n.changeLanguage("ru");
    await page.viewport(360, 740);
    await renderWithRequests([
      delivery({
        destination: {
          id: "DS0000000000AA",
          name: "alerts-production-europe-west-primary",
          type: "mattermost",
          health: { state: "broken", since: "2026-10-18T06:00:00Z", reason: "Refused" },
        },
        state: "waiting_for_broken_destination",
        thread_not_attached: true,
      }),
    ]);
    const row = page.getByTestId("delivery-row");
    const button = row.getByRole("button", { name: "Запросы", exact: true });
    await expect.element(button).toBeVisible();
    expect(scrollsSideways()).toBe(false);
    const rowBox = row.element().getBoundingClientRect();
    const buttonBox = button.element().getBoundingClientRect();
    expect(buttonBox.right).toBeLessThanOrEqual(rowBox.right);
    expect(buttonBox.left).toBeGreaterThanOrEqual(rowBox.left);

    await button.click();
    const dialog = page.getByRole("dialog", {
      name: "Запросы: alerts-production-europe-west-primary",
    });
    await expect.element(dialog.getByRole("button", { name: "createPost" })).toBeVisible();
    await expect.element(dialog.getByRole("img", { name: "Отправлен" })).toBeVisible();
    expect(scrollsSideways()).toBe(false);
    const content = dialog.element();
    await expect.poll(() => content.getBoundingClientRect().left).toBe(0);
    expect(content.scrollWidth).toBeLessThanOrEqual(content.clientWidth);
    // The status and the latency of the request stay in view, and the list's own box does not scroll sideways.
    for (const id of ["request-status", "request-latency"]) {
      const cell = dialog.getByTestId(id).element();
      expect(cell.getBoundingClientRect().right, id).toBeLessThanOrEqual(360);
    }
    const box = dialog.getByRole("table").element().parentElement;
    expect(box).not.toBeNull();
    expect(box?.scrollWidth).toBeLessThanOrEqual(box?.clientWidth ?? 0);
  });
});
