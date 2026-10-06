// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Stored Snapshots of an Integration (C-05.FR-7): a table of the recent ones, newest first, with the time received,
// the size, the group key, the Alert count and the processing state, filtered by state and time range with the filters
// in the URL of the Integration page. Each row opens the viewer.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getListStoredSnapshotsQueryKey,
  listStoredSnapshots,
} from "../api/gen/endpoints/integrations/integrations";
import {
  type ListStoredSnapshotsParams,
  SnapshotState,
  type StoredSnapshotSummary,
} from "../api/gen/model";
import { startOfDayIn, useTimeFormat } from "../lib/time";
import { type DataColumn, DataTable, useCursorList } from "./data-table";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** The filters of the Stored Snapshots in the URL of the Integration page. */
export const snapshotSearchSchema = z.object({
  snapshot_state: z.enum(SnapshotState).optional().catch(undefined),
  snapshot_from: z.string().regex(DAY).optional().catch(undefined),
  snapshot_to: z.string().regex(DAY).optional().catch(undefined),
});
export type SnapshotSearch = z.infer<typeof snapshotSearchSchema>;

/** A size in bytes as the language writes it, in SI units, such as "8 B" or "1.2 kB". */
export function formatBytes(t: TFunction, bytes: number, locale: string): string {
  const units = [
    ["megabyte", 1_000_000],
    ["kilobyte", 1000],
  ] as const;
  for (const [unit, size] of units) {
    if (bytes >= size) {
      return new Intl.NumberFormat(locale, {
        style: "unit",
        unit,
        unitDisplay: "short",
        maximumFractionDigits: 1,
      }).format(bytes / size);
    }
  }
  return t("snapshots.bytes", { size: new Intl.NumberFormat(locale).format(bytes) });
}

/** The processing state, with the error of a failed Snapshot. */
export function snapshotStateText(t: TFunction, snapshot: StoredSnapshotSummary): string {
  switch (snapshot.state) {
    case "pending":
      return t("snapshots.state.pending");
    case "processed":
      return t("snapshots.state.processed");
    default:
      return snapshot.processing_error
        ? t("snapshots.state.failedWith", { error: snapshot.processing_error })
        : t("snapshots.state.failed");
  }
}

function stateLabel(t: TFunction, state: SnapshotState): string {
  switch (state) {
    case "pending":
      return t("snapshots.state.pending");
    case "processed":
      return t("snapshots.state.processed");
    default:
      return t("snapshots.state.failed");
  }
}

function ReceivedCell({ row }: { row: StoredSnapshotSummary }) {
  const { dateTime } = useTimeFormat();
  return (
    <Link
      to="/integrations/$integrationId/snapshots/$storedSnapshotId"
      params={{ integrationId: row.integration.id, storedSnapshotId: row.id }}
      className="font-medium whitespace-nowrap text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      <time dateTime={row.received_at}>{dateTime(row.received_at)}</time>
    </Link>
  );
}

function GroupKeyCell({ row }: { row: StoredSnapshotSummary }) {
  if (!row.group_key) {
    return <span className="text-muted-foreground">—</span>;
  }
  return <code className="font-mono text-xs wrap-anywhere">{row.group_key}</code>;
}

function StateCell({ row }: { row: StoredSnapshotSummary }) {
  const { t } = useTranslation();
  return (
    <span
      className={row.state === "failed" ? "text-destructive wrap-anywhere" : undefined}
      data-testid="snapshot-state"
    >
      {snapshotStateText(t, row)}
    </span>
  );
}

function Filters({
  search,
  onSearch,
}: {
  search: SnapshotSearch;
  onSearch: (patch: Partial<SnapshotSearch>) => void;
}) {
  const { t } = useTranslation();
  const filtered = Object.values(search).some((v) => v !== undefined);
  return (
    <div className="flex flex-col gap-2" role="search" aria-label={t("snapshots.filters.label")}>
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="snapshot-state">{t("snapshots.filters.state")}</Label>
          <NativeSelect
            id="snapshot-state"
            className="w-full"
            value={search.snapshot_state ?? ""}
            onChange={(e) =>
              onSearch({
                snapshot_state: Object.values(SnapshotState).find((s) => s === e.target.value),
              })
            }
          >
            <NativeSelectOption value="">{t("snapshots.filters.anyState")}</NativeSelectOption>
            {Object.values(SnapshotState).map((s) => (
              <NativeSelectOption key={s} value={s}>
                {stateLabel(t, s)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="snapshot-from">{t("snapshots.filters.from")}</Label>
          <Input
            id="snapshot-from"
            type="date"
            value={search.snapshot_from ?? ""}
            max={search.snapshot_to}
            onChange={(e) =>
              onSearch({ snapshot_from: DAY.test(e.target.value) ? e.target.value : undefined })
            }
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="snapshot-to">{t("snapshots.filters.to")}</Label>
          <Input
            id="snapshot-to"
            type="date"
            value={search.snapshot_to ?? ""}
            min={search.snapshot_from}
            onChange={(e) =>
              onSearch({ snapshot_to: DAY.test(e.target.value) ? e.target.value : undefined })
            }
          />
        </div>
      </div>
      {filtered && (
        <div>
          <Button
            variant="link"
            className="px-0"
            onClick={() =>
              onSearch({
                snapshot_state: undefined,
                snapshot_from: undefined,
                snapshot_to: undefined,
              })
            }
          >
            {t("snapshots.filters.clear")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function StoredSnapshots({
  integrationId,
  search,
  onSearch,
}: {
  integrationId: string;
  search: SnapshotSearch;
  onSearch: (patch: Partial<SnapshotSearch>) => void;
}) {
  const { t, i18n } = useTranslation();
  const { timeZone } = useTimeFormat();
  const locale = i18n.resolvedLanguage ?? "en";
  const params: ListStoredSnapshotsParams = useMemo(
    () => ({
      integration: integrationId,
      state: search.snapshot_state ? [search.snapshot_state] : undefined,
      from: search.snapshot_from
        ? startOfDayIn(search.snapshot_from, timeZone).toISOString()
        : undefined,
      // The end day is included: the range ends when the next day starts.
      to: search.snapshot_to
        ? startOfDayIn(search.snapshot_to, timeZone, 1).toISOString()
        : undefined,
    }),
    [integrationId, search, timeZone],
  );
  const list = useCursorList<StoredSnapshotSummary>(
    getListStoredSnapshotsQueryKey(params),
    (cursor, signal) => listStoredSnapshots({ ...params, cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<StoredSnapshotSummary>[] => [
      { id: "received", header: t("snapshots.columns.received"), Cell: ReceivedCell },
      {
        id: "size",
        header: t("snapshots.columns.size"),
        className: "text-right whitespace-nowrap",
        text: (s) => formatBytes(t, s.size_bytes, locale),
      },
      {
        id: "group_key",
        header: t("snapshots.columns.groupKey"),
        className: "min-w-48",
        Cell: GroupKeyCell,
      },
      {
        id: "alerts",
        header: t("snapshots.columns.alerts"),
        className: "text-right",
        text: (s) =>
          s.alert_count === null || s.alert_count === undefined ? "—" : String(s.alert_count),
      },
      { id: "state", header: t("snapshots.columns.state"), className: "min-w-28", Cell: StateCell },
    ],
    [t, locale],
  );
  const filtered = Object.values(search).some((v) => v !== undefined);
  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>
          <h2>{t("snapshots.title")}</h2>
        </CardTitle>
        <CardDescription>{t("snapshots.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <Filters search={search} onSearch={onSearch} />
        <DataTable
          label={t("snapshots.title")}
          columns={columns}
          list={list}
          rowId={(s) => s.id}
          empty={filtered ? t("snapshots.emptyFiltered") : t("snapshots.empty")}
        />
      </CardContent>
    </Card>
  );
}
