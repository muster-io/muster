// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The list of a User's Account links (C-18.FR-10), shared by the profile and the Admin's user page: each link with its
// messenger, Connection, username (or the external id without one), last use and the date it was linked. A Mattermost
// link belongs to its Connection; a Telegram link is valid for every Telegram bot (D305), so its row reads "Telegram ·
// @<username> · for all Telegram bots" with "linked via <Connection>" and the last use on its secondary line. A Bridge
// link belongs to its Connection too and reads "<messenger> · <display name> · via <Connection>" with the last use on
// its secondary line (C-22.FR-15): the messenger is the one the Connection's bridge names in its info, read with
// connections:read, and "Bridge" without it. With remove, each row has a button ("Unlink" on the profile, "Remove" on
// the user page) that asks first.

import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { AccountLink, Connection } from "../api/gen/model";
import { problemText } from "../lib/api";
import { dayIn, useTimeFormat } from "../lib/time";
import { connectionMessengerName, useConnectionsById } from "./destination-label";
import { unreachable } from "./destination-test-panel";
import { elapsedSeconds, formatElapsed, useNow } from "./relative-time";
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

/**
 * The messenger of a link: Telegram and Mattermost by name, the same in every language; a Bridge link the messenger its
 * Connection's bridge names, and "Bridge" while that Connection is unknown or its info unread.
 */
export function linkMessenger(
  t: TFunction,
  link: AccountLink,
  connections: ReadonlyMap<string, Connection>,
): string {
  switch (link.messenger) {
    case "telegram":
      return "Telegram";
    case "mattermost":
      return "Mattermost";
    case "bridge": {
      const connection =
        link.connection === undefined ? undefined : connections.get(link.connection.id);
      return connection === undefined
        ? t("connections.types.bridge")
        : connectionMessengerName(t, connection);
    }
    default:
      return unreachable(link.messenger);
  }
}

/**
 * The account of a link as people know it: "@username", or the external id when the messenger gave no username; a
 * Bridge account by its display name, else its handle or external id as the bridge reported them.
 */
export function accountName(link: AccountLink): string {
  if (link.messenger === "bridge") {
    return link.display_name || link.username || link.external_id;
  }
  return link.username ? `@${link.username}` : link.external_id;
}

/** What removing a link needs: the button's text and label, the confirmation, the request and what follows it. */
export interface RemoveLink {
  action: string;
  ariaLabel: (link: AccountLink) => string;
  confirm: (link: AccountLink) => string;
  run: (link: AccountLink) => Promise<void>;
  onDone: () => void;
}

function LinkRow({
  link,
  connections,
}: {
  link: AccountLink;
  connections: ReadonlyMap<string, Connection>;
}) {
  const { t } = useTranslation();
  const { dateTime, date, timeZone } = useTimeFormat();
  const now = useNow();
  const lastUse = link.last_used_at
    ? t("accountLinks.list.lastUsedAgo", {
        duration: formatElapsed(t, elapsedSeconds(link.last_used_at, null, now)),
      })
    : t("accountLinks.list.never");
  const lastUseTitle = link.last_used_at ? dateTime(link.last_used_at) : undefined;
  const telegram = link.messenger === "telegram";
  const bridge = link.messenger === "bridge";
  const messenger = linkMessenger(t, link, connections);
  const summary = (
    telegram
      ? [messenger, accountName(link), t("accountLinks.list.allTelegramBots")]
      : bridge
        ? [
            messenger,
            accountName(link),
            link.connection === undefined
              ? undefined
              : t("accountLinks.list.via", { connection: link.connection.name }),
          ]
        : [messenger, link.connection?.name, accountName(link), lastUse]
  ).filter((part): part is string => part !== undefined && part !== "");
  const via = (
    telegram
      ? [
          link.connection === undefined
            ? undefined
            : t("accountLinks.list.linkedVia", { connection: link.connection.name }),
          lastUse,
        ]
      : bridge
        ? [lastUse]
        : []
  ).filter((part): part is string => part !== undefined);
  const lastUseOnSecondLine = telegram || bridge;
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-1">
      <span
        className="font-medium break-words"
        data-testid="account-link-summary"
        title={lastUseOnSecondLine ? undefined : lastUseTitle}
      >
        {summary.join(" · ")}
      </span>
      {via.length > 0 && (
        <span
          className="break-words text-muted-foreground"
          data-testid="account-link-via"
          title={lastUseTitle}
        >
          {via.join(" · ")}
        </span>
      )}
      <span className="text-muted-foreground">
        {t("accountLinks.list.linked", {
          date: date(dayIn(new Date(link.created_at), timeZone)),
        })}
      </span>
    </div>
  );
}

/** Asks before a link is removed; button presses from the account no longer act as its User. */
function RemoveDialog({
  link,
  remove,
  onOpenChange,
  pending,
  error,
  onConfirm,
}: {
  link: AccountLink | null;
  remove: RemoveLink;
  onOpenChange: (open: boolean) => void;
  pending: boolean;
  error: unknown;
  onConfirm: (link: AccountLink) => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={link !== null} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{remove.action}</DialogTitle>
          <DialogDescription className="break-words">
            {link === null ? "" : remove.confirm(link)}
          </DialogDescription>
        </DialogHeader>
        {error !== null && error !== undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">{problemText(t, error)}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button
            variant="destructive"
            disabled={pending}
            onClick={() => {
              if (link !== null) {
                onConfirm(link);
              }
            }}
          >
            {remove.action}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** The links of one User, never with a secret; with remove, each row offers to remove it after a confirmation. */
export function AccountLinksTable({
  links,
  loading,
  error,
  empty,
  label,
  remove,
}: {
  links: readonly AccountLink[];
  loading: boolean;
  error: unknown;
  empty: string;
  label: string;
  remove?: RemoveLink;
}) {
  const { t } = useTranslation();
  const connections = useConnectionsById();
  const [asking, setAsking] = useState<AccountLink | null>(null);
  const run = useMutation({
    mutationFn: (link: AccountLink) => (remove ? remove.run(link) : Promise.resolve()),
    onSuccess: () => {
      setAsking(null);
      remove?.onDone();
    },
  });
  if (links.length === 0) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {loading ? t("common.loading") : error ? problemText(t, error) : empty}
      </p>
    );
  }
  return (
    <>
      <ul className="flex flex-col divide-y rounded-lg border" aria-label={label}>
        {links.map((link) => (
          <li
            key={link.id}
            className="flex flex-col gap-2 px-3 py-2.5 text-sm sm:flex-row sm:items-start"
            data-testid="account-link-row"
          >
            <LinkRow link={link} connections={connections} />
            {remove !== undefined && (
              <Button
                variant="outline"
                size="sm"
                className="self-start"
                aria-label={remove.ariaLabel(link)}
                onClick={() => {
                  run.reset();
                  setAsking(link);
                }}
              >
                {remove.action}
              </Button>
            )}
          </li>
        ))}
      </ul>
      {error !== null && error !== undefined && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, error)}
        </p>
      )}
      {remove !== undefined && (
        <RemoveDialog
          link={asking}
          remove={remove}
          onOpenChange={(open) => {
            if (!open) {
              setAsking(null);
            }
          }}
          pending={run.isPending}
          error={run.error}
          onConfirm={(link) => run.mutate(link)}
        />
      )}
    </>
  );
}
