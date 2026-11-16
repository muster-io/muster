// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The view of the statistics page in the URL (C-09.FR-15, C-09.FR-30): the view (`by`), the period, the filters of the
// Alert Group list that every view takes, "Acknowledged first by" and the sorting of By people, as typed search
// parameters named as the Alert Group list and the API name them, so that a view is shared as a link. This module also
// maps the search to the two requests: the statistics per Route, Integration or Team, and the statistics by people.

import { z } from "zod";

import {
  type GetAlertGroupActorStatisticsParams,
  type GetAlertGroupStatisticsParams,
  SeverityLevel,
} from "../api/gen/model";
import { startOfDayIn } from "./time";

/** The views: one item per Route, Integration or Team, or one row per person. */
export const STATISTICS_VIEWS = ["route", "integration", "team", "people"] as const;
export type StatisticsView = (typeof STATISTICS_VIEWS)[number];
export const DEFAULT_VIEW: StatisticsView = "route";

/** The periods; the last 7 days (alert_group.list_range, which the server applies itself) when the URL names none. */
export const PERIODS = ["7d", "30d", "90d", "custom"] as const;
export type Period = (typeof PERIODS)[number];
export const DEFAULT_PERIOD: Period = "7d";
const DAY_MS = 24 * 60 * 60 * 1000;
const PERIOD_DAYS: Record<Exclude<Period, "custom">, number> = { "7d": 7, "30d": 30, "90d": 90 };

/** The columns of By people that sort; with a leading "-" a column sorts descending. */
export const PEOPLE_COLUMNS = [
  "name",
  "acknowledged_first",
  "resolved",
  "ack_median",
  "ack_p95",
] as const;
export type PeopleColumn = (typeof PEOPLE_COLUMNS)[number];
export const PEOPLE_SORTS = [...PEOPLE_COLUMNS, ...PEOPLE_COLUMNS.map((c) => `-${c}` as const)] as [
  PeopleSort,
  ...PeopleSort[],
];
export type PeopleSort = PeopleColumn | `-${PeopleColumn}`;

/** The sorting when the URL names none: the most first acknowledgements first. */
export const DEFAULT_SORT: PeopleSort = "-acknowledged_first";

/** The `first_acknowledged_by` value of the signed-in User. */
export const ME = "me";

/** A list of non-empty strings; one value in the URL may arrive as a plain string. */
const strings = z
  .preprocess((v) => (typeof v === "string" ? [v] : v), z.array(z.string().min(1)).min(1))
  .optional()
  .catch(undefined);

/** A value the URL may have parsed as a number or a boolean, kept as typed. */
const text = z
  .preprocess(
    (v) => (typeof v === "number" || typeof v === "boolean" ? String(v) : v),
    z.string().min(1),
  )
  .optional()
  .catch(undefined);

/** A calendar day as YYYY-MM-DD. */
const day = z
  .string()
  .regex(/^\d{4}-\d{2}-\d{2}$/)
  .optional()
  .catch(undefined);

export const statisticsSearchSchema = z.object({
  by: z.enum(STATISTICS_VIEWS).optional().catch(undefined),
  period: z.enum(PERIODS).optional().catch(undefined),
  from: day,
  to: day,
  route: strings,
  integration: strings,
  severity: z
    .preprocess((v) => (typeof v === "string" ? [v] : v), z.array(z.enum(SeverityLevel)).min(1))
    .optional()
    .catch(undefined),
  urgent: z.boolean().optional().catch(undefined),
  team: text,
  reopened: z.boolean().optional().catch(undefined),
  label: strings,
  first_acknowledged_by: text,
  sort: z.enum(PEOPLE_SORTS).optional().catch(undefined),
});
export type StatisticsSearch = z.infer<typeof statisticsSearchSchema>;

/** The filters every view takes. */
export const STATISTICS_FILTER_KEYS = [
  "route",
  "integration",
  "severity",
  "urgent",
  "team",
  "reopened",
  "label",
  "first_acknowledged_by",
] as const satisfies readonly (keyof StatisticsSearch)[];

/** How many filters are set, for the "Filters" button of a phone. */
export function activeFilterCount(search: StatisticsSearch): number {
  return STATISTICS_FILTER_KEYS.filter((k) => search[k] !== undefined).length;
}

/** The filters of the search as the two operations take them. */
export type StatisticsFilterParams = Pick<
  GetAlertGroupActorStatisticsParams,
  | "route"
  | "integration"
  | "severity"
  | "urgent"
  | "team"
  | "reopened"
  | "label"
  | "first_acknowledged_by"
>;

export function filterParams(search: StatisticsSearch): StatisticsFilterParams {
  return {
    route: search.route,
    integration: search.integration,
    severity: search.severity,
    urgent: search.urgent,
    team: search.team,
    reopened: search.reopened,
    label: search.label,
    first_acknowledged_by: search.first_acknowledged_by,
  };
}

/**
 * The period of the view at an instant: nothing for the default period, which the server counts with its own clock; the
 * start of a preset; whole days of the time zone for a custom period, the last one included. A custom period without
 * both days, or one that does not start before it ends, is not sent.
 */
export function periodParams(
  search: StatisticsSearch,
  timeZone: string,
  now: number,
): { from?: string; to?: string } | "incomplete" | "invalid" {
  const period = search.period ?? DEFAULT_PERIOD;
  if (period === "custom") {
    if (search.from === undefined || search.to === undefined) {
      return "incomplete";
    }
    const from = startOfDayIn(search.from, timeZone);
    const to = startOfDayIn(search.to, timeZone, 1);
    if (from.getTime() >= to.getTime()) {
      return "invalid";
    }
    return { from: from.toISOString(), to: to.toISOString() };
  }
  if (period === DEFAULT_PERIOD) {
    return {};
  }
  return { from: new Date(now - PERIOD_DAYS[period] * DAY_MS).toISOString() };
}

/** The parameters without the unset ones, so that equal views make equal query keys. */
function compact<T extends object>(params: T): T {
  const out = { ...params };
  for (const [key, value] of Object.entries(out)) {
    if (value === undefined) {
      Reflect.deleteProperty(out, key);
    }
  }
  return out;
}

/** What the view asks the server: the statistics per item, or the statistics by people. */
export type StatisticsRequest =
  | { view: "people"; params: GetAlertGroupActorStatisticsParams }
  | { view: Exclude<StatisticsView, "people">; params: GetAlertGroupStatisticsParams };

export function statisticsRequest(
  search: StatisticsSearch,
  timeZone: string,
  now: number,
): StatisticsRequest | "incomplete" | "invalid" {
  const period = periodParams(search, timeZone, now);
  if (typeof period === "string") {
    return period;
  }
  const view = search.by ?? DEFAULT_VIEW;
  if (view === "people") {
    return { view, params: compact({ ...filterParams(search), ...period }) };
  }
  return {
    view,
    params: compact({ ...filterParams(search), ...period, group_by: view, time_zone: timeZone }),
  };
}

/** The column of a sorting and whether it is descending. */
export function sortOf(sort: PeopleSort | undefined): {
  column: PeopleColumn;
  descending: boolean;
} {
  const value = sort ?? DEFAULT_SORT;
  const descending = value.startsWith("-");
  const name = descending ? value.slice(1) : value;
  const column = PEOPLE_COLUMNS.find((c) => c === name) ?? "acknowledged_first";
  return { column, descending };
}

/** The sorting a click on a column gives: the other direction on the sorted column, the first direction otherwise. */
export function nextSort(sort: PeopleSort | undefined, column: PeopleColumn): PeopleSort {
  const current = sortOf(sort);
  if (current.column === column) {
    return current.descending ? column : `-${column}`;
  }
  return column === "acknowledged_first" || column === "resolved" ? `-${column}` : column;
}
