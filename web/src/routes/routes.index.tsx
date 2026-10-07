// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Routes (C-08.FR-3, FR-11): the suggestions on top, then the Routes in evaluation order with the Default route last.
// "Create route" and the moves of the list need routes:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { RequirePermission, useCan } from "../components/app-shell";
import { RouteList } from "../components/route-list";
import { RouteSuggestions } from "../components/route-suggestions";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/routes/")({
  staticData: { shell: true },
  component: RoutesPage,
});

function Routes() {
  const { t } = useTranslation();
  const canWrite = useCan("routes:write");
  const heading = useRef<HTMLHeadingElement>(null);
  const [focusRouteId, setFocusRouteId] = useState<string>();
  const focused = useCallback(() => setFocusRouteId(undefined), []);
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1
            ref={heading}
            tabIndex={-1}
            className="text-2xl font-semibold tracking-tight outline-none"
          >
            {t("routes.title")}
          </h1>
          <p className="text-muted-foreground">{t("routes.hint")}</p>
        </div>
        {canWrite && (
          <Link to="/routes/new" className={buttonVariants()}>
            {t("routes.create.start")}
          </Link>
        )}
      </div>
      <RouteSuggestions
        canWrite={canWrite}
        onAccepted={(route) => setFocusRouteId(route.id)}
        onDismissed={() => heading.current?.focus()}
      />
      <RouteList canWrite={canWrite} focusRouteId={focusRouteId} onFocused={focused} />
    </div>
  );
}

function RoutesPage() {
  return (
    <RequirePermission permission="routes:read">
      <Routes />
    </RequirePermission>
  );
}
