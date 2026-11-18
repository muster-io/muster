// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "How it went" on the Escalation chain page (C-32.FR-18, AC-17): the recent runs of the chain, newest first, each with
// its Alert Group, the steps it made with their Pages — by Mention in the Threads, and calls through a Notification
// gateway with the gateway's name —, the Pages that were not delivered with the reason, the steps it skipped with the
// reason, and what stopped it: the Command and the person, or that it ran out. A Page has one outcome for all the
// people it names. A step shows the condition the chain has now, which is all the run's `severity` skip reason can be
// told by.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import { useListEscalationChainRuns } from "../api/gen/endpoints/escalation-chains/escalation-chains";
import type {
  ActorRef,
  DestinationRef,
  EscalationRun,
  EscalationRunPage,
  EscalationRunStep,
  EscalationStep,
} from "../api/gen/model";
import { problemText } from "../lib/api";
import { kindName } from "../lib/escalation-steps";
import { messengerName } from "../lib/mention-entries";
import { useTimeFormat } from "../lib/time";

/** The most runs "How it went" lists. */
const RUNS_LIMIT = 20;

function person(t: TFunction, actor: ActorRef | null): string {
  if (actor === null) {
    return t("escalation.runs.byMuster");
  }
  const text = actor.login !== undefined && actor.login !== "" ? `@${actor.login}` : actor.name;
  return actor.deactivated ? t("alertGroups.deactivated", { name: text }) : text;
}

function peopleOf(page: EscalationRunPage): string {
  return page.users
    .map((u) => (u.login !== undefined && u.login !== "" ? `@${u.login}` : u.name))
    .join(", ");
}

/** What stopped a run, or that it runs on or ran out. */
export function runEnd(t: TFunction, run: EscalationRun): string {
  if (run.state === "running") {
    return t("escalation.runs.running");
  }
  if (run.state === "ran_out" || run.stopped_by === null) {
    return t("escalation.runs.ranOut");
  }
  const who = person(t, run.stopped_by.actor);
  switch (run.stopped_by.command) {
    case "acknowledge":
      return t("escalation.runs.acknowledged", { who });
    case "snooze":
      return t("escalation.runs.snoozed", { who });
    case "resolve":
      return t("escalation.runs.resolved", { who });
    case "started_over":
      return t("escalation.runs.startedOver");
    default:
      return run.stopped_by.command;
  }
}

/**
 * Why a step was skipped, in words. The reason `severity` does not say which condition failed, so the words read it
 * from the chain's step of that number while it still is of that kind.
 */
export function skipText(
  t: TFunction,
  step: EscalationRunStep,
  current: readonly EscalationStep[],
): string {
  switch (step.reason) {
    case "outside_working_hours":
      return t("escalation.skip.outsideWorkingHours");
    case "severity": {
      const now = current.find((s) => s.number === step.number && s.kind === step.kind);
      if (now?.severity_condition === "critical") {
        return t("escalation.skip.onlyCritical");
      }
      return now?.severity_condition === "below_critical"
        ? t("escalation.skip.onlyBelowCritical")
        : t("escalation.skip.severity");
    }
    case "schedule_gap":
      return t("escalation.skip.scheduleGap");
    case "viewer":
      return t("escalation.skip.viewer");
    case "disabled":
      return t("escalation.skip.disabled");
    case "destination_deleted":
      return t("escalation.skip.destinationDeleted");
    case "webhook_events_required":
      return t("escalation.skip.webhookEventsRequired");
    case "no_mention_possible":
      return t("escalation.skip.noMentionPossible");
    case "gateway_deleted":
      return t("escalation.skip.gatewayDeleted");
    case "no_confirmed_contact":
      return t("escalation.skip.noConfirmedContact");
    default:
      // A reason a later version added shows as it is.
      return step.reason === null ? t("escalation.skip.unknown") : step.reason;
  }
}

/** The messenger a chain's Pages by Mention go through, when all its Destinations are of one type. */
function messengerOf(t: TFunction, destinations: readonly DestinationRef[]): string | undefined {
  const type = destinations[0]?.type;
  return type !== undefined && destinations.every((d) => d.type === type)
    ? messengerName(t, type)
    : undefined;
}

/** A Page that was not delivered, with its reason: one reason for all the people it names. */
export function notDeliveredText(
  t: TFunction,
  page: EscalationRunPage,
  destinations: readonly DestinationRef[],
): string {
  const people = peopleOf(page);
  const count = Math.max(1, page.users.length);
  switch (page.reason) {
    case "no_account_link": {
      const messenger = messengerOf(t, destinations);
      return messenger === undefined
        ? t("escalation.pages.noAccountLinkHere", { people, count })
        : t("escalation.pages.noAccountLink", { people, messenger, count });
    }
    case "no_root_message":
      return t("escalation.pages.noRootMessage");
    case "not_delivered":
      return t("escalation.pages.threadNotDelivered");
    case "call_refused":
      return t("escalation.pages.callRefused");
    case "call_unknown":
      return t("escalation.pages.callUnknown");
    case "call_budget_used":
      return t("escalation.pages.callBudgetUsed");
    case "no_confirmed_contact":
      return t("escalation.pages.noConfirmedContact", { people, count });
    case "gateway_deleted":
      return t("escalation.pages.gatewayDeleted");
    case "gateway_broken":
      return t("escalation.pages.gatewayBroken");
    case null:
    case undefined:
      return page.kind === "call"
        ? t("escalation.pages.callNotDelivered")
        : t("escalation.pages.severalReasons", { people });
    default:
      return t("escalation.pages.other", { reason: page.reason });
  }
}

/** What a Page is: who it names, and the gateway of a call. */
function pageWhat(t: TFunction, page: EscalationRunPage): string {
  if (page.kind === "call") {
    return t("escalation.pages.call", {
      gateway: page.gateway?.name ?? "",
      people: peopleOf(page),
    });
  }
  if (page.team !== null) {
    return t("escalation.pages.team", { team: page.team.name });
  }
  if (page.everyone !== null) {
    return t("escalation.pages.everyone", { word: `@${page.everyone}` });
  }
  if (page.kind === "mention") {
    return t("escalation.pages.mention", { people: peopleOf(page) });
  }
  // A kind a later version added shows as it is.
  return String(page.kind);
}

function PageLine({
  page,
  destinations,
}: {
  page: EscalationRunPage;
  destinations: readonly DestinationRef[];
}) {
  const { t } = useTranslation();
  return (
    <li
      className="flex min-w-0 flex-col gap-0.5 text-sm"
      data-testid="run-page"
      data-outcome={page.outcome}
      data-kind={page.kind}
    >
      <span className="wrap-anywhere">
        {pageWhat(t, page)}
        {" · "}
        <span
          className={
            page.outcome === "not_delivered" ? "text-destructive" : "text-muted-foreground"
          }
        >
          {page.outcome === "delivered"
            ? t("escalation.pages.delivered")
            : page.outcome === "pending"
              ? t("escalation.pages.pending")
              : t("escalation.pages.notDelivered")}
        </span>
      </span>
      {page.outcome === "not_delivered" && (
        <span className="text-destructive wrap-anywhere" data-testid="run-page-reason">
          {notDeliveredText(t, page, destinations)}
        </span>
      )}
    </li>
  );
}

function StepLine({
  step,
  current,
  destinations,
}: {
  step: EscalationRunStep;
  current: readonly EscalationStep[];
  destinations: readonly DestinationRef[];
}) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  return (
    <li className="flex min-w-0 flex-col gap-1" data-testid="run-step" data-outcome={step.outcome}>
      <span className="flex flex-wrap items-baseline gap-x-2 text-sm">
        {step.outcome === "skipped" ? (
          <span className="font-medium wrap-anywhere" data-testid="run-step-skipped">
            {step.reason === null
              ? t("escalation.runs.stepSkippedNoReason", { number: step.number })
              : t("escalation.runs.stepSkipped", {
                  number: step.number,
                  reason: skipText(t, step, current),
                })}
          </span>
        ) : (
          <span className="font-medium">{t("escalation.runs.step", { number: step.number })}</span>
        )}
        <span className="text-muted-foreground">
          {kindName(t, step.kind)} · {time(step.at)}
        </span>
      </span>
      {step.pages.length > 0 && (
        <ul className="ml-3 flex flex-col gap-1 border-l pl-3">
          {step.pages.map((page, i) => (
            <PageLine key={`${page.kind}-${i}`} page={page} destinations={destinations} />
          ))}
        </ul>
      )}
    </li>
  );
}

function Run({
  run,
  current,
  destinations,
}: {
  run: EscalationRun;
  current: readonly EscalationStep[];
  destinations: readonly DestinationRef[];
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  return (
    <li className="flex min-w-0 flex-col gap-2 rounded-lg border px-3 py-3" data-testid="chain-run">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1">
        <Link
          to="/alert-groups/$alertGroupId"
          params={{ alertGroupId: run.alert_group.id }}
          className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
        >
          {t("escalation.runs.alertGroup", {
            number: run.alert_group.number,
            title: run.alert_group.title,
          })}
        </Link>
        <span className="text-sm text-muted-foreground">{dateTime(run.started_at)}</span>
      </div>
      {run.steps.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="run-no-steps">
          {t("escalation.runs.noSteps")}
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {run.steps.map((step, i) => (
            <StepLine
              key={`${step.number}-${i}`}
              step={step}
              current={current}
              destinations={destinations}
            />
          ))}
        </ol>
      )}
      <p className="text-sm font-medium wrap-anywhere" data-testid="run-end">
        {runEnd(t, run)}
      </p>
    </li>
  );
}

export interface EscalationChainRunsProps {
  chainId: string;
  /** The chain's steps now, which tell a `severity` skip what the condition was. */
  steps: readonly EscalationStep[];
  /** The Destinations the chain writes in, which tell the messenger of an undelivered Mention. */
  destinations: readonly DestinationRef[];
}

export function EscalationChainRuns({ chainId, steps, destinations }: EscalationChainRunsProps) {
  const { t } = useTranslation();
  const id = useId();
  const runs = useListEscalationChainRuns(
    chainId,
    { limit: RUNS_LIMIT },
    { query: { refetchInterval: 15_000 } },
  );
  return (
    <section
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-3"
      data-testid="chain-runs"
    >
      <h2 id={`${id}-title`} className="text-lg font-semibold">
        {t("escalation.runs.title")}
      </h2>
      <p className="text-sm text-muted-foreground">{t("escalation.runs.hint")}</p>
      {runs.data === undefined ? (
        <p
          className={runs.isError ? "text-sm text-destructive" : "text-sm text-muted-foreground"}
          role={runs.isError ? "alert" : "status"}
        >
          {runs.isError ? problemText(t, runs.error) : t("common.loading")}
        </p>
      ) : runs.data.items.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="chain-runs-empty">
          {t("escalation.runs.empty")}
        </p>
      ) : (
        <ol className="flex flex-col gap-3">
          {runs.data.items.map((run) => (
            <Run key={run.id} run={run} current={steps} destinations={destinations} />
          ))}
        </ol>
      )}
    </section>
  );
}
