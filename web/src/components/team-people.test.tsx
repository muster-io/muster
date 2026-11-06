// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "People" of a Team in a real browser, against a stubbed API (C-25.FR-3, C-25.AC-10): each person with their Role, the
// source — "added by hand" or the identity provider group that matched — and the linked messengers; "Remove" only for
// people added by hand; "Add person" picks a User; a Responder sees the table without the controls that change it, in
// English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { TeamUser, User } from "../api/gen/model";
import i18n from "../i18n";
import { teamUser } from "../test/fixtures/teams";
import { TeamPeople } from "./team-people";

const TEAM = "TM0000000000PA";

const BOB = teamUser();
const OLGA = teamUser({
  user: { id: "SR0000000000OL", name: "olga", login: "olga", deactivated: false },
  sources: ["idp"],
  idp_entries: [{ group: "teams/payments/*" }],
  linked_messengers: [
    { type: "mattermost", connection: { id: "CN0000000000MM", name: "Example Mattermost" } },
    { type: "bridge", connection: { id: "CN0000000000BR", name: "br" } },
  ],
});
const BOTH = teamUser({
  user: { id: "SR0000000000DA", name: "Dana", login: "dana", deactivated: false },
  role: "admin",
  sources: ["by_hand", "idp"],
  idp_entries: [{ claim: "roles", group: "payments-oncall" }],
});

function user(id: string, name: string, login: string): User {
  return {
    id,
    name,
    login,
    role: "responder",
    source: "local",
    sign_in_method: "local",
    status: "active",
    totp_enabled: false,
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
  };
}

function json(body: unknown, status = 200): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

interface Calls {
  added: unknown[];
  removed: string[];
}

/** Stubs the API; the people of the Team change as the calls come. */
function stub(initial: TeamUser[]): Calls {
  const calls: Calls = { added: [], removed: [] };
  let people = initial;
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    if (method === "POST" && url === `/api/v1/teams/${TEAM}/users`) {
      const raw = init?.body;
      const sent: unknown = JSON.parse(typeof raw === "string" ? raw : "{}");
      const userId =
        typeof sent === "object" && sent !== null && "user_id" in sent ? String(sent.user_id) : "";
      calls.added.push({ user_id: userId });
      const added = teamUser({
        user: { id: userId, name: "carol", login: "carol", deactivated: false },
      });
      people = [...people, added];
      return json(added, 201);
    }
    if (method === "DELETE" && url.startsWith(`/api/v1/teams/${TEAM}/users/`)) {
      const id = url.split("/").at(-1) ?? "";
      calls.removed.push(id);
      people = people.filter((p) => p.user.id !== id);
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    if (url.startsWith(`/api/v1/teams/${TEAM}/users`)) {
      return json({ items: people });
    }
    if (url.startsWith("/api/v1/users")) {
      return json({
        items: [user("SR0000000000BB", "bob", "bob"), user("SR0000000000CA", "carol", "carol")],
        next_cursor: null,
      });
    }
    return json({ items: [], next_cursor: null });
  });
  return calls;
}

function renderPeople(canWrite: boolean) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TeamPeople teamId={TEAM} canWrite={canWrite} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

/** Waits until a row's text holds a text, or no longer holds it. */
const hasText = (row: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(row.element().textContent).toContain(text));
const lacksText = (row: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(row.element().textContent).not.toContain(text));

const rows = () => page.getByTestId("team-person");

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("People of a Team", () => {
  test("shows each person with Role, source and messengers, and Remove only for people added by hand", async () => {
    stub([BOB, OLGA, BOTH]);
    await renderPeople(true);
    await expect.element(rows().nth(0)).toBeVisible();
    await hasText(rows().nth(0), "bob");
    await hasText(rows().nth(0), "Responder");
    await hasText(rows().nth(0), "added by hand");
    await hasText(rows().nth(0), "not linked");
    await expect.element(rows().nth(0).getByRole("button", { name: "Remove bob" })).toBeVisible();

    await hasText(rows().nth(1), "group teams/payments/*");
    await hasText(rows().nth(1), "Mattermost · Example Mattermost");
    await hasText(rows().nth(1), "Bridge · br");
    expect(rows().nth(1).getByRole("button").elements()).toHaveLength(0);
    await lacksText(rows().nth(1), "added by hand");

    // A person in the Team both ways keeps Remove, which takes away only the hand-added source.
    await hasText(rows().nth(2), "Admin");
    await hasText(rows().nth(2), "added by hand");
    await hasText(rows().nth(2), "group payments-oncall");
    await expect.element(rows().nth(2).getByRole("button", { name: "Remove Dana" })).toBeVisible();
  });

  test("a Responder sees the people without Add person, Remove and the Actions column", async () => {
    stub([BOB, OLGA]);
    await renderPeople(false);
    await expect.element(rows().nth(0)).toBeVisible();
    expect(page.getByRole("button").elements()).toHaveLength(0);
    expect(
      page
        .getByRole("columnheader")
        .elements()
        .map((e) => e.textContent),
    ).toEqual(["User", "Role", "Source", "Messengers"]);
  });

  test("Add person picks a User and adds them by hand", async () => {
    const calls = stub([BOB]);
    await renderPeople(true);
    await expect.element(rows().nth(0)).toBeVisible();
    await page.getByRole("button", { name: "Add person" }).click();
    const picker = page.getByLabelText("User", { exact: true });
    // Users already added by hand are not offered.
    await vi.waitFor(() =>
      expect([...picker.element().querySelectorAll("option")].map((o) => o.textContent)).toEqual([
        "Choose a user",
        "carol",
      ]),
    );
    await picker.selectOptions("SR0000000000CA");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await vi.waitFor(() => expect(calls.added).toEqual([{ user_id: "SR0000000000CA" }]));
    await hasText(rows().nth(1), "carol");
    await expect.element(page.getByRole("button", { name: "Add person" })).toBeVisible();
  });

  test("Remove takes the person away", async () => {
    const calls = stub([BOB, OLGA]);
    await renderPeople(true);
    await rows().nth(0).getByRole("button", { name: "Remove bob" }).click();
    await vi.waitFor(() => expect(calls.removed).toEqual(["SR0000000000BB"]));
    await vi.waitFor(() => expect(rows().elements()).toHaveLength(1));
    await hasText(rows().nth(0), "olga");
  });

  test("says that nobody is in the Team, and reads in Russian", async () => {
    stub([]);
    await renderPeople(true);
    await expect.element(page.getByText("Nobody is in this Team yet.")).toBeVisible();
    await i18n.changeLanguage("ru");
    await expect.element(page.getByText("В команде пока никого нет.")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Добавить участника" })).toBeVisible();
    expect(
      page
        .getByRole("columnheader")
        .elements()
        .map((e) => e.textContent),
    ).toEqual(["Пользователь", "Роль", "Источник", "Мессенджеры", "Действия"]);
  });

  test("reads the source in Russian", async () => {
    await i18n.changeLanguage("ru");
    stub([BOB, OLGA]);
    await renderPeople(true);
    await hasText(rows().nth(0), "вручную");
    await hasText(rows().nth(0), "Исполнитель");
    await hasText(rows().nth(1), "группа teams/payments/*");
    await expect.element(rows().nth(0).getByRole("button", { name: "Убрать bob" })).toBeVisible();
  });
});
