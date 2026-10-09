// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Delete" of a Destination with its dialog (C-13.FR-9, C-11.FR-14, C-15.FR-12). Deleting is always allowed: the
// Destination leaves every Route, and the dialog says what happens to what it sent. Open Root messages of a messenger
// get one final edit. An outgoing webhook in events mode abandons its queued events and its secrets are deleted at
// once; in template mode its open messages get a last update through "Update" first, then the secrets are deleted; in
// mode both, both happen. A Destination changed since it was read is refused (412) and read again.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { getListConnectionsQueryKey } from "../api/gen/endpoints/connections/connections";
import {
  deleteDestination,
  getGetDestinationQueryKey,
  getListDestinationsQueryKey,
} from "../api/gen/endpoints/destinations/destinations";
import type { Destination } from "../api/gen/model";
import { isStale, problemText } from "../lib/api";
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

const CANCEL_ID = "destination-delete-cancel";

/** What deleting a Destination does, by its type and, for an outgoing webhook, its mode. */
export function deleteDescription(t: TFunction, destination: Destination): string {
  if (destination.type !== "webhook") {
    return t("destinations.delete.description");
  }
  switch (destination.mode) {
    case "events":
      return t("destinations.delete.webhookEvents");
    case "template":
      return t("destinations.delete.webhookTemplate");
    default:
      return t("destinations.delete.webhookBoth");
  }
}

export function DestinationDeleteDialog({ destination }: { destination: Destination }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () =>
      deleteDestination(destination.id, { headers: { "If-Match": destination.etag } }),
    onSuccess: async () => {
      await navigate({ to: "/destinations" });
      queryClient.removeQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
      void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    },
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
      }
    },
  });
  return (
    <>
      <Button
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("destinations.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("destinations.delete.title", { name: destination.name })}
            </DialogTitle>
            <DialogDescription data-testid="destination-delete-description">
              {deleteDescription(t, destination)}
            </DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription
                className="flex flex-wrap items-center gap-3 text-current"
                data-testid="destination-delete-error"
              >
                {isStale(remove.error) ? (
                  <>
                    <span>{t("destinations.errors.stale")}</span>
                    {/* The page has read the newer version; the dialog closes to show it. */}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => setOpen(false)}
                    >
                      {t("common.reload")}
                    </Button>
                  </>
                ) : (
                  problemText(t, remove.error)
                )}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" id={CANCEL_ID} />}>
              {t("common.cancel")}
            </DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("destinations.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
