// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Mattermost" of the profile (C-18.FR-3): the Connection and the user's Mattermost username, then the code the
// bot sent in a direct message, with the attempts left and "Request a new code". The code lives only in this dialog's
// state: the requests' results keep none, and closing the dialog drops it. The API does not say how many attempts are
// left after a wrong code, so the dialog counts down from the number the code request gave.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  confirmMattermostLink,
  getListMyAccountLinksQueryKey,
  startMattermostLink,
} from "../api/gen/endpoints/account-links/account-links";
import type { LinkableConnection } from "../api/gen/model";
import { fieldErrorText, isApiError, problemText } from "../lib/api";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const LINK_CODE_INVALID = "link_code_invalid";

function hasFieldCode(err: unknown, code: string): boolean {
  return isApiError(err) && (err.errors ?? []).some((e) => e.code === code);
}

/**
 * The text of a refused code request or confirmation: a wrong or used-up code, an unknown username, an account linked
 * to another user, too many code requests (in minutes, from Retry-After) and a busy messenger have their own texts.
 */
export function linkProblemText(t: TFunction, err: unknown, username: string): string {
  if (isApiError(err)) {
    if (err.status === 422 && hasFieldCode(err, LINK_CODE_INVALID)) {
      return t("accountLinks.mattermost.errors.codeInvalid");
    }
    if (err.status === 422 && hasFieldCode(err, "messenger_user_not_found")) {
      return t("accountLinks.mattermost.errors.userNotFound", {
        username: username.trim().replace(/^@/, ""),
      });
    }
    if (err.status === 409 && err.code === "account_linked_elsewhere") {
      return t("accountLinks.mattermost.errors.linkedElsewhere");
    }
    if (err.status === 429) {
      return t("accountLinks.mattermost.errors.tooManyCodes", {
        count: Math.max(1, Math.ceil((err.retry_after_seconds ?? 60) / 60)),
      });
    }
    if (err.status === 503) {
      return t("accountLinks.errors.busy");
    }
    if (err.status === 422 && err.errors?.length) {
      return err.errors.map((e) => fieldErrorText(t, e.code)).join(" ");
    }
  }
  return problemText(t, err);
}

/** A code request the user is answering: its id and the attempts left for its code. */
interface Pending {
  requestId: string;
  attempts: number;
}

export function LinkMattermostDialog({
  connections,
}: {
  connections: readonly LinkableConnection[];
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [chosen, setChosen] = useState("");
  const [username, setUsername] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const [code, setCode] = useState("");
  // Each opening and closing starts a new round; an answer that arrives in a later round than the one that asked for it
  // (the dialog was closed meanwhile) is dropped.
  const round = useRef(0);
  const connectionId = connections.some((c) => c.id === chosen)
    ? chosen
    : (connections[0]?.id ?? "");

  const start = useMutation({
    mutationFn: async (asked: number) => {
      const started = await startMattermostLink({ connection_id: connectionId, username });
      if (round.current === asked) {
        setCode("");
        setPending({ requestId: started.request_id, attempts: started.attempts_remaining });
      }
    },
  });
  // The only variable is the round: the code never sits in the mutation's state.
  const confirm = useMutation({
    mutationFn: async (_asked: number) => {
      if (pending !== null) {
        await confirmMattermostLink({ request_id: pending.requestId, code: code.trim() });
      }
    },
    onSuccess: (_data, asked) => {
      if (round.current !== asked) {
        return;
      }
      void queryClient.invalidateQueries({ queryKey: getListMyAccountLinksQueryKey() });
      close();
    },
    onError: (err, asked) => {
      if (round.current !== asked) {
        return;
      }
      if (hasFieldCode(err, LINK_CODE_INVALID)) {
        setPending((p) => (p === null ? p : { ...p, attempts: Math.max(0, p.attempts - 1) }));
        setCode("");
      }
    },
  });

  const reset = () => {
    round.current += 1;
    setChosen("");
    setUsername("");
    setPending(null);
    setCode("");
    start.reset();
    confirm.reset();
  };
  const close = () => {
    reset();
    setOpen(false);
  };
  const requestCode = () => {
    confirm.reset();
    start.mutate(round.current);
  };

  const error = confirm.error ?? start.error;
  const errorText =
    error === null
      ? null
      : isApiError(error) && error === confirm.error && error.status === 429
        ? problemText(t, error)
        : linkProblemText(t, error, username);
  const busy = start.isPending || confirm.isPending;
  const canEnter = pending !== null && pending.attempts > 0;

  return (
    <>
      <Button
        variant="outline"
        onClick={() => {
          reset();
          setOpen(true);
        }}
      >
        {t("accountLinks.mattermost.start")}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (next) {
            setOpen(true);
          } else {
            close();
          }
        }}
      >
        <DialogContent
          closeLabel={t("common.close")}
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto"
        >
          <DialogHeader>
            <DialogTitle>{t("accountLinks.mattermost.start")}</DialogTitle>
          </DialogHeader>
          {pending === null ? (
            <form
              noValidate
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                requestCode();
              }}
            >
              <div className="flex flex-col gap-2">
                <Label htmlFor="link-mattermost-connection">{t("accountLinks.connection")}</Label>
                <NativeSelect
                  id="link-mattermost-connection"
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
              <div className="flex flex-col gap-2">
                <Label htmlFor="link-mattermost-username">
                  {t("accountLinks.mattermost.username")}
                </Label>
                <Input
                  id="link-mattermost-username"
                  autoComplete="off"
                  spellCheck={false}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                />
              </div>
              {errorText !== null && (
                <Alert variant="destructive">
                  <AlertDescription className="text-current">{errorText}</AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <DialogClose render={<Button variant="outline" />}>
                  {t("common.cancel")}
                </DialogClose>
                <Button type="submit" disabled={busy}>
                  {t("accountLinks.mattermost.send")}
                </Button>
              </DialogFooter>
            </form>
          ) : (
            <form
              noValidate
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                if (canEnter) {
                  start.reset();
                  confirm.mutate(round.current);
                }
              }}
            >
              {canEnter && (
                <div className="flex flex-col gap-2">
                  <Label htmlFor="link-mattermost-code">{t("accountLinks.mattermost.code")}</Label>
                  <Input
                    id="link-mattermost-code"
                    autoComplete="one-time-code"
                    spellCheck={false}
                    autoCapitalize="characters"
                    className="uppercase"
                    value={code}
                    aria-describedby="link-mattermost-attempts"
                    onChange={(e) => setCode(e.target.value)}
                  />
                </div>
              )}
              <p
                id="link-mattermost-attempts"
                role="status"
                className="text-sm text-muted-foreground"
              >
                {t("accountLinks.mattermost.attemptsLeft", { count: pending.attempts })}
              </p>
              {errorText !== null && (
                <Alert variant="destructive">
                  <AlertDescription className="text-current">{errorText}</AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <Button type="button" variant="outline" disabled={busy} onClick={requestCode}>
                  {t("accountLinks.mattermost.newCode")}
                </Button>
                {canEnter && (
                  <Button type="submit" disabled={busy}>
                    {t("accountLinks.mattermost.confirm")}
                  </Button>
                )}
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
