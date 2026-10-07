// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Route's editor (C-08.FR-1, FR-2, FR-5): read-only without routes:write, and "Delete" except for the Default route.
// The form keeps the version it was read at and sends it as If-Match. When the Route changes elsewhere (a live hint
// reads it again) an untouched form takes the new version and says so; a form with changes keeps them, says that the
// Route was changed elsewhere, and its save is refused (412) with "Someone else changed this route. Reload to see the
// changes."

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetRouteQueryKey,
  getGetRouteQueryOptions,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
  updateRoute,
  useGetRoute,
} from "../api/gen/endpoints/routes/routes";
import type { Route as RouteModel } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { RouteDeleteDialog } from "../components/route-delete-dialog";
import { RouteForm } from "../components/route-form";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/routes/$routeId")({
  staticData: { shell: true },
  component: RoutePage,
});

function EditForm({ current }: { current: RouteModel }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const canWrite = useCan("routes:write");
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // A newer version (another Admin, another tab) replaces an untouched form, which says so.
  if (current.etag !== base.etag && !dirty) {
    setBase(current);
    setReplaced(true);
  }
  return (
    <>
      {replaced && (
        <Alert data-testid="route-replaced">
          <AlertDescription>{t("routes.errors.changedElsewhere")}</AlertDescription>
        </Alert>
      )}
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      <RouteForm
        key={base.etag}
        base={base}
        route={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={current.etag !== base.etag}
        onDirtyChange={setDirty}
        save={async (input) => {
          await queryClient.cancelQueries({ queryKey: getGetRouteQueryKey(base.id) });
          const updated = await updateRoute(base.id, input, {
            headers: { "If-Match": base.etag },
          });
          // The page leaves first, so that the editor never takes its own save for a change made elsewhere.
          await navigate({ to: "/routes" });
          queryClient.setQueryData(getGetRouteQueryKey(updated.id), updated);
          void queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() });
          void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
        }}
        onReload={() => {
          setReloadError(null);
          queryClient
            .fetchQuery({ ...getGetRouteQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              setDirty(false);
              setReplaced(false);
              setBase(fresh);
            })
            .catch((err: unknown) => setReloadError(err));
        }}
        onCancel={() => void navigate({ to: "/routes" })}
      />
    </>
  );
}

function RouteEditor({ routeId }: { routeId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("routes:write");
  const query = useGetRoute(routeId);
  const route = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to="/routes" className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("routes.title")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 text-2xl font-semibold tracking-tight wrap-anywhere">
            {route?.name ?? t("routes.edit.title")}
          </h1>
          {/* Outside the form, which a newer version replaces; Delete sends the version read last. */}
          {canWrite && route !== undefined && !route.is_default && (
            <RouteDeleteDialog route={route} />
          )}
        </div>
        {route?.is_default === true && (
          <p className="text-sm text-muted-foreground">{t("routes.list.defaultHint")}</p>
        )}
      </div>
      {route === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <EditForm current={route} />
      )}
    </div>
  );
}

function RoutePage() {
  const { routeId } = Route.useParams();
  return (
    <RequirePermission permission="routes:read">
      <RouteEditor routeId={routeId} />
    </RequirePermission>
  );
}
