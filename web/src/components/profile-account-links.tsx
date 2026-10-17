// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Messenger accounts" of the profile (C-18.FR-2, FR-3, FR-6, FR-10, C-03.FR-12): the signed-in user's Account links
// with "Unlink", "Link Telegram" and "Link Mattermost" for the Connections of each messenger, and "Link through
// {Connection}" for each Bridge Connection (C-22.FR-15). The Connections come from listLinkableConnections, which needs
// no Permission and gives their id, name and messenger, and for a Bridge how it links; the profile reads the
// Connections themselves only for the messenger a Bridge link names, with connections:read. The account-links hint
// keeps the list current when a bot links an account. With a Telegram link the button reads "Relink Telegram"; a hint
// says that Mattermost links are per Connection (D305).

import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  deleteMyAccountLink,
  getListMyAccountLinksQueryKey,
  useListLinkableConnections,
  useListMyAccountLinks,
} from "../api/gen/endpoints/account-links/account-links";
import { problemText } from "../lib/api";
import { AccountLinksTable, accountName } from "./account-links-table";
import { LinkBridgeDialog } from "./link-bridge-dialog";
import { LinkMattermostDialog } from "./link-mattermost-dialog";
import { LinkTelegramDialog } from "./link-telegram-dialog";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

export function ProfileAccountLinks() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const links = useListMyAccountLinks();
  const connections = useListLinkableConnections();
  const all = connections.data?.items ?? [];
  const telegram = all.filter((c) => c.messenger === "telegram");
  const mattermost = all.filter((c) => c.messenger === "mattermost");
  const bridge = all.filter((c) => c.messenger === "bridge");
  const items = links.data?.items ?? [];
  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>
          <h2>{t("accountLinks.title")}</h2>
        </CardTitle>
        <CardDescription>{t("accountLinks.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <AccountLinksTable
          label={t("accountLinks.list.label")}
          links={items}
          loading={links.isPending}
          error={links.error}
          empty={t("accountLinks.empty")}
          remove={{
            action: t("accountLinks.unlink.action"),
            ariaLabel: (link) => t("accountLinks.unlink.label", { account: accountName(link) }),
            confirm: (link) => t("accountLinks.unlink.confirm", { account: accountName(link) }),
            run: (link) => deleteMyAccountLink(link.id),
            onDone: () =>
              void queryClient.invalidateQueries({ queryKey: getListMyAccountLinksQueryKey() }),
          }}
        />
        {connections.isError && (
          <p className="text-sm text-destructive" role="alert">
            {problemText(t, connections.error)}
          </p>
        )}
        {(telegram.length > 0 || mattermost.length > 0 || bridge.length > 0) && (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap gap-2">
              {telegram.length > 0 && (
                <LinkTelegramDialog
                  connections={telegram}
                  relink={items.some((l) => l.messenger === "telegram")}
                />
              )}
              {mattermost.length > 0 && <LinkMattermostDialog connections={mattermost} />}
              {bridge.map((c) => (
                <LinkBridgeDialog key={c.id} connection={c} />
              ))}
            </div>
            {mattermost.length > 0 && (
              <p className="text-sm text-muted-foreground" data-testid="mattermost-links-hint">
                {t("accountLinks.mattermost.perConnection")}
              </p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
