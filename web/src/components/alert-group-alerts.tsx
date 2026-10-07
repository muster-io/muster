// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alerts of an Alert Group (C-09.FR-14, FR-24): firing first, resolved struck through with their reason, filtered
// by firing or resolved. Each shows its labels and, in its expandable details, the full reason of a resolved Alert —
// readable by touch, not only on hover (D261a) — its annotations, startsAt, the time last seen, the Alertmanager groups
// listing it and its source link. Labels, annotations and the link come from Alertmanager: text only, and the link
// only when it is http or https.

import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ExternalLinkIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getListAlertGroupAlertsQueryKey,
  listAlertGroupAlerts,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import type { AlertGroupAlert, ListAlertGroupAlertsParams } from "../api/gen/model";
import { problemText } from "../lib/api";
import { orderedLabels, reasonLabel, reasonText } from "./integration-alerts";
import { DateTime } from "./relative-time";
import { StatusTabs } from "./status-tabs";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { cn } from "./ui/utils";

const ALERT_FILTERS = ["all", "firing", "resolved"] as const;
type AlertFilter = (typeof ALERT_FILTERS)[number];

function filterLabel(t: TFunction, filter: AlertFilter): string {
  switch (filter) {
    case "firing":
      return t("alerts.tabs.firing");
    case "resolved":
      return t("alerts.tabs.resolved");
    default:
      return t("alerts.tabs.all");
  }
}

/** A link of an Alert that a browser may follow: http or https only, never javascript: or data:. */
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) {
    return undefined;
  }
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** Where an annotation goes: summary, then description, then the rest by name. */
function annotationRank(name: string): number {
  return name === "summary" ? 0 : name === "description" ? 1 : 2;
}

/** The annotations with summary and description first, then by name. */
export function orderedAnnotations(annotations: Record<string, string>): [string, string][] {
  return Object.entries(annotations).toSorted(
    ([a], [b]) => annotationRank(a) - annotationRank(b) || (a < b ? -1 : a > b ? 1 : 0),
  );
}

function Labels({ labels, resolved }: { labels: Record<string, string>; resolved: boolean }) {
  const { t } = useTranslation();
  return (
    <ul className="flex flex-wrap gap-1" aria-label={t("alerts.columns.labels")}>
      {orderedLabels(labels).map(([name, value]) => (
        <li
          key={name}
          className={cn(
            "max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere",
            name === "alertname" && "font-semibold",
            resolved && "text-muted-foreground line-through",
          )}
          data-testid="alert-label"
        >
          {name}={value}
        </li>
      ))}
    </ul>
  );
}

function Detail({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:gap-3">
      <dt className="shrink-0 text-xs text-muted-foreground sm:w-40">{term}</dt>
      <dd className="min-w-0 text-sm wrap-anywhere">{children}</dd>
    </div>
  );
}

function AlertItem({ alert }: { alert: AlertGroupAlert }) {
  const { t } = useTranslation();
  const resolved = alert.state === "resolved";
  const reason = reasonLabel(t, alert.resolve_reason);
  const full = reasonText(t, alert);
  const annotations = orderedAnnotations(alert.annotations ?? {});
  const href = safeHref(alert.source_url);
  return (
    <li className="flex min-w-0 flex-col gap-2 border-t py-3 first:border-t-0" data-testid="alert">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span
          className={cn(
            "text-sm font-medium whitespace-nowrap",
            resolved ? "text-muted-foreground" : "text-destructive",
          )}
          data-testid="alert-state"
        >
          {resolved
            ? reason
              ? t("alerts.state.resolvedWith", { reason })
              : t("alerts.state.resolved")
            : t("alerts.state.firing")}
        </span>
        {alert.integration !== undefined && (
          <span className="text-xs text-muted-foreground wrap-anywhere">
            {alert.integration.name}
          </span>
        )}
        <span className="text-xs text-muted-foreground">
          <DateTime iso={alert.starts_at} />
        </span>
      </div>
      <Labels labels={alert.labels} resolved={resolved} />
      <details className="group" data-testid="alert-details">
        <summary className="w-fit cursor-pointer rounded-md text-sm text-primary outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {t("alertGroups.alerts.details")}
        </summary>
        <dl className="mt-2 flex flex-col gap-2">
          {resolved && full !== null && (
            <Detail term={t("alertGroups.alerts.reason")}>
              <span data-testid="alert-reason">{full}</span>
            </Detail>
          )}
          {annotations.map(([name, value]) => (
            <Detail key={name} term={name}>
              <span className="whitespace-pre-wrap" data-testid="alert-annotation">
                {value}
              </span>
            </Detail>
          ))}
          <Detail term={t("alerts.columns.startsAt")}>
            <DateTime iso={alert.starts_at} />
          </Detail>
          {alert.resolved_at && (
            <Detail term={t("alertGroups.alerts.resolvedAt")}>
              <DateTime iso={alert.resolved_at} />
            </Detail>
          )}
          <Detail term={t("alerts.columns.lastSeen")}>
            <DateTime iso={alert.last_seen_at} />
          </Detail>
          {alert.alertmanager_groups.length > 0 && (
            <Detail term={t("alerts.columns.groups")}>
              <ul className="flex flex-col gap-1">
                {alert.alertmanager_groups.map((key) => (
                  <li key={key}>
                    <code className="font-mono text-xs wrap-anywhere">{key}</code>
                  </li>
                ))}
              </ul>
            </Detail>
          )}
          {href !== undefined && (
            <Detail term={t("alertGroups.alerts.source")}>
              <a
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-primary underline-offset-4 wrap-anywhere hover:underline focus-visible:underline"
              >
                {t("alertGroups.alerts.openSource")}
                <ExternalLinkIcon aria-hidden="true" className="size-3.5 shrink-0" />
              </a>
            </Detail>
          )}
          <Detail term={t("alertGroups.alerts.fingerprint")}>
            <code className="font-mono text-xs">{alert.fingerprint}</code>
          </Detail>
        </dl>
      </details>
    </li>
  );
}

export function AlertGroupAlerts({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<AlertFilter>("all");
  const params: ListAlertGroupAlertsParams = filter === "all" ? {} : { state: filter };
  const query = useInfiniteQuery({
    queryKey: [...getListAlertGroupAlertsQueryKey(alertGroupId, params), "pages"],
    queryFn: ({ pageParam, signal }) =>
      listAlertGroupAlerts(alertGroupId, { ...params, cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  const panelId = "alert-group-alerts-panel";
  const empty =
    filter === "firing"
      ? t("alerts.emptyFiring")
      : filter === "resolved"
        ? t("alerts.emptyResolved")
        : t("alerts.empty");
  return (
    <Card data-testid="alert-group-alerts">
      <CardHeader>
        <CardTitle>
          <h2>{t("alerts.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <StatusTabs
          tabs={ALERT_FILTERS}
          value={filter}
          label={t("alerts.tabs.label")}
          panelId={panelId}
          labelOf={(f) => filterLabel(t, f)}
          onSelect={setFilter}
        />
        <div role="tabpanel" id={panelId} aria-labelledby={`${panelId}-tab-${filter}`}>
          {items.length === 0 ? (
            <p className="py-4 text-sm text-muted-foreground" role="status">
              {query.isPending
                ? t("common.loading")
                : query.error
                  ? problemText(t, query.error)
                  : empty}
            </p>
          ) : (
            <ul aria-label={t("alerts.title")}>
              {items.map((a) => (
                <AlertItem
                  key={`${a.integration?.id ?? ""}:${a.fingerprint}:${a.starts_at}`}
                  alert={a}
                />
              ))}
            </ul>
          )}
          {query.hasNextPage && (
            <Button
              variant="outline"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? t("common.loading") : t("table.loadMore")}
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
