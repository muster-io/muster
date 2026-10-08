// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Lookup tables (C-12.FR-9): the tables with their name, description, columns and the count of their
// rows, page by page (lookup_table.page_max), and "Create table" with lookup-tables:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { getListLookupTablesQueryKey, listLookupTables } from "../api/gen/endpoints/links/links";
import type { LookupTable } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/admin/organization/lookup-tables/")({
  staticData: { shell: true },
  component: LookupTablesPage,
});

function NameCell({ row }: { row: LookupTable }) {
  return (
    <Link
      to="/admin/organization/lookup-tables/$lookupTableId"
      params={{ lookupTableId: row.id }}
      className="font-mono font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function ColumnsCell({ row }: { row: LookupTable }) {
  return <span className="font-mono text-xs wrap-anywhere">{row.columns.join(", ")}</span>;
}

function RowsCell({ row }: { row: LookupTable }) {
  const { t } = useTranslation();
  return (
    <span className="whitespace-nowrap" data-testid="lookup-table-rows">
      {t("lookupTables.list.rows", { count: row.entries.length })}
    </span>
  );
}

function LookupTablesTable() {
  const { t } = useTranslation();
  const list = useCursorList<LookupTable>(getListLookupTablesQueryKey(), (cursor, signal) =>
    listLookupTables({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<LookupTable>[] => [
      { id: "name", header: t("lookupTables.fields.name"), className: "min-w-32", Cell: NameCell },
      {
        id: "description",
        header: t("lookupTables.fields.description"),
        className: "hidden min-w-48 sm:table-cell",
        text: (row) => row.description ?? "",
      },
      { id: "columns", header: t("lookupTables.fields.columns"), Cell: ColumnsCell },
      { id: "rows", header: t("lookupTables.fields.rows"), Cell: RowsCell },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("lookupTables.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={t("lookupTables.list.empty")}
    />
  );
}

function LookupTablesPage() {
  const { t } = useTranslation();
  const canWrite = useCan("lookup-tables:write");
  return (
    <RequirePermission permission="lookup-tables:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-semibold tracking-tight">{t("lookupTables.title")}</h1>
            <p className="text-muted-foreground">{t("lookupTables.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/admin/organization/lookup-tables/new" className={buttonVariants()}>
              {t("lookupTables.create.start")}
            </Link>
          )}
        </div>
        <LookupTablesTable />
      </div>
    </RequirePermission>
  );
}
