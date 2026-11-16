// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Wall-clock times of a Schedule's time zone (C-27.FR-2, FR-4): the two weeks the Schedule page shows, as the days of
// the zone with their real lengths — a day of a change of the clocks has 23 or 25 hours — and where an instant lies on
// them, the way a time is written for the person ("Wed 11:00"), and the two-way conversion of the time fields of the
// Override form. The arithmetic is date-fns 4 with @date-fns/tz; a calendar day is a YYYY-MM-DD string.

import { TZDate } from "@date-fns/tz";

import { dayIn, startOfDayIn } from "./time";

/** The days of the view: two weeks. */
export const VIEW_DAYS = 14;

/** A calendar day moved by days, as YYYY-MM-DD; calendar arithmetic, so it has no clocks to change. */
export function shiftDay(day: string, days: number): string {
  const [year = 1970, month = 1, date = 1] = day.split("-").map(Number);
  return new Date(Date.UTC(year, month - 1, date + days)).toISOString().slice(0, 10);
}

/** The Monday of the week that holds the instant, in the zone, as YYYY-MM-DD. */
export function mondayOf(instant: Date, zone: string): string {
  const day = dayIn(instant, zone);
  const [year = 1970, month = 1, date = 1] = day.split("-").map(Number);
  const weekday = new Date(Date.UTC(year, month - 1, date)).getUTCDay();
  return shiftDay(day, -((weekday + 6) % 7));
}

export interface ViewDay {
  /** YYYY-MM-DD. */
  day: string;
  start: Date;
  end: Date;
}

export interface ViewRange {
  start: Date;
  end: Date;
  days: ViewDay[];
}

/** The two weeks from the Monday of the week of now, moved by whole weeks; the days start at midnight of the zone. */
export function viewRange(now: Date, zone: string, weekOffset = 0): ViewRange {
  const first = shiftDay(mondayOf(now, zone), weekOffset * 7);
  const days: ViewDay[] = [];
  for (let i = 0; i < VIEW_DAYS; i++) {
    const day = shiftDay(first, i);
    // startOfDayIn answers a TZDate, whose ISO string carries the zone's offset: the API takes plain instants.
    days.push({
      day,
      start: new Date(startOfDayIn(day, zone).getTime()),
      end: new Date(startOfDayIn(shiftDay(day, 1), zone).getTime()),
    });
  }
  return { start: days[0]?.start ?? now, end: days.at(-1)?.end ?? now, days };
}

/** Where an instant lies on the range, from 0 to 1; one outside it is held at the nearest end. */
export function fractionOf(
  instant: Date | string,
  range: Pick<ViewRange, "start" | "end">,
): number {
  const at = typeof instant === "string" ? Date.parse(instant) : instant.getTime();
  const from = range.start.getTime();
  const length = range.end.getTime() - from;
  return Math.min(1, Math.max(0, (at - from) / length));
}

/** Whether the instant is inside the range. */
export function within(instant: Date, range: Pick<ViewRange, "start" | "end">): boolean {
  return instant >= range.start && instant < range.end;
}

/** "HH:MM" on a 24-hour clock in the zone. */
export function clock(instant: Date | string, zone: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: zone,
  }).format(typeof instant === "string" ? new Date(instant) : instant);
}

/** "Wed 11:00": the short weekday and the time in the zone. */
export function stamp(instant: Date | string, zone: string, locale: string): string {
  const at = typeof instant === "string" ? new Date(instant) : instant;
  const weekday = new Intl.DateTimeFormat(locale, { weekday: "short", timeZone: zone }).format(at);
  return `${weekday} ${clock(at, zone, locale)}`;
}

/** The time alone when the instant is on the day of the reference, else the weekday with it, as "until 11:00" reads. */
export function stampNear(
  instant: Date | string,
  reference: Date | string,
  zone: string,
  locale: string,
): string {
  const at = typeof instant === "string" ? new Date(instant) : instant;
  const near = typeof reference === "string" ? new Date(reference) : reference;
  return dayIn(at, zone) === dayIn(near, zone) ? clock(at, zone, locale) : stamp(at, zone, locale);
}

/** "Wed 11:00 – 19:00" within a day, "Wed 17:00 – Thu 09:00" across midnight. */
export function span(from: Date | string, to: Date | string, zone: string, locale: string): string {
  const start = typeof from === "string" ? new Date(from) : from;
  const end = typeof to === "string" ? new Date(to) : to;
  const finish =
    dayIn(start, zone) === dayIn(end, zone) ? clock(end, zone, locale) : stamp(end, zone, locale);
  return `${stamp(start, zone, locale)} – ${finish}`;
}

/** The heading of a day column, such as "Mon 17". */
export function dayHeading(day: string, locale: string): string {
  const [year = 1970, month = 1, date = 1] = day.split("-").map(Number);
  const at = new Date(Date.UTC(year, month - 1, date, 12));
  const weekday = new Intl.DateTimeFormat(locale, { weekday: "short", timeZone: "UTC" }).format(at);
  return `${weekday} ${date}`;
}

/** The value of a `datetime-local` field for an instant: the wall clock of the zone, to the minute. */
export function toLocalInput(instant: Date, zone: string): string {
  const day = dayIn(instant, zone);
  const parts = new Intl.DateTimeFormat("en-GB", {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: zone,
  }).format(instant);
  return `${day}T${parts}`;
}

/** The instant a `datetime-local` value names on the wall clock of the zone; null when it is no date. */
export function fromLocalInput(value: string, zone: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (match === null) {
    return null;
  }
  const [, year = "", month = "", day = "", hour = "", minute = ""] = match;
  const at = new TZDate(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hour),
    Number(minute),
    0,
    zone,
  );
  const real =
    at.getFullYear() === Number(year) &&
    at.getMonth() === Number(month) - 1 &&
    at.getDate() === Number(day) &&
    at.getHours() === Number(hour) &&
    at.getMinutes() === Number(minute);
  if (!real) {
    return null;
  }
  // A wall-clock time that happens twice, when the clocks move back, is its first occurrence (RFC 5545), as the API
  // reads it: the TZDate has the second.
  for (const back of [2 * 60, 60, 30]) {
    const earlier = new Date(at.getTime() - back * 60_000);
    if (toLocalInput(earlier, zone) === value) {
      return earlier;
    }
  }
  return new Date(at.getTime());
}
