// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Connections (C-13.FR-1): the Connections with their type, name and the number of Destinations that use them, page by
// page, and "Create connection" with connections:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  getListConnectionsQueryKey,
  listConnections,
} from "../api/gen/endpoints/connections/connections";
import type { Connection } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/connections/")({
  staticData: { shell: true },
  component: ConnectionsPage,
});

/** The name of a messenger, which is not translated. */
function connectionTypeName(type: Connection["type"]): string {
  return type === "telegram" ? "Telegram" : "Mattermost";
}

function NameCell({ row }: { row: Connection }) {
  return (
    <Link
      to="/connections/$connectionId"
      params={{ connectionId: row.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function DestinationsCell({ row }: { row: Connection }) {
  return (
    <span className="whitespace-nowrap" data-testid="connection-destinations">
      {row.destination_count}
    </span>
  );
}

function ConnectionsTable() {
  const { t } = useTranslation();
  const list = useCursorList<Connection>(getListConnectionsQueryKey(), (cursor, signal) =>
    listConnections({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<Connection>[] => [
      {
        id: "type",
        header: t("connections.fields.type"),
        text: (row) => connectionTypeName(row.type),
      },
      { id: "name", header: t("connections.fields.name"), className: "min-w-32", Cell: NameCell },
      { id: "destinations", header: t("connections.fields.destinations"), Cell: DestinationsCell },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("connections.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={t("connections.list.empty")}
    />
  );
}

function ConnectionsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("connections:write");
  return (
    <RequirePermission permission="connections:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-semibold tracking-tight">{t("connections.title")}</h1>
            <p className="text-muted-foreground">{t("connections.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/connections/new" className={buttonVariants()}>
              {t("connections.create.start")}
            </Link>
          )}
        </div>
        <ConnectionsTable />
      </div>
    </RequirePermission>
  );
}
