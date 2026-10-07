// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Deleting a Route (C-08.FR-9, C-09.FR-19): the dialog says that the Alerts it would take go to the next matching
// Route. The Default route has no Delete. A Route with open Alert Groups cannot be deleted while they stay on it: the
// dialog says how many there are and offers "Move and delete", which moves them to the Default route — each gets a
// Timeline entry — and then deletes the Route. When new Alert Groups started meanwhile the deletion is refused (409);
// the dialog reads the Route again and asks once more with the new count.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetAlertGroupCountsQueryKey,
  getGetAlertGroupStatisticsQueryKey,
  getListAlertGroupsQueryKey,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import {
  deleteRoute,
  getGetRouteQueryKey,
  getGetRouteQueryOptions,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
  moveOpenAlertGroups,
} from "../api/gen/endpoints/routes/routes";
import type { Route } from "../api/gen/model";
import { isApiError, isStale, problemText } from "../lib/api";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

/** A deletion refused because the Route still has open Alert Groups. */
function hasOpenAlertGroups(err: unknown): boolean {
  return isApiError(err) && err.status === 409 && err.type.endsWith("/route-has-open-alert-groups");
}

export function RouteDeleteDialog({ route }: { route: Route }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const count = route.open_alert_group_count;
  const remove = useMutation({
    mutationFn: async ({ move }: { move: boolean }) => {
      if (move) {
        await moveOpenAlertGroups(route.id);
      }
      await deleteRoute(route.id, { headers: { "If-Match": route.etag } });
    },
    onSuccess: async () => {
      // The page leaves first, so that nothing reads the deleted Route again.
      await navigate({ to: "/routes" });
      queryClient.removeQueries({ queryKey: getGetRouteQueryKey(route.id) });
      void queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListAlertGroupsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getGetAlertGroupCountsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getGetAlertGroupStatisticsQueryKey() });
      // The moved Alert Groups' pages name their new Route.
      void queryClient.invalidateQueries({
        predicate: (q) => String(q.queryKey[0]).startsWith("/api/v1/alert-groups/"),
      });
    },
    onError: async (err) => {
      if (isStale(err)) {
        // A newer version was read meanwhile: the page reads it again, and a second Delete sends it.
        void queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(route.id) });
      } else if (hasOpenAlertGroups(err)) {
        // New Alert Groups started meanwhile: the Route is read again, so the dialog asks with their count.
        await queryClient
          .fetchQuery({ ...getGetRouteQueryOptions(route.id), staleTime: 0 })
          .catch(() => undefined);
      }
    },
  });
  const refused = hasOpenAlertGroups(remove.error);
  const moving = count > 0;
  return (
    <>
      <Button
        type="button"
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
          // The count of open Alert Groups is read again for the dialog.
          void queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(route.id) });
        }}
      >
        {t("routes.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("routes.delete.title", { name: route.name })}
            </DialogTitle>
            <DialogDescription data-testid="route-delete-description">
              {moving
                ? t("routes.delete.openAlertGroups", { count })
                : t("routes.delete.description")}
            </DialogDescription>
          </DialogHeader>
          {refused && (
            <Alert data-testid="route-delete-refused">
              <AlertDescription>{t("routes.delete.startedMeanwhile")}</AlertDescription>
            </Alert>
          )}
          {remove.isError && !refused && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isStale(remove.error) ? t("routes.errors.stale") : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate({ move: moving })}
            >
              {moving ? t("routes.delete.moveAndDelete") : t("routes.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
