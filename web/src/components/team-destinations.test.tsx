// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Destinations of the Team" in a real browser, against a stubbed API (C-26.FR-1, AC-9): the table of Destination,
// Connection and condition; "Add Destination" and "Remove" and the condition for an Admin, saved as a whole with the
// entries as shown; a refusal at the row it names (an unknown or a duplicated Destination); the line with the Routes
// that deliver to the Teams from the label, or that none does; the read-only page with no buttons and no Route wording
// beyond the line; the Russian texts; and the empty state.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Permission, Route, Session, Team } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { mattermostConnection } from "../test/fixtures/connections";
import { mattermostDestination, webhookDestination } from "../test/fixtures/destinations";
import { team } from "../test/fixtures/teams";
import type { Entry } from "./route-destinations";
import { TeamDestinations } from "./team-destinations";

const CONNECTION = mattermostConnection({ id: "CN000000000MMA", name: "mm-lab" });
const CRITICAL = mattermostDestination({
  id: "DS0000000PAYCR",
  name: "payments-critical",
  connection_id: CONNECTION.id,
  channel_name: "pay-crit",
});
const NOTICE = mattermostDestination({
  id: "DS0000000PAYNO",
  name: "payments-notice",
  connection_id: CONNECTION.id,
  channel_name: "pay-notice",
});
const SEARCH = mattermostDestination({
  id: "DS0000000SEANO",
  name: "search-notice",
  connection_id: CONNECTION.id,
  channel_name: "search",
});
const HOOK = webhookDestination({
  id: "DS0000000HOOK1",
  name: "hook",
  mode: "events",
  events: { url: "http://127.0.0.1:18093/hook", headers: [] },
});

const PAYMENTS = team({
  id: "TM0000000000PA",
  name: "payments",
  slug: "payments",
  destination_entries: [
    { destination_id: CRITICAL.id, severity_condition: "critical" },
    { destination_id: NOTICE.id, severity_condition: "below_critical" },
  ],
});

function route(name: string, deliver: boolean): Route {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the name and the option
  return { id: `RT${name}`, name, deliver_to_teams: deliver } as Route;
}

function reply(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

let routes: Route[];
let routesStatus: number;

beforeEach(async () => {
  await i18n.changeLanguage("en");
  routes = [route("Standalone", false), route("Infrastructure", true)];
  routesStatus = 200;
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/destinations")) {
      return reply({ items: [CRITICAL, NOTICE, SEARCH, HOOK], next_cursor: null });
    }
    if (url.startsWith("/api/v1/connections")) {
      return reply({ items: [CONNECTION], next_cursor: null });
    }
    if (url.startsWith("/api/v1/routes")) {
      return routesStatus === 200
        ? reply({ items: routes })
        : Promise.resolve(
            new Response(
              JSON.stringify({ type: "about:blank", title: "x", status: routesStatus }),
              {
                status: routesStatus,
                headers: { "Content-Type": "application/problem+json" },
              },
            ),
          );
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

async function renderSection(
  props: {
    team?: Team;
    canWrite?: boolean;
    save?: (entries: Entry[]) => Promise<Team>;
    permissions?: Permission[];
  } = {},
) {
  const save =
    props.save ??
    vi.fn((entries: Entry[]) => Promise.resolve({ ...PAYMENTS, destination_entries: entries }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the section reads only the Permissions
      user: { id: "US0000000000ME", name: "Me" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions: props.permissions ?? [
        "teams:read",
        "teams:write",
        "destinations:read",
        "connections:read",
        "routes:read",
      ],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const root = createRootRoute({
    component: () => (
      <TeamDestinations
        team={props.team ?? PAYMENTS}
        canWrite={props.canWrite ?? true}
        save={save}
      />
    ),
  });
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { save };
}

const rows = () => page.getByTestId("team-destination");
const cells = (row: number) =>
  rows()
    .nth(row)
    .getByRole("cell")
    .elements()
    .map((c) => c.textContent?.trim());

describe("the Destinations of a Team", () => {
  test("lists each Destination with its Connection and condition, and the Routes that deliver to the Teams", async () => {
    await renderSection();
    await expect
      .element(rows().first().getByRole("link", { name: "payments-critical" }))
      .toBeVisible();
    await expect
      .element(page.getByTestId("team-destination-condition-DS0000000PAYCR"))
      .toHaveValue("critical");
    await expect
      .element(page.getByTestId("team-destination-condition-DS0000000PAYNO"))
      .toHaveValue("below_critical");
    await vi.waitFor(() => expect(cells(0)[1]).toBe("mm-lab · #pay-crit"));
    expect(cells(1)[1]).toBe("mm-lab · #pay-notice");
    expect(
      page
        .getByRole("columnheader")
        .elements()
        .map((h) => h.textContent?.trim()),
    ).toEqual(["Destination", "Connection", "Condition", "Remove"]);
    await expect
      .element(page.getByTestId("team-destinations-routes"))
      .toHaveTextContent(
        "The Alert Groups of payments arrive here from the Routes that deliver to the Teams from the label: Infrastructure.",
      );
  });

  test("says when no Route delivers to the Teams, and shows a failed read of the Routes", async () => {
    routes = [route("Standalone", false)];
    await renderSection();
    await expect
      .element(page.getByTestId("team-destinations-routes"))
      .toHaveTextContent("No Route delivers to the Teams from the label yet.");
  });

  test("shows why the Routes could not be read", async () => {
    routesStatus = 500;
    await renderSection();
    await expect.element(page.getByRole("alert")).toBeVisible();
    expect(page.getByTestId("team-destinations-routes").elements()).toHaveLength(0);
  });

  test("adds a Destination, changes a condition, removes one, and saves the entries as shown", async () => {
    const { save } = await renderSection();
    await expect.element(rows().first()).toBeVisible();
    await page.getByLabelText("Add Destination").selectOptions(SEARCH.id);
    await expect.element(rows().nth(2).getByRole("link", { name: "search-notice" })).toBeVisible();
    await expect
      .element(page.getByTestId(`team-destination-condition-${SEARCH.id}`))
      .toHaveValue("any");
    await page.getByTestId(`team-destination-condition-${SEARCH.id}`).selectOptions("critical");
    await page.getByRole("button", { name: "Remove payments-critical" }).click();
    expect(rows().elements()).toHaveLength(2);
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() =>
      expect(save).toHaveBeenCalledWith([
        { destination_id: NOTICE.id, severity_condition: "below_critical" },
        { destination_id: SEARCH.id, severity_condition: "critical" },
      ]),
    );
    await expect.element(page.getByTestId("team-destinations-status")).toHaveTextContent("Saved.");
    // A change after the save clears "Saved.".
    await page.getByRole("button", { name: "Remove search-notice" }).click();
    await expect.element(page.getByTestId("team-destinations-status")).toHaveTextContent("");
  });

  test("removes the last Destination and shows the empty text", async () => {
    await renderSection({ team: team({ id: PAYMENTS.id, destination_entries: [] }) });
    await expect
      .element(page.getByTestId("team-destinations-empty"))
      .toHaveTextContent("The Team has no Destinations.");
    await page.getByLabelText("Add Destination").selectOptions(HOOK.id);
    await page.getByRole("button", { name: "Remove hook" }).click();
    await expect.element(page.getByTestId("team-destinations-empty")).toBeVisible();
  });

  test("shows a refusal of an entry at its row, and takes it away when the entries change", async () => {
    const save = vi.fn(() =>
      Promise.reject(
        new ApiError(
          422,
          {
            type: "about:blank",
            title: "Unprocessable",
            status: 422,
            code: "validation_failed",
            errors: [{ pointer: "/destination_entries/1/destination_id", code: "unknown_id" }],
          },
          undefined,
        ),
      ),
    );
    await renderSection({ save });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(rows().nth(1).getByRole("alert"))
      .toHaveTextContent("This Destination no longer exists. Remove it and save again.");
    expect(rows().nth(0).getByRole("alert").elements()).toHaveLength(0);
    // The refusal is at the row, not twice.
    expect(page.getByRole("alert").elements()).toHaveLength(1);
    await page.getByRole("button", { name: "Remove payments-notice" }).click();
    expect(page.getByRole("alert").elements()).toHaveLength(0);
  });

  test("shows a failure that no row names, and says nothing more for a conflict", async () => {
    const failing = vi.fn(() =>
      Promise.reject(
        new ApiError(500, { type: "about:blank", title: "Internal", status: 500 }, undefined),
      ),
    );
    await renderSection({ save: failing });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByRole("alert")).toBeVisible();
  });

  test("a conflict shows no alert of its own: the page tells it", async () => {
    const conflict = vi.fn(() =>
      Promise.reject(
        new ApiError(412, { type: "about:blank", title: "Precondition", status: 412 }, undefined),
      ),
    );
    await renderSection({ save: conflict });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(conflict).toHaveBeenCalled());
    await new Promise((done) => setTimeout(done, 50));
    expect(page.getByRole("alert").elements()).toHaveLength(0);
  });

  test("refuses a Destination named twice before it is sent", async () => {
    const save = vi.fn((entries: Entry[]) =>
      Promise.resolve({ ...PAYMENTS, destination_entries: entries }),
    );
    await renderSection({
      save,
      team: team({
        id: PAYMENTS.id,
        destination_entries: [
          { destination_id: NOTICE.id, severity_condition: "any" },
          { destination_id: NOTICE.id, severity_condition: "critical" },
        ],
      }),
    });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByRole("alert"))
      .toHaveTextContent("This Destination is listed twice.");
    expect(save).not.toHaveBeenCalled();
  });

  test("shows the table alone, with no buttons, to a reader", async () => {
    await renderSection({
      canWrite: false,
      permissions: ["teams:read", "destinations:read", "connections:read", "routes:read"],
    });
    await expect.element(rows().first()).toBeVisible();
    await expect
      .element(page.getByTestId("team-destination-condition-DS0000000PAYCR"))
      .toHaveTextContent("Severity = critical");
    expect(page.getByRole("button").elements()).toHaveLength(0);
    expect(page.getByLabelText("Add Destination").elements()).toHaveLength(0);
    expect(
      page
        .getByRole("columnheader")
        .elements()
        .map((h) => h.textContent?.trim()),
    ).toEqual(["Destination", "Connection", "Condition"]);
  });

  test("does not read the Routes without routes:read, and names the Destinations by id without destinations:read", async () => {
    await renderSection({ canWrite: false, permissions: ["teams:read"] });
    await expect.element(rows().first()).toBeVisible();
    expect(page.getByTestId("team-destinations-routes").elements()).toHaveLength(0);
    expect(cells(0)[0]).toBe("DS0000000PAYCR");
  });

  test("names a Destination the list lacks by the Team's own names, and keeps the row order after a save", async () => {
    const withNames = team({
      id: PAYMENTS.id,
      destination_entries: [
        { destination_id: "DS0000000GONE1", severity_condition: "any" },
        { destination_id: NOTICE.id, severity_condition: "any" },
      ],
      destinations: [
        {
          id: "DS0000000GONE1",
          name: "gone-one",
          type: "mattermost",
          health: { state: "healthy" },
        },
      ],
    });
    const { save } = await renderSection({ team: withNames });
    await expect.element(rows().first()).toBeVisible();
    expect(cells(0)[0]).toBe("gone-one");
    await page.getByTestId(`team-destination-condition-${NOTICE.id}`).selectOptions("critical");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalled());
    await expect.element(page.getByTestId("team-destinations-status")).toHaveTextContent("Saved.");
    expect(cells(0)[0]).toBe("gone-one");
    expect(cells(1)[0]).toBe("payments-notice");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderSection();
    await expect.element(page.getByRole("columnheader", { name: "Место доставки" })).toBeVisible();
    await expect.element(page.getByRole("columnheader", { name: "Подключение" })).toBeVisible();
    await expect.element(page.getByRole("columnheader", { name: "Условие" })).toBeVisible();
    await expect
      .element(page.getByTestId("team-destinations-routes"))
      .toHaveTextContent(
        "Сюда приходят группы команды payments из маршрутов, где отмечено «ещё командам из метки»: Infrastructure.",
      );
    await expect.element(page.getByLabelText("Добавить место доставки")).toBeVisible();
    await expect
      .element(page.getByRole("button", { name: "Убрать payments-critical" }))
      .toBeVisible();
    const condition = page.getByTestId("team-destination-condition-DS0000000PAYCR");
    await expect
      .element(condition.getByRole("option", { name: "важность < critical" }))
      .toBeInTheDocument();
  });
});
