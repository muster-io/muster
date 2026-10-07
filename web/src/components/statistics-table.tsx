// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The table of the statistics page (C-09.FR-15, C-10.AC-14): a row per Route or Integration with its number of Alert
// Groups, how many of them were acknowledged (the ones the time to acknowledge measures), and the median and 95th
// percentile of time to acknowledge and time to resolve, "—" where there is nothing to measure.
// The name opens the item's days below its row. The duration cells name both of their headers, so a screen reader
// reads "Time to resolve, Median" and not only "Median".

import { ChevronDownIcon, ChevronRightIcon } from "lucide-react";
import { Fragment, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import type { AlertGroupStatisticsItem, DurationStats } from "../api/gen/model";
import { StatisticsChart, durationText } from "./statistics-chart";
import { cn } from "./ui/utils";

export type StatisticsSubject = "route" | "integration";

export interface StatisticsTableProps {
  groupBy: StatisticsSubject;
  items: readonly AlertGroupStatisticsItem[];
  /** The text when there are no items. */
  empty: string;
  /** Newer statistics are being read; the table shows the last ones meanwhile. */
  busy?: boolean;
}

const MEASURES = ["ack", "resolve"] as const;
type Measure = (typeof MEASURES)[number];

function measureOf(item: AlertGroupStatisticsItem, measure: Measure): DurationStats {
  return measure === "ack" ? item.time_to_acknowledge : item.time_to_resolve;
}

export function StatisticsTable({ groupBy, items, empty, busy = false }: StatisticsTableProps) {
  const { t } = useTranslation();
  const id = useId();
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set());
  const toggle = (subject: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(subject)) {
        next.delete(subject);
      } else {
        next.add(subject);
      }
      return next;
    });
  const measureTitle = (m: Measure) =>
    m === "ack" ? t("statistics.columns.timeToAcknowledge") : t("statistics.columns.timeToResolve");
  const head = "px-3 py-2 align-bottom font-medium text-muted-foreground";
  const label =
    groupBy === "route" ? t("statistics.table.byRoute") : t("statistics.table.byIntegration");
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
        data-testid="statistics-table"
      >
        <thead className="bg-muted/50">
          <tr>
            <th id={`${id}-name`} scope="col" rowSpan={2} className={cn(head, "min-w-36")}>
              {groupBy === "route"
                ? t("statistics.columns.route")
                : t("statistics.columns.integration")}
            </th>
            <th id={`${id}-count`} scope="col" rowSpan={2} className={cn(head, "text-right")}>
              {t("statistics.columns.alertGroups")}
            </th>
            <th id={`${id}-acked`} scope="col" rowSpan={2} className={cn(head, "text-right")}>
              {t("statistics.columns.acknowledged")}
            </th>
            {MEASURES.map((m) => (
              <th
                key={m}
                id={`${id}-${m}`}
                scope="col"
                colSpan={2}
                className={cn(head, "border-l pb-0 text-center whitespace-nowrap")}
              >
                {measureTitle(m)}
              </th>
            ))}
          </tr>
          <tr>
            {MEASURES.map((m) => (
              <Fragment key={m}>
                <th
                  id={`${id}-${m}-median`}
                  scope="col"
                  className={cn(head, "border-l text-right font-normal whitespace-nowrap")}
                >
                  {t("statistics.columns.median")}
                </th>
                <th
                  id={`${id}-${m}-p95`}
                  scope="col"
                  className={cn(head, "text-right font-normal whitespace-nowrap")}
                >
                  {t("statistics.columns.p95")}
                </th>
              </Fragment>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((item, index) => {
            const subject = item.subject.id;
            const expanded = open.has(subject);
            const rowHeader = `${id}-row-${index}`;
            const panel = `${id}-days-${index}`;
            return (
              <Fragment key={subject}>
                <tr className="border-t align-top" data-testid="statistics-row">
                  <th
                    id={rowHeader}
                    scope="row"
                    headers={`${id}-name`}
                    className="px-3 py-2 font-normal"
                  >
                    <button
                      type="button"
                      aria-expanded={expanded}
                      aria-controls={panel}
                      className="-mx-1 inline-flex max-w-full items-start gap-1 rounded-md px-1 text-left font-medium outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                      onClick={() => toggle(subject)}
                    >
                      {expanded ? (
                        <ChevronDownIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
                      ) : (
                        <ChevronRightIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
                      )}
                      <span className="min-w-0 wrap-anywhere" data-testid="statistics-name">
                        {item.subject.name}
                      </span>
                    </button>
                  </th>
                  <td
                    headers={`${rowHeader} ${id}-count`}
                    className="px-3 py-2 text-right tabular-nums"
                    data-testid="statistics-count"
                  >
                    {item.alert_group_count}
                  </td>
                  <td
                    headers={`${rowHeader} ${id}-acked`}
                    className="px-3 py-2 text-right tabular-nums"
                    data-testid="statistics-ack-count"
                  >
                    {item.time_to_acknowledge.count}
                  </td>
                  {MEASURES.map((m) => {
                    const stats = measureOf(item, m);
                    return (
                      <Fragment key={m}>
                        <td
                          headers={`${rowHeader} ${id}-${m} ${id}-${m}-median`}
                          className="border-l px-3 py-2 text-right whitespace-nowrap tabular-nums"
                          data-testid={`statistics-${m}-median`}
                        >
                          {durationText(t, stats.median_seconds)}
                        </td>
                        <td
                          headers={`${rowHeader} ${id}-${m} ${id}-${m}-p95`}
                          className="px-3 py-2 text-right whitespace-nowrap tabular-nums"
                          data-testid={`statistics-${m}-p95`}
                        >
                          {durationText(t, stats.p95_seconds)}
                        </td>
                      </Fragment>
                    );
                  })}
                </tr>
                <tr id={panel} hidden={!expanded} className="bg-muted/20">
                  <td colSpan={7} headers={rowHeader} className="px-3 py-3">
                    {expanded && <StatisticsChart name={item.subject.name} days={item.per_day} />}
                  </td>
                </tr>
              </Fragment>
            );
          })}
        </tbody>
      </table>
      {items.length === 0 && (
        <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
          {empty}
        </p>
      )}
    </div>
  );
}
