// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The view of the Alert Group list in the URL (C-09.FR-13): tab, filters, time range, search, label columns and sort as
// typed search parameters, so that a view is shared as a link. The list route validates them before its page loads,
// so this module stays small: it loads with the application.

import { z } from "zod";

import {
  AgSortParameter,
  type AlertGroupStatus,
  type GetAlertGroupCountsParams,
  type ListAlertGroupsParams,
  ResolveReason,
  ResolverKind,
  SeverityLevel,
} from "../api/gen/model";

/** The status tabs; Open (firing, acknowledged and snoozed) when the URL names none. */
export const ALERT_GROUP_TABS = [
  "open",
  "firing",
  "acknowledged",
  "snoozed",
  "resolved",
  "all",
] as const;
export type AlertGroupTab = (typeof ALERT_GROUP_TABS)[number];

/** The time range presets and the custom range; the last 7 days (alert_group.list_range) when the URL names none. */
export const TIME_RANGES = ["1h", "24h", "7d", "30d", "custom"] as const;
export type TimeRange = (typeof TIME_RANGES)[number];
export const DEFAULT_TIME_RANGE: TimeRange = "7d";

/** The sort when the URL names none: newest start first. */
export const DEFAULT_SORT: AgSortParameter = "-started_at";

const HOUR_MS = 60 * 60 * 1000;
const PRESET_MS: Record<Exclude<TimeRange, "custom">, number> = {
  "1h": HOUR_MS,
  "24h": 24 * HOUR_MS,
  "7d": 7 * 24 * HOUR_MS,
  "30d": 30 * 24 * HOUR_MS,
};

/** A list of non-empty strings; one value in the URL may arrive as a plain string. */
const strings = z
  .preprocess((v) => (typeof v === "string" ? [v] : v), z.array(z.string().min(1)).min(1))
  .optional()
  .catch(undefined);

/** A value the URL may have parsed as a number or a boolean, such as a search for 42, kept as typed. */
const text = z
  .preprocess(
    (v) => (typeof v === "number" || typeof v === "boolean" ? String(v) : v),
    z.string().min(1),
  )
  .optional()
  .catch(undefined);

/** An instant as RFC 3339. */
const instant = z
  .string()
  .refine((v) => !Number.isNaN(Date.parse(v)))
  .optional()
  .catch(undefined);

export const alertGroupSearchSchema = z.object({
  tab: z.enum(ALERT_GROUP_TABS).optional().catch(undefined),
  route: strings,
  integration: strings,
  severity: z
    .preprocess((v) => (typeof v === "string" ? [v] : v), z.array(z.enum(SeverityLevel)).min(1))
    .optional()
    .catch(undefined),
  urgent: z.boolean().optional().catch(undefined),
  resolved_by: z.enum(ResolverKind).optional().catch(undefined),
  resolve_reason: z.enum(ResolveReason).optional().catch(undefined),
  reopened: z.boolean().optional().catch(undefined),
  label: strings,
  range: z.enum(TIME_RANGES).optional().catch(undefined),
  from: instant,
  to: instant,
  q: text,
  sort: z.enum(AgSortParameter).optional().catch(undefined),
  columns: strings,
});
export type AlertGroupSearch = z.infer<typeof alertGroupSearchSchema>;

/** The filters of the panel; the tab, range, search, sort and columns are not among them. */
export const FILTER_KEYS = [
  "route",
  "integration",
  "severity",
  "urgent",
  "resolved_by",
  "resolve_reason",
  "reopened",
  "label",
] as const satisfies readonly (keyof AlertGroupSearch)[];

/** How many filters of the panel are set, for the "Filters" button of a phone. */
export function activeFilterCount(search: AlertGroupSearch): number {
  return FILTER_KEYS.filter((k) => search[k] !== undefined).length;
}

/** The statuses a tab lists; undefined for Open, which is the API's default. */
export function tabStatuses(tab: AlertGroupTab): AlertGroupStatus[] | undefined {
  switch (tab) {
    case "open":
      return undefined;
    case "all":
      return ["firing", "acknowledged", "snoozed", "resolved"];
    default:
      return [tab];
  }
}

/**
 * The time range of the view at an instant: nothing for the default, which the server applies with its own clock; the
 * start of a preset; the bounds of a custom range.
 */
export function rangeParams(search: AlertGroupSearch, now: number): { from?: string; to?: string } {
  const range = search.range ?? DEFAULT_TIME_RANGE;
  if (range === "custom") {
    return { from: search.from, to: search.to };
  }
  if (range === DEFAULT_TIME_RANGE) {
    return {};
  }
  return { from: new Date(now - PRESET_MS[range]).toISOString() };
}

/** The filters that the list and the counts share, at an instant for the presets of the time range. */
export function countParams(search: AlertGroupSearch, now: number): GetAlertGroupCountsParams {
  const q = search.q?.trim();
  return {
    route: search.route,
    integration: search.integration,
    severity: search.severity,
    urgent: search.urgent,
    resolved_by: search.resolved_by,
    resolve_reason: search.resolved_by === "system" ? search.resolve_reason : undefined,
    reopened: search.reopened,
    label: search.label,
    ...rangeParams(search, now),
    q: q ? q : undefined,
  };
}

/** The request of the first page of the list for the view of the URL. */
export function listParams(search: AlertGroupSearch, now: number): ListAlertGroupsParams {
  return {
    ...countParams(search, now),
    status: tabStatuses(search.tab ?? "open"),
    sort: search.sort,
    label_columns: search.columns,
  };
}

/** The parameters without the unset ones, so that equal views make equal query keys. */
export function compact<T extends object>(params: T): T {
  const out = { ...params };
  for (const [key, value] of Object.entries(out)) {
    if (value === undefined) {
      Reflect.deleteProperty(out, key);
    }
  }
  return out;
}
