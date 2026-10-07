// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the Alert Group list in a real browser, against a stubbed API: every filter set through its control
// lands in the URL, the URL read back in a new router shows the same filters, and the view's request names them. A
// Matcher the server refuses leaves the URL as it was.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  defaultParseSearch,
  defaultStringifySearch,
  useLocation,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session } from "../api/gen/model";
import i18n from "../i18n";
import {
  type AlertGroupSearch,
  alertGroupSearchSchema,
  compact,
  countParams,
  listParams,
} from "../lib/alert-group-search";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { AlertGroupFilters } from "./alert-group-filters";

const ROUTES = [
  { id: "RT0000000000K8", name: "k8s", is_default: false },
  { id: "RT00000000000D", name: "Default", is_default: true },
];
const INTEGRATIONS = [
  { id: "NT0000000000AM", name: "prod-am" },
  { id: "NT0000000000LB", name: "lab" },
];

let requests: URL[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });
}

beforeEach(async () => {
  requests = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    requests.push(url);
    if (url.pathname === "/api/v1/routes") {
      return Promise.resolve(json(200, { items: ROUTES }));
    }
    if (url.pathname === "/api/v1/integrations") {
      return Promise.resolve(json(200, { items: INTEGRATIONS, next_cursor: null }));
    }
    const labels = url.searchParams.getAll("label");
    const bad = labels.findIndex((l) => l.includes("["));
    if (bad >= 0) {
      return Promise.resolve(
        json(400, {
          type: "about:blank",
          title: "Validation failed",
          status: 400,
          errors: [{ pointer: `/query/label/${bad}`, code: "invalid_regex", detail: "missing ]" }],
        }),
      );
    }
    return Promise.resolve(
      json(200, { firing: 1, acknowledged: 0, snoozed: 0, resolved: 2, all: 3 }),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

const SESSION: SessionRead = {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the filters read only the Permissions
  session: { state: "active", permissions: ["routes:read", "integrations:read"] } as Session,
  ended: false,
};

/**
 * The filters on a page whose search parameters are those of the list, in a router at href. The page reads and writes
 * the address as the list route does: through the schema and the router's own search serialization.
 */
async function renderAt(href: string) {
  const history = createMemoryHistory({ initialEntries: [href] });
  const rootRoute = createRootRoute();
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/alert-groups",
    validateSearch: alertGroupSearchSchema,
    component: function Page() {
      const location = useLocation();
      const search = alertGroupSearchSchema.parse(defaultParseSearch(location.searchStr));
      return (
        <AlertGroupFilters
          search={search}
          showColumns
          onChange={(patch) =>
            history.replace(
              `/alert-groups${defaultStringifySearch(compact({ ...search, ...patch }))}`,
            )
          }
        />
      );
    },
  });
  const router = createRouter({ routeTree: rootRoute.addChildren([listRoute]), history });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(SESSION_QUERY_KEY, SESSION);
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { router, screen };
}

/** The search parameters of the router's current address, as the list route reads them. */
function searchOf(router: { state: { location: { searchStr: string } } }): AlertGroupSearch {
  return alertGroupSearchSchema.parse(defaultParseSearch(router.state.location.searchStr));
}

describe("search parameters", () => {
  const full: AlertGroupSearch = {
    tab: "firing",
    route: ["RT0000000000K8"],
    integration: ["NT0000000000AM", "NT0000000000LB"],
    severity: ["critical", "warning"],
    urgent: true,
    resolved_by: "system",
    resolve_reason: "gone",
    reopened: false,
    label: ['namespace="payments"', 'pod=~"api-.*"'],
    range: "custom",
    from: "2026-09-01T00:00:00.000Z",
    to: "2026-10-01T00:00:00.000Z",
    q: "42",
    sort: "-last_changed_at",
    columns: ["pod", "namespace"],
  };

  test("round-trip through the URL, a search that looks like a number included", () => {
    const back = alertGroupSearchSchema.parse(defaultParseSearch(defaultStringifySearch(full)));
    expect(back).toEqual(full);
    // A hand-written address: one value of a list, a number as the search, nonsense dropped.
    expect(
      alertGroupSearchSchema.parse(
        defaultParseSearch("?route=RT1&q=7&tab=nonsense&urgent=maybe&severity=fatal"),
      ),
    ).toEqual({ route: ["RT1"], q: "7" });
  });

  test("turn into the request of the list and of the counts", () => {
    expect(listParams(full, 0)).toMatchObject({
      status: ["firing"],
      route: ["RT0000000000K8"],
      resolve_reason: "gone",
      from: full.from,
      to: full.to,
      q: "42",
      sort: "-last_changed_at",
      label_columns: ["pod", "namespace"],
    });
    expect(compact(listParams({}, 0))).toEqual({});
    expect(listParams({ tab: "all" }, 0).status).toEqual([
      "firing",
      "acknowledged",
      "snoozed",
      "resolved",
    ]);
    const now = Date.parse("2026-10-07T12:00:00Z");
    expect(compact(countParams({ range: "30d" }, now))).toEqual({
      from: "2026-09-07T12:00:00.000Z",
    });
    expect(compact(countParams({ range: "1h", resolve_reason: "gone" }, now))).toEqual({
      from: "2026-10-07T11:00:00.000Z",
    });
  });
});

describe("AlertGroupFilters", () => {
  test("puts every filter into the URL, and a new router shows them from it", async () => {
    const { router, screen } = await renderAt("/alert-groups");
    await expect.element(screen.getByRole("option", { name: "k8s" })).toBeInTheDocument();
    await screen.getByLabelText("Routes").selectOptions("k8s");
    await expect.element(screen.getByLabelText("Integrations")).toBeEnabled();
    await expect.element(screen.getByRole("option", { name: "lab" })).toBeInTheDocument();
    await screen.getByLabelText("Integrations").selectOptions("lab");
    await screen.getByLabelText("Severity").selectOptions("critical");
    await screen.getByLabelText("Urgent", { exact: true }).selectOptions("Urgent only");
    await screen.getByLabelText("Resolved by").selectOptions("The system");
    await screen.getByLabelText("Reason").selectOptions("Gone");
    await screen.getByLabelText("Reopened").selectOptions("Reopened at least once");
    await userEvent.type(
      screen.getByRole("textbox", { name: "Label filters" }),
      'namespace="payments"{Enter}',
    );
    await expect
      .element(screen.getByTestId("matcher-chip"))
      .toHaveTextContent('namespace="payments"');
    await userEvent.type(screen.getByRole("textbox", { name: "Label columns" }), "pod{Enter}");

    const expected: AlertGroupSearch = {
      route: ["RT0000000000K8"],
      integration: ["NT0000000000LB"],
      severity: ["critical"],
      urgent: true,
      resolved_by: "system",
      resolve_reason: "gone",
      reopened: true,
      label: ['namespace="payments"'],
      columns: ["pod"],
    };
    await expect.poll(() => searchOf(router)).toEqual(expected);
    // The Matcher was checked by the counts the list reads, with the other filters of the view.
    const checked = requests.find((r) => r.searchParams.getAll("label").length === 1);
    expect(checked?.pathname).toBe("/api/v1/alert-group-counts");
    expect(checked?.searchParams.get("urgent")).toBe("true");

    const href = router.state.location.href;
    await screen.unmount();
    const again = await renderAt(href);
    expect(searchOf(again.router)).toEqual(expected);
    const route = again.screen.getByTestId("filter-route");
    await expect.element(route.getByRole("listitem")).toHaveTextContent("k8s");
    await expect
      .element(again.screen.getByTestId("filter-integration").getByRole("listitem"))
      .toHaveTextContent("lab");
    await expect
      .element(again.screen.getByTestId("filter-severity").getByRole("listitem"))
      .toHaveTextContent("critical");
    await expect.element(again.screen.getByLabelText("Urgent", { exact: true })).toHaveValue("yes");
    await expect.element(again.screen.getByLabelText("Resolved by")).toHaveValue("system");
    await expect.element(again.screen.getByLabelText("Reason")).toHaveValue("gone");
    await expect.element(again.screen.getByLabelText("Reopened")).toHaveValue("yes");
    await expect
      .element(again.screen.getByTestId("matcher-chip"))
      .toHaveTextContent('namespace="payments"');
    await expect.element(again.screen.getByTestId("label-column-chip")).toHaveTextContent("pod");

    // Removing a chip and clearing take the filters out of the URL again.
    await route.getByRole("button", { name: "Remove k8s" }).click();
    await expect.poll(() => searchOf(again.router).route).toBeUndefined();
    await again.screen.getByRole("button", { name: "Clear filters" }).click();
    await expect.poll(() => searchOf(again.router)).toEqual({ columns: ["pod"] });
  });

  test("a Matcher the server refuses leaves the URL as it was", async () => {
    const { router, screen } = await renderAt("/alert-groups?tab=%22firing%22");
    const field = screen.getByRole("textbox", { name: "Label filters" });
    await field.fill('pod=~"["');
    await userEvent.keyboard("{Enter}");
    await expect
      .element(screen.getByTestId("matcher-error"))
      .toHaveTextContent("The regular expression is not valid: missing ]");
    expect(searchOf(router)).toEqual({ tab: "firing" });
    await expect.element(field).toHaveValue('pod=~"["');
    expect(page.getByTestId("matcher-chip").elements()).toHaveLength(0);
  });
});
