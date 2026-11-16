// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The statistics page (C-09.FR-15, C-09.FR-28 to C-09.FR-30): per Route, Integration or Team, or by people, for a period
// and the filters of the Alert Group list, which apply to every view. Per item it shows the number of Alert Groups that
// started in the period and the median and 95th percentile of time to acknowledge and time to resolve, as totals and,
// under each item, per day; by people, who acknowledged first and who resolved. Days split in the time zone of the
// profile (or the browser's), which the request passes. The view, the period, the filters and the sorting live in the
// URL, so a view is shared as a link. At phone width the filters move into a panel, as on the Alert Group list.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { SlidersHorizontalIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getAlertGroupActorStatistics,
  getGetAlertGroupActorStatisticsQueryKey,
  useGetAlertGroupActorStatistics,
  useGetAlertGroupStatistics,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import { useWideLayout } from "../components/alert-group-table";
import { RequirePermission } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { StatisticsFilters } from "../components/statistics-filters";
import { StatisticsPeopleTable } from "../components/statistics-people-table";
import { StatisticsTable } from "../components/statistics-table";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { isApiError, problemText } from "../lib/api";
import {
  DEFAULT_PERIOD,
  DEFAULT_SORT,
  DEFAULT_VIEW,
  PERIODS,
  type Period,
  STATISTICS_VIEWS,
  type StatisticsSearch,
  type StatisticsView,
  activeFilterCount,
  statisticsRequest,
  statisticsSearchSchema,
} from "../lib/statistics-search";
import { dayIn, useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/statistics")({
  validateSearch: statisticsSearchSchema,
  staticData: { shell: true },
  component: StatisticsPage,
});

const DAY_MS = 24 * 60 * 60 * 1000;

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

function viewLabel(t: TFunction, view: StatisticsView): string {
  switch (view) {
    case "route":
      return t("statistics.by.route");
    case "integration":
      return t("statistics.by.integration");
    case "team":
      return t("statistics.by.team");
    default:
      return t("statistics.by.people");
  }
}

function SubjectChoice({
  value,
  onChange,
}: {
  value: StatisticsView;
  onChange: (next: StatisticsView) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5">
      <legend className="mb-1.5 text-sm font-medium">{t("statistics.by.label")}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {STATISTICS_VIEWS.map((v) => (
          <div key={v} className="flex items-center gap-2">
            <input
              id={`${id}-${v}`}
              type="radio"
              name={`${id}-by`}
              value={v}
              checked={value === v}
              onChange={() => onChange(v)}
              className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            />
            <Label htmlFor={`${id}-${v}`}>{viewLabel(t, v)}</Label>
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

function emptyText(t: TFunction, view: StatisticsView): string {
  switch (view) {
    case "integration":
      return t("statistics.emptyIntegrations");
    case "team":
      return t("statistics.emptyTeams");
    default:
      return t("statistics.emptyRoutes");
  }
}

function Statistics() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const queryClient = useQueryClient();
  const { timeZone } = useTimeFormat();
  const wide = useWideLayout();
  const [sheet, setSheet] = useState(false);
  const by = search.by ?? DEFAULT_VIEW;
  // The presets count back from when the page opened or the period was chosen, so the view holds still.
  const [at, setAt] = useState(() => Date.now());
  const update = (patch: Partial<StatisticsSearch>) => {
    if ("period" in patch) {
      setAt(Date.now());
    }
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  };
  const request = useMemo(() => statisticsRequest(search, timeZone, at), [search, timeZone, at]);
  const itemParams = typeof request !== "string" && request.view !== "people" ? request : undefined;
  const peopleParams =
    typeof request !== "string" && request.view === "people" ? request : undefined;
  const statistics = useGetAlertGroupStatistics(itemParams?.params ?? { group_by: "route" }, {
    query: {
      enabled: itemParams !== undefined,
      // The last statistics stay while newer ones are read, but never those of the other choice.
      placeholderData: (previous) => (previous?.group_by === by ? previous : undefined),
    },
  });
  const people = useGetAlertGroupActorStatistics(peopleParams?.params, {
    query: {
      enabled: peopleParams !== undefined,
      placeholderData: (previous) => previous,
    },
  });
  const current = by === "people" ? people : statistics;

  // New Matchers are checked by the server first; a refused one leaves the URL, and the rows, as they were, and the
  // field shows why.
  const applyMatchers = async (next: string[]) => {
    // The period does not matter to whether a Matcher is valid, so a period that cannot be sent does not skip the check.
    const probe = statisticsRequest(
      { ...search, label: next, by: "people", period: undefined, from: undefined, to: undefined },
      timeZone,
      at,
    );
    if (typeof probe !== "string" && probe.view === "people") {
      await queryClient.fetchQuery({
        queryKey: getGetAlertGroupActorStatisticsQueryKey(probe.params),
        queryFn: ({ signal }) => getAlertGroupActorStatistics(probe.params, { signal }),
        staleTime: 0,
      });
    }
    update({ label: next.length > 0 ? next : undefined });
  };

  const active = activeFilterCount(search);
  const filters = (
    <StatisticsFilters
      search={search}
      onChange={update}
      applyMatchers={applyMatchers}
      problem={current.error}
    />
  );
  const teamLink = (id: string, name: string) => (
    <Link
      to="/statistics"
      search={{ ...search, by: "people", team: id }}
      className="rounded-md outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
    >
      {name}
    </Link>
  );
  const body =
    typeof request === "string" ? (
      <Alert variant="destructive">
        <AlertDescription className="text-current">
          {request === "invalid"
            ? t("statistics.period.invalid")
            : t("statistics.period.incomplete")}
        </AlertDescription>
      </Alert>
    ) : current.isError ? (
      <Alert variant="destructive">
        <AlertDescription className="text-current">
          {statisticsError(t, current.error)}
        </AlertDescription>
      </Alert>
    ) : by === "people" ? (
      people.data === undefined ? (
        <Loading />
      ) : (
        <StatisticsPeopleTable
          items={people.data.items}
          sort={search.sort}
          onSort={(next) => update({ sort: next === DEFAULT_SORT ? undefined : next })}
          busy={people.isPlaceholderData}
        />
      )
    ) : statistics.data === undefined ? (
      <Loading />
    ) : (
      <StatisticsTable
        groupBy={statistics.data.group_by}
        items={statistics.data.items}
        busy={statistics.isPlaceholderData}
        empty={emptyText(t, by)}
        teamLink={teamLink}
      />
    );
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <PageTitle docs="page.statistics">{t("statistics.title")}</PageTitle>
        <p className="text-sm text-muted-foreground">{t("statistics.hint", { timeZone })}</p>
      </div>
      <div
        className="grid min-w-0 items-start gap-4 sm:grid-cols-2"
        role="group"
        aria-label={t("statistics.controls")}
      >
        <SubjectChoice
          value={by}
          onChange={(next) => update({ by: next === DEFAULT_VIEW ? undefined : next })}
        />
        <PeriodPicker search={search} timeZone={timeZone} onChange={update} />
      </div>
      {!wide && (
        <div>
          <Button
            variant="outline"
            onClick={() => setSheet(true)}
            aria-haspopup="dialog"
            data-testid="filters-button"
          >
            <SlidersHorizontalIcon aria-hidden="true" />
            {active > 0
              ? t("alertGroups.filters.buttonActive", { count: active })
              : t("alertGroups.filters.button")}
          </Button>
          <Dialog open={sheet} onOpenChange={setSheet}>
            <DialogContent
              closeLabel={t("common.close")}
              className="top-0 right-0 bottom-0 left-auto flex h-dvh max-h-dvh w-full max-w-[min(24rem,100%)] translate-x-0 translate-y-0 flex-col gap-4 overflow-y-auto rounded-none sm:max-w-sm"
            >
              <DialogHeader>
                <DialogTitle>{t("alertGroups.filters.title")}</DialogTitle>
              </DialogHeader>
              {filters}
              <Button onClick={() => setSheet(false)}>{t("statistics.filters.done")}</Button>
            </DialogContent>
          </Dialog>
        </div>
      )}
      <div className="flex min-w-0 flex-col gap-4 lg:flex-row lg:items-start">
        {wide && (
          <aside
            className="flex w-72 shrink-0 flex-col gap-3 rounded-lg border p-3"
            aria-label={t("alertGroups.filters.title")}
          >
            <h2 className="text-sm font-semibold">{t("alertGroups.filters.title")}</h2>
            {filters}
          </aside>
        )}
        <div className="min-w-0 flex-1">{body}</div>
      </div>
    </div>
  );
}

function Loading() {
  const { t } = useTranslation();
  return (
    <p className="text-sm text-muted-foreground" role="status">
      {t("common.loading")}
    </p>
  );
}

function StatisticsPage() {
  return (
    <RequirePermission permission="alert-groups:read">
      <Statistics />
    </RequirePermission>
  );
}
