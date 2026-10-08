// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The callback address of a Mattermost Connection (C-13.FR-13): the address that the Mattermost server calls when
// someone presses a button, read-only from the Connection, with a copy button and the hint about
// AllowedUntrustedInternalConnections, since Muster cannot read that setting through the bot.

import { useTranslation } from "react-i18next";

import { CopyBlock } from "./integration-token-dialog";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

export function CallbackHint({ callbackUrl }: { callbackUrl: string }) {
  const { t } = useTranslation();
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.callback.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <CopyBlock
          id="connection-callback-url"
          label={t("connections.callback.label")}
          text={callbackUrl}
          testId="connection-callback-url"
        />
        <p
          className="text-sm wrap-anywhere text-muted-foreground"
          data-testid="connection-callback-hint"
        >
          {t("connections.callback.hint", { address: callbackUrl })}
        </p>
      </CardContent>
    </Card>
  );
}
