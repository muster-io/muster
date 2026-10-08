// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Check connection" of a Connection (C-13.FR-2, AC-16): each step with "ok" or its message, its latency and the path
// it took, directly or through the proxy; for Mattermost "Connected as {bot}" once the token works; and the warnings
// that do not fail the check, each with its hint. The check runs on the interactive path, so a busy messenger answers
// 503 with Retry-After: "The messenger is busy; try again in N s."

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { CircleCheckIcon, CircleXIcon, TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  getGetConnectionQueryKey,
  useCheckConnection,
} from "../api/gen/endpoints/connections/connections";
import type {
  ConnectionCheckResult,
  ConnectionCheckResultWarningsItem,
  ConnectionCheckStep,
  ConnectionCheckStepName,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
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

function warningText(t: TFunction, warning: ConnectionCheckResultWarningsItem): string {
  switch (warning) {
    case "press_answers_in_thread":
      return t("connections.check.warnings.pressAnswersInThread");
    default:
      return warning;
  }
}

/** The text of a refused check: a busy messenger names its wait, anything else as every form shows it. */
export function checkErrorText(t: TFunction, err: unknown): string {
  if (isApiError(err) && err.status === 503) {
    return t("connections.check.busy", { seconds: Math.max(1, err.retry_after_seconds ?? 1) });
  }
  return problemText(t, err);
}

function Step({ step }: { step: ConnectionCheckStep }) {
  const { t } = useTranslation();
  const details: string[] = [];
  if (step.skipped === true) {
    details.push(t("connections.check.skipped"));
  } else if (step.ok) {
    details.push(t("connections.check.ok"));
  }
  if (step.latency_ms !== undefined && step.latency_ms !== null && step.skipped !== true) {
    details.push(t("connections.check.latency", { ms: step.latency_ms }));
  }
  if (step.via !== undefined && step.skipped !== true) {
    details.push(
      step.via === "proxy" ? t("connections.check.viaProxy") : t("connections.check.direct"),
    );
  }
  const failed = !step.ok && step.skipped !== true;
  return (
    <li className="flex min-w-0 items-start gap-2 text-sm" data-testid="connection-check-step">
      {failed ? (
        <CircleXIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
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
          <span className="wrap-anywhere text-destructive">
            {step.message ?? t("connections.check.unknownError")}
          </span>
        )}
      </div>
    </li>
  );
}

function Result({ result }: { result: ConnectionCheckResult }) {
  const { t } = useTranslation();
  let summary: string;
  if (!result.ok) {
    summary = t("connections.check.failedSummary");
  } else if (result.bot_name !== undefined && result.bot_name !== null && result.bot_name !== "") {
    summary = t("connections.check.connectedAs", { bot: result.bot_name });
  } else {
    summary = t("connections.check.passed");
  }
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
          <Step key={`${step.name}-${index}`} step={step} />
        ))}
      </ul>
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
          <p className="min-w-0 wrap-anywhere">{warningText(t, warning)}</p>
        </div>
      ))}
    </div>
  );
}

export interface ConnectionCheckProps {
  connectionId: string;
  /** The form has unsaved changes; the check uses the saved Connection. */
  dirty: boolean;
}

export function ConnectionCheck({ connectionId, dirty }: ConnectionCheckProps) {
  const { t } = useTranslation();
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
        <CardDescription>{t("connections.check.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="outline"
            disabled={check.isPending}
            onClick={() => check.mutate({ connectionId })}
          >
            {check.isPending ? t("connections.check.running") : t("connections.check.start")}
          </Button>
          {dirty && (
            <span className="text-sm text-muted-foreground">{t("connections.check.unsaved")}</span>
          )}
        </div>
        {/* One live region, which stays mounted, announces the result or the refusal once. */}
        <div role="status" aria-live="polite" className="flex flex-col gap-3 empty:hidden">
          {check.isError && (
            <div
              className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm text-destructive"
              data-testid="connection-check-error"
            >
              {checkErrorText(t, check.error)}
            </div>
          )}
          {check.isSuccess && <Result result={check.data} />}
        </div>
      </CardContent>
    </Card>
  );
}
