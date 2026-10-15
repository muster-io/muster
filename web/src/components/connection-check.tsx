// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of a Connection (C-13.FR-2, AC-16; C-14.FR-11): each step with "ok" or what failed, translated by
// its code with the server's English message below as its detail, its latency and the path it took, directly or through
// the proxy; "Connected as {bot}" once the token works; and the warnings that do not fail the check, each with its hint.
// Telegram works in steps — the dry probe without the token, getMe with the bot, getWebhookInfo with the pending updates
// — and a set webhook in the long-polling mode gets its warning. While the form holds an unsaved base URL, the check
// sends it as base_url: the server makes only the dry probe against it, never with the token, and the other steps read
// "Skipped: save the address first". The check runs on the interactive path, so a busy messenger answers 503 with
// Retry-After: "The messenger is busy; try again in N s."

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { CircleCheckIcon, CircleMinusIcon, CircleXIcon, TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";

import {
  getGetConnectionQueryKey,
  useCheckConnection,
} from "../api/gen/endpoints/connections/connections";
import type {
  Connection,
  ConnectionCheckResult,
  ConnectionCheckResultWarningsItem,
  ConnectionCheckStep,
  ConnectionCheckStepName,
  NullableConnectionCheckCode,
  NullableDestinationCheckCode,
  TelegramUpdateMode,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { unreachable } from "./destination-test-panel";
import { isBaseUrl } from "./telegram-connection-fields";
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
    default:
      return t("connections.check.steps.getWebhookInfo");
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

/** Why a step was skipped: the address is not saved yet, or an earlier step failed, so the token stays put. */
type SkipReason = "unsaved" | "earlier";

function Step({
  step,
  skipReason,
  bot,
  pendingUpdates,
}: {
  step: ConnectionCheckStep;
  skipReason: SkipReason;
  bot?: string;
  pendingUpdates?: number;
}) {
  const { t } = useTranslation();
  const skipped = step.skipped === true;
  const details: string[] = [];
  if (skipped) {
    details.push(
      skipReason === "unsaved"
        ? t("connections.check.skippedUnsaved")
        : t("connections.check.skippedEarlier"),
    );
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
  skipReason,
  updateMode,
}: {
  result: ConnectionCheckResult;
  type: Connection["type"];
  skipReason: SkipReason;
  updateMode?: TelegramUpdateMode;
}) {
  const { t } = useTranslation();
  const name = result.bot_name ?? "";
  // Telegram names bots by their @username; Mattermost by its username alone.
  const bot = name === "" ? undefined : type === "telegram" ? `@${name}` : name;
  let summary: string;
  if (!result.ok) {
    summary = t("connections.check.failedSummary");
  } else if (skipReason === "unsaved") {
    summary = t("connections.check.probePassed");
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
            skipReason={skipReason}
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
  connectionId: string;
  type?: Connection["type"];
  /** The form has unsaved changes; the check uses the saved Connection. */
  dirty: boolean;
  /**
   * The Bot API base URL in the form of a Telegram Connection when it differs from the saved one: the check makes only
   * the dry probe against it, without the token.
   */
  unsavedBaseUrl?: string;
  /** The saved update mode of a Telegram Connection. */
  updateMode?: TelegramUpdateMode;
}

export function ConnectionCheck({
  connectionId,
  type = "mattermost",
  dirty,
  unsavedBaseUrl,
  updateMode,
}: ConnectionCheckProps) {
  const { t } = useTranslation();
  // An unsaved address the server would refuse is named before anything is sent.
  const [badAddress, setBadAddress] = useState(false);
  const queryClient = useQueryClient();
  // A successful check records the bot's name on the Connection, which the page shows.
  const check = useCheckConnection({
    mutation: {
      onSuccess: () =>
        void queryClient.invalidateQueries({ queryKey: getGetConnectionQueryKey(connectionId) }),
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("connections.check.title")}</h2>
        </CardTitle>
        <CardDescription>
          {type === "telegram" ? t("connections.check.telegramHint") : t("connections.check.hint")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            disabled={check.isPending}
            onClick={() => {
              if (unsavedBaseUrl !== undefined && !isBaseUrl(unsavedBaseUrl)) {
                check.reset();
                setBadAddress(true);
                return;
              }
              setBadAddress(false);
              check.mutate(
                unsavedBaseUrl === undefined
                  ? { connectionId }
                  : { connectionId, data: { base_url: unsavedBaseUrl.trim() } },
              );
            }}
          >
            {check.isPending ? t("connections.check.running") : t("connections.check.start")}
          </Button>
          {unsavedBaseUrl !== undefined ? (
            <span className="text-sm text-muted-foreground" data-testid="connection-check-unsaved">
              {t("connections.check.unsavedAddress")}
            </span>
          ) : (
            dirty && (
              <span className="text-sm text-muted-foreground">
                {t("connections.check.unsaved")}
              </span>
            )
          )}
        </div>
        {/* One live region, which stays mounted, announces the result or the refusal once. */}
        <div role="status" aria-live="polite" className="flex flex-col gap-3 empty:hidden">
          {badAddress && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm text-destructive"
              data-testid="connection-check-error"
            >
              {t("connections.errors.baseUrlFormat")}
            </div>
          )}
          {check.isError && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm text-destructive"
              data-testid="connection-check-error"
            >
              {checkErrorText(t, check.error)}
            </div>
          )}
          {check.isSuccess && (
            <Result
              result={check.data}
              type={type}
              skipReason={check.variables.data?.base_url ? "unsaved" : "earlier"}
              updateMode={updateMode}
            />
          )}
        </div>
      </CardContent>
    </Card>
  );
}
