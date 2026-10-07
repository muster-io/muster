// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Previous Alert Groups (C-09.FR-20): the earlier Alert Groups of the same Route with the same Group key values,
// newest first, each with its number linked, status, start, duration and who resolved it.

import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  getListRelatedAlertGroupsQueryKey,
  listRelatedAlertGroups,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import { problemText } from "../lib/api";
import { formatElapsed, DateTime } from "./relative-time";
import { StatusBadge, resolutionText } from "./alert-group-header";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

export function RelatedAlertGroups({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const query = useInfiniteQuery({
    queryKey: [...getListRelatedAlertGroupsQueryKey(alertGroupId), "pages"],
    queryFn: ({ pageParam, signal }) =>
      listRelatedAlertGroups(alertGroupId, { cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  return (
    <Card data-testid="related-alert-groups">
      <CardHeader>
        <CardTitle>
          <h2>{t("alertGroups.related.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground" role="status">
            {query.isPending
              ? t("common.loading")
              : query.error
                ? problemText(t, query.error)
                : t("alertGroups.related.empty")}
          </p>
        ) : (
          <ul className="flex flex-col" aria-label={t("alertGroups.related.title")}>
            {items.map((g) => (
              <li
                key={g.id}
                className="flex flex-col gap-1 border-t py-2 first:border-t-0"
                data-testid="related-alert-group"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Link
                    to="/alert-groups/$alertGroupId"
                    params={{ alertGroupId: g.id }}
                    className="font-mono text-sm font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
                  >
                    #{g.number}
                  </Link>
                  <StatusBadge status={g.status} />
                  {g.duration_seconds !== null && g.duration_seconds !== undefined && (
                    <span className="text-sm" data-testid="related-duration">
                      {formatElapsed(t, g.duration_seconds)}
                    </span>
                  )}
                </div>
                <div className="flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                  <DateTime iso={g.started_at} />
                  {g.resolution !== undefined && (
                    <span className="wrap-anywhere">{resolutionText(t, g.resolution)}</span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
        {query.hasNextPage && (
          <div>
            <Button
              variant="outline"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? t("common.loading") : t("table.loadMore")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
