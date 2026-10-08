// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create table" (C-12.FR-9): the editor of a new Lookup table; the list opens after it is created.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  createLookupTable,
  getGetLookupTableQueryKey,
  getListLookupTablesQueryKey,
} from "../api/gen/endpoints/links/links";
import { RequirePermission } from "../components/app-shell";
import { LookupTableEditor } from "../components/lookup-table-editor";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/admin/organization/lookup-tables/new")({
  staticData: { shell: true },
  component: NewLookupTablePage,
});

function NewLookupTable() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/admin/organization/lookup-tables"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("lookupTables.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("lookupTables.create.title")}</h1>
      </div>
      <LookupTableEditor
        submitLabel={t("lookupTables.create.submit")}
        save={async (input) => {
          const created = await createLookupTable(input);
          queryClient.setQueryData(getGetLookupTableQueryKey(created.id), created);
          void queryClient.invalidateQueries({ queryKey: getListLookupTablesQueryKey() });
          await navigate({ to: "/admin/organization/lookup-tables" });
        }}
        onCancel={() => void navigate({ to: "/admin/organization/lookup-tables" })}
      />
    </div>
  );
}

function NewLookupTablePage() {
  return (
    <RequirePermission permission="lookup-tables:write">
      <NewLookupTable />
    </RequirePermission>
  );
}
