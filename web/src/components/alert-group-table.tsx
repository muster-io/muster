// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The rows of the Alert Group list (C-09.FR-13, FR-24). On a desktop a table: status, #N, title, Severity level,
// Urgent, Route, Integrations, firing and total Alerts, start, duration, last change, Reopen count and the label columns
// the user picked. On a phone compact rows — status, #N, title, Urgent mark and duration — each a link to the page, so
// that nothing scrolls sideways. Titles, summaries and label values come from alerts: text only.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useMemo, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

import type { AlertGroup } from "../api/gen/model";
import { problemText } from "../lib/api";
import { EntityLink, StatusBadge, UrgentMark, statusLabel } from "./alert-group-header";
import { useCan } from "./app-shell";
import { type CursorList, type DataColumn, DataTable } from "./data-table";
import { severityLabel } from "./integration-alerts";
import { DateTime, Duration, elapsedSeconds, formatElapsed, useNow } from "./relative-time";
import { Button } from "./ui/button";

/** The width from which the list is a table and the filters a side panel (Tailwind's lg). */
const WIDE = "(min-width: 1024px)";

function subscribeWide(callback: () => void): () => void {
  const query = window.matchMedia(WIDE);
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}

/** Whether the window is wide enough for the table and the side panel. */
export function useWideLayout(): boolean {
  return useSyncExternalStore(subscribeWide, () => window.matchMedia(WIDE).matches);
}

const NONE = <span className="text-muted-foreground">—</span>;

function NumberCell({ row }: { row: AlertGroup }) {
  return (
    <Link
      to="/alert-groups/$alertGroupId"
      params={{ alertGroupId: row.id }}
      className="font-mono whitespace-nowrap text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      #{row.number}
    </Link>
  );
}

function TitleCell({ row }: { row: AlertGroup }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <Link
        to="/alert-groups/$alertGroupId"
        params={{ alertGroupId: row.id }}
        className="font-medium wrap-anywhere text-foreground underline-offset-4 hover:underline focus-visible:underline"
        data-testid="alert-group-title"
      >
        {row.title}
      </Link>
      {row.summary && (
        <span className="line-clamp-2 text-xs text-muted-foreground wrap-anywhere">
          {row.summary}
        </span>
      )}
    </div>
  );
}

function RouteCell({ row }: { row: AlertGroup }) {
  const can = useCan("routes:read");
  return <EntityLink entity={row.route} to="/routes/$routeId" can={can} />;
}

function alertCounts(t: TFunction, row: AlertGroup): string {
  return t("alertGroups.alertCounts", {
    count: row.firing_alert_count,
    total: row.firing_alert_count + row.resolved_alert_count,
  });
}

/** The desktop columns, with one per picked label. */
function useColumns(labelColumns: readonly string[]): DataColumn<AlertGroup>[] {
  const { t } = useTranslation();
  return useMemo(
    (): DataColumn<AlertGroup>[] => [
      {
        id: "status",
        header: t("alertGroups.columns.status"),
        Cell: ({ row }) => <StatusBadge status={row.status} />,
      },
      { id: "number", header: "#", Cell: NumberCell },
      {
        id: "title",
        header: t("alertGroups.columns.title"),
        className: "min-w-56",
        Cell: TitleCell,
      },
      {
        id: "severity",
        header: t("alertGroups.fields.severity"),
        text: (row) => severityLabel(t, row.severity_level),
      },
      {
        id: "urgent",
        header: t("alertGroups.urgent"),
        Cell: ({ row }) => (row.urgent ? <UrgentMark /> : NONE),
      },
      {
        id: "route",
        header: t("alertGroups.fields.route"),
        className: "min-w-24",
        Cell: RouteCell,
      },
      {
        id: "integrations",
        header: t("alertGroups.fields.integrations"),
        className: "min-w-24",
        Cell: ({ row }) => (
          <span className="wrap-anywhere">{row.integrations.map((i) => i.name).join(", ")}</span>
        ),
      },
      {
        id: "alerts",
        header: t("alertGroups.fields.alerts"),
        className: "whitespace-nowrap",
        text: (row) => alertCounts(t, row),
      },
      {
        id: "started",
        header: t("alertGroups.fields.started"),
        Cell: ({ row }) => <DateTime iso={row.started_at} />,
      },
      {
        id: "duration",
        header: t("alertGroups.fields.duration"),
        Cell: ({ row }) => <Duration start={row.started_at} end={row.resolved_at} />,
      },
      {
        id: "last_change",
        header: t("alertGroups.columns.lastChange"),
        Cell: ({ row }) => <DateTime iso={row.last_changed_at} />,
      },
      {
        id: "reopened",
        header: t("alertGroups.columns.reopened"),
        Cell: ({ row }) =>
          row.reopen_count > 0 ? (
            <span className="whitespace-nowrap">×{row.reopen_count}</span>
          ) : (
            NONE
          ),
      },
      ...labelColumns.map((name): DataColumn<AlertGroup> => ({
        id: `label:${name}`,
        header: name,
        className: "min-w-24 font-mono text-xs",
        Cell: ({ row }) => {
          const value = row.label_values?.[name];
          return value === undefined ? (
            NONE
          ) : (
            <span className="wrap-anywhere" data-testid="label-value">
              {value}
            </span>
          );
        },
      })),
    ],
    [t, labelColumns],
  );
}

/** A compact row of a phone: status, #N, Urgent mark and duration over the title, all one link. */
function CompactRow({ row }: { row: AlertGroup }) {
  const { t } = useTranslation();
  const at = useNow();
  const label = t(row.urgent ? "alertGroups.rowLabelUrgent" : "alertGroups.rowLabel", {
    title: row.title,
    number: row.number,
    status: statusLabel(t, row.status),
    duration: formatElapsed(t, elapsedSeconds(row.started_at, row.resolved_at, at)),
  });
  return (
    <li className="border-t first:border-t-0" data-testid="alert-group-row">
      <Link
        to="/alert-groups/$alertGroupId"
        params={{ alertGroupId: row.id }}
        aria-label={label}
        className="flex min-w-0 flex-col gap-1 px-3 py-2.5 outline-none hover:bg-accent focus-visible:bg-accent focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
      >
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <StatusBadge status={row.status} />
          <span className="font-mono text-sm text-muted-foreground">#{row.number}</span>
          {row.urgent && <UrgentMark />}
          <Duration
            start={row.started_at}
            end={row.resolved_at}
            className="ml-auto text-sm text-muted-foreground"
          />
        </span>
        <span className="text-sm font-medium wrap-anywhere" data-testid="alert-group-title">
          {row.title}
        </span>
      </Link>
    </li>
  );
}

export interface AlertGroupTableProps {
  list: CursorList<AlertGroup>;
  labelColumns: readonly string[];
  /** The text when nothing matches. */
  empty: string;
  wide: boolean;
}

export function AlertGroupTable({ list, labelColumns, empty, wide }: AlertGroupTableProps) {
  const { t } = useTranslation();
  const columns = useColumns(labelColumns);
  if (wide) {
    return (
      <DataTable
        label={t("alertGroups.title")}
        columns={columns}
        list={list}
        rowId={(g) => g.id}
        empty={empty}
      />
    );
  }
  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-lg border">
        {list.items.length === 0 ? (
          <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
            {list.isLoading ? t("common.loading") : list.error ? problemText(t, list.error) : empty}
          </p>
        ) : (
          <ul aria-label={t("alertGroups.title")}>
            {list.items.map((g) => (
              <CompactRow key={g.id} row={g} />
            ))}
          </ul>
        )}
      </div>
      {list.items.length > 0 && list.error !== null && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, list.error)}
        </p>
      )}
      {list.hasMore && (
        <div>
          <Button
            variant="outline"
            className="w-full"
            disabled={list.isLoadingMore}
            onClick={list.loadMore}
          >
            {list.isLoadingMore ? t("common.loading") : t("table.loadMore")}
          </Button>
        </div>
      )}
    </div>
  );
}
