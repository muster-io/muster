// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The tokens of an Integration (C-05.FR-2): the list with the name, creation and last use, never a value; for
// integrations:write "Create token" (the value and the Alertmanager configuration shown once) and "Revoke", which asks
// first because Alertmanager stops being able to send with the token at once.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createIntegrationToken,
  getGetIntegrationQueryKey,
  getListIntegrationTokensQueryKey,
  revokeIntegrationToken,
  useListIntegrationTokens,
} from "../api/gen/endpoints/integrations/integrations";
import type { Integration, IntegrationToken } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { IntegrationTokenDialog } from "./integration-token-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

function tokenName(t: (key: string) => string, token: IntegrationToken): string {
  return token.name ? token.name : t("integrations.tokens.unnamed");
}

function RevokeDialog({
  integrationId,
  token,
  onClose,
  onRevoked,
}: {
  integrationId: string;
  token: IntegrationToken | null;
  onClose: () => void;
  onRevoked: () => void;
}) {
  const { t } = useTranslation();
  const revoke = useMutation({
    mutationFn: (id: string) => revokeIntegrationToken(integrationId, id),
    onSuccess: () => {
      onClose();
      onRevoked();
    },
  });
  return (
    <Dialog
      open={token !== null}
      onOpenChange={(open) => {
        if (!open) {
          revoke.reset();
          onClose();
        }
      }}
    >
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle className="break-words">
            {t("tokens.revoke.title", { name: token === null ? "" : tokenName(t, token) })}
          </DialogTitle>
          <DialogDescription>{t("integrations.tokens.revokeConfirm")}</DialogDescription>
        </DialogHeader>
        {revoke.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, revoke.error)}
            </AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button
            variant="destructive"
            disabled={revoke.isPending}
            onClick={() => {
              if (token !== null) {
                revoke.mutate(token.id);
              }
            }}
          >
            {t("tokens.revoke.action")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function IntegrationTokens({
  integration,
  canWrite,
}: {
  integration: Integration;
  canWrite: boolean;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const queryClient = useQueryClient();
  const tokens = useListIntegrationTokens(integration.id);
  const [asking, setAsking] = useState<IntegrationToken | null>(null);
  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: getListIntegrationTokensQueryKey(integration.id),
    });
    void queryClient.invalidateQueries({ queryKey: getGetIntegrationQueryKey(integration.id) });
  };
  const items = tokens.data?.items ?? [];
  const writable = canWrite && !integration.builtin;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("integrations.tokens.title")}</h2>
        </CardTitle>
        <CardDescription>{t("integrations.tokens.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {writable && (
          <div>
            <IntegrationTokenDialog
              integrationName={integration.name}
              create={(name) => createIntegrationToken(integration.id, name === "" ? {} : { name })}
              onCreated={refresh}
            />
          </div>
        )}
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground" role="status">
            {tokens.isPending
              ? t("common.loading")
              : tokens.isError
                ? problemText(t, tokens.error)
                : t("integrations.tokens.empty")}
          </p>
        ) : (
          <ul
            className="flex flex-col divide-y rounded-lg border"
            aria-label={t("integrations.tokens.title")}
          >
            {items.map((token) => (
              <li
                key={token.id}
                className="flex flex-col gap-2 px-3 py-2.5 text-sm sm:flex-row sm:items-start"
                data-testid="integration-token-row"
              >
                <div className="flex min-w-0 flex-1 flex-col gap-1">
                  <span className="font-medium break-words" data-testid="integration-token-name">
                    {tokenName(t, token)}
                  </span>
                  <span className="text-muted-foreground">
                    {token.last_used_at
                      ? t("tokens.list.lastUsed", { time: dateTime(token.last_used_at) })
                      : t("tokens.list.neverUsed")}
                  </span>
                  <span className="text-muted-foreground">
                    {t("tokens.list.created", { time: dateTime(token.created_at) })}
                  </span>
                </div>
                {writable && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="self-start"
                    aria-label={t("tokens.revoke.label", { name: tokenName(t, token) })}
                    onClick={() => setAsking(token)}
                  >
                    {t("tokens.revoke.action")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {writable && (
          <RevokeDialog
            integrationId={integration.id}
            token={asking}
            onClose={() => setAsking(null)}
            onRevoked={refresh}
          />
        )}
      </CardContent>
    </Card>
  );
}
