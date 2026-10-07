// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Durations as the Alert Group pages show them (C-09.FR-17): relative, such as "2 h 14 min" or "3 d 4 h", with the
// absolute times in the user's time zone on hover. A duration still running follows one shared clock that ticks every
// half minute: the browser's, which in development mode does not follow the development clock, so a running duration
// there counts from the real time.

import type { TFunction } from "i18next";
import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

import { formatDuration, useTimeFormat } from "../lib/time";
import { cn } from "./ui/utils";

const TICK_MS = 30_000;
const DAY_SECONDS = 24 * 60 * 60;

let now = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const subscribers = new Set<() => void>();

function subscribe(callback: () => void): () => void {
  subscribers.add(callback);
  if (timer === undefined) {
    now = Date.now();
    timer = setInterval(() => {
      now = Date.now();
      for (const s of subscribers) {
        s();
      }
    }, TICK_MS);
  }
  return () => {
    subscribers.delete(callback);
    if (subscribers.size === 0) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

/** The current time in milliseconds, renewed every half minute while a component shows it. */
export function useNow(): number {
  return useSyncExternalStore(subscribe, () => now);
}

/**
 * A duration in whole seconds: below a day as the other pages show intervals ("45 s", "5 min", "2 h 14 min"), from a
 * day on in days and hours ("3 d", "3 d 4 h").
 */
export function formatElapsed(t: TFunction, seconds: number): string {
  if (seconds < DAY_SECONDS) {
    return formatDuration(t, seconds);
  }
  const hours = Math.floor(seconds / 3600);
  const days = Math.floor(hours / 24);
  const rest = hours % 24;
  return rest === 0
    ? t("duration.days", { value: days })
    : t("duration.daysHours", { days, hours: rest });
}

/** The seconds from start to end, or to now while it runs. */
export function elapsedSeconds(start: string, end: string | null | undefined, at: number): number {
  const until = end ? Date.parse(end) : at;
  return Math.max(0, Math.round((until - Date.parse(start)) / 1000));
}

/** How long something lasted or has lasted, with its absolute start (and end) on hover. */
export function Duration({
  start,
  end,
  className,
}: {
  start: string;
  end?: string | null;
  className?: string;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const at = useNow();
  const seconds = elapsedSeconds(start, end, at);
  const absolute = end
    ? t("duration.between", { start: dateTime(start), end: dateTime(end) })
    : t("duration.since", { start: dateTime(start) });
  return (
    <time
      dateTime={`PT${seconds}S`}
      title={absolute}
      className={cn("whitespace-nowrap", className)}
      data-testid="duration"
    >
      {formatElapsed(t, seconds)}
    </time>
  );
}

/** A time in the user's time zone. */
export function DateTime({ iso, className }: { iso: string; className?: string }) {
  const { dateTime } = useTimeFormat();
  return (
    <time dateTime={iso} className={cn("whitespace-nowrap", className)}>
      {dateTime(iso)}
    </time>
  );
}
