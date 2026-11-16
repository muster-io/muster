// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The search of the statistics page: every parameter, the defaults, a URL from before the views By team and By people,
// the values the schema cannot read, and the two requests the search maps to.

import { describe, expect, test } from "vitest";

import {
  type StatisticsSearch,
  activeFilterCount,
  nextSort,
  periodParams,
  sortOf,
  statisticsRequest,
  statisticsSearchSchema,
} from "./statistics-search";

const NOW = Date.parse("2026-11-16T12:00:00Z");

describe("statisticsSearchSchema", () => {
  test("reads every parameter", () => {
    const search = statisticsSearchSchema.parse({
      by: "people",
      period: "custom",
      from: "2026-11-01",
      to: "2026-11-10",
      route: ["RT1"],
      integration: ["NT1"],
      severity: ["critical"],
      urgent: true,
      team: "mine",
      reopened: false,
      label: ['namespace="payments"'],
      first_acknowledged_by: "me",
      sort: "-resolved",
    });
    expect(search).toEqual({
      by: "people",
      period: "custom",
      from: "2026-11-01",
      to: "2026-11-10",
      route: ["RT1"],
      integration: ["NT1"],
      severity: ["critical"],
      urgent: true,
      team: "mine",
      reopened: false,
      label: ['namespace="payments"'],
      first_acknowledged_by: "me",
      sort: "-resolved",
    });
  });

  test("an empty URL has no value, so the defaults apply", () => {
    expect(statisticsSearchSchema.parse({})).toEqual({});
  });

  test("a URL from before the views By team and By people opens the same view", () => {
    expect(statisticsSearchSchema.parse({ by: "route", route: ["RT1", "RT2"] })).toEqual({
      by: "route",
      route: ["RT1", "RT2"],
    });
    expect(statisticsSearchSchema.parse({ by: "integration", period: "30d" })).toEqual({
      by: "integration",
      period: "30d",
    });
  });

  test("one value of a list may arrive as a plain string; a number is kept as typed", () => {
    expect(
      statisticsSearchSchema.parse({ route: "RT1", label: "a=b", severity: "info", team: 42 }),
    ).toEqual({ route: ["RT1"], label: ["a=b"], severity: ["info"], team: "42" });
  });

  test("a value the schema cannot read is dropped", () => {
    expect(
      statisticsSearchSchema.parse({
        by: "month",
        period: "1y",
        from: "yesterday",
        severity: ["fatal"],
        urgent: "yes",
        reopened: 3,
        sort: "age",
        route: [],
      }),
    ).toEqual({});
  });
});

describe("filters and period", () => {
  test("the filters are counted by the ones that are set", () => {
    expect(activeFilterCount({})).toBe(0);
    expect(activeFilterCount({ by: "people", period: "30d", sort: "name" })).toBe(0);
    expect(activeFilterCount({ route: ["RT1"], urgent: false, first_acknowledged_by: "me" })).toBe(
      3,
    );
  });

  test("a preset counts back from the instant, the default period is left to the server", () => {
    expect(periodParams({}, "UTC", NOW)).toEqual({});
    expect(periodParams({ period: "7d" }, "UTC", NOW)).toEqual({});
    expect(periodParams({ period: "30d" }, "UTC", NOW)).toEqual({
      from: "2026-10-17T12:00:00.000Z",
    });
  });

  test("a custom period is whole days of the time zone, the last one included", () => {
    expect(
      periodParams(
        { period: "custom", from: "2026-11-01", to: "2026-11-02" },
        "Europe/Berlin",
        NOW,
      ),
    ).toEqual({ from: "2026-11-01T00:00:00.000+01:00", to: "2026-11-03T00:00:00.000+01:00" });
    expect(periodParams({ period: "custom", from: "2026-11-01" }, "UTC", NOW)).toBe("incomplete");
    expect(
      periodParams({ period: "custom", from: "2026-11-05", to: "2026-11-01" }, "UTC", NOW),
    ).toBe("invalid");
  });
});

describe("statisticsRequest", () => {
  const filters: StatisticsSearch = {
    route: ["RT1"],
    integration: ["NT1"],
    severity: ["critical"],
    urgent: true,
    team: "TM1",
    reopened: true,
    label: ['namespace="payments"'],
    first_acknowledged_by: "me",
  };

  test("a view per item takes the filters, the period and the time zone", () => {
    for (const by of ["route", "integration", "team"] as const) {
      expect(statisticsRequest({ ...filters, by, period: "30d" }, "Europe/Berlin", NOW)).toEqual({
        view: by,
        params: {
          ...filters,
          group_by: by,
          time_zone: "Europe/Berlin",
          from: "2026-10-17T12:00:00.000Z",
        },
      });
    }
  });

  test("the route view is the default and sends no unset parameter", () => {
    expect(statisticsRequest({}, "UTC", NOW)).toEqual({
      view: "route",
      params: { group_by: "route", time_zone: "UTC" },
    });
  });

  test("By people takes the same filters and neither group_by nor time_zone", () => {
    expect(statisticsRequest({ ...filters, by: "people", sort: "name" }, "UTC", NOW)).toEqual({
      view: "people",
      params: filters,
    });
  });

  test("a period that cannot be sent is named", () => {
    expect(statisticsRequest({ period: "custom" }, "UTC", NOW)).toBe("incomplete");
    expect(
      statisticsRequest({ period: "custom", from: "2026-11-05", to: "2026-11-01" }, "UTC", NOW),
    ).toBe("invalid");
  });
});

describe("sorting", () => {
  test("the default is the most first acknowledgements first", () => {
    expect(sortOf(undefined)).toEqual({ column: "acknowledged_first", descending: true });
    expect(sortOf("name")).toEqual({ column: "name", descending: false });
    expect(sortOf("-ack_p95")).toEqual({ column: "ack_p95", descending: true });
  });

  test("a click sorts a count most first, a name or a time least first, and a second click reverses", () => {
    expect(nextSort(undefined, "resolved")).toBe("-resolved");
    expect(nextSort("-resolved", "resolved")).toBe("resolved");
    expect(nextSort("resolved", "resolved")).toBe("-resolved");
    expect(nextSort(undefined, "name")).toBe("name");
    expect(nextSort("name", "name")).toBe("-name");
    expect(nextSort(undefined, "ack_median")).toBe("ack_median");
    expect(nextSort(undefined, "acknowledged_first")).toBe("acknowledged_first");
  });
});
