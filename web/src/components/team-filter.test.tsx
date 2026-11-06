// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The choices of a Team in a real browser, against a stubbed API (C-25.FR-2, C-25.AC-11, C-26.FR-7): the filter "Team"
// with "All", "My Teams" and each Team, whose value goes to the search parameter `team`; the Routes list asks the API
// for it; "Owning Team" with "None", every Team and the hint that it does not affect delivery — in English and Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { team, teamRef } from "../test/fixtures/teams";
import { routesQueryFor } from "./route-list";
import { MY_TEAMS, OwningTeamField, TeamFilter } from "./team-filter";

const PAYMENTS = team({ id: "TM0000000000PA", name: "payments", slug: "payments" });
const SEARCH = team({ id: "TM0000000000SE", name: "search", slug: "search" });

let requested: string[] = [];

beforeEach(async () => {
  await i18n.changeLanguage("en");
  requested = [];
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    requested.push(url);
    const body = url.startsWith("/api/v1/teams")
      ? { items: [PAYMENTS, SEARCH], next_cursor: null }
      : { items: [], next_cursor: null };
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json", ETag: '"1"' },
      }),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function providers(children: React.ReactNode) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        {children}
      </QueryClientProvider>
    </I18nextProvider>
  );
}

const optionsOf = (el: Element) =>
  [...el.querySelectorAll<HTMLOptionElement>("option")].map((o) => [o.value, o.text]);

function FilterHarness({ seen }: { seen: (string | undefined)[] }) {
  const [value, setValue] = useState<string>();
  return (
    <TeamFilter
      value={value}
      onChange={(next) => {
        seen.push(next);
        setValue(next);
      }}
    />
  );
}

describe("the filter by Team", () => {
  test("offers All, My Teams and every Team, and tells the choice", async () => {
    const seen: (string | undefined)[] = [];
    await render(providers(<FilterHarness seen={seen} />));
    const select = page.getByLabelText("Team", { exact: true });
    await vi.waitFor(() =>
      expect(optionsOf(select.element())).toEqual([
        ["", "All"],
        ["mine", "My Teams"],
        ["TM0000000000PA", "payments"],
        ["TM0000000000SE", "search"],
      ]),
    );
    await select.selectOptions("mine");
    await select.selectOptions("TM0000000000PA");
    await select.selectOptions("");
    expect(seen).toEqual([MY_TEAMS, "TM0000000000PA", undefined]);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await render(providers(<FilterHarness seen={[]} />));
    const select = page.getByLabelText("Команда", { exact: true });
    await vi.waitFor(() =>
      expect(optionsOf(select.element()).slice(0, 2)).toEqual([
        ["", "Все"],
        ["mine", "Мои команды"],
      ]),
    );
  });

  test("the Routes list asks the API for the Team, or for mine", async () => {
    const client = new QueryClient();
    await client.fetchQuery(routesQueryFor("mine"));
    await client.fetchQuery(routesQueryFor("TM0000000000PA"));
    await client.fetchQuery(routesQueryFor());
    expect(requested).toEqual([
      "/api/v1/routes?team=mine",
      "/api/v1/routes?team=TM0000000000PA",
      "/api/v1/routes",
    ]);
    expect(routesQueryFor("mine").queryKey).not.toEqual(routesQueryFor().queryKey);
  });
});

describe("Owning Team", () => {
  test("offers None and every Team, keeps a stored owner the list lacks, and tells it does not affect delivery", async () => {
    const owner = teamRef({ id: "TM0000000000GO", name: "gone", slug: "gone" });
    await render(
      providers(
        <OwningTeamField id="owner" value="TM0000000000GO" current={owner} onChange={() => {}} />,
      ),
    );
    const select = page.getByLabelText("Owning Team", { exact: true });
    await vi.waitFor(() =>
      expect(optionsOf(select.element())).toEqual([
        ["", "None"],
        ["TM0000000000GO", "gone"],
        ["TM0000000000PA", "payments"],
        ["TM0000000000SE", "search"],
      ]),
    );
    await expect.element(select).toHaveValue("TM0000000000GO");
    await expect
      .element(page.getByText("For ownership and the filter of the UI; does not affect delivery."))
      .toBeVisible();
  });

  test("tells the change and reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    const seen: string[] = [];
    await render(
      providers(<OwningTeamField id="owner" value="" onChange={(next) => seen.push(next)} />),
    );
    const select = page.getByLabelText("Команда-владелец", { exact: true });
    await vi.waitFor(() => expect(select.element().querySelectorAll("option").length).toBe(3));
    await select.selectOptions("TM0000000000SE");
    expect(seen).toEqual(["TM0000000000SE"]);
    await expect
      .element(page.getByText("Для владения и фильтра в интерфейсе; на доставку не влияет."))
      .toBeVisible();
  });
});
