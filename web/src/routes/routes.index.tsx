// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Routes (C-08.FR-3, FR-11): the suggestions on top, then the Routes in evaluation order with the Default route last.
// "Create route" and the moves of the list need routes:write.

import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { useCallback, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { RequirePermission, useCan } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { RouteList } from "../components/route-list";
import { RouteSuggestions } from "../components/route-suggestions";
import { TeamFilter } from "../components/team-filter";
import { buttonVariants } from "../components/ui/button";

// The filter by Team (C-25.AC-11) is in the URL: the id of a Team, or "mine" for the Teams of the signed-in User.
const searchSchema = z.object({
  team: z.string().min(1).optional().catch(undefined),
});

export const Route = createFileRoute("/routes/")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: RoutesPage,
});

function Routes() {
  const { t } = useTranslation();
  const { team } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const canWrite = useCan("routes:write");
  const heading = useRef<HTMLHeadingElement>(null);
  const [focusRouteId, setFocusRouteId] = useState<string>();
  const focused = useCallback(() => setFocusRouteId(undefined), []);
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-col gap-1">
          <PageTitle docs="page.routes" ref={heading} tabIndex={-1} className="outline-none">
            {t("routes.title")}
          </PageTitle>
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
      <TeamFilter
        value={team}
        onChange={(next) => void navigate({ search: { team: next }, replace: true })}
      />
      <RouteList canWrite={canWrite} team={team} focusRouteId={focusRouteId} onFocused={focused} />
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
