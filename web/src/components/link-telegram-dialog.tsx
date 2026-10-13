// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Telegram" of the profile (C-18.FR-2): for the chosen Telegram Connection (no choice when there is one) the
// dialog asks for a deep link and shows it as "Open Telegram" and as a QR code, with the time it expires. When the bot
// links the account, the account-links hint closes the dialog and the list shows the link; once the link expired, the
// dialog offers a new one. The deep link lives only in this dialog's state: the request's result keeps none, and
// closing the dialog drops it. With a Telegram link already made, the button and the dialog read "Relink Telegram":
// the link is valid for every Telegram bot, and a new one replaces it.

import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { startTelegramLink } from "../api/gen/endpoints/account-links/account-links";
import type { LinkableConnection, TelegramLinkStart } from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { onHint } from "../lib/live";
import { useTimeFormat } from "../lib/time";
import { QrCode } from "./qr-code";
import { Alert, AlertDescription } from "./ui/alert";
import { Button, buttonVariants } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The text of a refused link request: a busy messenger (503) asks to try again shortly. */
export function startProblemText(t: TFunction, err: unknown): string {
  return isApiError(err) && err.status === 503
    ? t("accountLinks.errors.busy")
    : problemText(t, err);
}

export function LinkTelegramDialog({
  connections,
  relink = false,
}: {
  connections: readonly LinkableConnection[];
  /** The user has a Telegram link already. */
  relink?: boolean;
}) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  const title = relink ? t("accountLinks.telegram.relink") : t("accountLinks.telegram.start");
  const [open, setOpen] = useState(false);
  const [chosen, setChosen] = useState("");
  const [link, setLink] = useState<TelegramLinkStart | null>(null);
  const [expired, setExpired] = useState(false);
  // Each opening and closing starts a new round; a link that arrives in a later round than the one that asked for it
  // (the dialog was closed meanwhile) is dropped.
  const round = useRef(0);
  const start = useMutation({
    mutationFn: async ({ connectionId, asked }: { connectionId: string; asked: number }) => {
      const started = await startTelegramLink({ connection_id: connectionId });
      if (round.current === asked) {
        setExpired(false);
        setLink(started);
      }
    },
  });
  const connectionId = connections.some((c) => c.id === chosen)
    ? chosen
    : (connections[0]?.id ?? "");
  const ask = (id: string) => {
    start.reset();
    start.mutate({ connectionId: id, asked: round.current });
  };

  useEffect(() => {
    if (link === null) {
      return undefined;
    }
    // setTimeout fires at once for a delay past 2^31-1 ms, so the delay is clamped to it.
    const delay = Date.parse(link.expires_at) - Date.now();
    const timer = setTimeout(() => setExpired(true), Math.min(Math.max(delay, 0), 2_147_483_647));
    return () => clearTimeout(timer);
  }, [link]);

  // The bot linked the account: the list reads it again through the hint's queries, and the dialog closes.
  useEffect(() => {
    if (link === null) {
      return undefined;
    }
    return onHint("account-links", () => {
      round.current += 1;
      setLink(null);
      setExpired(false);
      setOpen(false);
    });
  }, [link]);

  const reset = () => {
    round.current += 1;
    setLink(null);
    setExpired(false);
    start.reset();
  };

  return (
    <>
      <Button
        variant="outline"
        onClick={() => {
          reset();
          setOpen(true);
          const only = connections.length === 1 ? connections[0] : undefined;
          if (only !== undefined) {
            ask(only.id);
          }
        }}
      >
        {title}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) {
            reset();
          }
        }}
      >
        <DialogContent
          closeLabel={t("common.close")}
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto"
        >
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{t("accountLinks.telegram.hint")}</DialogDescription>
          </DialogHeader>
          {link === null && connections.length > 1 && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="link-telegram-connection">{t("accountLinks.connection")}</Label>
              <NativeSelect
                id="link-telegram-connection"
                className="w-full"
                value={connectionId}
                onChange={(e) => setChosen(e.target.value)}
              >
                {connections.map((c) => (
                  <NativeSelectOption key={c.id} value={c.id}>
                    {c.name}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
          )}
          {link !== null && !expired && (
            <div className="flex min-w-0 flex-col items-center gap-3">
              <a
                href={link.deep_link}
                target="_blank"
                rel="noopener noreferrer"
                className={buttonVariants()}
              >
                {t("accountLinks.telegram.open")}
              </a>
              <QrCode
                value={link.deep_link}
                label={t("accountLinks.telegram.qrLabel")}
                className="size-48 max-w-full shrink-0 rounded-md border"
              />
              <p className="text-center text-sm text-muted-foreground">
                {t("accountLinks.telegram.expires", { time: time(link.expires_at) })}
              </p>
            </div>
          )}
          {link !== null && expired && (
            <p className="text-sm" role="status">
              {t("accountLinks.telegram.expired")}
            </p>
          )}
          {link === null && start.isPending && (
            <p className="text-sm text-muted-foreground" role="status">
              {t("common.loading")}
            </p>
          )}
          {start.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {startProblemText(t, start.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.close")}</DialogClose>
            {link === null && !start.isPending && connectionId !== "" && (
              <Button onClick={() => ask(connectionId)}>
                {t("accountLinks.telegram.getLink")}
              </Button>
            )}
            {link !== null && expired && (
              <Button
                onClick={() => {
                  round.current += 1;
                  setLink(null);
                  setExpired(false);
                  ask(connectionId);
                }}
              >
                {t("accountLinks.telegram.newLink")}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
