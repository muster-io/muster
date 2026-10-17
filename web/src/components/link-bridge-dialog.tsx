// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Link through {Connection}" of the profile for a Bridge Connection (C-18, C-22.FR-15). The dialog asks for a token at
// once and shows it with "Copy", "Send this token to the bot in a direct message", the time it expires and, when the
// bridge has a link template, the link that opens the chat with the bot; once the token expired, it offers a new one.
// When the bridge links the account, the account-links hint names the link: the dialog closes once the list read again
// shows it as a Bridge link of this Connection, in the round it was asked in. Where the Connection's info offers a code
// (link_methods has code), a second tab asks for the account's handle, labelled by its kind (Email, Login, Username,
// User id), then for the code the bot sent in a direct message, with the attempts left that the server gives and
// "Request a new code", as the Mattermost dialog does. A refusal shows at the field it concerns: an unknown handle, an
// account the bot cannot write to, a bridge that offers no code, an account linked to another user and too many code
// requests (in minutes) at the handle, a wrong code at the code. The token and the code live only in this dialog's
// state: closing the dialog drops them.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  confirmBridgeLink,
  getListMyAccountLinksQueryKey,
  getListMyAccountLinksQueryOptions,
  startBridgeCodeLink,
  startBridgeLink,
} from "../api/gen/endpoints/account-links/account-links";
import type { BridgeLinkStart, LinkHandleKind, LinkableConnection } from "../api/gen/model";
import { fieldErrorText, isApiError, problemText } from "../lib/api";
import { onHint } from "../lib/live";
import { useTimeFormat } from "../lib/time";
import { safeMessageUrl } from "./alert-group-deliveries";
import { unreachable } from "./destination-test-panel";
import { CopyBlock } from "./integration-token-dialog";
import { startProblemText } from "./link-telegram-dialog";
import { StatusTabs } from "./status-tabs";
import { Alert, AlertDescription } from "./ui/alert";
import { Button, buttonVariants } from "./ui/button";
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

/** The kind of handle a bridge finds people by, when it offers a code. */
export type HandleKind = NonNullable<LinkHandleKind>;

/** The longest handle, in characters, as the API checks it. */
const HANDLE_MAX = 320;

const TABS = ["token", "code"] as const;
type Tab = (typeof TABS)[number];

const PANEL_ID = "link-bridge-panel";

function hasFieldCode(err: unknown, code: string): boolean {
  return isApiError(err) && (err.errors ?? []).some((e) => e.code === code);
}

/** The label of the handle field: Email, Login, Username or User id. */
export function handleKindLabel(t: TFunction, kind: HandleKind): string {
  switch (kind) {
    case "email":
      return t("accountLinks.bridge.handleKinds.email");
    case "login":
      return t("accountLinks.bridge.handleKinds.login");
    case "username":
      return t("accountLinks.bridge.handleKinds.username");
    case "user_id":
      return t("accountLinks.bridge.handleKinds.userId");
    default:
      return unreachable(kind);
  }
}

function handleNotFoundText(t: TFunction, kind: HandleKind): string {
  switch (kind) {
    case "email":
      return t("accountLinks.bridge.errors.handleNotFound.email");
    case "login":
      return t("accountLinks.bridge.errors.handleNotFound.login");
    case "username":
      return t("accountLinks.bridge.errors.handleNotFound.username");
    case "user_id":
      return t("accountLinks.bridge.errors.handleNotFound.userId");
    default:
      return unreachable(kind);
  }
}

/** Where a refusal shows: under the handle field, under the code field, or for the whole dialog. */
export type ProblemPlace = "handle" | "code" | "dialog";

/**
 * The text of a refused code request or confirmation and the field it concerns: an unknown handle (named by its kind),
 * an account the bot cannot write to, a bridge that offers no code, an account linked to another user and too many code
 * requests (link_code_requests_exceeded, in minutes from Retry-After) belong to the handle; a wrong or used-up code to
 * the code; a busy messenger and anything else to the dialog.
 */
export function bridgeLinkProblem(
  t: TFunction,
  err: unknown,
  kind: HandleKind,
): { at: ProblemPlace; text: string } {
  if (isApiError(err)) {
    if (err.status === 422 && hasFieldCode(err, "link_code_invalid")) {
      return { at: "code", text: t("accountLinks.bridge.errors.codeInvalid") };
    }
    if (err.status === 422 && hasFieldCode(err, "handle_not_found")) {
      return { at: "handle", text: handleNotFoundText(t, kind) };
    }
    if (err.status === 422 && hasFieldCode(err, "direct_message_refused")) {
      return { at: "handle", text: t("accountLinks.bridge.errors.directMessageRefused") };
    }
    if (err.status === 409 && err.code === "code_not_offered") {
      return { at: "handle", text: t("accountLinks.bridge.errors.codeNotOffered") };
    }
    if (err.status === 409 && err.code === "account_linked_elsewhere") {
      return { at: "handle", text: t("accountLinks.bridge.errors.linkedElsewhere") };
    }
    if (err.status === 429 && err.code === "link_code_requests_exceeded") {
      return {
        at: "handle",
        text: t("accountLinks.bridge.errors.tooManyCodes", {
          count: Math.max(1, Math.ceil((err.retry_after_seconds ?? 60) / 60)),
        }),
      };
    }
    if (err.status === 503) {
      return { at: "dialog", text: t("accountLinks.errors.busy") };
    }
    const errors = err.errors ?? [];
    if (err.status === 422 && errors.length > 0) {
      const at = errors.every((e) => e.pointer === "/code")
        ? "code"
        : errors.every((e) => e.pointer === "/handle")
          ? "handle"
          : "dialog";
      return { at, text: errors.map((e) => fieldErrorText(t, e.code)).join(" ") };
    }
  }
  return { at: "dialog", text: problemText(t, err) };
}

/** A code request the user is answering: its id and the attempts left for its code. */
interface Pending {
  requestId: string;
  attempts: number;
}

function ProblemAlert({ text }: { text: string }) {
  return (
    <Alert variant="destructive">
      <AlertDescription className="text-current">{text}</AlertDescription>
    </Alert>
  );
}

export function LinkBridgeDialog({ connection }: { connection: LinkableConnection }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { time } = useTimeFormat();
  const title = t("accountLinks.bridge.start", { connection: connection.name });
  const kind = connection.handle_kind ?? null;
  const codeKind = (connection.link_methods ?? []).includes("code") ? kind : null;
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<Tab>("token");
  const [token, setToken] = useState<BridgeLinkStart | null>(null);
  const [expired, setExpired] = useState(false);
  const [handle, setHandle] = useState("");
  const [handleCheck, setHandleCheck] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [code, setCode] = useState("");
  // Each opening and closing starts a new round; an answer that arrives in a later round than the one that asked for it
  // (the dialog was closed meanwhile) is dropped.
  const round = useRef(0);

  const start = useMutation({
    mutationFn: async (asked: number) => {
      const started = await startBridgeLink({ connection_id: connection.id });
      if (round.current === asked) {
        setExpired(false);
        setToken(started);
      }
    },
  });
  const startCode = useMutation({
    mutationFn: async (asked: number) => {
      const started = await startBridgeCodeLink({
        connection_id: connection.id,
        handle: handle.trim(),
      });
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
        await confirmBridgeLink({ request_id: pending.requestId, code: code.trim() });
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
      if (hasFieldCode(err, "link_code_invalid")) {
        // A refusal without the number leaves no attempt: the dialog then offers only a new code.
        const left = isApiError(err) ? (err.attempts_remaining ?? 0) : 0;
        setPending((p) => (p === null ? p : { ...p, attempts: Math.max(0, left) }));
        setCode("");
      }
    },
  });

  const askToken = () => {
    start.reset();
    start.mutate(round.current);
  };

  useEffect(() => {
    if (token === null) {
      return undefined;
    }
    // setTimeout fires at once for a delay past 2^31-1 ms, so the delay is clamped to it.
    const delay = Date.parse(token.expires_at) - Date.now();
    const timer = setTimeout(() => setExpired(true), Math.min(Math.max(delay, 0), 2_147_483_647));
    return () => clearTimeout(timer);
  }, [token]);

  // The bridge linked the account: the hint names the link, the list reads it again through the hint's queries, and the
  // dialog closes when that link is a Bridge link of this Connection — in the round it was asked in.
  useEffect(() => {
    if (token === null) {
      return undefined;
    }
    return onHint("account-links", (id) => {
      if (id === null) {
        return;
      }
      const asked = round.current;
      void queryClient
        .fetchQuery(getListMyAccountLinksQueryOptions())
        .then((list) => {
          if (
            round.current === asked &&
            list.items.some(
              (l) => l.id === id && l.messenger === "bridge" && l.connection?.id === connection.id,
            )
          ) {
            round.current += 1;
            setToken(null);
            setExpired(false);
            setOpen(false);
          }
        })
        .catch(() => undefined);
    });
  }, [token, queryClient, connection.id]);

  const reset = () => {
    round.current += 1;
    setTab("token");
    setToken(null);
    setExpired(false);
    setHandle("");
    setHandleCheck(null);
    setPending(null);
    setCode("");
    start.reset();
    startCode.reset();
    confirm.reset();
  };
  const close = () => {
    reset();
    setOpen(false);
  };

  const requestCode = () => {
    const trimmed = handle.trim();
    const check = trimmed === "" ? "required" : trimmed.length > HANDLE_MAX ? "too_long" : null;
    setHandleCheck(check);
    if (check !== null) {
      return;
    }
    confirm.reset();
    startCode.mutate(round.current);
  };

  const linkUrl = safeMessageUrl(token?.link_url);
  const busy = startCode.isPending || confirm.isPending;
  const canEnter = pending !== null && pending.attempts > 0;
  const codeError = confirm.error ?? startCode.error;
  const problem =
    codeError === null || codeKind === null ? null : bridgeLinkProblem(t, codeError, codeKind);
  const handleError =
    handleCheck !== null
      ? fieldErrorText(t, handleCheck)
      : problem?.at === "handle"
        ? problem.text
        : null;
  const codeFieldError = problem?.at === "code" ? problem.text : null;

  const tokenPanel = (
    <div className="flex min-w-0 flex-col gap-4">
      {token !== null && !expired && (
        <>
          <CopyBlock
            id="link-bridge-token"
            label={t("accountLinks.bridge.tabs.token")}
            text={token.token}
            testId="link-bridge-token"
          />
          <p className="text-sm" data-testid="link-bridge-instruction">
            {t("accountLinks.bridge.sendToken")}
          </p>
          {linkUrl !== undefined && (
            <a
              href={linkUrl}
              target="_blank"
              rel="noopener noreferrer"
              className={buttonVariants({ className: "self-start" })}
              data-testid="link-bridge-open"
            >
              {t("accountLinks.bridge.open")}
            </a>
          )}
          <p className="text-sm text-muted-foreground" data-testid="link-bridge-expires">
            {t("accountLinks.bridge.expires", { time: time(token.expires_at) })}
          </p>
        </>
      )}
      {token !== null && expired && (
        <p className="text-sm" role="status">
          {t("accountLinks.bridge.expired")}
        </p>
      )}
      {token === null && start.isPending && (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      )}
      {start.isError && <ProblemAlert text={startProblemText(t, start.error)} />}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.close")}</DialogClose>
        {((token !== null && expired) || (token === null && start.isError)) && (
          <Button
            onClick={() => {
              setToken(null);
              setExpired(false);
              askToken();
            }}
          >
            {t("accountLinks.bridge.newToken")}
          </Button>
        )}
      </DialogFooter>
    </div>
  );

  const handlePanel = codeKind !== null && (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        requestCode();
      }}
    >
      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor="link-bridge-handle">{handleKindLabel(t, codeKind)}</Label>
        <Input
          id="link-bridge-handle"
          inputMode={codeKind === "email" ? "email" : "text"}
          autoComplete="off"
          spellCheck={false}
          value={handle}
          aria-invalid={handleError !== null}
          aria-describedby={
            handleError === null
              ? "link-bridge-handle-hint"
              : "link-bridge-handle-hint link-bridge-handle-error"
          }
          onChange={(e) => {
            setHandle(e.target.value);
            setHandleCheck(null);
          }}
        />
        <p id="link-bridge-handle-hint" className="text-sm text-muted-foreground">
          {t("accountLinks.bridge.handleHint")}
        </p>
        {handleError !== null && (
          <p
            id="link-bridge-handle-error"
            className="text-sm wrap-anywhere text-destructive"
            data-testid="link-bridge-handle-error"
          >
            {handleError}
          </p>
        )}
      </div>
      {problem?.at === "dialog" && <ProblemAlert text={problem.text} />}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
        <Button type="submit" disabled={busy}>
          {t("accountLinks.bridge.send")}
        </Button>
      </DialogFooter>
    </form>
  );

  const codePanel = pending !== null && (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (canEnter) {
          startCode.reset();
          confirm.mutate(round.current);
        }
      }}
    >
      {canEnter && (
        <div className="flex min-w-0 flex-col gap-2">
          <Label htmlFor="link-bridge-code">{t("accountLinks.bridge.code")}</Label>
          <Input
            id="link-bridge-code"
            autoComplete="one-time-code"
            spellCheck={false}
            autoCapitalize="characters"
            className="uppercase"
            value={code}
            aria-invalid={codeFieldError !== null}
            aria-describedby={
              codeFieldError === null
                ? "link-bridge-attempts"
                : "link-bridge-attempts link-bridge-code-error"
            }
            onChange={(e) => setCode(e.target.value)}
          />
        </div>
      )}
      <p
        id="link-bridge-attempts"
        role="status"
        className="text-sm text-muted-foreground"
        data-testid="link-bridge-attempts"
      >
        {t("accountLinks.bridge.attemptsLeft", { count: pending.attempts })}
      </p>
      {codeFieldError !== null && (
        <p
          id="link-bridge-code-error"
          className="text-sm wrap-anywhere text-destructive"
          data-testid="link-bridge-code-error"
        >
          {codeFieldError}
        </p>
      )}
      {problem !== null && problem.at !== "code" && <ProblemAlert text={problem.text} />}
      <DialogFooter>
        <Button type="button" variant="outline" disabled={busy} onClick={requestCode}>
          {t("accountLinks.bridge.newCode")}
        </Button>
        {canEnter && (
          <Button type="submit" disabled={busy}>
            {t("accountLinks.bridge.confirm")}
          </Button>
        )}
      </DialogFooter>
    </form>
  );

  const panel =
    tab === "code" && codeKind !== null ? (pending === null ? handlePanel : codePanel) : tokenPanel;

  return (
    <>
      <Button
        variant="outline"
        onClick={() => {
          reset();
          setOpen(true);
          askToken();
        }}
      >
        {title}
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
            <DialogTitle className="wrap-anywhere">{title}</DialogTitle>
          </DialogHeader>
          {codeKind !== null ? (
            <>
              <StatusTabs
                tabs={TABS}
                value={tab}
                label={t("accountLinks.bridge.tabs.label")}
                panelId={PANEL_ID}
                labelOf={(value) =>
                  value === "code"
                    ? t("accountLinks.bridge.tabs.code")
                    : t("accountLinks.bridge.tabs.token")
                }
                onSelect={setTab}
              />
              <div id={PANEL_ID} role="tabpanel" aria-labelledby={`${PANEL_ID}-tab-${tab}`}>
                {panel}
              </div>
            </>
          ) : (
            panel
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
