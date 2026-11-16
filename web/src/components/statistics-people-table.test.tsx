// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// By people in a real browser (C-09.FR-28, C-09.AC-28): the rows of the data of the acceptance criterion, the mark of a
// Service account and "(deactivated)", the sorting of every column with `aria-sort` on its header, the row "Unknown"
// always last, the empty text and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AlertGroupActorStatisticsItem } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import type { PeopleSort } from "../lib/statistics-search";
import { acceptanceRows, actor, personItem } from "../test/fixtures/statistics";
import { StatisticsPeopleTable, sortPeople } from "./statistics-people-table";

const UNKNOWN = personItem({
  actor: null,
  acknowledged_first_count: 1,
  time_to_acknowledge: { count: 1, median_seconds: 60, p95_seconds: 60 },
});

/** The table with the sort kept as the page keeps it; `sorts` records every sort a click asked for. */
function Page({
  items,
  sorts,
  initial,
}: {
  items: AlertGroupActorStatisticsItem[];
  sorts: (PeopleSort | undefined)[];
  initial?: PeopleSort;
}) {
  const [sort, setSort] = useState<PeopleSort | undefined>(initial);
  return (
    <StatisticsPeopleTable
      items={items}
      sort={sort}
      onSort={(next) => {
        sorts.push(next);
        setSort(next);
      }}
    />
  );
}

async function renderTable(
  items: AlertGroupActorStatisticsItem[],
  initial?: PeopleSort,
): Promise<(PeopleSort | undefined)[]> {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const sorts: (PeopleSort | undefined)[] = [];
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Page items={items} sorts={sorts} initial={initial} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return sorts;
}

const rows = () => page.getByTestId("statistics-people-row");
const cellsOf = (i: number) =>
  ["people-first", "people-resolved", "people-median", "people-p95"].map(
    (id) => rows().nth(i).getByTestId(id).element().textContent,
  );
const header = (name: string) => page.getByRole("columnheader", { name, exact: true });
/** The names in the order of the rows. */
const names = () =>
  Array.from(document.querySelectorAll('[data-testid="people-name"]')).map((e) => e.textContent);

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("sortPeople", () => {
  test("keeps the order of the server on ties and puts a missing value and Unknown last", () => {
    const items = [...acceptanceRows(), UNKNOWN];
    const order = (sort: PeopleSort | undefined) =>
      sortPeople(items, sort).map((i) => i.actor?.name ?? "unknown");
    expect(order(undefined)).toEqual(["alice", "bob", "terraform", "unknown"]);
    expect(order("acknowledged_first")).toEqual(["terraform", "bob", "alice", "unknown"]);
    expect(order("-resolved")).toEqual(["bob", "terraform", "alice", "unknown"]);
    expect(order("resolved")).toEqual(["alice", "terraform", "bob", "unknown"]);
    expect(order("ack_median")).toEqual(["alice", "bob", "terraform", "unknown"]);
    expect(order("-ack_median")).toEqual(["bob", "alice", "terraform", "unknown"]);
    expect(order("-name")).toEqual(["terraform", "bob", "alice", "unknown"]);
    expect(order("name")).toEqual(["alice", "bob", "terraform", "unknown"]);
  });

  test("sorts names without regard to case", () => {
    const items = ["bob", "Alice", "carol"].map((name, i) =>
      personItem({ actor: actor({ id: `SR000000000${i}`, name }) }),
    );
    expect(sortPeople(items, "name").map((i) => i.actor?.name)).toEqual(["Alice", "bob", "carol"]);
  });
});

describe("StatisticsPeopleTable", () => {
  test("shows the data of the acceptance criterion, first by first acknowledgements", async () => {
    await renderTable(acceptanceRows());
    await expect.element(page.getByRole("table", { name: "Statistics by people" })).toBeVisible();
    for (const name of ["Name", "Acknowledged first", "Resolved", "Time to acknowledge"]) {
      await expect.element(page.getByRole("columnheader", { name })).toBeVisible();
    }
    await expect.element(page.getByRole("columnheader", { name: "Median" })).toBeVisible();
    await expect.element(page.getByRole("columnheader", { name: "95th percentile" })).toBeVisible();
    expect(names()).toEqual(["alice", "bob", "terraform"]);
    expect(cellsOf(0)).toEqual(["3", "0", "4 min", "27 min"]);
    expect(cellsOf(1)).toEqual(["1", "2", "10 min", "10 min"]);
    expect(cellsOf(2)).toEqual(["0", "1", "—", "—"]);
  });

  test("marks a Service account and a deleted person", async () => {
    await renderTable([
      ...acceptanceRows(),
      personItem({ actor: actor({ id: "SR0000000000CA", name: "carol", deactivated: true }) }),
    ]);
    await expect
      .element(rows().nth(2).getByTestId("people-service-account"))
      .toHaveTextContent("Service account");
    await expect
      .element(rows().nth(0).getByTestId("people-service-account"))
      .not.toBeInTheDocument();
    expect(names()).toContain("carol (deactivated)");
  });

  test("a click sorts a column, a second click reverses it, and every column has aria-sort", async () => {
    const sorts = await renderTable(acceptanceRows());
    await expect.element(header("Acknowledged first")).toHaveAttribute("aria-sort", "descending");
    for (const name of ["Name", "Resolved", "Median", "95th percentile"]) {
      await expect.element(header(name)).toHaveAttribute("aria-sort", "none");
    }
    await page.getByTestId("sort-resolved").click();
    expect(names()).toEqual(["bob", "terraform", "alice"]);
    await expect.element(header("Resolved")).toHaveAttribute("aria-sort", "descending");
    await expect.element(header("Acknowledged first")).toHaveAttribute("aria-sort", "none");
    await page.getByTestId("sort-resolved").click();
    expect(names()).toEqual(["alice", "terraform", "bob"]);
    await expect.element(header("Resolved")).toHaveAttribute("aria-sort", "ascending");
    await page.getByTestId("sort-name").click();
    expect(names()).toEqual(["alice", "bob", "terraform"]);
    await page.getByTestId("sort-ack_p95").click();
    await expect.element(header("95th percentile")).toHaveAttribute("aria-sort", "ascending");
    expect(sorts).toEqual(["-resolved", "resolved", "name", "ack_p95"]);
  });

  test("the row Unknown is last whatever the sort, with its hint and a dash under Resolved", async () => {
    await renderTable([UNKNOWN, ...acceptanceRows()]);
    expect(names()).toEqual(["alice", "bob", "terraform", "Unknown"]);
    expect(rows().nth(3).element().textContent).toContain(
      "Acknowledged before Muster recorded who acknowledged first.",
    );
    await expect.element(rows().nth(3).getByTestId("people-resolved")).toHaveTextContent("—");
    await expect.element(rows().nth(3).getByTestId("people-first")).toHaveTextContent("1");
    for (const sort of [
      "sort-resolved",
      "sort-resolved",
      "sort-name",
      "sort-name",
      "sort-ack_median",
    ]) {
      await page.getByTestId(sort).click();
      expect(names().at(-1)).toBe("Unknown");
    }
  });

  test("says that nobody acknowledged or resolved without rows", async () => {
    await renderTable([]);
    await expect
      .element(page.getByRole("status"))
      .toHaveTextContent("Nobody acknowledged or resolved an Alert Group in this period.");
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderTable([...acceptanceRows(), UNKNOWN]);
    await expect.element(page.getByRole("table", { name: "Статистика по людям" })).toBeVisible();
    for (const name of ["Имя", "Подтверждено первым", "Закрыто", "Время до подтверждения"]) {
      await expect.element(page.getByRole("columnheader", { name })).toBeVisible();
    }
    await expect
      .element(rows().nth(2).getByTestId("people-service-account"))
      .toHaveTextContent("Сервисный аккаунт");
    expect(rows().nth(3).element().textContent).toContain("Неизвестно");
    expect(rows().nth(3).element().textContent).toContain(
      "Подтверждено до того, как Muster начал записывать, кто подтвердил первым.",
    );
    await expect.element(rows().nth(0).getByTestId("people-median")).toHaveTextContent("4 мин");
  });
});
