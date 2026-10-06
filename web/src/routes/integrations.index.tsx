// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Integrations (C-05.FR-1): the list with the name, the Connection mode and the time of the last Snapshot, and "Create
// integration" for integrations:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  getListIntegrationsQueryKey,
  listIntegrations,
} from "../api/gen/endpoints/integrations/integrations";
import type { Integration } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { connectionModeLabel } from "../components/integration-form";
import { buttonVariants } from "../components/ui/button";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/integrations/")({
  staticData: { shell: true },
  component: IntegrationsPage,
});

function NameCell({ row }: { row: Integration }) {
  return (
    <Link
      to="/integrations/$integrationId"
      params={{ integrationId: row.id }}
      className="font-medium break-words text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function LastSnapshotCell({ row }: { row: Integration }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  if (!row.last_snapshot_at) {
    return <span className="text-muted-foreground">{t("integrations.never")}</span>;
  }
  return <time dateTime={row.last_snapshot_at}>{dateTime(row.last_snapshot_at)}</time>;
}

function IntegrationsTable() {
  const { t } = useTranslation();
  const list = useCursorList<Integration>(getListIntegrationsQueryKey(), (cursor, signal) =>
    listIntegrations({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<Integration>[] => [
      { id: "name", header: t("integrations.fields.name"), className: "min-w-40", Cell: NameCell },
      {
        id: "connection_mode",
        header: t("integrations.fields.connectionMode"),
        className: "whitespace-nowrap",
        text: (i) => connectionModeLabel(t, i.connection_mode),
      },
      {
        id: "last_snapshot",
        header: t("integrations.fields.lastSnapshot"),
        className: "whitespace-nowrap",
        Cell: LastSnapshotCell,
      },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("integrations.title")}
      columns={columns}
      list={list}
      rowId={(i) => i.id}
      empty={t("integrations.empty")}
    />
  );
}

function IntegrationsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("integrations:write");
  return (
    <RequirePermission permission="integrations:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-semibold tracking-tight">{t("integrations.title")}</h1>
            <p className="text-muted-foreground">{t("integrations.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/integrations/new" className={buttonVariants()}>
              {t("integrations.create.start")}
            </Link>
          )}
        </div>
        <IntegrationsTable />
      </div>
    </RequirePermission>
  );
}
