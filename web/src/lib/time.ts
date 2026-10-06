// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Times are shown in the time zone of the profile, or the browser's when the profile names none (NFR-9). The API
// sends RFC 3339 UTC; Intl formats it.

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

/** The formatters for the signed-in user's time zone and language. */
export function useTimeFormat(): {
  timeZone: string;
  time: (iso: string) => string;
  dateTime: (iso: string) => string;
} {
  const { i18n } = useTranslation();
  const session = useSession();
  const timeZone = effectiveTimeZone(session?.user.time_zone);
  const locale = i18n.resolvedLanguage ?? "en";
  return {
    timeZone,
    time: (iso) => formatTime(iso, timeZone, locale),
    dateTime: (iso) => formatDateTime(iso, timeZone, locale),
  };
}
