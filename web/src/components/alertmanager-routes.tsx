// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alertmanager routes of an Integration (C-06.FR-18, C-05.FR-7): the routes Muster saw in the groupKey of its
// Snapshots, with the learned repeat interval, the time to resolve by absence that follows from it and the truncated
// groups. A route whose interval is too long shows the warning with the recommended route snippet and "Copy".

import { TriangleAlertIcon } from "lucide-react";
import { Fragment } from "react";
import { useTranslation } from "react-i18next";

import { useListAlertmanagerRoutes } from "../api/gen/endpoints/integrations/integrations";
import type { AlertmanagerRoute } from "../api/gen/model";
import { problemText } from "../lib/api";
import { formatDuration } from "../lib/time";
import { CopyBlock } from "./integration-token-dialog";
import { longIntervalText } from "./integration-warnings";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

const COLUMNS = 4;

/** The warning of a route with a long learned interval, and the snippet that sets a shorter one. */
function LongIntervalWarning({ route, index }: { route: AlertmanagerRoute; index: number }) {
  const { t } = useTranslation();
  const interval = route.learned_repeat_interval_seconds ?? 0;
  return (
    // The block takes the width of the scroll box, not of the table, and stays in view when the table scrolls
    // sideways; a long snippet scrolls inside it.
    <div
      className="sticky left-3 flex w-[calc(100cqw-1.5rem)] flex-col gap-3"
      data-testid="route-long-interval"
    >
      <p className="flex items-start gap-2 break-words">
        <TriangleAlertIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
        />
        <span className="min-w-0">
          {longIntervalText(
            t,
            route.route_path,
            interval,
            route.resolve_by_absence_after_seconds ?? undefined,
          )}
        </span>
      </p>
      {route.recommended_snippet && (
        <CopyBlock
          id={`route-snippet-${index}`}
          label={t("alertmanagerRoutes.snippet")}
          text={route.recommended_snippet}
          wrap={false}
          testId="route-snippet"
        />
      )}
    </div>
  );
}

function RoutesTable({ routes }: { routes: readonly AlertmanagerRoute[] }) {
  const { t } = useTranslation();
  return (
    <div className="@container relative overflow-x-auto rounded-lg border">
      <table
        className="w-full border-collapse text-left text-sm"
        aria-label={t("alertmanagerRoutes.title")}
      >
        <thead className="bg-muted/50">
          <tr>
            <th
              scope="col"
              className="min-w-40 px-3 py-2 align-bottom font-medium text-muted-foreground"
            >
              {t("alertmanagerRoutes.columns.route")}
            </th>
            <th scope="col" className="px-3 py-2 align-bottom font-medium text-muted-foreground">
              {t("alertmanagerRoutes.columns.repeatInterval")}
            </th>
            <th scope="col" className="px-3 py-2 align-bottom font-medium text-muted-foreground">
              {t("alertmanagerRoutes.columns.resolvesAfter")}
            </th>
            <th scope="col" className="px-3 py-2 align-bottom font-medium text-muted-foreground">
              {t("alertmanagerRoutes.columns.truncatedGroups")}
            </th>
          </tr>
        </thead>
        <tbody>
          {routes.map((route, index) => (
            <Fragment key={route.route_path}>
              <tr className="border-t align-top" data-testid="alertmanager-route">
                <th scope="row" className="px-3 py-2 text-left font-normal">
                  <code className="font-mono text-xs wrap-anywhere" data-testid="route-path">
                    {route.route_path}
                  </code>
                </th>
                <td className="px-3 py-2 whitespace-nowrap" data-testid="route-repeat-interval">
                  {route.learned_repeat_interval_seconds === null ||
                  route.learned_repeat_interval_seconds === undefined ? (
                    <span className="text-muted-foreground">
                      {t("alertmanagerRoutes.notLearned")}
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1">
                      {route.long_interval_warning && (
                        <TriangleAlertIcon
                          aria-hidden="true"
                          className="size-4 shrink-0 text-amber-700 dark:text-warning"
                        />
                      )}
                      {formatDuration(t, route.learned_repeat_interval_seconds)}
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 whitespace-nowrap" data-testid="route-resolves-after">
                  {route.resolve_by_absence_after_seconds === null ||
                  route.resolve_by_absence_after_seconds === undefined
                    ? "—"
                    : formatDuration(t, route.resolve_by_absence_after_seconds)}
                </td>
                <td
                  className={
                    route.truncated_group_count > 0
                      ? "px-3 py-2 text-right font-medium text-amber-800 dark:text-warning"
                      : "px-3 py-2 text-right"
                  }
                  data-testid="route-truncated-groups"
                >
                  {route.truncated_group_count}
                </td>
              </tr>
              {route.long_interval_warning && (
                <tr className="bg-warning-surface/60">
                  <td colSpan={COLUMNS} className="px-3 pt-1 pb-3">
                    <LongIntervalWarning route={route} index={index} />
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function AlertmanagerRoutes({ integrationId }: { integrationId: string }) {
  const { t } = useTranslation();
  const query = useListAlertmanagerRoutes(integrationId);
  const routes = query.data?.items;
  return (
    <Card className="lg:col-span-2" data-testid="alertmanager-routes">
      <CardHeader>
        <CardTitle>
          <h2>{t("alertmanagerRoutes.title")}</h2>
        </CardTitle>
        <CardDescription>{t("alertmanagerRoutes.hint")}</CardDescription>
      </CardHeader>
      <CardContent>
        {routes === undefined ? (
          <p className="text-sm text-muted-foreground" role="status">
            {query.isError ? problemText(t, query.error) : t("common.loading")}
          </p>
        ) : routes.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("alertmanagerRoutes.empty")}</p>
        ) : (
          <RoutesTable routes={routes} />
        )}
      </CardContent>
    </Card>
  );
}
