// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The wall-clock helpers of the Schedule page (C-27.FR-2): the two weeks of the view by the days of the zone, across a
// change of the clocks, where an instant lies on them, and the conversion of the time fields of the Override form.

import { describe, expect, test } from "vitest";

import {
  VIEW_DAYS,
  clock,
  dayHeading,
  fractionOf,
  fromLocalInput,
  mondayOf,
  shiftDay,
  span,
  stamp,
  stampNear,
  toLocalInput,
  viewRange,
} from "./schedule-time";

const BERLIN = "Europe/Berlin";

/** The instant of a wall-clock time in Berlin. */
function instant(local: string): Date {
  return fromLocalInput(local, BERLIN) ?? new Date(Number.NaN);
}

describe("the days of the view", () => {
  test("starts on the Monday of the week of now and holds two weeks of days of the zone", () => {
    // Monday 2026-11-16 09:30 UTC is 10:30 in Berlin.
    const range = viewRange(new Date("2026-11-16T09:30:00Z"), BERLIN);
    expect(range.days).toHaveLength(VIEW_DAYS);
    expect(range.days[0]?.day).toBe("2026-11-16");
    expect(range.days.at(-1)?.day).toBe("2026-11-29");
    expect(range.start.toISOString()).toBe("2026-11-15T23:00:00.000Z");
    expect(range.end.toISOString()).toBe("2026-11-29T23:00:00.000Z");
  });

  test("takes the Monday of the zone, not of UTC, when the zone is already in the next day", () => {
    // Sunday 23:30 UTC is Monday 00:30 in Berlin.
    expect(mondayOf(new Date("2026-11-15T23:30:00Z"), BERLIN)).toBe("2026-11-16");
    expect(mondayOf(new Date("2026-11-15T23:30:00Z"), "UTC")).toBe("2026-11-09");
  });

  test("moves by whole weeks", () => {
    const now = new Date("2026-11-16T09:30:00Z");
    expect(viewRange(now, BERLIN, 1).days[0]?.day).toBe("2026-11-23");
    expect(viewRange(now, BERLIN, -1).days[0]?.day).toBe("2026-11-09");
  });

  test("has a day of 23 hours and one of 25 at a change of the clocks", () => {
    // Europe/Berlin moves its clocks on Sunday 2026-03-29 (forward) and 2026-10-25 (back).
    const spring = viewRange(new Date("2026-03-25T12:00:00Z"), BERLIN);
    const forward = spring.days.find((d) => d.day === "2026-03-29");
    expect((forward?.end.getTime() ?? 0) - (forward?.start.getTime() ?? 0)).toBe(23 * 3_600_000);
    const autumn = viewRange(new Date("2026-10-21T12:00:00Z"), BERLIN);
    const back = autumn.days.find((d) => d.day === "2026-10-25");
    expect((back?.end.getTime() ?? 0) - (back?.start.getTime() ?? 0)).toBe(25 * 3_600_000);
  });

  test("keeps a handover at 10:00 at 10:00 across the change of the clocks", () => {
    const before = instant("2026-03-23T10:00");
    const after = instant("2026-03-30T10:00");
    expect(clock(before, BERLIN, "en")).toBe("10:00");
    expect(clock(after, BERLIN, "en")).toBe("10:00");
    // One hour fewer between them than a plain week.
    expect(after.getTime() - before.getTime()).toBe(7 * 24 * 3_600_000 - 3_600_000);
  });

  test("shifts a calendar day over months and years", () => {
    expect(shiftDay("2026-12-30", 3)).toBe("2027-01-02");
    expect(shiftDay("2026-03-01", -1)).toBe("2026-02-28");
  });
});

describe("where an instant lies", () => {
  const range = viewRange(new Date("2026-11-16T09:30:00Z"), BERLIN);

  test("is a fraction of the range by real time", () => {
    expect(fractionOf(range.start, range)).toBe(0);
    expect(fractionOf(range.end, range)).toBe(1);
    // Monday 10:30 Berlin is 10.5 hours into 14 days.
    expect(fractionOf("2026-11-16T09:30:00Z", range)).toBeCloseTo(10.5 / (14 * 24), 6);
  });

  test("holds an instant outside the range at its nearest end", () => {
    expect(fractionOf("2026-01-01T00:00:00Z", range)).toBe(0);
    expect(fractionOf("2027-01-01T00:00:00Z", range)).toBe(1);
  });
});

describe("the way a time is written", () => {
  test("writes the weekday and the time of the zone", () => {
    expect(stamp("2026-11-18T10:00:00Z", BERLIN, "en")).toBe("Wed 11:00");
    expect(stamp("2026-11-18T10:00:00Z", BERLIN, "ru")).toMatch(/^ср\.? 11:00$/);
  });

  test("leaves out the weekday of a time on the day of the reference", () => {
    const now = "2026-11-16T09:30:00Z";
    expect(stampNear("2026-11-16T10:00:00Z", now, BERLIN, "en")).toBe("11:00");
    expect(stampNear("2026-11-17T10:00:00Z", now, BERLIN, "en")).toBe("Tue 11:00");
  });

  test("writes a span within a day and across midnight", () => {
    expect(span("2026-11-18T10:00:00Z", "2026-11-18T18:00:00Z", BERLIN, "en")).toBe(
      "Wed 11:00 – 19:00",
    );
    expect(span("2026-11-18T16:00:00Z", "2026-11-19T08:00:00Z", BERLIN, "en")).toBe(
      "Wed 17:00 – Thu 09:00",
    );
  });

  test("heads a day column with its weekday and date", () => {
    expect(dayHeading("2026-11-16", "en")).toBe("Mon 16");
  });
});

describe("the time fields of the Override form", () => {
  test("writes the wall clock of the zone and reads it back", () => {
    const at = new Date("2026-11-18T10:00:00Z");
    expect(toLocalInput(at, BERLIN)).toBe("2026-11-18T11:00");
    expect(fromLocalInput("2026-11-18T11:00", BERLIN)?.toISOString()).toBe(at.toISOString());
  });

  test("reads a time the clocks skip as nothing, and a time that happens twice as its first occurrence", () => {
    // Berlin moves 02:00 to 03:00 on 2026-03-29, and 03:00 back to 02:00 on 2026-10-25.
    expect(fromLocalInput("2026-03-29T02:30", BERLIN)).toBeNull();
    expect(fromLocalInput("2026-10-25T02:30", BERLIN)?.toISOString()).toBe(
      "2026-10-25T00:30:00.000Z",
    );
  });

  test("reads nothing from text that is no date", () => {
    expect(fromLocalInput("", BERLIN)).toBeNull();
    expect(fromLocalInput("2026-13-45T99:99", BERLIN)).toBeNull();
  });
});
