// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Times are shown in the time zone of the profile, or the browser's when the profile names none (NFR-9). The API
// sends RFC 3339 UTC; Intl formats it, and date-fns with @date-fns/tz does the arithmetic in a time zone.

import { TZDate } from "@date-fns/tz";
import { addDays } from "date-fns";
import { useTranslation } from "react-i18next";

import { useSession } from "./api";

/** The time zone of the browser, such as "Europe/Moscow". */
export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone;
}

/** Every IANA time zone the browser knows, for the profile's choice. */
export function timeZones(): string[] {
  return Intl.supportedValuesOf("timeZone");
}

/** The time zone to show times in: the profile's when it names a known one, else the browser's. */
export function effectiveTimeZone(profileTimeZone: string | null | undefined): string {
  if (profileTimeZone) {
    try {
      return new Intl.DateTimeFormat("en", { timeZone: profileTimeZone }).resolvedOptions()
        .timeZone;
    } catch {
      // An unknown zone falls back to the browser's.
    }
  }
  return browserTimeZone();
}

/** "HH:MM" on a 24-hour clock in the time zone, as the banners show it. */
export function formatTime(iso: string, timeZone: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone,
  }).format(new Date(iso));
}

/** A date with its time in the time zone, for lists such as the sessions of the profile. */
export function formatDateTime(iso: string, timeZone: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
    timeZone,
  }).format(new Date(iso));
}

/** A calendar date as the API sends it (YYYY-MM-DD), such as "Oct 16, 2026"; it names a day, not an instant. */
export function formatDate(date: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeZone: "UTC" }).format(
    new Date(`${date}T00:00:00Z`),
  );
}

/** The day of an instant in the time zone, as YYYY-MM-DD (the Canadian English date format is ISO 8601). */
export function dayIn(instant: Date, timeZone: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    timeZone,
  }).format(instant);
}

/** The instant a day (YYYY-MM-DD) starts in the time zone; days later with offset. */
export function startOfDayIn(day: string, timeZone: string, offset = 0): Date {
  const [year = 1970, month = 1, date = 1] = day.split("-").map(Number);
  return addDays(new TZDate(year, month - 1, date, timeZone), offset);
}

/** The formatters for the signed-in user's time zone and language. */
export function useTimeFormat(): {
  timeZone: string;
  time: (iso: string) => string;
  dateTime: (iso: string) => string;
  date: (day: string) => string;
} {
  const { i18n } = useTranslation();
  const session = useSession();
  const timeZone = effectiveTimeZone(session?.user.time_zone);
  const locale = i18n.resolvedLanguage ?? "en";
  return {
    timeZone,
    time: (iso) => formatTime(iso, timeZone, locale),
    dateTime: (iso) => formatDateTime(iso, timeZone, locale),
    date: (day) => formatDate(day, locale),
  };
}
