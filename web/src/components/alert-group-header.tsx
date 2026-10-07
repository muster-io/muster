// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The header of the Alert Group page (C-09.FR-14, FR-10): status, #N, title and summary, Severity level, Urgent, the
// Route and the Integrations linked, start and duration, "🔁 Reopened ×N" and, once resolved, who resolved it or the
// system's reason; the Owner, the end of a Snooze ("No end" too) and who set it (C-10.FR-13), a deleted user named
// "(deactivated)". Status and urgency are words, never only a colour. The title and summary come from alert labels
// and annotations, so they show as text only. The list shares the status and the Urgent mark.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { ArrowLeftIcon, SirenIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type {
  ActorRef,
  AlertGroup,
  AlertGroupStatus,
  EntityRef,
  ResolvedBy,
} from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { reasonLabel, severityLabel } from "./integration-alerts";
import { DateTime, Duration } from "./relative-time";
import { cn } from "./ui/utils";

export function statusLabel(t: TFunction, status: AlertGroupStatus): string {
  switch (status) {
    case "firing":
      return t("alertGroups.status.firing");
    case "acknowledged":
      return t("alertGroups.status.acknowledged");
    case "snoozed":
      return t("alertGroups.status.snoozed");
    default:
      return t("alertGroups.status.resolved");
  }
}

const STATUS_TONE: Record<AlertGroupStatus, string> = {
  firing: "border-destructive/40 bg-destructive/10 text-destructive",
  acknowledged: "border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-300",
  snoozed: "border-sky-500/40 bg-sky-500/10 text-sky-800 dark:text-sky-300",
  resolved: "border-emerald-600/40 bg-emerald-600/10 text-emerald-800 dark:text-emerald-300",
};

/** The status of an Alert Group as a word on its tone. */
export function StatusBadge({
  status,
  className,
}: {
  status: AlertGroupStatus;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap",
        STATUS_TONE[status],
        className,
      )}
      data-testid="alert-group-status"
    >
      {statusLabel(t, status)}
    </span>
  );
}

/** The Urgent mark: an icon with the word. */
export function UrgentMark({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-xs font-semibold whitespace-nowrap text-destructive",
        className,
      )}
      data-testid="urgent-mark"
    >
      <SirenIcon aria-hidden="true" className="size-3.5" />
      {t("alertGroups.urgent")}
    </span>
  );
}

/** Who resolved an Alert Group: "Resolved by {name}", or "Resolved: {reason}" for the system. */
export function resolutionText(t: TFunction, resolution: ResolvedBy): string {
  if (resolution.by === "user") {
    const actor = resolution.actor;
    if (actor === undefined) {
      return t("alertGroups.resolution.byUnknown");
    }
    const name = actor.deactivated
      ? t("alertGroups.deactivated", { name: actor.name })
      : actor.name;
    return t("alertGroups.resolution.byPerson", { name });
  }
  const code = resolution.reason_code;
  // A deleted Integration is named in the server's text; the other reasons have their own words.
  const reason =
    code === "integration_deleted" || code === null || code === undefined
      ? (resolution.reason ?? reasonLabel(t, code))
      : reasonLabel(t, code);
  return reason
    ? t("alertGroups.resolution.bySystem", { reason })
    : t("alertGroups.resolution.bySystemNoReason");
}

/** A linked entity, or its name when the session may not open it. */
export function EntityLink({
  entity,
  to,
  can,
}: {
  entity: EntityRef;
  to: "/routes/$routeId" | "/integrations/$integrationId";
  can: boolean;
}) {
  if (!can) {
    return <span className="wrap-anywhere">{entity.name}</span>;
  }
  return (
    <Link
      to={to}
      params={to === "/routes/$routeId" ? { routeId: entity.id } : { integrationId: entity.id }}
      className="wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {entity.name}
    </Link>
  );
}

function Fact({ term, children, testId }: { term: string; children: ReactNode; testId: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5" data-testid={testId}>
      <dt className="text-xs text-muted-foreground">{term}</dt>
      <dd className="min-w-0 text-sm">{children}</dd>
    </div>
  );
}

function actorName(t: TFunction, actor: Pick<ActorRef, "name" | "deactivated">): string {
  return actor.deactivated ? t("alertGroups.deactivated", { name: actor.name }) : actor.name;
}

/** The Owner and the Snooze of an Alert Group: "Owner: Alice", "Snoozed until …" or "Snoozed with no end", "Snoozed by …". */
function Ownership({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const snoozed = group.status === "snoozed";
  if (group.owner === undefined && !snoozed) {
    return null;
  }
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
      {group.owner !== undefined && (
        <p className="font-medium wrap-anywhere" data-testid="alert-group-owner">
          {t("alertGroups.owner", { name: actorName(t, group.owner) })}
        </p>
      )}
      {snoozed && (
        <p className="wrap-anywhere" data-testid="alert-group-snooze">
          {group.snooze_until
            ? t("alertGroups.snoozedUntil", { until: dateTime(group.snooze_until) })
            : t("alertGroups.snoozedNoEnd")}
        </p>
      )}
      {snoozed && group.snoozed_by !== undefined && (
        <p className="text-muted-foreground wrap-anywhere" data-testid="alert-group-snoozed-by">
          {t("alertGroups.snoozedBy", { name: actorName(t, group.snoozed_by) })}
        </p>
      )}
    </div>
  );
}

export function AlertGroupHeader({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const canRoutes = useCan("routes:read");
  const canIntegrations = useCan("integrations:read");
  return (
    <header className="flex flex-col gap-4" data-testid="alert-group-header">
      <Link
        to="/alert-groups"
        className="inline-flex w-fit items-center gap-1 rounded-md text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ArrowLeftIcon aria-hidden="true" className="size-4" />
        {t("alertGroups.page.back")}
      </Link>
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={group.status} />
          <span
            className="font-mono text-sm text-muted-foreground"
            data-testid="alert-group-number"
          >
            #{group.number}
          </span>
          {group.urgent && <UrgentMark />}
          {group.reopen_count > 0 && (
            <span className="text-sm whitespace-nowrap" data-testid="reopen-count">
              {t("alertGroups.reopened", { value: group.reopen_count })}
            </span>
          )}
        </div>
        <h1
          className="text-2xl font-semibold tracking-tight wrap-anywhere"
          data-testid="alert-group-title"
        >
          {group.title}
        </h1>
        {group.summary && (
          <p className="text-muted-foreground wrap-anywhere" data-testid="alert-group-summary">
            {group.summary}
          </p>
        )}
        {group.status === "resolved" && group.resolution !== undefined && (
          <p className="text-sm font-medium wrap-anywhere" data-testid="alert-group-resolution">
            {resolutionText(t, group.resolution)}
          </p>
        )}
        <Ownership group={group} />
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3 lg:grid-cols-6">
        <Fact term={t("alertGroups.fields.severity")} testId="fact-severity">
          {severityLabel(t, group.severity_level)}
        </Fact>
        <Fact term={t("alertGroups.fields.route")} testId="fact-route">
          <EntityLink entity={group.route} to="/routes/$routeId" can={canRoutes} />
        </Fact>
        <Fact term={t("alertGroups.fields.integrations")} testId="fact-integrations">
          {group.integrations.length === 0 ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            <ul className="flex flex-wrap gap-x-2 gap-y-0.5">
              {group.integrations.map((i) => (
                <li key={i.id} className="min-w-0">
                  <EntityLink entity={i} to="/integrations/$integrationId" can={canIntegrations} />
                </li>
              ))}
            </ul>
          )}
        </Fact>
        <Fact term={t("alertGroups.fields.started")} testId="fact-started">
          <DateTime iso={group.started_at} />
        </Fact>
        <Fact term={t("alertGroups.fields.duration")} testId="fact-duration">
          <Duration start={group.started_at} end={group.resolved_at} />
        </Fact>
        <Fact term={t("alertGroups.fields.alerts")} testId="fact-alerts">
          {t("alertGroups.alertCounts", {
            count: group.firing_alert_count,
            total: group.firing_alert_count + group.resolved_alert_count,
          })}
        </Fact>
      </dl>
    </header>
  );
}
