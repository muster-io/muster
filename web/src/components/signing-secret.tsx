// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Signing secret section of an outgoing webhook Destination (C-15.FR-5): whether the secret is set and when it
// changed, never its value. "Regenerate" asks first, then shows the new secret once; requests then carry signatures
// with the new and the previous secret, and the section says "The previous secret still signs, since {date}." with
// "Retire previous secret" until it is retired. Without destinations:write the section only shows the status. A change
// of the Signing secret gives the Destination a new version, so the Destination and its Secrets are read again.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlertIcon } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  generateSigningSecret,
  getGetSigningSecretQueryKey,
  getGetSigningSecretQueryOptions,
  retirePreviousSigningSecret,
} from "../api/gen/endpoints/destinations/destinations";
import type { WebhookDestination } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { refreshWebhook } from "./destination-secrets";
import { SigningSecretDialog } from "./signing-secret-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

export function SigningSecret({ destination }: { destination: WebhookDestination }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const queryClient = useQueryClient();
  const canWrite = useCan("destinations:write");
  const status = useQuery({
    ...getGetSigningSecretQueryOptions(destination.id),
    placeholderData: destination.signing_secret_status,
  }).data;
  const [confirming, setConfirming] = useState(false);
  const regenerateButton = useRef<HTMLButtonElement>(null);
  // The new secret lives here only: the request's result keeps nothing, and closing the dialog drops it.
  const [shown, setShown] = useState<string | null>(null);
  const regenerate = useMutation({
    mutationFn: async () => {
      const generated = await generateSigningSecret(destination.id);
      queryClient.setQueryData(getGetSigningSecretQueryKey(destination.id), generated.status);
      setConfirming(false);
      setShown(generated.secret);
      refreshWebhook(queryClient, destination.id);
    },
  });
  const retire = useMutation({
    mutationFn: async () => {
      await retirePreviousSigningSecret(destination.id);
      refreshWebhook(queryClient, destination.id);
      await queryClient.invalidateQueries({
        queryKey: getGetSigningSecretQueryKey(destination.id),
      });
    },
  });
  const previous = status?.previous_active_since;
  return (
    <Card data-testid="signing-secret">
      <CardHeader>
        <CardTitle>
          <h2>{t("destinations.signing.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">{t("destinations.signing.hint")}</p>
        <p className="text-sm" role="status" data-testid="signing-secret-status">
          {status?.set !== true
            ? t("destinations.signing.notSet")
            : status.updated_at
              ? t("destinations.signing.setChanged", { time: dateTime(status.updated_at) })
              : t("destinations.signing.set")}
        </p>
        {previous && (
          <Alert data-testid="signing-secret-previous">
            <TriangleAlertIcon aria-hidden="true" />
            <AlertDescription className="flex flex-col gap-3">
              <span>{t("destinations.signing.previousActive", { date: dateTime(previous) })}</span>
              {canWrite && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="w-fit"
                  disabled={retire.isPending}
                  onClick={() => retire.mutate()}
                >
                  {t("destinations.signing.retire")}
                </Button>
              )}
            </AlertDescription>
          </Alert>
        )}
        {retire.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current" data-testid="signing-secret-error">
              {problemText(t, retire.error)}
            </AlertDescription>
          </Alert>
        )}
        {canWrite && (
          <Button
            ref={regenerateButton}
            type="button"
            variant="outline"
            className="w-fit"
            onClick={() => {
              regenerate.reset();
              setConfirming(true);
            }}
          >
            {t("destinations.signing.regenerate")}
          </Button>
        )}
        <Dialog open={confirming} onOpenChange={setConfirming}>
          <DialogContent closeLabel={t("common.close")}>
            <DialogHeader>
              <DialogTitle>{t("destinations.signing.confirmTitle")}</DialogTitle>
              <DialogDescription>{t("destinations.signing.confirmHint")}</DialogDescription>
            </DialogHeader>
            {regenerate.isError && (
              <Alert variant="destructive">
                <AlertDescription className="text-current" data-testid="signing-regenerate-error">
                  {problemText(t, regenerate.error)}
                </AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
              <Button
                type="button"
                disabled={regenerate.isPending}
                onClick={() => regenerate.mutate()}
              >
                {t("destinations.signing.regenerate")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        <SigningSecretDialog
          secret={shown}
          finalFocus={regenerateButton}
          onClose={() => setShown(null)}
        />
      </CardContent>
    </Card>
  );
}
