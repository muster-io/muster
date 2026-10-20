// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Connections (C-13.FR-1, C-14.FR-1, C-22.FR-1, C-24.FR-1): the Connections with their type — a Bridge by the messenger
// its bridge names once a check read it — name, with the host of a Mattermost server or a bridge's base URL, or the
// @username of a Telegram bot or the name of a Slack bot once a Connection check found it (D309), and the number of
// Destinations that use them, page by page, and "Create connection" with connections:write.

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
import { connectionAccount, connectionMessengerName } from "../components/destination-label";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/connections/")({
  staticData: { shell: true },
  component: ConnectionsPage,
});

function NameCell({ row }: { row: Connection }) {
  const account = connectionAccount(row);
  return (
    <span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
      <Link
        to="/connections/$connectionId"
        params={{ connectionId: row.id }}
        className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
      >
        {row.name}
      </Link>
      {account !== undefined && (
        <span className="text-muted-foreground wrap-anywhere" data-testid="connection-account">
          {account}
        </span>
      )}
    </span>
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
        text: (row) => connectionMessengerName(t, row),
      },
      { id: "name", header: t("connections.fields.name"), className: "min-w-40", Cell: NameCell },
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
