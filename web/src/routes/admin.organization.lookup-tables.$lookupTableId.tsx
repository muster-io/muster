// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Lookup table (C-12.FR-9): read-only without lookup-tables:write, else its editor and "Delete". The editor keeps the
// version it was read at and sends it as If-Match. When the table changes elsewhere an untouched editor takes the new
// version and says so; an editor with changes keeps them, and its save is refused (412) with "Someone else changed
// this Lookup table. Reload to see the changes."

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetLookupTableQueryKey,
  getGetLookupTableQueryOptions,
  getListLookupTablesQueryKey,
  updateLookupTable,
  useGetLookupTable,
} from "../api/gen/endpoints/links/links";
import type { LookupTable } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { LookupTableDeleteDialog, LookupTableEditor } from "../components/lookup-table-editor";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/lookup-tables/$lookupTableId")({
  staticData: { shell: true },
  component: LookupTablePage,
});

const LIST = { to: "/admin/organization/lookup-tables" } as const;

function EditForm({ current }: { current: LookupTable }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const canWrite = useCan("lookup-tables:write");
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // A newer version (another Admin, another tab) replaces an untouched editor, which says so.
  if (current.etag !== base.etag && !dirty) {
    setBase(current);
    setReplaced(true);
  }
  return (
    <>
      {replaced && (
        <Alert data-testid="lookup-table-replaced">
          <AlertDescription>{t("lookupTables.errors.changedElsewhere")}</AlertDescription>
        </Alert>
      )}
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      <LookupTableEditor
        key={base.etag}
        table={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={current.etag !== base.etag}
        onDirtyChange={setDirty}
        save={async (input) => {
          await queryClient.cancelQueries({ queryKey: getGetLookupTableQueryKey(base.id) });
          const updated = await updateLookupTable(base.id, input, {
            headers: { "If-Match": base.etag ?? "" },
          });
          // The page leaves first, so that the editor never takes its own save for a change made elsewhere.
          await navigate(LIST);
          queryClient.setQueryData(getGetLookupTableQueryKey(updated.id), updated);
          void queryClient.invalidateQueries({ queryKey: getListLookupTablesQueryKey() });
        }}
        onReload={() => {
          setReloadError(null);
          queryClient
            .fetchQuery({ ...getGetLookupTableQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              setDirty(false);
              setReplaced(false);
              setBase(fresh);
            })
            .catch((err: unknown) => setReloadError(err));
        }}
        onCancel={() => void navigate(LIST)}
      />
    </>
  );
}

function LookupTableView({ lookupTableId }: { lookupTableId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("lookup-tables:write");
  const query = useGetLookupTable(lookupTableId);
  const table = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to={LIST.to} className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("lookupTables.title")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 font-mono text-2xl font-semibold tracking-tight wrap-anywhere">
            {table?.name ?? t("lookupTables.edit.title")}
          </h1>
          {/* Outside the editor, which a newer version replaces; Delete sends the version read last. */}
          {canWrite && table !== undefined && <LookupTableDeleteDialog table={table} />}
        </div>
      </div>
      {table === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <EditForm current={table} />
      )}
    </div>
  );
}

function LookupTablePage() {
  const { lookupTableId } = Route.useParams();
  return (
    <RequirePermission permission="lookup-tables:read">
      <LookupTableView lookupTableId={lookupTableId} />
    </RequirePermission>
  );
}
