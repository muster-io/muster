// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The statistics page (C-09.FR-15): per Route or per Integration — all of them, or the chosen ones — and for a period,
// the number of Alert Groups that started in it and the median and 95th percentile of time to acknowledge and time to
// resolve, as totals and, under each item, per day. Days split in the time zone of the profile (or the browser's), which
// the request passes. The choice and the period live in the URL, so a view is shared as a link.

import { createFileRoute, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { useGetAlertGroupStatistics } from "../api/gen/endpoints/alert-groups/alert-groups";
import { useListIntegrations } from "../api/gen/endpoints/integrations/integrations";
import { useListRoutes } from "../api/gen/endpoints/routes/routes";
import type { GetAlertGroupStatisticsParams } from "../api/gen/model";
import { MultiPicker } from "../components/alert-group-filters";
import { RequirePermission, useCan } from "../components/app-shell";
import { type StatisticsSubject, StatisticsTable } from "../components/statistics-table";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { isApiError, problemText } from "../lib/api";
import { dayIn, startOfDayIn, useTimeFormat } from "../lib/time";

/** The periods; the last 7 days (alert_group.list_range, which the server applies itself) when the URL names none. */
const PERIODS = ["7d", "30d", "90d", "custom"] as const;
type Period = (typeof PERIODS)[number];
const DEFAULT_PERIOD: Period = "7d";
const DAY_MS = 24 * 60 * 60 * 1000;
const PERIOD_DAYS: Record<Exclude<Period, "custom">, number> = { "7d": 7, "30d": 30, "90d": 90 };

/** A list of non-empty strings; one value in the URL may arrive as a plain string. */
const strings = z
  .preprocess((v) => (typeof v === "string" ? [v] : v), z.array(z.string().min(1)).min(1))
  .optional()
  .catch(undefined);

/** A calendar day as YYYY-MM-DD. */
const day = z
  .string()
  .regex(/^\d{4}-\d{2}-\d{2}$/)
  .optional()
  .catch(undefined);

const statisticsSearchSchema = z.object({
  by: z.enum(["route", "integration"]).optional().catch(undefined),
  period: z.enum(PERIODS).optional().catch(undefined),
  from: day,
  to: day,
  route: strings,
  integration: strings,
});

type StatisticsSearch = z.infer<typeof statisticsSearchSchema>;

export const Route = createFileRoute("/statistics")({
  validateSearch: statisticsSearchSchema,
  staticData: { shell: true },
  component: StatisticsPage,
});

/**
 * The request for the view of the URL at an instant: nothing for the default period, which the server counts with its
 * own clock; the start of a preset; whole days of the time zone for a custom period, the last one included. A custom
 * period without both days, or one that does not start before it ends, is not sent.
 */
function statisticsParams(
  search: StatisticsSearch,
  timeZone: string,
  now: number,
): GetAlertGroupStatisticsParams | "incomplete" | "invalid" {
  const by = search.by ?? "route";
  const params: GetAlertGroupStatisticsParams = { group_by: by, time_zone: timeZone };
  if (by === "route" && search.route !== undefined) {
    params.route = search.route;
  }
  if (by === "integration" && search.integration !== undefined) {
    params.integration = search.integration;
  }
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
    params.from = from.toISOString();
    params.to = to.toISOString();
  } else if (period !== DEFAULT_PERIOD) {
    params.from = new Date(now - PERIOD_DAYS[period] * DAY_MS).toISOString();
  }
  return params;
}

function periodLabel(t: TFunction, period: Period): string {
  switch (period) {
    case "7d":
      return t("statistics.period.last7Days");
    case "30d":
      return t("statistics.period.last30Days");
    case "90d":
      return t("statistics.period.last90Days");
    default:
      return t("statistics.period.custom");
  }
}

/** The text of a refused request: a chosen item that does not exist any more, or the general text. */
function statisticsError(t: TFunction, err: unknown): string {
  if (isApiError(err) && err.errors?.some((e) => e.code === "unknown_id") === true) {
    return t("statistics.errors.unknownItem");
  }
  return problemText(t, err);
}

function SubjectChoice({
  value,
  onChange,
}: {
  value: StatisticsSubject;
  onChange: (next: StatisticsSubject) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const options: { value: StatisticsSubject; label: string }[] = [
    { value: "route", label: t("statistics.by.route") },
    { value: "integration", label: t("statistics.by.integration") },
  ];
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5">
      <legend className="mb-1.5 text-sm font-medium">{t("statistics.by.label")}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {options.map((o) => (
          <div key={o.value} className="flex items-center gap-2">
            <input
              id={`${id}-${o.value}`}
              type="radio"
              name={`${id}-by`}
              value={o.value}
              checked={value === o.value}
              onChange={() => onChange(o.value)}
              className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            />
            <Label htmlFor={`${id}-${o.value}`}>{o.label}</Label>
          </div>
        ))}
      </div>
    </fieldset>
  );
}

function PeriodPicker({
  search,
  timeZone,
  onChange,
}: {
  search: StatisticsSearch;
  timeZone: string;
  onChange: (patch: Partial<StatisticsSearch>) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const period = search.period ?? DEFAULT_PERIOD;
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-period`}>{t("statistics.period.label")}</Label>
        <NativeSelect
          id={`${id}-period`}
          className="w-full"
          value={period}
          onChange={(e) => {
            const next = PERIODS.find((p) => p === e.target.value);
            if (next === undefined) {
              return;
            }
            if (next !== "custom") {
              onChange({
                period: next === DEFAULT_PERIOD ? undefined : next,
                from: undefined,
                to: undefined,
              });
              return;
            }
            // A custom period starts as the last 7 days, so that the inputs are never empty.
            const now = Date.now();
            onChange({
              period: "custom",
              from: dayIn(new Date(now - 6 * DAY_MS), timeZone),
              to: dayIn(new Date(now), timeZone),
            });
          }}
        >
          {PERIODS.map((p) => (
            <NativeSelectOption key={p} value={p}>
              {periodLabel(t, p)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {period === "custom" && (
        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
          {(["from", "to"] as const).map((bound) => (
            <div key={bound} className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-${bound}`}>
                {bound === "from" ? t("statistics.period.from") : t("statistics.period.to")}
              </Label>
              <Input
                id={`${id}-${bound}`}
                type="date"
                className="min-w-0"
                value={search[bound] ?? ""}
                onChange={(e) => {
                  const value = e.target.value;
                  if (value === "" || /^\d{4}-\d{2}-\d{2}$/.test(value)) {
                    onChange({ [bound]: value === "" ? undefined : value });
                  }
                }}
              />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function Statistics() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { timeZone } = useTimeFormat();
  const canRoutes = useCan("routes:read");
  const canIntegrations = useCan("integrations:read");
  const by = search.by ?? "route";
  // The presets count back from when the page opened or the period was chosen, so the view holds still.
  const [at, setAt] = useState(() => Date.now());
  const update = (patch: Partial<StatisticsSearch>) => {
    if ("period" in patch) {
      setAt(Date.now());
    }
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  };
  const params = useMemo(() => statisticsParams(search, timeZone, at), [search, timeZone, at]);
  const request = typeof params === "string" ? undefined : params;
  const statistics = useGetAlertGroupStatistics(request ?? { group_by: by }, {
    query: {
      enabled: request !== undefined,
      // The last statistics stay while newer ones are read, but never those of the other choice.
      placeholderData: (previous) => (previous?.group_by === by ? previous : undefined),
    },
  });
  const routes = useListRoutes({ query: { enabled: canRoutes && by === "route" } });
  const integrations = useListIntegrations(
    { limit: 500 },
    { query: { enabled: canIntegrations && by === "integration" } },
  );
  const items = statistics.data?.items ?? [];
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">{t("statistics.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("statistics.hint", { timeZone })}</p>
      </div>
      <div
        className="grid min-w-0 items-start gap-4 sm:grid-cols-2 lg:grid-cols-3"
        role="group"
        aria-label={t("statistics.controls")}
      >
        <SubjectChoice
          value={by}
          onChange={(next) =>
            update({
              by: next === "route" ? undefined : next,
              route: undefined,
              integration: undefined,
            })
          }
        />
        <PeriodPicker search={search} timeZone={timeZone} onChange={update} />
        {by === "route" && canRoutes && (
          <MultiPicker
            testId="statistics-routes"
            label={t("statistics.items.routes")}
            placeholder={t("statistics.items.allRoutes")}
            choices={(routes.data?.items ?? []).map((r) => ({ value: r.id, label: r.name }))}
            value={search.route ?? []}
            onChange={(next) => update({ route: next.length > 0 ? next : undefined })}
          />
        )}
        {by === "integration" && canIntegrations && (
          <MultiPicker
            testId="statistics-integrations"
            label={t("statistics.items.integrations")}
            placeholder={t("statistics.items.allIntegrations")}
            choices={(integrations.data?.items ?? []).map((i) => ({ value: i.id, label: i.name }))}
            value={search.integration ?? []}
            onChange={(next) => update({ integration: next.length > 0 ? next : undefined })}
          />
        )}
      </div>
      {typeof params === "string" ? (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {params === "invalid"
              ? t("statistics.period.invalid")
              : t("statistics.period.incomplete")}
          </AlertDescription>
        </Alert>
      ) : statistics.isError ? (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {statisticsError(t, statistics.error)}
          </AlertDescription>
        </Alert>
      ) : statistics.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : (
        <StatisticsTable
          groupBy={statistics.data.group_by}
          items={items}
          busy={statistics.isPlaceholderData}
          empty={by === "route" ? t("statistics.emptyRoutes") : t("statistics.emptyIntegrations")}
        />
      )}
    </div>
  );
}

function StatisticsPage() {
  return (
    <RequirePermission permission="alert-groups:read">
      <Statistics />
    </RequirePermission>
  );
}
