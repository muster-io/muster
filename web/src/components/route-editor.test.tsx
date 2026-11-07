// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The page of a Route in a real browser, against a stubbed API (C-08.FR-1, C-26.FR-1, FR-2): a save sends the version
// the page was read at as If-Match, with the Destinations and the option as the form shows them; a newer version from
// someone else, while the form has changes, shows one banner with Reload, which takes the new version; a refused
// save (412) shows that banner once and no second alert; a failed read of the Route keeps the editor and says why; and a
// Responder reads the Destinations, their conditions and the option as text, with no Save and no Reload.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import { getGetRouteQueryKey } from "../api/gen/endpoints/routes/routes";
import type { Permission, Route, Session } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { mattermostConnection } from "../test/fixtures/connections";
import { mattermostDestination } from "../test/fixtures/destinations";
import { RouteEditor } from "../routes/routes.$routeId";

const ID = "RT0000000000IN";
const CONNECTION = mattermostConnection({ id: "CN000000000MMA", name: "mm-lab" });
const CRITICAL = mattermostDestination({
  id: "DS000000000ICR",
  name: "infra-critical",
  connection_id: CONNECTION.id,
});
const NOTICE = mattermostDestination({
  id: "DS000000000INO",
  name: "infra-notice",
  connection_id: CONNECTION.id,
});

function routeOf(overrides: Partial<Route> = {}): Route {
  return {
    id: ID,
    name: "Infrastructure",
    description: "",
    matchers: [{ label: "category", op: "=", value: "infra" }],
    minimum_severity: "none",
    urgent: false,
    group_key: ["alertname", "team"],
    destination_entries: [
      { destination_id: CRITICAL.id, severity_condition: "critical" },
      { destination_id: NOTICE.id, severity_condition: "below_critical" },
    ],
    destination_ids: [CRITICAL.id, NOTICE.id],
    deliver_to_teams: true,
    run_team_chains: false,
    own_message_labels: false,
    message_labels: [],
    owning_team: null,
    position: 2,
    is_default: false,
    destinations: [CRITICAL, NOTICE].map((d) => ({
      id: d.id,
      name: d.name,
      type: d.type,
      health: d.health,
    })),
    storm_active: false,
    open_alert_group_count: 0,
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
    policy: {
      reopen_window_seconds: 900,
      grace_period_seconds: 900,
      urgent_rise_removes_ack: true,
      snooze_durations_seconds: [3600],
      thread_batching_window_seconds: 60,
      storm_threshold: 20,
      language: "en",
      templates: { root_message: null, line: null, ack_timeout_notice: null },
      ack_timeout: { enabled: true, first_interval_seconds: 900 },
      reminders: { enabled: true, first_interval_seconds: 14400, cap_seconds: 86400 },
      auto_unacknowledge: false,
    },
    ...overrides,
  };
}

interface Server {
  route: Route;
  getStatus: number;
  putStatus: number;
  puts: { ifMatch: string | null; body: Record<string, unknown> }[];
}

let server: Server;

function reply(status: number, body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
    }),
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  server = { route: routeOf(), getStatus: 200, putStatus: 200, puts: [] };
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    if (url === `/api/v1/routes/${ID}`) {
      if (method === "PUT") {
        const raw = init?.body;
        const body: unknown = JSON.parse(typeof raw === "string" ? raw : "{}");
        server.puts.push({
          ifMatch: new Headers(init?.headers).get("if-match"),
          body: typeof body === "object" && body !== null ? { ...body } : {},
        });
        return server.putStatus === 200
          ? reply(200, { ...server.route, etag: '"9"' })
          : reply(server.putStatus, { type: "about:blank", title: "x", status: server.putStatus });
      }
      return server.getStatus === 200
        ? reply(200, server.route)
        : reply(server.getStatus, { type: "about:blank", title: "x", status: server.getStatus });
    }
    if (url.startsWith("/api/v1/destinations")) {
      return reply(200, { items: [CRITICAL, NOTICE], next_cursor: null });
    }
    if (url.startsWith("/api/v1/connections")) {
      return reply(200, { items: [CONNECTION], next_cursor: null });
    }
    if (url === "/api/v1/organization") {
      return reply(200, { team_label: "team" });
    }
    return reply(200, { items: [], next_cursor: null });
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderEditor(
  permissions: Permission[] = ["routes:read", "routes:write", "destinations:read"],
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the page reads only the Permissions
      user: { id: "US0000000000ME", name: "Me", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const root = createRootRoute({ component: () => <Outlet /> });
  const editor = createRoute({
    getParentRoute: () => root,
    path: "/",
    component: () => <RouteEditor routeId={ID} />,
  });
  const list = createRoute({ getParentRoute: () => root, path: "/routes", component: () => null });
  const router = createRouter({
    routeTree: root.addChildren([editor, list]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { queryClient, router };
}

const condition = (id: string) => page.getByTestId(`route-destination-condition-${id}`);

describe("the page of a Route", () => {
  test("a save sends the version it was read at, with the Destinations and the option as shown", async () => {
    const { router } = await renderEditor();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
    await expect.element(page.getByTestId("route-deliver-to-teams")).toBeChecked();
    await expect.element(page.getByTestId("route-run-team-chains")).not.toBeChecked();
    await condition(NOTICE.id).selectOptions("any");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(server.puts).toHaveLength(1));
    expect(server.puts[0]?.ifMatch).toBe('"1"');
    expect(server.puts[0]?.body).toMatchObject({
      name: "Infrastructure",
      group_key: ["alertname", "team"],
      destination_entries: [
        { destination_id: CRITICAL.id, severity_condition: "critical" },
        { destination_id: NOTICE.id, severity_condition: "any" },
      ],
      deliver_to_teams: true,
      run_team_chains: false,
    });
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/routes"));
  });

  test("a newer version from someone else, while the form has changes, shows one banner, and Reload takes it", async () => {
    const { queryClient } = await renderEditor();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
    await condition(NOTICE.id).selectOptions("critical");
    server.route = routeOf({
      etag: '"2"',
      destination_entries: [
        { destination_id: CRITICAL.id, severity_condition: "any" },
        { destination_id: NOTICE.id, severity_condition: "any" },
      ],
    });
    await queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(ID) });
    const banner = page.getByText("This route was changed elsewhere.");
    await expect.element(banner).toBeVisible();
    expect(page.getByRole("alert").elements()).toHaveLength(1);
    // The form keeps the changes; a save over the newer version is sent with the version it was read at.
    await expect.element(condition(NOTICE.id)).toHaveValue("critical");
    server.putStatus = 412;
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(server.puts).toHaveLength(1));
    expect(server.puts[0]?.ifMatch).toBe('"1"');
    await expect
      .element(page.getByText("Someone else changed this route. Reload to see the changes."))
      .toBeVisible();
    expect(
      page.getByText("Someone else changed this route.", { exact: false }).elements(),
    ).toHaveLength(1);
    expect(page.getByRole("alert").elements()).toHaveLength(1);
    await page.getByRole("button", { name: "Reload" }).click();
    await expect.element(condition(CRITICAL.id)).toHaveValue("any");
    await expect.element(condition(NOTICE.id)).toHaveValue("any");
    await expect.element(page.getByRole("button", { name: "Reload" })).not.toBeInTheDocument();
  });

  test("a save of its own shows no banner, not even for a moment, and leaves for the list", async () => {
    const { queryClient, router } = await renderEditor();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
    let shown = false;
    const watch = new MutationObserver(() => {
      shown ||= document.body.textContent.includes("This route was changed elsewhere.");
    });
    watch.observe(document.body, { childList: true, subtree: true, characterData: true });
    try {
      await condition(NOTICE.id).selectOptions("any");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await vi.waitFor(() => expect(server.puts).toHaveLength(1));
      await vi.waitFor(() => expect(router.state.location.pathname).toBe("/routes"));
      await queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(ID) });
      await new Promise((done) => setTimeout(done, 100));
    } finally {
      watch.disconnect();
    }
    expect(shown).toBe(false);
  });

  test("a failed read of the Route keeps the editor and says why", async () => {
    const { queryClient } = await renderEditor();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
    server.getStatus = 500;
    await queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(ID) });
    await expect.element(page.getByTestId("route-read-error")).toBeVisible();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
  });

  test("a Responder reads the Destinations, conditions and option as text, with no Save", async () => {
    await renderEditor(["routes:read", "destinations:read"]);
    await expect.element(condition(CRITICAL.id)).toHaveTextContent("Severity = critical");
    await expect.element(condition(NOTICE.id)).toHaveTextContent("Severity < critical");
    await expect.element(page.getByTestId("route-deliver-to-teams")).toHaveTextContent("On");
    expect(page.getByRole("button", { name: "Save" }).elements()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Reload" }).elements()).toHaveLength(0);
    expect(page.getByRole("switch").elements()).toHaveLength(0);
    // The Route page has Route wording only: no section of a Team screen.
    expect(page.getByText("Destinations of the Team", { exact: true }).elements()).toHaveLength(0);
  });
});
