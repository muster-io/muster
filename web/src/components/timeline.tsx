// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Timeline of an Alert Group (C-09.FR-11, FR-14): newest or oldest first, filtered by kind. Each entry shows its
// time, the actor ("Muster" for the system) with the Transport, a text for its event, a "Loud" mark and the symbolic
// Mentions it asked for, and its details: fingerprints, the replaced label, Static label conflicts, the period of
// Muster's downtime, a Note's text, a delivery error. Notes, names and errors are untrusted text and show as such.

import { useInfiniteQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { BellRingIcon } from "lucide-react";
import { type ReactNode, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getAlertGroupTimeline,
  getGetAlertGroupTimelineQueryKey,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import {
  type GetAlertGroupTimelineParams,
  TimelineKind,
  type TimelineActor,
  type TimelineEntry,
  type Transport,
  type UserRef,
} from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { reasonLabel } from "./integration-alerts";
import { templateName } from "./template-editor";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { cn } from "./ui/utils";

const KINDS = Object.values(TimelineKind);

export function kindLabel(t: TFunction, kind: TimelineKind): string {
  switch (kind) {
    case "status":
      return t("timeline.kinds.status");
    case "alerts":
      return t("timeline.kinds.alerts");
    case "notes":
      return t("timeline.kinds.notes");
    case "timers":
      return t("timeline.kinds.timers");
    case "delivery":
      return t("timeline.kinds.delivery");
    default:
      return t("timeline.kinds.system");
  }
}

function transportLabel(t: TFunction, transport: Transport): string {
  switch (transport) {
    case "ui":
      return t("timeline.transport.ui");
    case "api":
      return t("timeline.transport.api");
    case "mattermost":
      return t("timeline.transport.mattermost");
    case "telegram":
      return t("timeline.transport.telegram");
    case "cli":
      return t("timeline.transport.cli");
    default:
      return t("timeline.transport.system");
  }
}

function userName(t: TFunction, user: UserRef | undefined): string {
  if (user === undefined) {
    return t("timeline.nobody");
  }
  return user.deactivated ? t("alertGroups.deactivated", { name: user.name }) : user.name;
}

/** Who caused an entry and how: "Muster", or "Alice via Mattermost", with the token an API call came through. */
export function actorText(t: TFunction, actor: TimelineActor): string {
  if (actor.kind === "system") {
    return t("timeline.muster");
  }
  const name = actor.name ?? t("timeline.deletedActor");
  if (actor.transport === undefined || actor.transport === "system") {
    return name;
  }
  const transport = transportLabel(t, actor.transport);
  return actor.token_name
    ? t("timeline.actorWithToken", { actor: name, transport, token: actor.token_name })
    : t("timeline.actorVia", { actor: name, transport });
}

/** What the reason of an automatic status change says: the reason of a resolved Alert, or why an Owner left. */
function changeReason(t: TFunction, reason: string | null | undefined): string | null {
  switch (reason) {
    case null:
    case undefined:
    case "":
      return null;
    case "owner_disabled":
      return t("timeline.reasons.ownerDisabled");
    case "owner_deleted":
      return t("timeline.reasons.ownerDeleted");
    case "resolved":
    case "gone":
    case "stale":
    case "integration_deleted":
      return reasonLabel(t, reason);
    default:
      return reason;
  }
}

/** What failed in a fallback_template_used entry, from its detail "<template> template failed: <error>". */
export function fallbackFailure(
  detail: string | null | undefined,
): { template: string; error: string } | undefined {
  const m = /^([a-z_]+) template failed: ([\s\S]*)$/.exec(detail ?? "");
  return m === null ? undefined : { template: m[1] ?? "", error: m[2] ?? "" };
}

/** "Fallback template used: {template} failed — {error}" (C-12.FR-6), or the plain text without a readable detail. */
function fallbackText(t: TFunction, detail: string | null | undefined): string {
  const failure = fallbackFailure(detail);
  return failure === undefined
    ? t("timeline.system.fallbackTemplateUsed")
    : t("timeline.system.fallbackTemplateFailed", {
        template: templateName(t, failure.template),
        error: failure.error,
      });
}

/** The text of an entry, in the language of the page; times in the user's time zone through dateTime. */
export function entryText(
  t: TFunction,
  entry: TimelineEntry,
  dateTime: (iso: string) => string,
): string {
  switch (entry.kind) {
    case "status": {
      const reason = changeReason(t, entry.reason);
      switch (entry.event) {
        case "created":
          return t("timeline.events.created");
        case "urgency_raised":
          return t("timeline.events.urgencyRaised");
        case "reopened":
          return t("timeline.events.reopened");
        case "snooze_ended":
          return t("timeline.events.snoozeEnded");
        case "resolved":
          return reason === null
            ? t("timeline.events.resolved")
            : t("timeline.events.resolvedWith", { reason });
        case "acknowledged":
          return t("timeline.events.acknowledged");
        case "takeover":
          return t("timeline.events.takeover", { name: userName(t, entry.previous_owner) });
        case "unacknowledged":
          return reason === null
            ? t("timeline.events.unacknowledged")
            : t("timeline.events.unacknowledgedWith", { reason });
        case "unresolved":
          return t("timeline.events.unresolved");
        case "snoozed":
          return entry.snooze_until
            ? t("timeline.events.snoozedUntil", { until: dateTime(entry.snooze_until) })
            : t("timeline.events.snoozedNoEnd");
        case "unsnoozed":
          return t("timeline.events.unsnoozed");
        case "auto_unacknowledged":
          return t("timeline.events.autoUnacknowledged");
        default:
          return t("timeline.events.other");
      }
    }
    case "alerts": {
      const count = entry.fingerprints?.length ?? 0;
      switch (entry.event) {
        case "alerts_added":
          return t("timeline.events.alertsAdded", { count });
        case "alert_replaced":
          return t("timeline.events.alertReplaced");
        case "alert_resolved":
          return t("timeline.events.alertResolved", { count });
        case "alert_continued":
          return t("timeline.events.alertContinued", { count });
        case "annotations_changed":
          return t("timeline.events.annotationsChanged");
        case "severity_raised":
          return t("timeline.events.severityRaised");
        default:
          return t("timeline.events.other");
      }
    }
    case "notes":
      return t("timeline.events.noteAdded");
    case "timers":
      switch (entry.event) {
        case "ack_timeout":
          return t("timeline.events.ackTimeout", { number: entry.notice_number ?? 1 });
        case "unclaimed":
          return t("timeline.events.unclaimed");
        case "reminder":
          return t("timeline.events.reminder", { number: entry.notice_number ?? 1 });
        case "reminder_answered":
          return t("timeline.events.reminderAnswered");
        case "notices_missed":
          return t("timeline.events.noticesMissed", { count: entry.missed_count ?? 0 });
        default:
          return t("timeline.events.other");
      }
    case "delivery": {
      const destination = entry.destination.name;
      switch (entry.delivery_event) {
        case "publication":
          return t("timeline.delivery.publication", { destination });
        case "possible_duplicate":
          return t("timeline.delivery.possibleDuplicate", { destination });
        case "not_delivered":
          return t("timeline.delivery.notDelivered", { destination });
        case "delivered_late":
          return t("timeline.delivery.deliveredLate", { destination });
        case "deleted_in_messenger":
          return t("timeline.delivery.deletedInMessenger", { destination });
        case "republished":
          return t("timeline.delivery.republished", { destination });
        case "thread_not_attached":
          return t("timeline.delivery.threadNotAttached", { destination });
        case "markup_rejected":
          return t("timeline.delivery.markupRejected", { destination });
        case "destination_broken":
          return t("timeline.delivery.destinationBroken", { destination });
        case "destination_recovered":
          return t("timeline.delivery.destinationRecovered", { destination });
        case "storm_summary":
          return t("timeline.delivery.stormSummary", { destination });
        case "final_edit":
          return t("timeline.delivery.finalEdit", { destination });
        default:
          return t("timeline.delivery.other", { destination });
      }
    }
    default:
      switch (entry.system_event) {
        case "moved_to_default_route":
          return t("timeline.events.movedToDefaultRoute");
        case "muster_unavailable":
          return entry.period_from && entry.period_to
            ? t("timeline.system.musterUnavailable", {
                from: dateTime(entry.period_from),
                to: dateTime(entry.period_to),
              })
            : t("timeline.system.musterUnavailableUnknown");
        case "fallback_template_used":
          return fallbackText(t, entry.detail);
        case "template_value_missing":
          return t("timeline.system.templateValueMissing");
        default:
          return t("timeline.events.other");
      }
  }
}

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-wrap gap-x-1.5 text-xs">
      <dt className="text-muted-foreground">{term}:</dt>
      <dd className="min-w-0 wrap-anywhere">{children}</dd>
    </div>
  );
}

/** The details of an entry that are not in its text. */
function EntryDetails({ entry }: { entry: TimelineEntry }) {
  const { t } = useTranslation();
  const rows: ReactNode[] = [];
  if (entry.kind === "alerts" && entry.replaced_label) {
    rows.push(
      <Detail key="replaced" term={t("timeline.details.replacedLabel")}>
        <code className="font-mono" data-testid="replaced-label">
          {entry.replaced_label}
        </code>
      </Detail>,
    );
  }
  if ((entry.kind === "status" || entry.kind === "alerts") && entry.label_conflicts?.length) {
    rows.push(
      <Detail key="conflicts" term={t("timeline.details.labelConflicts")}>
        <span className="font-mono">{entry.label_conflicts.join(", ")}</span>
      </Detail>,
    );
  }
  if (entry.kind === "status" && entry.owner !== undefined && entry.event !== "takeover") {
    rows.push(
      <Detail key="owner" term={t("timeline.details.owner")}>
        {userName(t, entry.owner)}
      </Detail>,
    );
  }
  if (entry.kind === "status" && entry.previous_owner !== undefined && entry.event !== "takeover") {
    rows.push(
      <Detail key="previous" term={t("timeline.details.previousOwner")}>
        {userName(t, entry.previous_owner)}
      </Detail>,
    );
  }
  if (entry.kind === "delivery" && entry.error) {
    rows.push(
      <Detail key="error" term={t("timeline.details.error")}>
        {entry.error}
      </Detail>,
    );
  }
  // The detail of a fallback_template_used entry is already in its text when it could be read.
  if (
    entry.kind === "system" &&
    entry.detail &&
    !(entry.system_event === "fallback_template_used" && fallbackFailure(entry.detail))
  ) {
    rows.push(
      <Detail key="detail" term={t("timeline.details.detail")}>
        <span className="font-mono">{entry.detail}</span>
      </Detail>,
    );
  }
  const fingerprints =
    entry.kind === "status" || entry.kind === "alerts" ? (entry.fingerprints ?? []) : [];
  return (
    <>
      {entry.kind === "notes" && (
        <p className="text-sm whitespace-pre-wrap wrap-anywhere" data-testid="note-body">
          {entry.note.body}
        </p>
      )}
      {rows.length > 0 && <dl className="flex flex-col gap-0.5">{rows}</dl>}
      {fingerprints.length > 0 && (
        <details className="text-xs">
          <summary className="w-fit cursor-pointer rounded-md text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring">
            {t("timeline.details.fingerprints", { count: fingerprints.length })}
          </summary>
          <ul className="mt-1 flex flex-col gap-0.5">
            {fingerprints.map((f) => (
              <li key={f}>
                <code className="font-mono wrap-anywhere">{f}</code>
              </li>
            ))}
          </ul>
        </details>
      )}
    </>
  );
}

/** The loudness and Mentions an entry carries; a system entry has them only when it records a lifecycle event. */
function soundOf(entry: TimelineEntry): { loud: boolean; mentions: string[] } {
  if (entry.kind === "system") {
    return { loud: entry.loudness === "loud", mentions: entry.mentions ?? [] };
  }
  return { loud: entry.loudness === "loud", mentions: entry.mentions };
}

export function TimelineItem({ entry }: { entry: TimelineEntry }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const { loud, mentions } = soundOf(entry);
  return (
    <li
      className="flex min-w-0 flex-col gap-1 border-l-2 py-2 pl-3"
      data-testid="timeline-entry"
      data-kind={entry.kind}
    >
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
        <time dateTime={entry.at} className="whitespace-nowrap">
          {dateTime(entry.at)}
        </time>
        <span className="wrap-anywhere" data-testid="timeline-actor">
          {actorText(t, entry.actor)}
        </span>
      </div>
      <p className="text-sm font-medium wrap-anywhere" data-testid="timeline-text">
        {entryText(t, entry, dateTime)}
      </p>
      {(loud || mentions.length > 0) && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs">
          {loud && (
            <span
              className="inline-flex items-center gap-1 font-medium text-amber-800 dark:text-amber-300"
              data-testid="timeline-loud"
            >
              <BellRingIcon aria-hidden="true" className="size-3.5" />
              {t("timeline.loud")}
            </span>
          )}
          {mentions.length > 0 && (
            <span data-testid="timeline-mentions">
              {t("timeline.mentions", { mentions: mentions.join(", ") })}
            </span>
          )}
        </div>
      )}
      <EntryDetails entry={entry} />
    </li>
  );
}

export function Timeline({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const id = useId();
  const [order, setOrder] = useState<"desc" | "asc">("desc");
  const [kinds, setKinds] = useState<TimelineKind[]>([]);
  const params: GetAlertGroupTimelineParams = {
    order,
    kind: kinds.length > 0 ? kinds : undefined,
  };
  const query = useInfiniteQuery({
    queryKey: [...getGetAlertGroupTimelineQueryKey(alertGroupId, params), "pages"],
    queryFn: ({ pageParam, signal }) =>
      getAlertGroupTimeline(alertGroupId, { ...params, cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  const toggle = (kind: TimelineKind) =>
    setKinds((prev) =>
      prev.includes(kind)
        ? prev.filter((k) => k !== kind)
        : KINDS.filter((k) => k === kind || prev.includes(k)),
    );
  return (
    <Card data-testid="timeline">
      <CardHeader>
        <CardTitle>
          <h2>{t("timeline.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div
            role="group"
            aria-label={t("timeline.kindsLabel")}
            className="flex flex-wrap gap-1.5"
          >
            {KINDS.map((kind) => (
              <button
                key={kind}
                type="button"
                aria-pressed={kinds.includes(kind)}
                className={cn(
                  "rounded-full border px-2.5 py-0.5 text-xs font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  kinds.includes(kind)
                    ? "border-primary bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
                onClick={() => toggle(kind)}
              >
                {kindLabel(t, kind)}
              </button>
            ))}
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-order`}>{t("timeline.order.label")}</Label>
            <NativeSelect
              id={`${id}-order`}
              value={order}
              onChange={(e) => setOrder(e.target.value === "asc" ? "asc" : "desc")}
            >
              <NativeSelectOption value="desc">{t("timeline.order.newest")}</NativeSelectOption>
              <NativeSelectOption value="asc">{t("timeline.order.oldest")}</NativeSelectOption>
            </NativeSelect>
          </div>
        </div>
        {items.length === 0 ? (
          <p className="py-4 text-sm text-muted-foreground" role="status">
            {query.isPending
              ? t("common.loading")
              : query.error
                ? problemText(t, query.error)
                : t("timeline.empty")}
          </p>
        ) : (
          <ol className="flex flex-col gap-1" aria-label={t("timeline.title")}>
            {items.map((entry) => (
              <TimelineItem key={entry.id} entry={entry} />
            ))}
          </ol>
        )}
        {query.hasNextPage && (
          <div>
            <Button
              variant="outline"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? t("common.loading") : t("table.loadMore")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
