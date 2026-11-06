// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The page of a Team in a real browser, against a stubbed API (C-25.FR-3, FR-4, FR-9): every save sends the version the
// page was read at as If-Match, with the other fields as that version has them, so that a Team another tab changed is
// never overwritten under a fresh version; the page says that a newer version exists, and Reload mounts the sections
// again from it; Delete Team sends the version too and offers Reload on a conflict; a failed read of the Team keeps what
// was read and says why; the Working hours and the groups are checked before they are sent.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import { getGetTeamQueryKey } from "../api/gen/endpoints/teams/teams";
import type { Permission, Session, Team } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { team } from "../test/fixtures/teams";
import { TeamView } from "../routes/teams.$teamId";

const ID = "TM0000000000PA";

interface Server {
  team: Team;
  getStatus: number;
  putStatus: number;
  deleteStatus: number;
  puts: { ifMatch: string | null; body: Record<string, unknown> }[];
  deletes: (string | null)[];
}

let server: Server;

function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

function problem(status: number) {
  return { type: "about:blank", title: `HTTP ${status}`, status };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  server = {
    team: team({ id: ID, description: "a", etag: '"1"' }),
    getStatus: 200,
    putStatus: 200,
    deleteStatus: 204,
    puts: [],
    deletes: [],
  };
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    if (url === `/api/v1/teams/${ID}`) {
      if (method === "PUT") {
        const raw = init?.body;
        const body: unknown = JSON.parse(typeof raw === "string" ? raw : "{}");
        server.puts.push({
          ifMatch: new Headers(init?.headers).get("if-match"),
          body: typeof body === "object" && body !== null ? { ...body } : {},
        });
        if (server.putStatus !== 200) {
          return Promise.resolve(reply(server.putStatus, problem(server.putStatus)));
        }
        server.team = { ...server.team, ...(typeof body === "object" ? body : {}), etag: '"9"' };
        return Promise.resolve(reply(200, server.team));
      }
      if (method === "DELETE") {
        server.deletes.push(new Headers(init?.headers).get("if-match"));
        return Promise.resolve(
          server.deleteStatus === 204
            ? new Response(null, { status: 204 })
            : reply(server.deleteStatus, problem(server.deleteStatus)),
        );
      }
      return Promise.resolve(
        server.getStatus === 200
          ? reply(200, server.team)
          : reply(server.getStatus, problem(server.getStatus)),
      );
    }
    return Promise.resolve(reply(200, { items: [], next_cursor: null }));
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

async function renderView() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const permissions: Permission[] = ["teams:read", "teams:write"];
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
  const root = createRootRoute({ component: () => <TeamView teamId={ID} /> });
  const list = createRoute({ getParentRoute: () => root, path: "/teams", component: () => null });
  const router = createRouter({
    routeTree: root.addChildren([list]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  await expect.element(page.getByLabelText("Description", { exact: true })).toHaveValue("a");
  return { queryClient, router };
}

/** Another tab changed the Team: the server has a newer version, which the page reads again. */
async function otherTabChanges(queryClient: QueryClient) {
  server.team = { ...server.team, description: "b", etag: '"2"' };
  await queryClient.invalidateQueries({ queryKey: getGetTeamQueryKey(ID) });
}

/** Waits until the text of an element holds a text. */
const hasText = (el: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(el.element().textContent).toContain(text));

/** Clicks an element of the dialog in the page itself: the browser's hit test finds the inert backdrop of the dialog. */
const press = (el: { element: () => Element }) => {
  const target = el.element();
  if (target instanceof HTMLElement) {
    target.click();
  }
};

const hours = () => page.getByTestId("team-hours-section");

describe("the page of a Team", () => {
  test("a save sends the version the page was read at and the fields as it had them, never the newer ones", async () => {
    const { queryClient } = await renderView();
    await otherTabChanges(queryClient);
    await hasText(
      page.getByTestId("team-stale"),
      "Someone else changed this Team. Reload to see the changes.",
    );
    // The description field still shows what was read; a save of the Working hours must not send "b" either.
    await expect.element(page.getByLabelText("Description", { exact: true })).toHaveValue("a");
    server.putStatus = 412;
    await hours().getByRole("checkbox", { name: "Own" }).click();
    await hours().getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(server.puts).toHaveLength(1));
    expect(server.puts[0]?.ifMatch).toBe('"1"');
    expect(server.puts[0]?.body).toMatchObject({ description: "a", own_working_hours: true });

    // Reload mounts the sections again from the newer version, which the next save sends.
    await page.getByTestId("team-stale").getByRole("button", { name: "Reload" }).click();
    await expect.element(page.getByLabelText("Description", { exact: true })).toHaveValue("b");
    await expect.element(page.getByTestId("team-stale")).not.toBeInTheDocument();
    const own = hours().getByRole("checkbox", { name: "Own" }).element();
    expect(own instanceof HTMLInputElement && own.checked).toBe(false);
    server.putStatus = 200;
    await hours().getByRole("checkbox", { name: "Own" }).click();
    await hours().getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(server.puts).toHaveLength(2));
    expect(server.puts[1]?.ifMatch).toBe('"2"');
    expect(server.puts[1]?.body).toMatchObject({ description: "b" });
    await hasText(page.getByTestId("team-hours-status"), "Saved.");
    // Its own save is the base now: the next one sends the version it brought, and no banner shows.
    expect(page.getByTestId("team-stale").elements()).toHaveLength(0);
  });

  test("a refusal of a save over a newer version shows the banner with Reload", async () => {
    await renderView();
    server.putStatus = 412;
    await page.getByLabelText("Description", { exact: true }).fill("c");
    await page.getByTestId("team-details").getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByTestId("team-stale")).toBeVisible();
    expect(server.puts[0]?.ifMatch).toBe('"1"');
  });

  test("Delete Team sends the version, and a conflict offers Reload", async () => {
    await renderView();
    server.deleteStatus = 412;
    await page.getByRole("button", { name: "Delete Team" }).click();
    const dialog = page.getByRole("dialog");
    await hasText(dialog, "Delete Team Payments?");
    press(dialog.getByRole("button", { name: "Delete Team" }));
    await hasText(dialog, "Someone else changed this Team. Reload to see the changes.");
    expect(server.deletes).toEqual(['"1"']);
    press(dialog.getByRole("button", { name: "Reload" }));
    await expect.element(page.getByRole("dialog")).not.toBeInTheDocument();
  });

  test("Delete Team removes the Team and leaves for the list", async () => {
    const { router } = await renderView();
    await page.getByRole("button", { name: "Delete Team" }).click();
    press(page.getByRole("dialog").getByRole("button", { name: "Delete Team" }));
    await vi.waitFor(() => expect(server.deletes).toEqual(['"1"']));
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/teams"));
  });

  test("a Team that is gone keeps what was read and says why", async () => {
    const { queryClient } = await renderView();
    server.getStatus = 404;
    await queryClient.invalidateQueries({ queryKey: getGetTeamQueryKey(ID) });
    await hasText(page.getByTestId("team-read-error"), "It no longer exists. Reload the page.");
    await expect.element(page.getByLabelText("Description", { exact: true })).toHaveValue("a");
  });

  test("Working hours that end before they start, or no day, are not sent", async () => {
    await renderView();
    await hours().getByRole("checkbox", { name: "Own" }).click();
    await userEvent.fill(hours().getByLabelText("To", { exact: true }), "08:00");
    await hours().getByRole("button", { name: "Save", exact: true }).click();
    await hasText(hours(), "The end must be later than the start.");
    expect(
      hours().getByLabelText("To", { exact: true }).element().getAttribute("aria-invalid"),
    ).toBe("true");
    for (const day of ["Mon", "Tue", "Wed", "Thu", "Fri"]) {
      await hours().getByRole("checkbox", { name: day }).click();
    }
    await userEvent.fill(hours().getByLabelText("To", { exact: true }), "18:00");
    await hours().getByRole("button", { name: "Save", exact: true }).click();
    await hasText(hours(), "Fill in this field.");
    expect(server.puts).toHaveLength(0);
  });

  test("a group that is empty or longer than 200 characters is not sent", async () => {
    await renderView();
    const idp = page.getByTestId("team-idp");
    await idp.getByRole("button", { name: "Add", exact: true }).click();
    await idp.getByRole("button", { name: "Save", exact: true }).click();
    await hasText(idp, "Fill in this field.");
    await userEvent.fill(
      idp.getByLabelText("Identity provider group 1", { exact: true }),
      "g".repeat(201),
    );
    await idp.getByRole("button", { name: "Save", exact: true }).click();
    await hasText(idp, "This value is too long.");
    expect(server.puts).toHaveLength(0);
    await userEvent.fill(
      idp.getByLabelText("Identity provider group 1", { exact: true }),
      "teams/*",
    );
    await idp.getByRole("button", { name: "Save", exact: true }).click();
    await vi.waitFor(() => expect(server.puts).toHaveLength(1));
    expect(server.puts[0]?.body).toMatchObject({ idp_entries: [{ group: "teams/*" }] });
  });
});
