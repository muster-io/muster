// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The statistics table in a real browser: durations as the pages show them, "—" where there is nothing to measure,
// the days of an item under its row as bars with the counts and medians beside them, and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { AlertGroupStatisticsItem } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { barPercent, durationText } from "./statistics-chart";
import { StatisticsTable } from "./statistics-table";

const NONE = { count: 0, median_seconds: null, p95_seconds: null };

/** The Route "st" of the story: three Alert Groups resolved after 10, 20 and 30 minutes, on one day. */
const ST: AlertGroupStatisticsItem = {
  subject: { id: "RT00000000000S", name: "st" },
  alert_group_count: 3,
  time_to_acknowledge: NONE,
  time_to_resolve: { count: 3, median_seconds: 1200, p95_seconds: 1740 },
  per_day: [
    {
      date: "2026-10-07",
      alert_group_count: 3,
      time_to_acknowledge: NONE,
      time_to_resolve: { count: 3, median_seconds: 1200, p95_seconds: 1740 },
    },
  ],
};

/** A Route without Alert Groups in the period. */
const QUIET: AlertGroupStatisticsItem = {
  subject: { id: "RT00000000000Q", name: "quiet" },
  alert_group_count: 0,
  time_to_acknowledge: NONE,
  time_to_resolve: NONE,
  per_day: [],
};

async function renderTable(items: AlertGroupStatisticsItem[], empty = "Nothing") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <StatisticsTable groupBy="route" items={items} empty={empty} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const row = (name: string) => page.getByTestId("statistics-row").filter({ hasText: name });

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("durationText", () => {
  test("formats durations as the pages show intervals, and nothing as a dash", () => {
    expect(durationText(i18n.t, 1200)).toBe("20 min");
    expect(durationText(i18n.t, 1740)).toBe("29 min");
    expect(durationText(i18n.t, 45)).toBe("45 s");
    expect(durationText(i18n.t, 5400)).toBe("1 h 30 min");
    expect(durationText(i18n.t, null)).toBe("—");
    expect(durationText(i18n.t, undefined)).toBe("—");
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    expect(durationText(i18n.t, 1200)).toBe("20 мин");
    expect(durationText(i18n.t, 5400)).toBe("1 ч 30 мин");
    expect(durationText(i18n.t, null)).toBe("—");
  });
});

describe("barPercent", () => {
  test("scales to the longest bar and keeps a sliver for a small count", () => {
    expect(barPercent(3, 3)).toBe(100);
    expect(barPercent(1, 4)).toBe(25);
    expect(barPercent(1, 1000)).toBe(2);
    expect(barPercent(0, 5)).toBe(0);
  });
});

describe("StatisticsTable", () => {
  test("shows the count and the median and 95th percentile, with a dash without data", async () => {
    await renderTable([QUIET, ST]);
    await expect.element(page.getByRole("table", { name: "Statistics by route" })).toBeVisible();
    await expect.element(page.getByRole("columnheader", { name: "Time to resolve" })).toBeVisible();
    await expect.element(row("st").getByTestId("statistics-count")).toHaveTextContent("3");
    await expect
      .element(row("st").getByTestId("statistics-resolve-median"))
      .toHaveTextContent("20 min");
    await expect
      .element(row("st").getByTestId("statistics-resolve-p95"))
      .toHaveTextContent("29 min");
    await expect.element(row("st").getByTestId("statistics-ack-median")).toHaveTextContent("—");
    await expect.element(row("quiet").getByTestId("statistics-count")).toHaveTextContent("0");
    await expect.element(row("quiet").getByTestId("statistics-resolve-p95")).toHaveTextContent("—");
  });

  test("a duration cell names its row and both of its headers", async () => {
    await renderTable([ST]);
    const cell = row("st").getByTestId("statistics-resolve-median").element();
    const ids = (cell.getAttribute("headers") ?? "").split(" ");
    const texts = ids.map((id) => document.getElementById(id)?.textContent);
    expect(texts).toEqual(["st", "Time to resolve", "Median"]);
  });

  test("the name opens the days of the item, with a bar, the count and the medians", async () => {
    await renderTable([ST, QUIET]);
    const open = row("st").getByRole("button", { name: "st" });
    await expect.element(open).toHaveAttribute("aria-expanded", "false");
    await expect.element(page.getByTestId("statistics-days")).not.toBeInTheDocument();
    await open.click();
    await expect.element(open).toHaveAttribute("aria-expanded", "true");
    const days = page.getByRole("table", { name: "Alert Groups per day of start: st" });
    await expect.element(days).toBeVisible();
    const day = days.getByTestId("statistics-day");
    await expect.element(day.getByRole("rowheader")).toHaveTextContent("Oct 7, 2026");
    await expect.element(day.getByTestId("day-count")).toHaveTextContent("3");
    await expect.element(day.getByTestId("day-resolve")).toHaveTextContent("20 min");
    expect(day.element().querySelector("rect")?.getAttribute("width")).toBe("100");
    expect(day.element().querySelector("[style]")).toBeNull();

    await row("quiet").getByRole("button", { name: "quiet" }).click();
    await expect
      .element(page.getByTestId("statistics-days-empty"))
      .toHaveTextContent("No Alert Groups started in this period.");

    await open.click();
    await expect.element(days).not.toBeInTheDocument();
  });

  test("shows the empty text without items", async () => {
    await renderTable([], "There are no routes to show.");
    await expect
      .element(page.getByRole("status"))
      .toHaveTextContent("There are no routes to show.");
  });

  test("in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderTable([ST]);
    await expect
      .element(page.getByRole("table", { name: "Статистика по маршрутам" }))
      .toBeVisible();
    await expect
      .element(page.getByRole("columnheader", { name: "Время до закрытия" }))
      .toBeVisible();
    await expect
      .element(page.getByRole("columnheader", { name: "95-й перцентиль" }).first())
      .toBeVisible();
    await expect
      .element(row("st").getByTestId("statistics-resolve-median"))
      .toHaveTextContent("20 мин");
    await row("st").getByRole("button", { name: "st" }).click();
    await expect
      .element(page.getByTestId("statistics-day").getByRole("rowheader"))
      .toHaveTextContent("7 окт. 2026 г.");
  });
});
