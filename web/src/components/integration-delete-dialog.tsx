// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Deleting an Integration (C-05.FR-8): the dialog says that its tokens stop working at once and how long its Stored
// Snapshots are kept (retention.stored_snapshots of the Organization). The deletion is soft; the Integration leaves the
// list.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteIntegration,
  getGetIntegrationQueryKey,
  getListIntegrationsQueryKey,
} from "../api/gen/endpoints/integrations/integrations";
import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import type { Integration } from "../api/gen/model";
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

export function IntegrationDeleteDialog({ integration }: { integration: Integration }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const organization = useGetOrganization({ query: { enabled: open } });
  const days = organization.data?.retention.stored_snapshots_days;
  const remove = useMutation({
    mutationFn: () =>
      deleteIntegration(integration.id, { headers: { "If-Match": integration.etag ?? "" } }),
    onSuccess: async () => {
      // The page leaves first, so that nothing reads the deleted Integration again.
      await navigate({ to: "/integrations" });
      queryClient.removeQueries({ queryKey: getGetIntegrationQueryKey(integration.id) });
      void queryClient.invalidateQueries({ queryKey: getListIntegrationsQueryKey() });
    },
    // A newer version was read meanwhile: the page reads it again, and a second Delete sends it.
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({
          queryKey: getGetIntegrationQueryKey(integration.id),
        });
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
        {t("integrations.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("integrations.delete.title", { name: integration.name })}
            </DialogTitle>
            <DialogDescription data-testid="integration-delete-description">
              {t("integrations.delete.tokens")}
              {days !== undefined && (
                <> {t("integrations.delete.snapshotsKept", { count: days })}</>
              )}
            </DialogDescription>
          </DialogHeader>
          {organization.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, organization.error)}
              </AlertDescription>
            </Alert>
          )}
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isStale(remove.error)
                  ? t("integrations.errors.stale")
                  : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending || days === undefined}
              onClick={() => remove.mutate()}
            >
              {t("integrations.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
