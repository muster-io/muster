// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link Slack" of the profile (C-24.FR-12, AC-18): the Slack Connection and the user's Slack email or name, then the
// code the bot sent by direct message — shown in the app's Messages tab — with the attempts left and "Request a new
// code". An email is looked up as Slack's users.lookupByEmail does, a name over the people of the workspace; no account
// and several accounts with the name are refused with their texts, and so is an account the bot cannot write to; an app
// whose Messages tab is off is told to turn it on. The code lives only in this dialog's state: the requests' results
// keep none, and closing the dialog drops it. The attempts left come from the server: the code request gives them, and
// so does each refused code (attempts_remaining).

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  confirmSlackLink,
  getListMyAccountLinksQueryKey,
  startSlackLink,
} from "../api/gen/endpoints/account-links/account-links";
import type { LinkableConnection } from "../api/gen/model";
import { fieldErrorText, isApiError, problemText } from "../lib/api";
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
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const LINK_CODE_INVALID = "link_code_invalid";
const LINK_CODE_REQUESTS_EXCEEDED = "link_code_requests_exceeded";

/** The longest email or name the server takes. */
const HANDLE_MAX = 320;

function hasFieldCode(err: unknown, code: string): boolean {
  return isApiError(err) && (err.errors ?? []).some((e) => e.code === code);
}

/**
 * The text of a refused code request or confirmation: no account with the email or name, several accounts with the
 * name, an account the bot cannot write to, an app whose Messages tab is off, a wrong or used-up code, an account linked
 * to another user, too many code requests (link_code_requests_exceeded, in minutes from Retry-After) and a busy
 * messenger have their own texts; anything else as every form shows it.
 */
export function slackLinkProblemText(t: TFunction, err: unknown): string {
  if (isApiError(err)) {
    if (err.status === 422 && hasFieldCode(err, LINK_CODE_INVALID)) {
      return t("accountLinks.slack.errors.codeInvalid");
    }
    if (err.status === 422 && hasFieldCode(err, "handle_not_found")) {
      return t("accountLinks.slack.errors.handleNotFound");
    }
    if (err.status === 422 && hasFieldCode(err, "handle_ambiguous")) {
      return t("accountLinks.slack.errors.handleAmbiguous");
    }
    if (hasFieldCode(err, "messages_tab_disabled") || err.code === "messages_tab_disabled") {
      return t("accountLinks.slack.errors.messagesTabDisabled");
    }
    if (err.status === 422 && hasFieldCode(err, "direct_message_refused")) {
      return t("accountLinks.slack.errors.directMessageRefused");
    }
    if (err.status === 422 && hasFieldCode(err, "unknown_id")) {
      return t("accountLinks.slack.errors.connectionGone");
    }
    if (err.status === 409 && err.code === "account_linked_elsewhere") {
      return t("accountLinks.slack.errors.linkedElsewhere");
    }
    if (err.status === 429 && err.code === LINK_CODE_REQUESTS_EXCEEDED) {
      return t("accountLinks.slack.errors.tooManyCodes", {
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

export function LinkSlackDialog({ connections }: { connections: readonly LinkableConnection[] }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [chosen, setChosen] = useState("");
  const [handle, setHandle] = useState("");
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
      const started = await startSlackLink({ connection_id: connectionId, handle: handle.trim() });
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
        await confirmSlackLink({ request_id: pending.requestId, code: code.trim() });
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
        // A refusal without the number leaves no attempt: the dialog then offers only a new code.
        const left = isApiError(err) ? (err.attempts_remaining ?? 0) : 0;
        setPending((p) => (p === null ? p : { ...p, attempts: Math.max(0, left) }));
        setCode("");
      }
    },
  });

  const reset = () => {
    round.current += 1;
    setChosen("");
    setHandle("");
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
  const errorText = error === null ? null : slackLinkProblemText(t, error);
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
        {t("accountLinks.slack.start")}
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
          data-testid="link-slack-dialog"
        >
          <DialogHeader>
            <DialogTitle>{t("accountLinks.slack.start")}</DialogTitle>
            <DialogDescription>{t("accountLinks.slack.hint")}</DialogDescription>
          </DialogHeader>
          {pending === null ? (
            <form
              noValidate
              className="flex min-w-0 flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                requestCode();
              }}
            >
              <div className="flex min-w-0 flex-col gap-2">
                <Label htmlFor="link-slack-connection">{t("accountLinks.connection")}</Label>
                <NativeSelect
                  id="link-slack-connection"
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
              <div className="flex min-w-0 flex-col gap-2">
                <Label htmlFor="link-slack-handle">{t("accountLinks.slack.handle")}</Label>
                <Input
                  id="link-slack-handle"
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={HANDLE_MAX}
                  value={handle}
                  aria-describedby="link-slack-handle-hint"
                  onChange={(e) => setHandle(e.target.value)}
                />
                <p id="link-slack-handle-hint" className="text-sm text-muted-foreground">
                  {t("accountLinks.slack.handleHint")}
                </p>
              </div>
              {errorText !== null && (
                <Alert variant="destructive">
                  <AlertDescription
                    className="wrap-anywhere text-current"
                    data-testid="link-slack-error"
                  >
                    {errorText}
                  </AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <DialogClose render={<Button variant="outline" />}>
                  {t("common.cancel")}
                </DialogClose>
                <Button type="submit" disabled={busy || handle.trim() === ""}>
                  {t("accountLinks.slack.send")}
                </Button>
              </DialogFooter>
            </form>
          ) : (
            <form
              noValidate
              className="flex min-w-0 flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                if (canEnter) {
                  start.reset();
                  confirm.mutate(round.current);
                }
              }}
            >
              {canEnter && (
                <div className="flex min-w-0 flex-col gap-2">
                  <Label htmlFor="link-slack-code">{t("accountLinks.slack.code")}</Label>
                  <Input
                    id="link-slack-code"
                    autoComplete="one-time-code"
                    spellCheck={false}
                    autoCapitalize="characters"
                    className="uppercase"
                    value={code}
                    aria-describedby="link-slack-attempts"
                    onChange={(e) => setCode(e.target.value)}
                  />
                </div>
              )}
              <p id="link-slack-attempts" role="status" className="text-sm text-muted-foreground">
                {t("accountLinks.slack.attemptsLeft", { count: pending.attempts })}
              </p>
              {errorText !== null && (
                <Alert variant="destructive">
                  <AlertDescription
                    className="wrap-anywhere text-current"
                    data-testid="link-slack-error"
                  >
                    {errorText}
                  </AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <Button type="button" variant="outline" disabled={busy} onClick={requestCode}>
                  {t("accountLinks.slack.newCode")}
                </Button>
                {canEnter && (
                  <Button type="submit" disabled={busy}>
                    {t("accountLinks.slack.confirm")}
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
