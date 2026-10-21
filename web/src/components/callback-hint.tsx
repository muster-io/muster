// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The callback address of a Connection: the address that the messenger calls when someone presses a button, read-only
// from the Connection, with a copy button and its hint. A Mattermost Connection (C-13.FR-13) shows it in a card of its
// own, with the hint about AllowedUntrustedInternalConnections, since Muster cannot read that setting through the bot;
// a Slack Connection in HTTP mode (C-24.FR-1, FR-9) shows it in its form, with the hint of reference.md. The Mattermost
// hint ends with "Learn more".

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { DocsLink } from "./docs-link";
import { CopyBlock } from "./integration-token-dialog";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

/** The callback address with its copy button and the hint below it, which names the address. */
export function CallbackAddress({
  callbackUrl,
  hint,
  docs,
  testId,
}: {
  callbackUrl: string;
  hint: string;
  /** A "Learn more" link at the end of the hint. */
  docs?: ReactNode;
  /** The base of the test ids of the address and the hint. */
  testId: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <CopyBlock
        id="connection-callback-url"
        label={t("connections.callback.label")}
        text={callbackUrl}
        testId={`${testId}-url`}
      />
      <p className="text-sm wrap-anywhere text-muted-foreground" data-testid={`${testId}-hint`}>
        {hint}
        {docs !== undefined && <> {docs}</>}
      </p>
    </div>
  );
}

export function CallbackHint({ callbackUrl }: { callbackUrl: string }) {
  const { t } = useTranslation();
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.callback.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <CallbackAddress
          callbackUrl={callbackUrl}
          hint={t("connections.callback.hint", { address: callbackUrl })}
          docs={
            <DocsLink
              to="field.mattermost_callback"
              kind="learn_more"
              about={t("docs.fields.mattermostCallback")}
            />
          }
          testId="connection-callback"
        />
      </CardContent>
    </Card>
  );
}
