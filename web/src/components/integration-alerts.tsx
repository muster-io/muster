// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alerts view of an Integration (C-06.FR-19): the Alerts Muster tracks for it, with their labels, the state
// (firing, or resolved with its reason and time), the Route that took each, linked, and its Severity level with the
// value as received when it has no mapping (C-08.FR-13), startsAt, the time last seen, the Alertmanager groups listing
// them and the Static label warning. The full reason of a Gone or Stale Alert opens under its state by a tap, not only
// on hover (C-09.FR-24). State tabs, label Matchers, text search and the sort live in the URL of the page. Labels are
// what Alertmanager sent: they show as text only.

import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getListIntegrationAlertsQueryKey,
  listIntegrationAlerts,
} from "../api/gen/endpoints/integrations/integrations";
import {
  type IntegrationAlert,
  type IntegrationAlertList,
  type ListIntegrationAlertsParams,
  ListIntegrationAlertsSort,
  type NullableResolveReason,
  type SeverityLevel,
} from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { type DataColumn, DataTable, useCursorList } from "./data-table";
import { ALERT_TABS, type AlertSearch, type AlertTab } from "./integration-alerts-search";
import { LabelMatchersInput } from "./label-matchers-input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { cn } from "./ui/utils";

/** How long the text search waits for typing to pause before it filters. */
const SEARCH_DELAY_MS = 300;

/** How long a page read stays fresh: the first page a new Matcher was checked with is not read again. */
const FRESH_MS = 5000;

/** The request of a page of the view for the filters of the URL; label replaces the applied Matchers when given. */
export function alertParams(
  search: AlertSearch,
  label: readonly string[] | undefined = search.alerts_label,
): ListIntegrationAlertsParams {
  const tab = search.alerts_state ?? "firing";
  const q = search.alerts_q?.trim();
  return {
    state: tab === "all" ? undefined : tab,
    label: label !== undefined && label.length > 0 ? [...label] : undefined,
    q: q ? q : undefined,
    sort: search.alerts_sort,
  };
}

/** The query key of the view's pages, as the cursor list keeps them. */
function pagesKey(integrationId: string, params: ListIntegrationAlertsParams) {
  return [...getListIntegrationAlertsQueryKey(integrationId, params), "pages"];
}

/** The labels with alertname first, then by name. */
export function orderedLabels(labels: Record<string, string>): [string, string][] {
  return Object.entries(labels).toSorted(([a], [b]) =>
    a === "alertname" ? -1 : b === "alertname" ? 1 : a < b ? -1 : a > b ? 1 : 0,
  );
}

export function reasonLabel(t: TFunction, reason: NullableResolveReason | undefined): string {
  switch (reason) {
    case "resolved":
      return t("alerts.reason.resolved");
    case "gone":
      return t("alerts.reason.gone");
    case "stale":
      return t("alerts.reason.stale");
    case "integration_deleted":
      return t("alerts.reason.integrationDeleted");
    default:
      return "";
  }
}

/**
 * The full reason of a resolved Alert in the language of the page: the text of C-06.FR-10 for Gone and Stale, else
 * the server's text (which names a deleted Integration).
 */
export function reasonText(
  t: TFunction,
  alert: Pick<IntegrationAlert, "resolve_reason" | "resolve_reason_text">,
): string | null {
  switch (alert.resolve_reason) {
    case "gone":
    case "stale":
      return t("alerts.reasonText.absent");
    default:
      return alert.resolve_reason_text ?? null;
  }
}

function LabelsCell({ row }: { row: IntegrationAlert }) {
  const { t } = useTranslation();
  const warnings = row.static_label_warnings ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <ul className="flex flex-wrap gap-1" aria-label={t("alerts.columns.labels")}>
        {orderedLabels(row.labels).map(([name, value]) => (
          <li
            key={name}
            className={cn(
              "max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere",
              name === "alertname" && "font-semibold",
            )}
            data-testid="alert-label"
          >
            {name}={value}
          </li>
        ))}
      </ul>
      {warnings.map((label) => (
        <p
          key={label}
          className="flex items-start gap-1 text-xs break-words text-amber-800 dark:text-warning"
          data-testid="static-label-warning"
        >
          <TriangleAlertIcon aria-hidden="true" className="mt-px size-3.5 shrink-0" />
          <span className="min-w-0">{t("alerts.staticLabelWarning", { label })}</span>
        </p>
      ))}
    </div>
  );
}

function StateCell({ row }: { row: IntegrationAlert }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  if (row.state === "firing") {
    return (
      <span className="font-medium whitespace-nowrap text-destructive" data-testid="alert-state">
        {t("alerts.state.firing")}
      </span>
    );
  }
  const reason = reasonLabel(t, row.resolve_reason);
  const full = reasonText(t, row) ?? reason;
  const label = reason ? t("alerts.state.resolvedWith", { reason }) : t("alerts.state.resolved");
  return (
    <div className="flex flex-col gap-0.5">
      {full !== reason && full !== "" ? (
        <details data-testid="alert-reason-details">
          <summary
            title={full}
            data-testid="alert-state"
            className="w-fit cursor-pointer rounded-md whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {label}
          </summary>
          <p className="mt-1 max-w-64 text-xs wrap-anywhere" data-testid="alert-reason">
            {full}
          </p>
        </details>
      ) : (
        <span data-testid="alert-state" className="whitespace-nowrap">
          {label}
        </span>
      )}
      {row.resolved_at && (
        <time
          className="text-xs whitespace-nowrap text-muted-foreground"
          dateTime={row.resolved_at}
        >
          {dateTime(row.resolved_at)}
        </time>
      )}
    </div>
  );
}

/** The name of a Severity level. */
export function severityLabel(t: TFunction, level: SeverityLevel): string {
  switch (level) {
    case "critical":
      return t("alerts.severity.critical");
    case "warning":
      return t("alerts.severity.warning");
    default:
      return t("alerts.severity.info");
  }
}

/** The Severity level of an Alert, with the value as received when it has no mapping: "warning (P5)". */
export function severityText(t: TFunction, alert: IntegrationAlert): string | null {
  if (alert.severity_level === undefined) {
    return null;
  }
  const level = severityLabel(t, alert.severity_level);
  return alert.severity_raw
    ? t("alerts.severity.withRaw", { level, raw: alert.severity_raw })
    : level;
}

function RouteCell({ row }: { row: IntegrationAlert }) {
  if (row.route === undefined) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <Link
      to="/routes/$routeId"
      params={{ routeId: row.route.id }}
      className="wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
      data-testid="alert-route"
    >
      {row.route.name}
    </Link>
  );
}

function SeverityCell({ row }: { row: IntegrationAlert }) {
  const { t } = useTranslation();
  const text = severityText(t, row);
  return text === null ? (
    <span className="text-muted-foreground">—</span>
  ) : (
    <span className="wrap-anywhere" data-testid="alert-severity">
      {text}
    </span>
  );
}

function TimeCell({ iso }: { iso: string }) {
  const { dateTime } = useTimeFormat();
  return (
    <time className="whitespace-nowrap" dateTime={iso}>
      {dateTime(iso)}
    </time>
  );
}

function StartsAtCell({ row }: { row: IntegrationAlert }) {
  return <TimeCell iso={row.starts_at} />;
}

function LastSeenCell({ row }: { row: IntegrationAlert }) {
  return <TimeCell iso={row.last_seen_at} />;
}

function GroupsCell({ row }: { row: IntegrationAlert }) {
  const { t } = useTranslation();
  const groups = row.alertmanager_groups;
  if (groups.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <details data-testid="alert-groups">
      <summary className="cursor-pointer rounded-md whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring">
        {t("alerts.groups", { count: groups.length })}
      </summary>
      <ul className="mt-1 flex flex-col gap-1">
        {groups.map((key) => (
          <li key={key}>
            <code className="font-mono text-xs wrap-anywhere">{key}</code>
          </li>
        ))}
      </ul>
    </details>
  );
}

function tabLabel(t: TFunction, tab: AlertTab): string {
  switch (tab) {
    case "firing":
      return t("alerts.tabs.firing");
    case "resolved":
      return t("alerts.tabs.resolved");
    default:
      return t("alerts.tabs.all");
  }
}

function sortLabel(t: TFunction, sort: ListIntegrationAlertsSort): string {
  switch (sort) {
    case "-last_seen_at":
      return t("alerts.sort.lastSeenDesc");
    case "last_seen_at":
      return t("alerts.sort.lastSeenAsc");
    case "-starts_at":
      return t("alerts.sort.startsAtDesc");
    default:
      return t("alerts.sort.startsAtAsc");
  }
}

/** The state tabs, with the arrow keys moving between them. */
function StateTabs({
  value,
  panelId,
  onSelect,
}: {
  value: AlertTab;
  panelId: string;
  onSelect: (tab: AlertTab) => void;
}) {
  const { t } = useTranslation();
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: KeyboardEvent, index: number) => {
    const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    let next: number | undefined;
    if (step !== undefined) {
      next = (index + step + ALERT_TABS.length) % ALERT_TABS.length;
    } else if (e.key === "Home") {
      next = 0;
    } else if (e.key === "End") {
      next = ALERT_TABS.length - 1;
    }
    const tab = next === undefined ? undefined : ALERT_TABS[next];
    if (next !== undefined && tab !== undefined) {
      e.preventDefault();
      onSelect(tab);
      refs.current[next]?.focus();
    }
  };
  return (
    <div
      role="tablist"
      aria-label={t("alerts.tabs.label")}
      className="inline-flex w-fit max-w-full rounded-lg border bg-muted/50 p-0.5"
    >
      {ALERT_TABS.map((tab, index) => (
        <button
          key={tab}
          ref={(el) => {
            refs.current[index] = el;
          }}
          type="button"
          role="tab"
          id={`${panelId}-tab-${tab}`}
          aria-selected={tab === value}
          aria-controls={panelId}
          tabIndex={tab === value ? 0 : -1}
          className={cn(
            "rounded-md px-3 py-1 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring",
            tab === value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
          onClick={() => onSelect(tab)}
          onKeyDown={(e) => onKeyDown(e, index)}
        >
          {tabLabel(t, tab)}
        </button>
      ))}
    </div>
  );
}

/** The text search, applied once typing pauses. */
function TextSearch({ value, onSearch }: { value: string; onSearch: (q: string) => void }) {
  const { t } = useTranslation();
  const [text, setText] = useState(value);
  const [shown, setShown] = useState(value);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const sent = useRef(value);
  // The URL changed elsewhere (back, a link): the field follows it.
  if (value !== shown) {
    setShown(value);
    setText(value);
  }
  // ...and a search still waiting for a pause in typing is dropped, so that it does not undo that change.
  useEffect(() => {
    if (value !== sent.current) {
      clearTimeout(timer.current);
      sent.current = value;
    }
  }, [value]);
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor="alerts-q">{t("alerts.filters.search")}</Label>
      <Input
        id="alerts-q"
        type="search"
        value={text}
        autoComplete="off"
        placeholder={t("alerts.filters.searchPlaceholder")}
        onChange={(e) => {
          const next = e.target.value;
          setText(next);
          clearTimeout(timer.current);
          timer.current = setTimeout(() => {
            sent.current = next.trim();
            setShown(next.trim());
            onSearch(next.trim());
          }, SEARCH_DELAY_MS);
        }}
      />
    </div>
  );
}

export function IntegrationAlerts({
  integrationId,
  search,
  onSearch,
}: {
  integrationId: string;
  search: AlertSearch;
  onSearch: (patch: Partial<AlertSearch>) => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const tab = search.alerts_state ?? "firing";
  const matchers = useMemo(() => search.alerts_label ?? [], [search.alerts_label]);
  const params = useMemo(() => alertParams(search), [search]);
  const fetchPage =
    (p: ListIntegrationAlertsParams) => (cursor: string | undefined, signal: AbortSignal) =>
      listIntegrationAlerts(integrationId, { ...p, cursor }, { signal });
  const list = useCursorList<IntegrationAlert>(
    getListIntegrationAlertsQueryKey(integrationId, params),
    fetchPage(params),
    true,
    FRESH_MS,
  );

  // New Matchers are read first and applied only when the server takes them: a refused one leaves the URL, and the
  // rows shown, as they were. The read fills the cache the view then reads.
  const applyMatchers = async (next: string[]) => {
    const p = alertParams(search, next);
    const read = fetchPage(p);
    await queryClient.fetchInfiniteQuery<
      IntegrationAlertList,
      Error,
      IntegrationAlertList,
      ReturnType<typeof pagesKey>,
      string | undefined
    >({
      queryKey: pagesKey(integrationId, p),
      queryFn: ({ pageParam, signal }) => read(pageParam, signal),
      initialPageParam: undefined,
      getNextPageParam: (last: IntegrationAlertList) => last.next_cursor ?? undefined,
    });
    onSearch({ alerts_label: next.length > 0 ? next : undefined });
  };

  const columns = useMemo(
    (): DataColumn<IntegrationAlert>[] => [
      { id: "labels", header: t("alerts.columns.labels"), className: "min-w-56", Cell: LabelsCell },
      { id: "state", header: t("alerts.columns.state"), className: "min-w-32", Cell: StateCell },
      { id: "route", header: t("alerts.columns.route"), className: "min-w-28", Cell: RouteCell },
      {
        id: "severity",
        header: t("alerts.columns.severity"),
        className: "min-w-24",
        Cell: SeverityCell,
      },
      { id: "starts_at", header: t("alerts.columns.startsAt"), Cell: StartsAtCell },
      { id: "last_seen", header: t("alerts.columns.lastSeen"), Cell: LastSeenCell },
      {
        id: "groups",
        header: t("alerts.columns.groups"),
        className: "min-w-48",
        Cell: GroupsCell,
      },
    ],
    [t],
  );
  const filtered = matchers.length > 0 || Boolean(params.q);
  const empty = filtered
    ? t("alerts.emptyFiltered")
    : tab === "firing"
      ? t("alerts.emptyFiring")
      : tab === "resolved"
        ? t("alerts.emptyResolved")
        : t("alerts.empty");
  const panelId = "integration-alerts-panel";
  return (
    <Card className="lg:col-span-2" data-testid="integration-alerts">
      <CardHeader>
        <CardTitle>
          <h2>{t("alerts.title")}</h2>
        </CardTitle>
        <CardDescription>{t("alerts.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <StateTabs
          value={tab}
          panelId={panelId}
          onSelect={(next) => onSearch({ alerts_state: next })}
        />
        <div
          className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr]"
          role="search"
          aria-label={t("alerts.filters.label")}
        >
          <LabelMatchersInput
            id="alerts-matchers"
            value={matchers}
            onChange={applyMatchers}
            problem={list.error}
          />
          <TextSearch
            value={search.alerts_q ?? ""}
            onSearch={(q) => onSearch({ alerts_q: q === "" ? undefined : q })}
          />
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor="alerts-sort">{t("alerts.filters.sort")}</Label>
            <NativeSelect
              id="alerts-sort"
              className="w-full"
              value={search.alerts_sort ?? "-last_seen_at"}
              onChange={(e) => {
                const sort = Object.values(ListIntegrationAlertsSort).find(
                  (s) => s === e.target.value,
                );
                onSearch({ alerts_sort: sort === "-last_seen_at" ? undefined : sort });
              }}
            >
              {Object.values(ListIntegrationAlertsSort)
                .toSorted(
                  (a, b) =>
                    (a.endsWith("last_seen_at") ? -1 : 0) - (b.endsWith("last_seen_at") ? -1 : 0),
                )
                .map((s) => (
                  <NativeSelectOption key={s} value={s}>
                    {sortLabel(t, s)}
                  </NativeSelectOption>
                ))}
            </NativeSelect>
          </div>
        </div>
        <div role="tabpanel" id={panelId} aria-labelledby={`${panelId}-tab-${tab}`}>
          <DataTable
            label={t("alerts.title")}
            columns={columns}
            list={list}
            rowId={(a) => a.fingerprint}
            empty={empty}
          />
        </div>
      </CardContent>
    </Card>
  );
}
