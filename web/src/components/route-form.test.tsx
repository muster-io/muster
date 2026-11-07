// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Route editor's Destinations, its option "Also to the Teams from the label" and the hint under the Group key in a
// real browser, against a stubbed API (C-26.FR-1, FR-2, FR-12, AC-9): a save sends `destination_entries` with the
// conditions, `deliver_to_teams` and `run_team_chains` as the form shows them, the other fields as the Route had them,
// and no `destination_ids`; the checks of the API are repeated before the send (a Destination named twice, more than 100
// entries); a refusal lands at the entry it names; a conflict (412) shows the editor's one banner with Reload; the hint
// under the Group key follows the option and "Add team to the key" ends it; the read-only editor shows the same as text.

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

import type { Permission, Route, RouteInput, Session } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { mattermostConnection } from "../test/fixtures/connections";
import { mattermostDestination } from "../test/fixtures/destinations";
import { RouteForm } from "./route-form";

const CONNECTION = mattermostConnection({ id: "CN000000000MMA", name: "mm-lab" });
const CRITICAL = mattermostDestination({
  id: "DS000000000ICR",
  name: "infra-critical",
  connection_id: CONNECTION.id,
  channel_name: "infra-crit",
});
const NOTICE = mattermostDestination({
  id: "DS000000000INO",
  name: "infra-notice",
  connection_id: CONNECTION.id,
  channel_name: "infra-notice",
});
const OTHER = mattermostDestination({
  id: "DS000000000OTH",
  name: "other",
  connection_id: CONNECTION.id,
  channel_name: "other",
});

function routeOf(overrides: Partial<Route> = {}): Route {
  return {
    id: "RT0000000000IN",
    name: "Infrastructure",
    description: "",
    matchers: [{ label: "category", op: "=", value: "infra" }],
    minimum_severity: "none",
    urgent: false,
    group_key: ["alertname", "system"],
    destination_entries: [
      { destination_id: CRITICAL.id, severity_condition: "critical" },
      { destination_id: NOTICE.id, severity_condition: "below_critical" },
    ],
    destination_ids: [CRITICAL.id, NOTICE.id],
    deliver_to_teams: false,
    run_team_chains: true,
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

function reply(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/destinations")) {
      return reply({ items: [CRITICAL, NOTICE, OTHER], next_cursor: null });
    }
    if (url.startsWith("/api/v1/connections")) {
      return reply({ items: [CONNECTION], next_cursor: null });
    }
    if (url === "/api/v1/organization") {
      return reply({ team_label: "team" });
    }
    if (url.startsWith("/api/v1/teams")) {
      return reply({ items: [], next_cursor: null });
    }
    return reply({ items: [], next_cursor: null });
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

type Save = (input: RouteInput) => Promise<unknown>;

async function renderForm(
  route: Route,
  options: {
    save?: Save;
    readOnly?: boolean;
    base?: RouteInput;
  } = {},
) {
  const save = vi.fn<Save>(options.save ?? (() => Promise.resolve({})));
  const onReload = vi.fn();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const permissions: Permission[] = [
    "routes:read",
    "routes:write",
    "destinations:read",
    "connections:read",
    "teams:read",
  ];
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads only the Permissions
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
  const root = createRootRoute({
    component: () => (
      <RouteForm
        base={options.base ?? route}
        route={route}
        readOnly={options.readOnly}
        submitLabel="Save"
        save={save}
        onReload={onReload}
        onCancel={() => {}}
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
  return { save, onReload };
}

const rows = () => page.getByTestId("route-destination");
const condition = (id: string) => page.getByTestId(`route-destination-condition-${id}`);
const lastInput = (save: ReturnType<typeof vi.fn<Save>>): RouteInput => {
  const call = save.mock.calls.at(-1);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form saves a RouteInput
  return call?.[0] as RouteInput;
};

describe("the Route editor's Destinations and Teams option", () => {
  test("saves the entries, the option and the sub-option as shown, and the other fields as read", async () => {
    const { save } = await renderForm(routeOf());
    await expect.element(rows().first()).toBeVisible();
    await expect.element(condition(CRITICAL.id)).toHaveValue("critical");
    await expect.element(condition(NOTICE.id)).toHaveValue("below_critical");
    await page.getByLabelText("Add a Destination").selectOptions(OTHER.id);
    await condition(OTHER.id).selectOptions("critical");
    await condition(NOTICE.id).selectOptions("any");
    await page.getByTestId("route-deliver-to-teams").click();
    await page.getByTestId("route-run-team-chains").click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce());
    const input = lastInput(save);
    expect(input.destination_entries).toEqual([
      { destination_id: CRITICAL.id, severity_condition: "critical" },
      { destination_id: NOTICE.id, severity_condition: "any" },
      { destination_id: OTHER.id, severity_condition: "critical" },
    ]);
    expect(input.deliver_to_teams).toBe(true);
    expect(input.run_team_chains).toBe(false);
    expect(input).not.toHaveProperty("destination_ids");
    expect(input.group_key).toEqual(["alertname", "system"]);
    expect(input.name).toBe("Infrastructure");
  });

  test("shows the Destinations by name, as the API lists them, though the entries come in another order", async () => {
    // The API gives the entries by Destination id and the Destinations by name.
    const byId = [NOTICE, CRITICAL, OTHER].map((d) => ({
      destination_id: d.id,
      severity_condition: "any" as const,
    }));
    const route = routeOf({
      destination_entries: byId,
      destinations: [CRITICAL, NOTICE, OTHER].map((d) => ({
        id: d.id,
        name: d.name,
        type: d.type,
        health: d.health,
      })),
    });
    const { save } = await renderForm(route);
    await expect.element(rows().first()).toBeVisible();
    expect(
      rows()
        .elements()
        .map((row) => row.querySelector("a")?.textContent),
    ).toEqual(["infra-critical", "infra-notice", "other"]);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce());
    expect(lastInput(save).destination_entries?.map((e) => e.destination_id)).toEqual([
      CRITICAL.id,
      NOTICE.id,
      OTHER.id,
    ]);
  });

  test("starts a Route stored with destination_ids alone with any severity", async () => {
    const stored = routeOf();
    Reflect.deleteProperty(stored, "destination_entries");
    const { save } = await renderForm(stored);
    await expect.element(condition(CRITICAL.id)).toHaveValue("any");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce());
    expect(lastInput(save).destination_entries).toEqual([
      { destination_id: CRITICAL.id, severity_condition: "any" },
      { destination_id: NOTICE.id, severity_condition: "any" },
    ]);
  });

  test("refuses a Destination named twice before sending, at its row", async () => {
    const entries = [
      { destination_id: CRITICAL.id, severity_condition: "any" },
      { destination_id: CRITICAL.id, severity_condition: "critical" },
    ] as const;
    const { save } = await renderForm(routeOf({ destination_entries: [...entries] }));
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .element(rows().nth(1).getByRole("alert"))
      .toHaveTextContent("This Destination is listed twice.");
    expect(save).not.toHaveBeenCalled();
    // Removing one of them ends the refusal and lets the save go.
    await page.getByRole("button", { name: "Remove infra-critical" }).first().click();
    await expect.element(page.getByRole("alert")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce());
  });

  test("refuses more than 100 entries before sending", async () => {
    const many = Array.from({ length: 101 }, (_, i) => ({
      destination_id: `DS${String(i).padStart(12, "0")}`,
      severity_condition: "any" as const,
    }));
    const { save } = await renderForm(routeOf({ destination_entries: many }));
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.element(page.getByRole("alert").first()).toBeVisible();
    expect(save).not.toHaveBeenCalled();
    await expect.element(page.getByLabelText("Add a Destination")).toBeDisabled();
  });

  test("shows a refusal of an entry at the entry the server names", async () => {
    const save = vi.fn<Save>(() =>
      Promise.reject(
        new ApiError(
          422,
          {
            type: "about:blank",
            title: "Unprocessable",
            status: 422,
            code: "validation_failed",
            errors: [{ pointer: "/destination_entries/1/destination_id", code: "duplicate" }],
          },
          undefined,
        ),
      ),
    );
    await renderForm(routeOf(), { save });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .element(rows().nth(1).getByRole("alert"))
      .toHaveTextContent("This Destination is listed twice.");
    expect(rows().nth(0).getByRole("alert").elements()).toHaveLength(0);
    // The refusal is at the row, and no second alert repeats it.
    expect(page.getByRole("alert").elements()).toHaveLength(1);
  });

  test("a conflict shows the one banner with Reload, and Reload asks the page", async () => {
    const save = vi.fn<Save>(() =>
      Promise.reject(
        new ApiError(412, { type: "about:blank", title: "Precondition", status: 412 }, undefined),
      ),
    );
    const { onReload } = await renderForm(routeOf(), { save });
    await expect.element(rows().first()).toBeVisible();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .element(page.getByText("Someone else changed this route. Reload to see the changes."))
      .toBeVisible();
    expect(page.getByRole("alert").elements()).toHaveLength(1);
    await page.getByRole("button", { name: "Reload" }).click();
    expect(onReload).toHaveBeenCalledOnce();
  });

  test("shows the hint under the Group key while the option is on and the key lacks the label", async () => {
    const { save } = await renderForm(routeOf({ deliver_to_teams: true }));
    await expect.element(page.getByTestId("shared-key-hint")).toBeVisible();
    await page.getByRole("button", { name: "Add team to the key" }).click();
    expect(page.getByTestId("shared-key-hint").elements()).toHaveLength(0);
    expect(
      page
        .getByTestId("group-key-label")
        .elements()
        .map((c) => c.textContent?.trim()),
    ).toEqual(["alertname", "system", "team"]);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledOnce());
    expect(lastInput(save).group_key).toEqual(["alertname", "system", "team"]);
    expect(lastInput(save).deliver_to_teams).toBe(true);
  });

  test("shows the Destinations, conditions and the option as text without routes:write", async () => {
    await renderForm(routeOf({ deliver_to_teams: true }), { readOnly: true });
    await expect.element(page.getByTestId("route-read-only")).toBeVisible();
    await expect.element(condition(CRITICAL.id)).toHaveTextContent("Severity = critical");
    await expect.element(condition(NOTICE.id)).toHaveTextContent("Severity < critical");
    await expect.element(page.getByTestId("route-deliver-to-teams")).toHaveTextContent("On");
    await expect.element(page.getByTestId("route-run-team-chains")).toHaveTextContent("On");
    expect(page.getByRole("button", { name: "Save" }).elements()).toHaveLength(0);
    expect(page.getByRole("switch").elements()).toHaveLength(0);
    expect(page.getByTestId("route-delivery-preview").elements()).toHaveLength(0);
  });
});
