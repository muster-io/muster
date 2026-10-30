// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What a button set shows, as internal/buttons/set_test.go has it: every status and set, a Route without Snooze
// durations, the labels in English and Russian, and the conversions between the API's sets and the editor's.

import { afterEach, describe, expect, test } from "vitest";

import i18n from "../i18n";
import {
  type ShownSet,
  acknowledgeItem,
  buttonDuration,
  destinationButtons,
  destinationButtonsInput,
  itemErrors,
  offeredDurations,
  offeredItems,
  orderedItems,
  organizationButtons,
  organizationButtonsInput,
  presetItem,
  previewButtons,
  previewDurations,
  previewLabel,
  resolveItem,
  shownSet,
  snoozeItem,
} from "./button-sets";

const HOUR = 3600;
const DURATIONS = [HOUR, 4 * HOUR, 24 * HOUR];
const ALL: ShownSet = { all: true };

function keys(status: "firing" | "acknowledged", set: ShownSet, durations = DURATIONS): string[] {
  return previewButtons(status, set, durations).map((b) => b.key);
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("previewButtons", () => {
  test("shows every button of a firing Root message in the order of reference.md with the set all", () => {
    expect(keys("firing", ALL)).toEqual([
      "acknowledge",
      "resolve",
      "snooze:3600",
      "snooze:14400",
      "snooze:86400",
      "preset:tomorrow",
      "preset:week_start",
    ]);
  });

  test("shows Unack instead of Ack on an acknowledged Root message", () => {
    expect(keys("acknowledged", ALL)).toEqual([
      "unacknowledge",
      "resolve",
      "snooze:3600",
      "snooze:14400",
      "snooze:86400",
      "preset:tomorrow",
      "preset:week_start",
    ]);
  });

  test("keeps only the listed buttons, in the order of the message", () => {
    const set: ShownSet = {
      all: false,
      items: [presetItem("tomorrow"), snoozeItem(24 * HOUR), acknowledgeItem()],
    };
    expect(keys("firing", set)).toEqual(["acknowledge", "snooze:86400", "preset:tomorrow"]);
    expect(keys("acknowledged", set)).toEqual(["unacknowledge", "snooze:86400", "preset:tomorrow"]);
  });

  test("shows no Resolve unless the set lists it", () => {
    expect(keys("firing", { all: false, items: [acknowledgeItem()] })).toEqual(["acknowledge"]);
    expect(keys("firing", { all: false, items: [resolveItem()] })).toEqual(["resolve"]);
  });

  test("shows nothing for an empty list", () => {
    expect(keys("firing", { all: false, items: [] })).toEqual([]);
  });

  test("never shows a duration that no Route has, and shows no preset without durations", () => {
    const set: ShownSet = {
      all: false,
      items: [snoozeItem(5 * HOUR), presetItem("tomorrow"), presetItem("week_start")],
    };
    expect(keys("firing", set)).toEqual(["preset:tomorrow", "preset:week_start"]);
    expect(keys("firing", set, [])).toEqual([]);
    expect(keys("firing", ALL, [])).toEqual(["acknowledge", "resolve"]);
  });
});

function label(key: Parameters<typeof previewLabel>[1]): string {
  return previewLabel(i18n.t, key, "09:00", "monday");
}

describe("previewLabel", () => {
  test("names the buttons as the messengers do", () => {
    const shown = previewButtons("firing", ALL, DURATIONS).map(label);
    expect(shown).toEqual([
      "Ack",
      "Resolve",
      "Snooze 1 h",
      "Snooze 4 h",
      "Snooze 24 h",
      "Until tomorrow 09:00",
      "Until Monday 09:00",
    ]);
    expect(label({ key: "unacknowledge" })).toBe("Unack");
  });

  test("names the presets after the Organization's working day and week", () => {
    expect(
      previewLabel(i18n.t, { key: "preset:week_start", preset: "week_start" }, "08:30", "sunday"),
    ).toBe("Until Sunday 08:30");
  });

  test("names them in Russian, the day in the genitive", async () => {
    await i18n.changeLanguage("ru");
    const shown = previewButtons("acknowledged", ALL, DURATIONS).map(label);
    expect(shown).toEqual([
      "Снять подтверждение",
      "Закрыть",
      "Отложить на 1 ч",
      "Отложить на 4 ч",
      "Отложить на 24 ч",
      "До завтра 09:00",
      "До понедельника 09:00",
    ]);
  });
});

describe("the offered durations and items", () => {
  test("take the distinct durations of the Routes in ascending order", () => {
    expect(offeredDurations([[4 * HOUR, HOUR], [HOUR, 24 * HOUR], []], [900])).toEqual(DURATIONS);
  });

  test("fall back to the Paging profile's durations when no Route has any", () => {
    expect(offeredDurations([[], []], [HOUR, 24 * HOUR])).toEqual([HOUR, 24 * HOUR]);
    expect(offeredDurations([], [])).toEqual([]);
  });

  test("offer every button, and a stored duration that no Route has after the others", () => {
    const items = offeredItems([HOUR, 24 * HOUR], [snoozeItem(2 * HOUR), snoozeItem(HOUR)]);
    expect(items.map((i) => [i.button, i.snooze_seconds ?? i.preset ?? null])).toEqual([
      ["acknowledge", null],
      ["resolve", null],
      ["snooze", HOUR],
      ["snooze", 24 * HOUR],
      ["snooze", 2 * HOUR],
      ["snooze_preset", "tomorrow"],
      ["snooze_preset", "week_start"],
    ]);
  });

  test("order the items as a Root message does, each once", () => {
    const items = orderedItems([
      presetItem("week_start"),
      snoozeItem(24 * HOUR),
      resolveItem(),
      snoozeItem(HOUR),
      presetItem("tomorrow"),
      acknowledgeItem(),
      snoozeItem(HOUR),
    ]);
    expect(items.map((i) => i.snooze_seconds ?? i.preset ?? i.button)).toEqual([
      "acknowledge",
      "resolve",
      HOUR,
      24 * HOUR,
      "tomorrow",
      "week_start",
    ]);
  });
});

describe("buttonDuration", () => {
  test("mirrors messages.Duration: days above one day, then whole hours, minutes, else seconds", async () => {
    const day = 24 * HOUR;
    const shown = [90, 5400, 7200, 60, 3600, day, 2 * day, 3 * day + HOUR].map((s) =>
      buttonDuration(i18n.t, s),
    );
    expect(shown).toEqual(["90 s", "90 min", "2 h", "1 min", "1 h", "24 h", "2 d", "73 h"]);
    await i18n.changeLanguage("ru");
    expect(buttonDuration(i18n.t, 5400)).toBe("90 мин");
    expect(buttonDuration(i18n.t, 90)).toBe("90 с");
  });
});

describe("previewDurations", () => {
  const routes = [
    { destinationIds: ["DS1"], durations: [] },
    { destinationIds: ["DS1", "DS2"], durations: [4 * HOUR, HOUR] },
    { destinationIds: ["DS2"], durations: [24 * HOUR] },
  ];

  test("takes the durations of the first Route that sends to the Destination and has any, in their order", () => {
    expect(previewDurations(routes, "DS1", DURATIONS)).toEqual([4 * HOUR, HOUR]);
    expect(previewDurations(routes, "DS2", DURATIONS)).toEqual([4 * HOUR, HOUR]);
  });

  test("falls back to the offered durations for a new Destination or one that no Route sends to", () => {
    expect(previewDurations(routes, undefined, DURATIONS)).toEqual(DURATIONS);
    expect(previewDurations(routes, "DS9", DURATIONS)).toEqual(DURATIONS);
  });
});

describe("itemErrors", () => {
  test("repeats the checks of internal/buttons.Validate that the generated schema leaves out", () => {
    expect(
      itemErrors([
        acknowledgeItem(),
        { button: "snooze" },
        { button: "snooze", snooze_seconds: 0 },
        { button: "snooze", snooze_seconds: -5 },
        { button: "snooze", snooze_seconds: 1.5 },
        { button: "snooze", snooze_seconds: 1 },
        { button: "snooze_preset" },
        presetItem("tomorrow"),
      ]),
    ).toEqual({
      1: "required",
      2: "out_of_range",
      3: "out_of_range",
      4: "out_of_range",
      6: "required",
    });
  });
});

describe("the sets of the API", () => {
  test("a Destination without a set of its own follows the Organization", () => {
    expect(destinationButtons(undefined)).toEqual({ follows: true, items: [] });
    expect(destinationButtons({ source: "organization" })).toEqual({ follows: true, items: [] });
    expect(destinationButtonsInput({ follows: true, items: [snoozeItem(HOUR)] })).toEqual({
      source: "organization",
    });
  });

  test("a Destination's own set keeps its items in the order of a message", () => {
    const own = destinationButtons({
      source: "own",
      items: [presetItem("tomorrow"), acknowledgeItem()],
    });
    expect(own).toEqual({ follows: false, items: [acknowledgeItem(), presetItem("tomorrow")] });
    expect(destinationButtonsInput(own)).toEqual({
      source: "own",
      items: [acknowledgeItem(), presetItem("tomorrow")],
    });
    expect(destinationButtonsInput({ follows: false, items: [] })).toEqual({
      source: "own",
      items: [],
    });
  });

  test("the Organization shows every button unless it lists them", () => {
    expect(organizationButtons(undefined)).toEqual({ follows: true, items: [] });
    expect(organizationButtons({ all: true })).toEqual({ follows: true, items: [] });
    expect(organizationButtons({ all: false, items: [resolveItem()] })).toEqual({
      follows: false,
      items: [resolveItem()],
    });
    expect(organizationButtonsInput({ follows: true, items: [] })).toEqual({ all: true });
    expect(organizationButtonsInput({ follows: false, items: [resolveItem()] })).toEqual({
      all: false,
      items: [resolveItem()],
    });
  });

  test("a Destination that follows shows the Organization's set", () => {
    const organization: ShownSet = { all: false, items: [resolveItem()] };
    expect(shownSet({ follows: true, items: [] }, organization)).toBe(organization);
    expect(shownSet({ follows: false, items: [acknowledgeItem()] }, organization)).toEqual({
      all: false,
      items: [acknowledgeItem()],
    });
  });
});
