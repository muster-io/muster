// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Service accounts (C-04.FR-2): the list with Role, status and token count, and "Create service account" for
// service-accounts:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  getListServiceAccountsQueryKey,
  listServiceAccounts,
} from "../api/gen/endpoints/api-tokens/api-tokens";
import type { ServiceAccount } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { ServiceAccountDialog } from "../components/service-account-dialog";
import { roleLabel, statusLabel } from "../components/user-create-dialog";

export const Route = createFileRoute("/admin/service-accounts/")({
  staticData: { shell: true },
  component: ServiceAccountsPage,
});

function NameCell({ row }: { row: ServiceAccount }) {
  return (
    <Link
      to="/admin/service-accounts/$serviceAccountId"
      params={{ serviceAccountId: row.id }}
      className="font-medium break-words text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function ServiceAccountsTable() {
  const { t } = useTranslation();
  const list = useCursorList<ServiceAccount>(getListServiceAccountsQueryKey(), (cursor, signal) =>
    listServiceAccounts({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<ServiceAccount>[] => [
      {
        id: "name",
        header: t("serviceAccounts.fields.name"),
        className: "min-w-40",
        Cell: NameCell,
      },
      {
        id: "role",
        header: t("serviceAccounts.fields.role"),
        text: (a) => roleLabel(t, a.role),
      },
      {
        id: "status",
        header: t("serviceAccounts.fields.status"),
        text: (a) => statusLabel(t, a.status),
      },
      {
        id: "tokens",
        header: t("serviceAccounts.fields.tokens"),
        className: "text-right",
        text: (a) => String(a.token_count),
      },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("serviceAccounts.title")}
      columns={columns}
      list={list}
      rowId={(a) => a.id}
      empty={t("serviceAccounts.empty")}
    />
  );
}

function ServiceAccountsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("service-accounts:write");
  return (
    <RequirePermission permission="service-accounts:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-semibold tracking-tight">{t("serviceAccounts.title")}</h1>
            <p className="text-muted-foreground">{t("serviceAccounts.hint")}</p>
          </div>
          {canWrite && <ServiceAccountDialog />}
        </div>
        <ServiceAccountsTable />
      </div>
    </RequirePermission>
  );
}
