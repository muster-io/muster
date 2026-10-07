// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warnings of an Integration (C-06.FR-18): the truncation and long-interval warnings as banners on the Integration
// page, and as a mark in the Integrations list with the same texts on hover and in the row's details. The Heartbeat
// kinds (C-07.FR-5) are a banner of their own at the top of the Integration page; in the list the Heartbeat badge
// stands for them, so the mark leaves them out.

import type { TFunction } from "i18next";
import { HeartCrackIcon, InfoIcon, TriangleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { IntegrationWarning } from "../api/gen/model";
import { dayIn, formatDuration, formatTime, useTimeFormat } from "../lib/time";

/** processing.stale_after_factor: an Alert resolves by absence after this many learned repeat intervals. */
export const STALE_AFTER_FACTOR = 3;

/** The text of a long learned repeat interval of a route, with the time to resolve by absence that follows from it. */
export function longIntervalText(
  t: TFunction,
  route: string,
  intervalSeconds: number,
  absenceSeconds: number = STALE_AFTER_FACTOR * intervalSeconds,
): string {
  return t("integrationWarnings.longRepeatInterval", {
    route,
    interval: formatDuration(t, intervalSeconds),
    absence: formatDuration(t, absenceSeconds),
  });
}

/** The text of a warning, or null for the kinds shown elsewhere. */
export function warningText(t: TFunction, warning: IntegrationWarning): string | null {
  switch (warning.kind) {
    case "snapshot_truncated":
      return t("integrationWarnings.snapshotTruncated", {
        count: warning.truncated_group_count ?? 1,
      });
    case "long_repeat_interval":
      return longIntervalText(t, warning.route_path ?? "", warning.repeat_interval_seconds ?? 0);
    default:
      // heartbeat_not_configured, heartbeat_waiting and heartbeat_lost have a banner of their own.
      return null;
  }
}

/**
 * The time a lost Heartbeat is shown since: "HH:MM" in the time zone, with the date in front when that is not today
 * there, and the year when that is not this year.
 */
export function lostSinceText(
  since: string,
  timeZone: string,
  locale: string,
  now: Date = new Date(),
): string {
  if (dayIn(new Date(since), timeZone) === dayIn(now, timeZone)) {
    return formatTime(since, timeZone, locale);
  }
  const year = (d: Date) => dayIn(d, timeZone).slice(0, 4);
  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    ...(year(new Date(since)) === year(now) ? {} : { year: "numeric" }),
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone,
  }).format(new Date(since));
}

/** The Heartbeat warning of an Integration, if it has one; the built-in Integration has none. */
function heartbeatWarning(warnings: readonly IntegrationWarning[]): IntegrationWarning | undefined {
  return warnings.find(
    (w) =>
      w.kind === "heartbeat_not_configured" ||
      w.kind === "heartbeat_waiting" ||
      w.kind === "heartbeat_lost",
  );
}

/**
 * The Heartbeat banner of the Integration page (C-07.FR-5): without a Heartbeat, before its first signal, or since when
 * Muster has had no contact, in the user's time zone. The region is live, so a change that a live hint brings is read
 * out.
 */
export function HeartbeatBanner({ warnings }: { warnings: readonly IntegrationWarning[] }) {
  const { t, i18n } = useTranslation();
  const { timeZone } = useTimeFormat();
  const warning = heartbeatWarning(warnings);
  let banner: ReactNode = null;
  if (warning?.kind === "heartbeat_lost") {
    banner = (
      <p
        className="flex items-start gap-2 rounded-lg border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm break-words text-foreground dark:bg-destructive/20"
        data-testid="heartbeat-banner"
        data-kind={warning.kind}
      >
        <HeartCrackIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
        <span className="min-w-0">
          {warning.since
            ? t("heartbeat.banner.lost", {
                time: lostSinceText(warning.since, timeZone, i18n.resolvedLanguage ?? "en"),
              })
            : t("heartbeat.banner.lostNoTime")}
        </span>
      </p>
    );
  } else if (warning !== undefined) {
    const waiting = warning.kind === "heartbeat_waiting";
    const Icon = waiting ? InfoIcon : TriangleAlertIcon;
    banner = (
      <p
        className={
          waiting
            ? "flex items-start gap-2 rounded-lg border bg-muted/50 px-3 py-2 text-sm break-words text-foreground"
            : "flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm break-words text-foreground"
        }
        data-testid="heartbeat-banner"
        data-kind={warning.kind}
      >
        <Icon
          aria-hidden="true"
          className={
            waiting
              ? "mt-0.5 size-4 shrink-0 text-muted-foreground"
              : "mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          }
        />
        <span className="min-w-0">
          {waiting ? t("heartbeat.banner.waiting") : t("heartbeat.banner.notConfigured")}
        </span>
      </p>
    );
  }
  return (
    <div role="status" aria-live="polite" className="empty:hidden">
      {banner}
    </div>
  );
}

/** The texts of the warnings this component shows, in the order the API lists them. */
function texts(t: TFunction, warnings: readonly IntegrationWarning[]): string[] {
  return warnings.map((w) => warningText(t, w)).filter((text) => text !== null);
}

/** The warnings of an Integration as banners; nothing when it has none. */
export function IntegrationWarnings({ warnings }: { warnings: readonly IntegrationWarning[] }) {
  const { t } = useTranslation();
  const shown = texts(t, warnings);
  if (shown.length === 0) {
    return null;
  }
  return (
    <ul
      className="flex flex-col gap-2 lg:col-span-2"
      aria-label={t("integrationWarnings.label")}
      data-testid="integration-warnings"
    >
      {shown.map((text) => (
        <li
          key={text}
          className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm break-words text-foreground"
          data-testid="integration-warning"
        >
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <span className="min-w-0">{text}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The mark of an Integration with warnings in the Integrations list: the texts show on hover and, opened, in the
 * row's details.
 */
export function WarningMark({ warnings }: { warnings: readonly IntegrationWarning[] }) {
  const { t } = useTranslation();
  const shown = texts(t, warnings);
  if (shown.length === 0) {
    return null;
  }
  return (
    <details className="group mt-1" data-testid="integration-warning-mark">
      <summary
        title={shown.join("\n")}
        className="inline-flex cursor-pointer list-none items-center gap-1 rounded-md text-xs text-amber-800 outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-warning [&::-webkit-details-marker]:hidden"
      >
        <TriangleAlertIcon aria-hidden="true" className="size-4 shrink-0" />
        {t("integrationWarnings.mark", { count: shown.length })}
      </summary>
      <ul className="mt-1 flex flex-col gap-1 text-xs break-words text-muted-foreground">
        {shown.map((text) => (
          <li key={text}>{text}</li>
        ))}
      </ul>
    </details>
  );
}
