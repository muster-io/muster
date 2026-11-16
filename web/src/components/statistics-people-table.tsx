// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The table of By people on the statistics page (C-09.FR-28): a row per User or Service account with the Alert Groups
// they acknowledged first, the Alert Groups they resolved, and the median and 95th percentile of the time to
// acknowledge those they acknowledged first, "—" where there is nothing to measure. Every column sorts, with
// `aria-sort` on its header; the row "Unknown" — the Alert Groups acknowledged before Muster recorded who acknowledged
// first — is always last, whatever the sort. A Service account carries a mark: it appears through what it resolved.

import { ArrowDownIcon, ArrowUpDownIcon, ArrowUpIcon } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import type { AlertGroupActorStatisticsItem } from "../api/gen/model";
import { type PeopleColumn, type PeopleSort, nextSort, sortOf } from "../lib/statistics-search";
import { durationText } from "./statistics-chart";
import { cn } from "./ui/utils";

export interface StatisticsPeopleTableProps {
  items: readonly AlertGroupActorStatisticsItem[];
  sort: PeopleSort | undefined;
  onSort: (next: PeopleSort) => void;
  /** Newer statistics are being read; the table shows the last ones meanwhile. */
  busy?: boolean;
}

/** The value a column sorts by; null where there is nothing to measure, which sorts after every number. */
function valueOf(
  item: AlertGroupActorStatisticsItem,
  column: PeopleColumn,
): number | string | null {
  switch (column) {
    case "name":
      return item.actor?.name ?? "";
    case "acknowledged_first":
      return item.acknowledged_first_count;
    case "resolved":
      return item.resolved_count;
    case "ack_median":
      return item.time_to_acknowledge.median_seconds ?? null;
    default:
      return item.time_to_acknowledge.p95_seconds ?? null;
  }
}

function compareValues(a: number | string | null, b: number | string | null): number {
  if (typeof a === "string" && typeof b === "string") {
    return a.localeCompare(b, undefined, { sensitivity: "base" }) || (a < b ? -1 : a > b ? 1 : 0);
  }
  if (typeof a === "number" && typeof b === "number") {
    return a - b;
  }
  return 0;
}

/**
 * The rows in the order of the sort, ties kept in the order the server gave (the most first acknowledgements, then the
 * name); a column without a value, and the row "Unknown", come after the others in both directions.
 */
export function sortPeople(
  items: readonly AlertGroupActorStatisticsItem[],
  sort: PeopleSort | undefined,
): AlertGroupActorStatisticsItem[] {
  const { column, descending } = sortOf(sort);
  const known = items.filter((i) => i.actor !== null);
  const unknown = items.filter((i) => i.actor === null);
  const sorted = known
    .map((item, index) => ({ item, index, value: valueOf(item, column) }))
    .toSorted((a, b) => {
      if (a.value === null || b.value === null) {
        return a.value === b.value ? a.index - b.index : a.value === null ? 1 : -1;
      }
      const order = compareValues(a.value, b.value);
      return order === 0 ? a.index - b.index : descending ? -order : order;
    })
    .map((x) => x.item);
  return [...sorted, ...unknown];
}

function SortHeader({
  column,
  sort,
  onSort,
  id,
  className,
  rowSpan,
  children,
}: {
  column: PeopleColumn;
  sort: PeopleSort | undefined;
  onSort: (next: PeopleSort) => void;
  id: string;
  className: string;
  rowSpan?: number;
  children: string;
}) {
  const current = sortOf(sort);
  const sorted = current.column === column;
  const ariaSort = sorted ? (current.descending ? "descending" : "ascending") : "none";
  const Icon = !sorted ? ArrowUpDownIcon : current.descending ? ArrowDownIcon : ArrowUpIcon;
  return (
    <th id={id} scope="col" rowSpan={rowSpan} aria-sort={ariaSort} className={className}>
      <button
        type="button"
        className={cn(
          "-mx-1 inline-flex items-center gap-1 rounded-md px-1 text-inherit outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
          sorted && "text-foreground",
        )}
        data-testid={`sort-${column}`}
        onClick={() => onSort(nextSort(sort, column))}
      >
        <span>{children}</span>
        <Icon aria-hidden="true" className={cn("size-3.5 shrink-0", !sorted && "opacity-50")} />
      </button>
    </th>
  );
}

export function StatisticsPeopleTable({
  items,
  sort,
  onSort,
  busy = false,
}: StatisticsPeopleTableProps) {
  const { t } = useTranslation();
  const id = useId();
  const head = "px-3 py-2 align-bottom font-medium text-muted-foreground";
  const label = t("statistics.table.byPeople");
  const rows = sortPeople(items, sort);
  return (
    <div
      className="relative overflow-x-auto rounded-lg border outline-none focus-visible:ring-2 focus-visible:ring-ring"
      role="region"
      aria-label={label}
      aria-busy={busy}
      tabIndex={0}
    >
      <table
        className={cn("w-full border-collapse text-left text-sm", busy && "opacity-60")}
        aria-label={label}
        data-testid="statistics-people-table"
      >
        <thead className="bg-muted/50">
          <tr>
            <SortHeader
              column="name"
              sort={sort}
              onSort={onSort}
              id={`${id}-name`}
              rowSpan={2}
              className={cn(head, "min-w-36")}
            >
              {t("statistics.columns.name")}
            </SortHeader>
            <SortHeader
              column="acknowledged_first"
              sort={sort}
              onSort={onSort}
              id={`${id}-first`}
              rowSpan={2}
              className={cn(head, "text-right")}
            >
              {t("statistics.columns.acknowledgedFirst")}
            </SortHeader>
            <SortHeader
              column="resolved"
              sort={sort}
              onSort={onSort}
              id={`${id}-resolved`}
              rowSpan={2}
              className={cn(head, "text-right")}
            >
              {t("statistics.columns.resolved")}
            </SortHeader>
            <th
              id={`${id}-ack`}
              scope="col"
              colSpan={2}
              className={cn(head, "border-l pb-0 text-center whitespace-nowrap")}
            >
              {t("statistics.columns.timeToAcknowledge")}
            </th>
          </tr>
          <tr>
            <SortHeader
              column="ack_median"
              sort={sort}
              onSort={onSort}
              id={`${id}-ack-median`}
              className={cn(head, "border-l text-right font-normal whitespace-nowrap")}
            >
              {t("statistics.columns.median")}
            </SortHeader>
            <SortHeader
              column="ack_p95"
              sort={sort}
              onSort={onSort}
              id={`${id}-ack-p95`}
              className={cn(head, "text-right font-normal whitespace-nowrap")}
            >
              {t("statistics.columns.p95")}
            </SortHeader>
          </tr>
        </thead>
        <tbody>
          {rows.map((item, index) => {
            const rowHeader = `${id}-row-${index}`;
            const actor = item.actor;
            return (
              <tr
                key={actor?.id ?? "unknown"}
                className="border-t align-top"
                data-testid="statistics-people-row"
              >
                <th
                  id={rowHeader}
                  scope="row"
                  headers={`${id}-name`}
                  className="px-3 py-2 font-normal"
                >
                  {actor === null ? (
                    <div className="flex min-w-0 flex-col gap-0.5">
                      <span className="font-medium" data-testid="people-name">
                        {t("statistics.unknown")}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {t("statistics.unknownHint")}
                      </span>
                    </div>
                  ) : (
                    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                      <span className="font-medium wrap-anywhere" data-testid="people-name">
                        {actor.deactivated
                          ? t("alertGroups.deactivated", { name: actor.name })
                          : actor.name}
                      </span>
                      {actor.kind === "service_account" && (
                        <span
                          className="rounded-md border bg-muted px-1.5 py-0.5 text-xs text-muted-foreground"
                          data-testid="people-service-account"
                        >
                          {t("statistics.serviceAccount")}
                        </span>
                      )}
                    </div>
                  )}
                </th>
                <td
                  headers={`${rowHeader} ${id}-first`}
                  className="px-3 py-2 text-right tabular-nums"
                  data-testid="people-first"
                >
                  {item.acknowledged_first_count}
                </td>
                <td
                  headers={`${rowHeader} ${id}-resolved`}
                  className="px-3 py-2 text-right tabular-nums"
                  data-testid="people-resolved"
                >
                  {actor === null ? t("statistics.none") : item.resolved_count}
                </td>
                <td
                  headers={`${rowHeader} ${id}-ack ${id}-ack-median`}
                  className="border-l px-3 py-2 text-right whitespace-nowrap tabular-nums"
                  data-testid="people-median"
                >
                  {durationText(t, item.time_to_acknowledge.median_seconds)}
                </td>
                <td
                  headers={`${rowHeader} ${id}-ack ${id}-ack-p95`}
                  className="px-3 py-2 text-right whitespace-nowrap tabular-nums"
                  data-testid="people-p95"
                >
                  {durationText(t, item.time_to_acknowledge.p95_seconds)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {items.length === 0 && (
        <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
          {t("statistics.emptyPeople")}
        </p>
      )}
    </div>
  );
}
