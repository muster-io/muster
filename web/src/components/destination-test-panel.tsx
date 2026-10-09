// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Test" of a Destination (C-16.FR-1 to FR-3, C-16.AC-3, AC-7; C-13.FR-13): "Send test message" sends, with the saved
// settings and through the interactive path, one test message to a messenger or the test event and the "create"
// request of an outgoing webhook, and shows one block per step — "Message", "Button press", "Event" or "Create" — with
// its outcome ("Sent" or the text of its error class), the error, the duration, the request with every Secret, the
// Signing secret and the tokens masked, the response status, the response body as plain text and the extracted
// values. The "Button press" block of a Mattermost Destination says whether the bot's press of the test message reached
// Muster. The health of the result is the page's at once: a test that succeeded in every step ends the Broken state,
// and when a step failed the Destination stays Broken, which the result explains, the button press included.

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { CircleCheckIcon, CircleXIcon } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetDestinationQueryKey,
  getListDestinationsQueryKey,
  useTestDestination,
} from "../api/gen/endpoints/destinations/destinations";
import type {
  DeliveryErrorClass,
  Destination,
  DestinationTestResult,
  DestinationType,
  TestStep,
  TestStepName,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { CodeText, RenderedRequest, readableBody } from "./rendered-request";
import { type PickedSource, TEST_SOURCE_QUERY, testSource } from "./test-source-picker";
import { Button } from "./ui/button";
import { cn } from "./ui/utils";
import { requestName } from "./webhook-destination-fields";

export function stepName(t: TFunction, name: TestStepName): string {
  switch (name) {
    case "message":
      return t("destinationTest.steps.message");
    case "press":
      return t("destinationTest.steps.press");
    case "event":
      return t("destinationTest.steps.event");
    case "create":
      return requestName(t, "create");
    default:
      return unreachable(name);
  }
}

/** Fails the type check when an enum of the API gains a value that a switch does not name. */
export function unreachable(value: never): string {
  return String(value);
}

/** The text of an error class; "Sent" for none. */
export function classText(t: TFunction, errorClass: DeliveryErrorClass): string {
  switch (errorClass) {
    case "none":
      return t("destinationTest.classes.none");
    case "retry_after":
      return t("destinationTest.classes.retryAfter");
    case "transient":
      return t("destinationTest.classes.transient");
    case "fatal":
      return t("destinationTest.classes.fatal");
    case "template_error":
      return t("destinationTest.classes.templateError");
    case "blocked":
      return t("destinationTest.classes.blocked");
    case "limited":
      return t("destinationTest.classes.limited");
    case "unknown":
      return t("destinationTest.classes.unknown");
    default:
      return unreachable(errorClass);
  }
}

/**
 * The outcome of a step: the button press says whether presses reach Muster, or its error, which names what to change
 * on the Mattermost server; any other step "Sent" or the text of its class.
 */
export function outcomeText(t: TFunction, step: TestStep): string {
  if (step.name === "press") {
    if (step.error_class === "none") {
      return t("destinationTest.pressReached");
    }
    if (step.error_class === "limited") {
      // The message was posted; only its press waited for a limiter token in vain.
      return t("destinationTest.pressLimited");
    }
    return step.error ?? classText(t, step.error_class);
  }
  return classText(t, step.error_class);
}

/** Whether a test or a preview was refused because its Alert Group is no longer offered. */
export function isSourceGone(err: unknown): boolean {
  return (
    isApiError(err) &&
    err.status === 422 &&
    (err.errors ?? []).some(
      (e) => e.code === "unknown_id" && e.pointer === "/source/alert_group_id",
    )
  );
}

/** The text of a refused test or preview. */
export function testErrorText(t: TFunction, err: unknown): string {
  return isSourceGone(err) ? t("destinationTest.errors.unknownAlertGroup") : problemText(t, err);
}

function StepBlock({ step }: { step: TestStep }) {
  const { t } = useTranslation();
  const headingId = useId();
  const ok = step.error_class === "none";
  // The press shows its error as its outcome; the other steps show it below.
  const error = step.name === "press" ? null : (step.error ?? null);
  const extracted = Object.entries(step.extracted ?? {});
  return (
    <article
      aria-labelledby={headingId}
      className={cn(
        "flex min-w-0 flex-col gap-3 rounded-lg border p-3",
        ok ? "bg-card" : "border-destructive/40",
      )}
      data-testid="test-step"
      data-step={step.name}
      data-class={step.error_class}
    >
      <h3 id={headingId} className="flex min-w-0 items-start gap-2 text-sm">
        {ok ? (
          <CircleCheckIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400"
          />
        ) : (
          <CircleXIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
        )}
        <span className="min-w-0 wrap-anywhere" data-testid="test-step-title">
          <span className="font-medium">{stepName(t, step.name)}:</span>{" "}
          <span className={ok ? undefined : "text-destructive"}>{outcomeText(t, step)}</span>
        </span>
      </h3>
      {error !== null && error !== "" && (
        <p
          className="text-sm wrap-anywhere whitespace-pre-wrap text-destructive"
          data-testid="test-step-error"
        >
          {error}
        </p>
      )}
      <p className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span data-testid="test-step-duration">
          {t("destinationTest.duration", { value: step.duration_ms })}
        </span>
        {step.response_status !== null && step.response_status !== undefined && (
          <span data-testid="test-step-status">
            {t("destinationTest.responseStatus", { status: step.response_status })}
          </span>
        )}
      </p>
      {step.request !== undefined && (
        <div className="flex min-w-0 flex-col gap-1">
          <h4 className="text-xs font-medium text-muted-foreground">
            {t("destinationTest.request.title")}
          </h4>
          <RenderedRequest request={step.request} />
        </div>
      )}
      {step.response_body !== null && step.response_body !== undefined && (
        <div className="flex min-w-0 flex-col gap-1">
          <h4 className="text-xs font-medium text-muted-foreground">
            {t("destinationTest.responseBody")}
          </h4>
          {step.response_body === "" ? (
            <p className="text-xs text-muted-foreground">{t("destinationTest.emptyBody")}</p>
          ) : (
            <CodeText text={readableBody(step.response_body)} testId="test-step-response" />
          )}
        </div>
      )}
      {extracted.length > 0 && (
        <table
          className="w-full min-w-0 table-fixed text-left text-xs"
          data-testid="test-step-extracted"
        >
          <caption className="pb-1 text-left text-xs font-medium text-muted-foreground">
            {t("destinationTest.extracted")}
          </caption>
          <thead className="sr-only">
            <tr>
              <th scope="col">{t("destinationTest.extractedName")}</th>
              <th scope="col">{t("destinationTest.extractedValue")}</th>
            </tr>
          </thead>
          <tbody className="font-mono">
            {extracted.map(([name, value]) => (
              <tr key={name} className="border-t align-top">
                <th scope="row" className="w-1/3 py-1 pr-2 font-semibold wrap-anywhere">
                  {name}
                </th>
                <td className="py-1 wrap-anywhere">{value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </article>
  );
}

/** Why a Destination that a failed test leaves Broken stays so; nothing when it is healthy or every step succeeded. */
export function stillBrokenText(t: TFunction, result: DestinationTestResult): string | null {
  const failed = result.steps.some((s) => s.error_class !== "none");
  if (!failed || result.health.state !== "broken") {
    return null;
  }
  const message = result.steps.find((s) => s.name === "message");
  const press = result.steps.find((s) => s.name === "press");
  // The message was posted, so only the press keeps the Destination Broken.
  if (message?.error_class === "none" && press !== undefined && press.error_class !== "none") {
    return t("destinationTest.stillBrokenPress");
  }
  return t("destinationTest.stillBroken");
}

/** Whether every step of a test succeeded. */
export function testPassed(result: DestinationTestResult): boolean {
  return result.steps.length > 0 && result.steps.every((s) => s.error_class === "none");
}

export function TestResult({ result }: { result: DestinationTestResult }) {
  const { t } = useTranslation();
  const passed = testPassed(result);
  const broken = stillBrokenText(t, result);
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="test-result">
      <div
        className={cn(
          "rounded-lg border px-4 py-2.5 text-sm",
          passed ? "bg-card" : "border-destructive/40 text-destructive",
        )}
        data-testid="test-summary"
      >
        {passed ? t("destinationTest.passed") : t("destinationTest.failed")}
      </div>
      {broken !== null && (
        <p
          className="rounded-lg border border-destructive/40 bg-card px-4 py-2.5 text-sm wrap-anywhere"
          data-testid="test-still-broken"
        >
          {broken}
        </p>
      )}
      {result.steps.map((step, index) => (
        <StepBlock key={`${step.name}-${index}`} step={step} />
      ))}
    </div>
  );
}

function testHint(t: TFunction, type: DestinationType): string {
  switch (type) {
    case "mattermost":
      return t("destinationTest.hints.mattermost");
    case "telegram":
      return t("destinationTest.hints.telegram");
    default:
      return t("destinationTest.hints.webhook");
  }
}

export interface DestinationTestPanelProps {
  destination: Destination;
  source: PickedSource;
  /** The form has unsaved changes; the test uses the saved Destination. */
  dirty: boolean;
  /** The chosen Alert Group is no longer offered: the selector reads its list again and returns to the example. */
  onSourceGone: () => void;
}

export function DestinationTestPanel({
  destination,
  source,
  dirty,
  onSourceGone,
}: DestinationTestPanelProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const test = useTestDestination({
    mutation: {
      onError: (err) => {
        if (isSourceGone(err)) {
          void queryClient.invalidateQueries({ queryKey: [TEST_SOURCE_QUERY] });
          onSourceGone();
        }
      },
      onSuccess: (result) => {
        // The health of the result is the Destination's now; the page shows it without waiting for the hint.
        queryClient.setQueryData<Destination>(getGetDestinationQueryKey(destination.id), (old) =>
          old === undefined ? old : { ...old, health: result.health },
        );
        void queryClient.invalidateQueries({ queryKey: getGetDestinationQueryKey(destination.id) });
        void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
      },
    },
  });
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="destination-test">
      <p className="text-sm text-muted-foreground">
        {testHint(t, destination.type)} {t("destinationTest.hints.broken")}
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <Button
          disabled={test.isPending}
          onClick={() =>
            test.mutate({ destinationId: destination.id, data: { source: testSource(source) } })
          }
        >
          {test.isPending ? t("destinationTest.sending") : t("destinationTest.send")}
        </Button>
        {dirty && (
          <span className="text-sm text-muted-foreground">{t("destinationTest.unsaved")}</span>
        )}
      </div>
      {/* One live region, which stays mounted, announces the outcome or the refusal once; the steps follow it. */}
      <div role="status" aria-live="polite" className="sr-only">
        {test.isSuccess &&
          (testPassed(test.data) ? t("destinationTest.passed") : t("destinationTest.failed"))}
        {test.isError && testErrorText(t, test.error)}
      </div>
      {test.isError && (
        <div
          className="rounded-lg border border-destructive/40 px-4 py-2.5 text-sm wrap-anywhere text-destructive"
          data-testid="test-error"
        >
          {testErrorText(t, test.error)}
        </div>
      )}
      {test.isSuccess && <TestResult result={test.data} />}
    </div>
  );
}
