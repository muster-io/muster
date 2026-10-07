// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The days of one item of the statistics page (C-09.FR-15): a bar per day of start, as long as its number of Alert
// Groups, with the medians of time to acknowledge and time to resolve beside it. The bars are SVG rectangles sized by
// attributes, so the chart needs no style element and no style attribute (the Content Security Policy of the app
// listener is style-src 'self'). The chart is a table, so the numbers it draws are also its text alternative.

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import type { StatisticsDay } from "../api/gen/model";
import { formatDuration, useTimeFormat } from "../lib/time";

/** A median or a 95th percentile as the page shows it, such as "20 min", or "—" without data. */
export function durationText(t: TFunction, seconds: number | null | undefined): string {
  return seconds === null || seconds === undefined
    ? t("statistics.none")
    : formatDuration(t, seconds);
}

/** The width of the bar of a count, in percent of the longest bar; a day with Alert Groups always shows a sliver. */
export function barPercent(count: number, max: number): number {
  if (count <= 0 || max <= 0) {
    return 0;
  }
  return Math.max(2, Math.round((count / max) * 100));
}

export function StatisticsChart({ name, days }: { name: string; days: readonly StatisticsDay[] }) {
  const { t } = useTranslation();
  const { date } = useTimeFormat();
  if (days.length === 0) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="statistics-days-empty">
        {t("statistics.days.empty")}
      </p>
    );
  }
  const max = Math.max(...days.map((d) => d.alert_group_count));
  return (
    <div
      className="overflow-x-auto rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
      role="region"
      aria-label={t("statistics.days.caption", { name })}
      tabIndex={0}
    >
      <table className="w-full border-collapse text-left text-sm" data-testid="statistics-days">
        <caption className="pb-2 text-left text-sm font-medium wrap-anywhere">
          {t("statistics.days.caption", { name })}
        </caption>
        <thead>
          <tr className="text-muted-foreground">
            <th scope="col" className="py-1 pr-3 font-medium whitespace-nowrap">
              {t("statistics.days.day")}
            </th>
            <th scope="col" className="w-full min-w-28 py-1 pr-3 font-medium whitespace-nowrap">
              {t("statistics.columns.alertGroups")}
            </th>
            <th scope="col" className="py-1 pr-3 font-medium whitespace-nowrap">
              {t("statistics.days.ackMedian")}
            </th>
            <th scope="col" className="py-1 font-medium whitespace-nowrap">
              {t("statistics.days.resolveMedian")}
            </th>
          </tr>
        </thead>
        <tbody>
          {days.map((day) => (
            <tr key={day.date} className="border-t" data-testid="statistics-day">
              <th scope="row" className="py-1.5 pr-3 font-normal whitespace-nowrap">
                <time dateTime={day.date}>{date(day.date)}</time>
              </th>
              <td className="py-1.5 pr-3">
                <div className="flex items-center gap-2">
                  <svg
                    viewBox="0 0 100 10"
                    preserveAspectRatio="none"
                    className="h-3 min-w-0 flex-1"
                    aria-hidden="true"
                  >
                    <rect
                      x="0"
                      y="0"
                      height="10"
                      width={barPercent(day.alert_group_count, max)}
                      className="fill-primary"
                    />
                  </svg>
                  <span className="w-8 shrink-0 text-right tabular-nums" data-testid="day-count">
                    {day.alert_group_count}
                  </span>
                </div>
              </td>
              <td className="py-1.5 pr-3 whitespace-nowrap tabular-nums">
                {durationText(t, day.time_to_acknowledge.median_seconds)}
              </td>
              <td className="py-1.5 whitespace-nowrap tabular-nums" data-testid="day-resolve">
                {durationText(t, day.time_to_resolve.median_seconds)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
