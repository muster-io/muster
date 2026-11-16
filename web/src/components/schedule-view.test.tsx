// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The two-week view of a Schedule in a real browser (C-27.FR-4, FR-5, FR-7, C-27.AC-9): a row per Rotation layer, top
// first, a row of Overrides and the row "Result — who is On-call", gaps in red, the Shifts of a disabled person hatched,
// a mark for now, "Previous week", "Today" and "Next week", the segments of the Result as buttons that name the person
// and the time, in English and in Russian.

import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { OnCallSegment, ScheduleOnCall } from "../api/gen/model";
import i18n from "../i18n";
import { viewRange } from "../lib/schedule-time";
import {
  OLGA,
  PETR,
  SERGEY,
  layerShifts,
  onCall,
  scheduleOverride,
  segment,
  shift,
  hasText,
} from "../test/fixtures/schedules";
import { ScheduleView } from "./schedule-view";

const ZONE = "Europe/Berlin";
// Monday 2026-11-16 10:30 in Berlin.
const NOW = new Date("2026-11-16T09:30:00Z");
const RANGE = viewRange(NOW, ZONE);

const DATA: ScheduleOnCall = onCall({
  from: RANGE.start.toISOString(),
  to: RANGE.end.toISOString(),
  rotation_layers: [
    layerShifts("Around the clock", [
      shift("2026-11-16T09:00:00Z", "2026-11-23T09:00:00Z", PETR),
      shift("2026-11-23T09:00:00Z", "2026-11-30T09:00:00Z", SERGEY, "disabled"),
    ]),
    layerShifts(
      "Working hours 11-19",
      [shift("2026-11-18T10:00:00Z", "2026-11-18T18:00:00Z", OLGA)],
      "RL0000000000BB",
    ),
  ],
  overrides: [scheduleOverride()],
  segments: [
    segment("2026-11-15T23:00:00Z", "2026-11-18T10:00:00Z", PETR),
    segment("2026-11-18T10:00:00Z", "2026-11-18T18:00:00Z", PETR, null),
    segment("2026-11-18T18:00:00Z", "2026-11-23T09:00:00Z", PETR),
    segment("2026-11-23T09:00:00Z", "2026-11-23T10:00:00Z", null),
    segment("2026-11-23T10:00:00Z", "2026-11-29T23:00:00Z", OLGA),
  ],
});

interface Calls {
  previous: number;
  today: number;
  next: number;
  picked: OnCallSegment[];
}

async function renderView(
  options: { pick?: boolean; now?: Date | null; picked?: OnCallSegment } = {},
) {
  const calls: Calls = { previous: 0, today: 0, next: 0, picked: [] };
  const screen = await render(
    <I18nextProvider i18n={i18n}>
      <ScheduleView
        zone={ZONE}
        data={DATA}
        range={RANGE}
        now={options.now === undefined ? NOW : options.now}
        onPrevious={() => (calls.previous += 1)}
        onToday={() => (calls.today += 1)}
        onNext={() => (calls.next += 1)}
        {...(options.pick === true ? { onPick: (s: OnCallSegment) => calls.picked.push(s) } : {})}
        picked={options.picked ?? null}
      />
    </I18nextProvider>,
  );
  return { screen, calls };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the rows of the view", () => {
  test("lists the Rotation layers top first, then Overrides, then the Result", async () => {
    await renderView();
    const labels = page.getByTestId("schedule-row-label");
    await hasText(labels.nth(0), "Working hours 11-19");
    await hasText(labels.nth(1), "Around the clock");
    await expect.element(page.getByText("Overrides", { exact: true })).toBeVisible();
    await expect.element(page.getByText("Result — who is On-call")).toBeVisible();
  });

  test("shows the Shifts of each layer by person", async () => {
    await renderView();
    await expect.element(page.getByRole("img", { name: "@olga, Wed 11:00 – 19:00" })).toBeVisible();
    await expect
      .element(page.getByRole("img", { name: "@petr, Mon 10:00 – Mon 10:00" }))
      .toBeVisible();
  });

  test("shows an Override with the person it replaces", async () => {
    await renderView();
    await hasText(page.getByTestId("schedule-override-block"), "@olga → @petr");
  });

  test("draws a gap in red and names it", async () => {
    await renderView();
    const gap = page.getByTestId("schedule-segment").nth(3);
    await expect.element(gap).toHaveAttribute("data-gap", "true");
    await hasText(gap, "Gap");
    await expect.element(page.getByRole("img", { name: "Gap, Mon 10:00 – 11:00" })).toBeVisible();
    const border = getComputedStyle(gap.element()).borderTopColor;
    expect(border).not.toBe(
      getComputedStyle(page.getByTestId("schedule-segment").nth(0).element()).borderTopColor,
    );
  });

  test("hatches the Shift of a person who may not be Paged", async () => {
    await renderView();
    const skipped = page.getByTestId("schedule-shift").nth(2);
    await expect.element(skipped).toHaveAttribute("data-skipped", "disabled");
    expect(getComputedStyle(skipped.element()).backgroundImage).toContain(
      "repeating-linear-gradient",
    );
    const regular = page.getByTestId("schedule-shift").nth(1);
    expect(getComputedStyle(regular.element()).backgroundImage).toBe("none");
  });
});

describe("the mark for now", () => {
  test("stands on Monday 10:30, a little into the first day", async () => {
    await renderView();
    const mark = page.getByTestId("schedule-now");
    await expect.element(mark).toBeVisible();
    await hasText(mark, "Now");
  });

  test("is not drawn when now is outside the two weeks, or unknown", async () => {
    await renderView({ now: new Date("2026-12-24T09:30:00Z") });
    await expect.element(page.getByTestId("schedule-now")).not.toBeInTheDocument();
  });

  test("is not drawn while the clock is not known", async () => {
    await renderView({ now: null });
    await expect.element(page.getByTestId("schedule-now")).not.toBeInTheDocument();
  });
});

describe("moving the view", () => {
  test("has Previous week, Today and Next week", async () => {
    const { calls } = await renderView();
    await userEvent.click(page.getByRole("button", { name: "Previous week" }));
    await userEvent.click(page.getByRole("button", { name: "Today" }));
    await userEvent.click(page.getByRole("button", { name: "Next week" }));
    expect(calls).toMatchObject({ previous: 1, today: 1, next: 1 });
  });

  test("heads each day of the two weeks", async () => {
    await renderView();
    const days = page.getByTestId("schedule-days").element().children;
    expect(days).toHaveLength(14);
    expect(days[0]?.textContent).toBe("Mon 16");
    expect(days[13]?.textContent).toBe("Sun 29");
  });
});

describe("the segments of the Result", () => {
  test("are not buttons for a person who cannot make an Override", async () => {
    await renderView();
    await expect.element(page.getByRole("button", { name: /^@olga/ })).not.toBeInTheDocument();
  });

  test("are buttons with the person and the time as their name, which the form takes", async () => {
    const { calls } = await renderView({ pick: true });
    await userEvent.click(page.getByRole("button", { name: "@olga, Mon 11:00 – Mon 00:00" }));
    expect(calls.picked.map((s) => s.user?.login)).toEqual(["olga"]);
  });

  test("are not buttons once they have ended: an Override may not rewrite the past", async () => {
    // Wednesday 2026-11-18 20:00 Berlin: the segments up to Wednesday 19:00 are the past.
    await renderView({ pick: true, now: new Date("2026-11-18T19:00:00Z") });
    await expect
      .element(page.getByRole("button", { name: /^@petr, Mon 00:00 – Wed 11:00/ }))
      .not.toBeInTheDocument();
    await expect
      .element(page.getByRole("img", { name: /^@petr, Mon 00:00 – Wed 11:00/ }))
      .toBeVisible();
    await expect
      .element(page.getByRole("button", { name: "@olga, Mon 11:00 – Mon 00:00" }))
      .toBeVisible();
  });

  test("can be reached and pressed with the keyboard, and name a gap", async () => {
    const { calls } = await renderView({ pick: true });
    const gap = page.getByRole("button", { name: "Gap, Mon 10:00 – 11:00" });
    gap.element().focus();
    await userEvent.keyboard("{Enter}");
    expect(calls.picked).toHaveLength(1);
    expect(calls.picked[0]?.user).toBeNull();
  });

  test("mark the picked one", async () => {
    const picked = DATA.segments[4];
    await renderView({ pick: true, ...(picked === undefined ? {} : { picked }) });
    await expect
      .element(page.getByRole("button", { name: "@olga, Mon 11:00 – Mon 00:00" }))
      .toHaveAttribute("aria-pressed", "true");
  });
});

describe("while the next week is read", () => {
  test("dims the view and marks it busy", async () => {
    const calls: Calls = { previous: 0, today: 0, next: 0, picked: [] };
    await render(
      <I18nextProvider i18n={i18n}>
        <ScheduleView
          zone={ZONE}
          data={DATA}
          range={RANGE}
          now={NOW}
          onPrevious={() => (calls.previous += 1)}
          onToday={() => (calls.today += 1)}
          onNext={() => (calls.next += 1)}
          loading
        />
      </I18nextProvider>,
    );
    await expect
      .element(page.getByTestId("schedule-view-scroll"))
      .toHaveAttribute("aria-busy", "true");
  });
});

describe("in Russian", () => {
  test("reads the rows and the buttons in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderView({ pick: true });
    await expect.element(page.getByText("Итог — кто дежурит")).toBeVisible();
    await expect.element(page.getByText("Замены", { exact: true })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Предыдущая неделя" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Сегодня" })).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Следующая неделя" })).toBeVisible();
    await hasText(page.getByTestId("schedule-now"), "Сейчас");
    await hasText(page.getByTestId("schedule-segment").nth(3), "Дыра");
  });
});
