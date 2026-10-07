// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Deleting a Route (C-08.FR-9): the dialog says that the Alerts it would take go to the next matching Route. The
// Default route has no Delete. A Route with open Alert Groups is refused until they are moved, which a later story
// adds to this dialog.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteRoute,
  getGetRouteQueryKey,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
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

export function RouteDeleteDialog({ route }: { route: Route }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteRoute(route.id, { headers: { "If-Match": route.etag } }),
    onSuccess: async () => {
      // The page leaves first, so that nothing reads the deleted Route again.
      await navigate({ to: "/routes" });
      queryClient.removeQueries({ queryKey: getGetRouteQueryKey(route.id) });
      void queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
    },
    // A newer version was read meanwhile: the page reads it again, and a second Delete sends it.
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetRouteQueryKey(route.id) });
      }
    },
  });
  const openGroups =
    isApiError(remove.error) &&
    remove.error.status === 409 &&
    remove.error.type.endsWith("/route-has-open-alert-groups");
  return (
    <>
      <Button
        type="button"
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
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
            <DialogDescription>{t("routes.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isStale(remove.error)
                  ? t("routes.errors.stale")
                  : openGroups
                    ? t("routes.delete.openAlertGroups")
                    : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("routes.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
