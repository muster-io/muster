// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of a Connection (C-13.FR-2, FR-14, AC-16, AC-17; C-14.FR-11, AC-20; C-22.FR-2, AC-26): each step
// with "ok" or what failed, translated by its code with the server's English message below as its detail, its latency
// and the path it took, directly or through the proxy; "Connected as {bot}" once the token works; and the warnings that
// do not fail the check, each with its hint. Telegram works in steps — the dry probe without the token, getMe with the
// bot, getWebhookInfo with the pending updates — and a set webhook in the long-polling mode gets its warning. A Bridge
// has two steps, "Bridge info" and "Event delivery"; the info a check of the saved Connection reads is stored on it,
// whose page shows it. The form's values are checked before they are saved: the create form always sends them as a
// draft, and the edit form while it holds changes, with the saved Connection's id; without changes it checks the saved
// Connection. A draft stores nothing. The server decides which token goes where: a typed token goes to the typed
// address, the stored one only to the saved address through the saved proxy, and otherwise only the dry probe runs —
// the skipped steps then ask for the token, which the panel reads from the result, never deciding it itself. A change of
// the form makes an earlier result stale, and the panel hides it. The check runs on the interactive path, so a busy
// messenger answers 503 with Retry-After: "The messenger is busy; try again in N s." A result names its check run, and
// "Requests of this check" opens the requests it made (C-23.FR-6), a check before saving included.

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { CircleCheckIcon, CircleMinusIcon, CircleXIcon, TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";

import {
  getGetConnectionQueryKey,
  useCheckConnection,
  useCheckConnectionDraft,
} from "../api/gen/endpoints/connections/connections";
import type {
  Connection,
  ConnectionCheckResult,
  ConnectionCheckResultWarningsItem,
  ConnectionCheckStep,
  ConnectionCheckStepName,
  ConnectionDraft,
  NullableConnectionCheckCode,
  NullableDestinationCheckCode,
  TelegramUpdateMode,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { draftAddress, draftFieldErrorText, serverErrors } from "./connection-form";
import { unreachable } from "./destination-test-panel";
import { RequestLogButton } from "./request-log";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { cn } from "./ui/utils";

function stepName(t: TFunction, name: ConnectionCheckStepName): string {
  switch (name) {
    case "token":
      return t("connections.check.steps.token");
    case "dry_probe":
      return t("connections.check.steps.dryProbe");
    case "get_me":
      return t("connections.check.steps.getMe");
    case "get_webhook_info":
      return t("connections.check.steps.getWebhookInfo");
    case "info":
      return t("connections.check.steps.info");
    case "event_delivery":
      return t("connections.check.steps.eventDelivery");
    default:
      return unreachable(name);
  }
}

function checkHint(t: TFunction, type: Connection["type"]): string {
  switch (type) {
    case "mattermost":
      return t("connections.check.hint");
    case "telegram":
      return t("connections.check.telegramHint");
    case "bridge":
      return t("connections.check.bridgeHint");
    default:
      return unreachable(type);
  }
}

/** The code of what a step of the Connection check or a check of the Destination check found. */
export type CheckCode =
  | NonNullable<NullableConnectionCheckCode>
  | NonNullable<NullableDestinationCheckCode>;

/** What a check found, by its code, in the reader's language; the server's English message is its detail. */
export function checkCodeText(t: TFunction, code: CheckCode): string {
  switch (code) {
    case "token_invalid":
      return t("checks.codes.tokenInvalid");
    case "not_bot_api":
      return t("checks.codes.notBotApi");
    case "wrong_path_prefix":
      return t("checks.codes.wrongPathPrefix");
    case "unexpected_status":
      return t("checks.codes.unexpectedStatus");
    case "dns_failed":
      return t("checks.codes.dnsFailed");
    case "connect_failed":
      return t("checks.codes.connectFailed");
    case "tls_failed":
      return t("checks.codes.tlsFailed");
    case "timeout":
      return t("checks.codes.timeout");
    case "proxy_refused":
      return t("checks.codes.proxyRefused");
    case "blocked":
      return t("checks.codes.blocked");
    case "request_failed":
      return t("checks.codes.requestFailed");
    case "webhook_set":
      return t("checks.codes.webhookSet");
    case "bot_not_in_channel":
      return t("checks.codes.botNotInChannel");
    case "channel_in_other_team":
      return t("checks.codes.channelInOtherTeam");
    case "channel_archived":
      return t("checks.codes.channelArchived");
    case "channel_forbidden":
      return t("checks.codes.channelForbidden");
    case "channel_not_found":
      return t("checks.codes.channelNotFound");
    case "not_a_channel":
      return t("checks.codes.notAChannel");
    case "comments_disabled":
      return t("checks.codes.commentsDisabled");
    case "changed_since_saved":
      return t("checks.codes.changedSinceSaved");
    case "bot_not_channel_admin":
      return t("checks.codes.botNotChannelAdmin");
    case "bot_may_not_post":
      return t("checks.codes.botMayNotPost");
    case "bot_may_not_edit":
      return t("checks.codes.botMayNotEdit");
    case "bot_not_group_admin":
      return t("checks.codes.botNotGroupAdmin");
    case "protocol_version_unknown":
      return t("checks.codes.protocolVersionUnknown");
    case "protocol_mismatch":
      return t("checks.codes.protocolMismatch");
    case "event_delivery_not_offered":
      return t("checks.codes.eventDeliveryNotOffered");
    case "not_mattermost":
      return t("checks.codes.notMattermost");
    case "not_bridge":
      return t("checks.codes.notBridge");
    default:
      return unreachable(code);
  }
}

/**
 * The server's English message as the detail of the text of its code, when it says more than that text — in another
 * language, or with a host, a status or a name; null without a code, which shows the message itself.
 */
export function checkDetail(
  t: TFunction,
  code: CheckCode | null | undefined,
  message: string | null | undefined,
): string | null {
  if (
    code === null ||
    code === undefined ||
    message === null ||
    message === undefined ||
    message === ""
  ) {
    return null;
  }
  return message === checkCodeText(t, code) ? null : message;
}

/** The detail of a check's code: the server's English message, small and muted below the translated text. */
export function CheckDetail({
  code,
  message,
  testId,
}: {
  code: CheckCode | null | undefined;
  message: string | null | undefined;
  testId: string;
}) {
  const { t } = useTranslation();
  const detail = checkDetail(t, code, message);
  if (detail === null) {
    return null;
  }
  return (
    <span className="text-xs wrap-anywhere text-muted-foreground" data-testid={testId}>
      {t("checks.detail", { detail })}
    </span>
  );
}

/** The hint of a warning; the permission is Mattermost's identifier and shows as code. */
function WarningText({ warning }: { warning: ConnectionCheckResultWarningsItem }) {
  switch (warning) {
    case "press_answers_in_thread":
      return (
        <Trans
          i18nKey="connections.check.warnings.pressAnswersInThread"
          components={{ code: <code className="rounded bg-muted px-1 font-mono text-xs" /> }}
        />
      );
    default:
      return warning;
  }
}

/**
 * The text of a refused check: a busy messenger names its wait, an unsaved base URL the server refuses says what it
 * takes, anything else as every form shows it.
 */
export function checkErrorText(t: TFunction, err: unknown): string {
  if (isApiError(err) && err.status === 503) {
    return t("connections.check.busy", { seconds: Math.max(1, err.retry_after_seconds ?? 1) });
  }
  if (
    isApiError(err) &&
    err.status === 422 &&
    (err.errors ?? []).some((e) => e.pointer === "/base_url" && e.code === "invalid_format")
  ) {
    return t("connections.errors.baseUrlFormat");
  }
  return problemText(t, err);
}

/**
 * The text of a refused check of a Connection: a busy messenger names its wait, a draft that names a Connection that is
 * gone asks for a reload, a value of the form the server refuses names its field, anything else as every form shows
 * it.
 */
export function connectionCheckErrorText(
  t: TFunction,
  err: unknown,
  type: Connection["type"],
): string {
  if (isApiError(err) && err.status === 503) {
    return t("connections.check.busy", { seconds: Math.max(1, err.retry_after_seconds ?? 1) });
  }
  if (isApiError(err) && err.status === 422) {
    const items = err.errors ?? [];
    if (items.some((e) => e.pointer === "/connection_id" && e.code === "unknown_id")) {
      return t("connections.check.unknownConnection");
    }
    const first = Object.entries(serverErrors(err, false))[0];
    if (first !== undefined) {
      return draftFieldErrorText(t, first[0], first[1], type);
    }
  }
  return problemText(t, err);
}

/**
 * Why a step was skipped: only the dry probe ran, for want of a token — a new Connection's draft has none typed, and an
 * edited one has a changed address or proxy that the stored token never goes to —, or an earlier step failed.
 */
type SkipReason = "noToken" | "retype" | "earlier";

/** Whether the check was made from the create form or from the page of a saved Connection. */
type CheckMode = "new" | "edit";

/**
 * The reason of each skipped step, read from the result: a step skipped right after a dry probe that passed was skipped
 * for want of a token, any other after a step that failed.
 */
function skipReasons(result: ConnectionCheckResult, mode: CheckMode): (SkipReason | undefined)[] {
  let last: ConnectionCheckStep | undefined;
  return result.steps.map((step) => {
    if (step.skipped !== true) {
      last = step;
      return undefined;
    }
    if (last?.name === "dry_probe" && last.ok) {
      return mode === "new" ? "noToken" : "retype";
    }
    return "earlier";
  });
}

function skipText(t: TFunction, reason: SkipReason): string {
  switch (reason) {
    case "noToken":
      return t("connections.check.skippedNoToken");
    case "retype":
      return t("connections.check.skippedRetype");
    case "earlier":
      return t("connections.check.skippedEarlier");
    default:
      return unreachable(reason);
  }
}

/** The summary of a check whose dry probe passed and whose other steps want a token: what the address answers as. */
function probePassedText(t: TFunction, type: Connection["type"]): string {
  switch (type) {
    case "mattermost":
      return t("connections.check.probePassed.mattermost");
    case "telegram":
      return t("connections.check.probePassed.telegram");
    case "bridge":
      return t("connections.check.probePassed.bridge");
    default:
      return unreachable(type);
  }
}

function Step({
  step,
  skipReason,
  bot,
  pendingUpdates,
}: {
  step: ConnectionCheckStep;
  skipReason?: SkipReason;
  bot?: string;
  pendingUpdates?: number;
}) {
  const { t } = useTranslation();
  const skipped = step.skipped === true;
  const details: string[] = [];
  if (skipped) {
    details.push(skipText(t, skipReason ?? "earlier"));
  } else if (step.ok) {
    details.push(t("connections.check.ok"));
  }
  if (step.latency_ms !== undefined && step.latency_ms !== null && !skipped) {
    details.push(t("connections.check.latency", { ms: step.latency_ms }));
  }
  if (step.via !== undefined && !skipped) {
    details.push(
      step.via === "proxy" ? t("connections.check.viaProxy") : t("connections.check.direct"),
    );
  }
  if (step.ok && !skipped && step.name === "get_me" && bot !== undefined) {
    details.push(t("connections.check.bot", { bot }));
  }
  if (step.ok && !skipped && step.name === "get_webhook_info" && pendingUpdates !== undefined) {
    details.push(t("connections.check.pendingUpdates", { count: pendingUpdates }));
  }
  const failed = !step.ok && !skipped;
  const code = step.code ?? null;
  // A step that passed may say what it found, such as the host of a webhook that is set.
  const found = step.ok && !skipped && step.message !== undefined && step.message !== null;
  return (
    <li className="flex min-w-0 items-start gap-2 text-sm" data-testid="connection-check-step">
      {failed ? (
        <CircleXIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      ) : skipped ? (
        <CircleMinusIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-muted-foreground"
          data-icon="skipped"
        />
      ) : (
        <CircleCheckIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400"
        />
      )}
      <div className="flex min-w-0 flex-col gap-0.5">
        <span>
          <span className="font-medium">{stepName(t, step.name)}:</span>{" "}
          {failed && <span className="sr-only">{t("connections.check.failed")} </span>}
          {details.join(" · ")}
        </span>
        {failed && (
          <span
            className="wrap-anywhere text-destructive"
            data-testid="connection-check-step-reason"
          >
            {code === null
              ? (step.message ?? t("connections.check.unknownError"))
              : checkCodeText(t, code)}
          </span>
        )}
        {found && (
          <span
            className="wrap-anywhere text-muted-foreground"
            data-testid="connection-check-step-reason"
          >
            {code === null ? step.message : checkCodeText(t, code)}
          </span>
        )}
        {(failed || found) && (
          <CheckDetail code={code} message={step.message} testId="connection-check-step-detail" />
        )}
      </div>
    </li>
  );
}

function Result({
  result,
  type,
  mode,
  updateMode,
}: {
  result: ConnectionCheckResult;
  type: Connection["type"];
  mode: CheckMode;
  updateMode?: TelegramUpdateMode;
}) {
  const { t } = useTranslation();
  const name = result.bot_name ?? "";
  // Telegram names bots by their @username; Mattermost by its username alone.
  const bot = name === "" ? undefined : type === "telegram" ? `@${name}` : name;
  const reasons = skipReasons(result, mode);
  let summary: string;
  if (!result.ok) {
    summary = t("connections.check.failedSummary");
  } else if (reasons.some((r) => r === "noToken" || r === "retype")) {
    summary = probePassedText(t, type);
  } else if (bot !== undefined) {
    summary = t("connections.check.connectedAs", { bot });
  } else {
    summary = t("connections.check.passed");
  }
  // A set webhook makes getUpdates fail with 409; in the webhook mode Muster set it itself.
  const webhookWarning = result.webhook_set === true && updateMode !== "webhook";
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="connection-check-result">
      <div
        className={cn(
          "rounded-lg border px-4 py-2.5 text-sm",
          result.ok ? "bg-card" : "border-destructive/40 text-destructive",
        )}
        data-testid="connection-check-summary"
      >
        {summary}
      </div>
      <ul className="flex flex-col gap-1.5" aria-label={t("connections.check.stepsLabel")}>
        {result.steps.map((step, index) => (
          <Step
            key={`${step.name}-${index}`}
            step={step}
            skipReason={reasons[index]}
            bot={bot}
            pendingUpdates={result.pending_updates ?? undefined}
          />
        ))}
      </ul>
      {webhookWarning && (
        <div
          className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
          data-testid="connection-check-warning"
        >
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <p className="min-w-0 wrap-anywhere">{t("connections.check.webhookSet")}</p>
        </div>
      )}
      {result.warnings.map((warning) => (
        <div
          key={warning}
          className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
          data-testid="connection-check-warning"
        >
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <p className="min-w-0 wrap-anywhere">
            <WarningText warning={warning} />
          </p>
        </div>
      ))}
    </div>
  );
}

export interface ConnectionCheckProps {
  /** The saved Connection of an edit, which the check reads while the form holds no changes; none for a new one. */
  connectionId?: string;
  type?: Connection["type"];
  /**
   * The form's values, to check before they are saved: always in the create form, and in the edit form while it holds
   * changes, with the saved Connection's id.
   */
  draft?: ConnectionDraft;
  /** The text of the first value of the draft that the server would refuse: the check names it and sends nothing. */
  invalid?: string;
  /** The update mode of a Telegram Connection, as the form holds it. */
  updateMode?: TelegramUpdateMode;
}

/** What a check was made of: the draft as it was sent, or the saved Connection. */
function subjectOf(connectionId: string | undefined, draft: ConnectionDraft | undefined): string {
  return draft === undefined ? `saved ${connectionId ?? ""}` : JSON.stringify(draft);
}

export function ConnectionCheck({
  connectionId,
  type = "mattermost",
  draft,
  invalid,
  updateMode,
}: ConnectionCheckProps) {
  const { t } = useTranslation();
  const mode: CheckMode = connectionId === undefined ? "new" : "edit";
  const queryClient = useQueryClient();
  // A check of the saved Connection records the bot's name, or the info of a bridge, on it, which the page shows.
  const saved = useCheckConnection({
    mutation: {
      onSuccess: () => {
        if (connectionId !== undefined) {
          void queryClient.invalidateQueries({ queryKey: getGetConnectionQueryKey(connectionId) });
        }
      },
    },
  });
  // A draft's check stores nothing.
  const drafted = useCheckConnectionDraft();
  // What the shown result or refusal was made of; a change of the form makes it stale, and it is no longer shown.
  const current = subjectOf(connectionId, draft);
  const [checked, setChecked] = useState<string | null>(null);
  // A value the server would refuse, named before anything is sent.
  const [refused, setRefused] = useState<string>();
  if (checked !== null && checked !== current) {
    setChecked(null);
    setRefused(undefined);
  }
  const check = draft === undefined ? saved : drafted;
  const pending = saved.isPending || drafted.isPending;
  const disabled =
    pending ||
    (draft === undefined ? connectionId === undefined : draftAddress(draft).trim() === "");
  const shown = checked === current;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.check.title")}</h2>
        </CardTitle>
        <CardDescription>{checkHint(t, type)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            disabled={disabled}
            onClick={() => {
              saved.reset();
              drafted.reset();
              setChecked(current);
              if (draft === undefined) {
                setRefused(undefined);
                if (connectionId !== undefined) {
                  saved.mutate({ connectionId });
                }
                return;
              }
              setRefused(invalid);
              if (invalid === undefined) {
                drafted.mutate({ data: draft });
              }
            }}
          >
            {pending ? t("connections.check.running") : t("connections.check.start")}
          </Button>
          {draft !== undefined && (
            <span className="text-sm text-muted-foreground" data-testid="connection-check-draft">
              {t("connections.check.draft")}
            </span>
          )}
        </div>
        {/* One live region, which stays mounted, announces the result or the refusal once. */}
        <div role="status" aria-live="polite" className="flex flex-col gap-3 empty:hidden">
          {shown && refused !== undefined && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm wrap-anywhere text-destructive"
              data-testid="connection-check-error"
            >
              {refused}
            </div>
          )}
          {shown && check.isError && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm wrap-anywhere text-destructive"
              data-testid="connection-check-error"
            >
              {connectionCheckErrorText(t, check.error, type)}
            </div>
          )}
          {shown && check.isSuccess && (
            <Result result={check.data} type={type} mode={mode} updateMode={updateMode} />
          )}
        </div>
        {shown && check.isSuccess && check.data.request_log_run !== undefined && (
          <div>
            <RequestLogButton
              scope={{ run: check.data.request_log_run }}
              label={t("requestLog.ofThisCheck")}
              title={t("requestLog.ofThisCheck")}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
