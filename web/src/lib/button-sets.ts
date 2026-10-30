// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What a button set shows (C-28.FR-8), computed in the browser as internal/buttons does, so that the preview follows
// the tick boxes before anything is saved: a Root message shows, in the order of reference.md, the buttons its status
// offers that the set lists; of the Route's Snooze durations only those in the set, and the presets only when there are
// durations. The server's filter is the rule; button-sets.test.ts mirrors internal/buttons/set_test.go.

import type { TFunction } from "i18next";

import type {
  ButtonSetItem,
  DestinationButtonSet,
  OrganizationButtonSet,
  SnoozePreset,
  Weekday,
} from "../api/gen/model";
import { snoozePresetLabel } from "./commands";

/** The statuses of a Root message whose buttons the set chooses; a snoozed one always shows Ack, Unsnooze, Resolve. */
export type PreviewStatus = "firing" | "acknowledged";

export const PRESETS: readonly SnoozePreset[] = ["tomorrow", "week_start"];

/** A button of a Root message as the preview lists it. */
export type PreviewButton =
  | { key: "acknowledge" | "unacknowledge" | "resolve" }
  | { key: `snooze:${number}`; seconds: number }
  | { key: `preset:${SnoozePreset}`; preset: SnoozePreset };

/** The key of an item, the identity the tick boxes and the test ids use: "acknowledge", "snooze:86400", "preset:tomorrow". */
export function itemKey(item: ButtonSetItem): string {
  switch (item.button) {
    case "snooze":
      return `snooze:${item.snooze_seconds ?? 0}`;
    case "snooze_preset":
      return `preset:${item.preset ?? ""}`;
    default:
      return item.button;
  }
}

export function acknowledgeItem(): ButtonSetItem {
  return { button: "acknowledge" };
}

export function resolveItem(): ButtonSetItem {
  return { button: "resolve" };
}

export function snoozeItem(seconds: number): ButtonSetItem {
  return { button: "snooze", snooze_seconds: seconds };
}

export function presetItem(preset: SnoozePreset): ButtonSetItem {
  return { button: "snooze_preset", preset };
}

/** The most Snooze durations a Root message offers as buttons (internal/buttons.MaxSnoozeDurations). */
const MAX_DURATIONS = 254;

/**
 * A Snooze duration as a Root message's button shows it, as messages.Duration does: days when divisible and above one
 * day, otherwise hours, minutes or seconds when it divides evenly — 5400 s is "90 min" and 90 s is "90 s".
 */
export function buttonDuration(t: TFunction, seconds: number): string {
  if (seconds > 86400 && seconds % 86400 === 0) {
    return t("duration.days", { value: seconds / 86400 });
  }
  if (seconds >= 3600 && seconds % 3600 === 0) {
    return t("duration.hours", { value: seconds / 3600 });
  }
  if (seconds >= 60 && seconds % 60 === 0) {
    return t("duration.minutes", { value: seconds / 60 });
  }
  return t("duration.seconds", { value: seconds });
}

/** A Route as the preview reads it: where it sends and its Snooze durations in their order. */
export interface RouteDurations {
  destinationIds: readonly string[];
  durations: readonly number[];
}

/**
 * The durations a Root message of a Destination offers: those of the first Route, in Route order, that sends to it and
 * has any, as the Root message takes its Route's; for a Destination without such a Route (a new one), the offered
 * durations of every Route.
 */
export function previewDurations(
  routes: readonly RouteDurations[],
  destinationId: string | undefined,
  offered: readonly number[],
): number[] {
  const own =
    destinationId === undefined
      ? undefined
      : routes.find((r) => r.destinationIds.includes(destinationId) && r.durations.length > 0);
  return (own === undefined ? offered : own.durations).slice(0, MAX_DURATIONS);
}

/** The distinct values of the lists in ascending order. */
export function distinctDurations(lists: readonly (readonly number[])[]): number[] {
  return [...new Set(lists.flat())].toSorted((a, b) => a - b);
}

/**
 * The Snooze durations the tick boxes offer: those of the Organization's Routes, or the Paging profile's when no Route
 * has any, in ascending order. They are what the preview shows too.
 */
export function offeredDurations(
  routes: readonly (readonly number[])[],
  profile: readonly number[],
): number[] {
  const fromRoutes = distinctDurations(routes);
  return fromRoutes.length > 0 ? fromRoutes : distinctDurations([profile]);
}

/**
 * Every item a set can list: Acknowledge, Resolve, each offered duration, and both presets; a duration the stored items
 * name that no Route has follows the offered ones, so that saving never drops it.
 */
export function offeredItems(
  durations: readonly number[],
  stored: readonly ButtonSetItem[],
): ButtonSetItem[] {
  const extra = distinctDurations([
    stored.flatMap((i) => (i.button === "snooze" && i.snooze_seconds ? [i.snooze_seconds] : [])),
  ]).filter((s) => !durations.includes(s));
  return [
    acknowledgeItem(),
    resolveItem(),
    ...[...durations, ...extra].map(snoozeItem),
    ...PRESETS.map(presetItem),
  ];
}

function rank(item: ButtonSetItem): number {
  return item.button === "acknowledge"
    ? 0
    : item.button === "resolve"
      ? 1
      : item.button === "snooze"
        ? 2
        : 3;
}

/** The items in the order of a Root message, each once. */
export function orderedItems(items: readonly ButtonSetItem[]): ButtonSetItem[] {
  const seen = new Set<string>();
  const unique = items.filter((i) => {
    const key = itemKey(i);
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
  return unique.toSorted(
    (a, b) =>
      rank(a) - rank(b) ||
      (a.snooze_seconds ?? 0) - (b.snooze_seconds ?? 0) ||
      PRESETS.indexOf(a.preset ?? "tomorrow") - PRESETS.indexOf(b.preset ?? "tomorrow"),
  );
}

/** A set as the preview reads it: every button, or the items listed. */
export type ShownSet = { all: true } | { all: false; items: readonly ButtonSetItem[] };

function listed(set: ShownSet, item: ButtonSetItem): boolean {
  return set.all || set.items.some((i) => itemKey(i) === itemKey(item));
}

/**
 * The buttons a Root message in a status shows on a Route with the durations given: Ack (Unack when acknowledged),
 * Resolve, the durations in the set and, when the Route has durations, the presets in the set.
 */
export function previewButtons(
  status: PreviewStatus,
  set: ShownSet,
  durations: readonly number[],
): PreviewButton[] {
  const out: PreviewButton[] = [];
  if (listed(set, acknowledgeItem())) {
    out.push({ key: status === "firing" ? "acknowledge" : "unacknowledge" });
  }
  if (listed(set, resolveItem())) {
    out.push({ key: "resolve" });
  }
  for (const seconds of durations) {
    if (listed(set, snoozeItem(seconds))) {
      out.push({ key: `snooze:${seconds}`, seconds });
    }
  }
  if (durations.length > 0) {
    for (const preset of PRESETS) {
      if (listed(set, presetItem(preset))) {
        out.push({ key: `preset:${preset}`, preset });
      }
    }
  }
  return out;
}

/** The label of a button on a Root message: "Ack", "Unack", "Resolve", "Snooze 24 h", "Until tomorrow 09:00". */
export function previewLabel(
  t: TFunction,
  button: PreviewButton,
  workdayStart: string,
  weekStart: Weekday,
): string {
  if ("seconds" in button) {
    return t("buttons.snooze", { duration: buttonDuration(t, button.seconds) });
  }
  if ("preset" in button) {
    return snoozePresetLabel(t, button.preset, workdayStart, weekStart);
  }
  return button.key === "acknowledge"
    ? t("buttons.acknowledge")
    : button.key === "unacknowledge"
      ? t("buttons.unacknowledge")
      : t("buttons.resolve");
}

/**
 * The checks the server repeats on the items (internal/buttons.Validate): a Snooze item needs its duration, at least
 * 1 s, which the generated schema leaves unchecked; a preset item needs its preset. Returns the code by the index of
 * the item.
 */
export function itemErrors(items: readonly ButtonSetItem[]): Record<number, string> {
  const errors: Record<number, string> = {};
  items.forEach((item, index) => {
    if (item.button === "snooze") {
      if (item.snooze_seconds === undefined) {
        errors[index] = "required";
      } else if (!Number.isInteger(item.snooze_seconds) || item.snooze_seconds < 1) {
        errors[index] = "out_of_range";
      }
    } else if (item.button === "snooze_preset" && item.preset === undefined) {
      errors[index] = "required";
    }
  });
  return errors;
}

/**
 * A button set as the editor holds it: "follows" is "As in the Organization" on a Destination and "All buttons" on the
 * Organization; the items are listed only when it does not.
 */
export interface ButtonSetValue {
  follows: boolean;
  items: ButtonSetItem[];
}

/** The set of a Destination as read; one the API leaves out follows the Organization. */
export function destinationButtons(buttons: DestinationButtonSet | undefined): ButtonSetValue {
  return buttons?.source === "own"
    ? { follows: false, items: orderedItems(buttons.items ?? []) }
    : { follows: true, items: [] };
}

export function destinationButtonsInput(value: ButtonSetValue): DestinationButtonSet {
  return value.follows
    ? { source: "organization" }
    : { source: "own", items: orderedItems(value.items) };
}

/** The Organization's set as read; one the API leaves out is every button, the default. */
export function organizationButtons(buttons: OrganizationButtonSet | undefined): ButtonSetValue {
  return buttons === undefined || buttons.all
    ? { follows: true, items: [] }
    : { follows: false, items: orderedItems(buttons.items ?? []) };
}

export function organizationButtonsInput(value: ButtonSetValue): OrganizationButtonSet {
  return value.follows ? { all: true } : { all: false, items: orderedItems(value.items) };
}

/** The set a Destination or the Organization shows, as the preview reads it. */
export function shownSet(value: ButtonSetValue, organization: ShownSet = { all: true }): ShownSet {
  return value.follows ? organization : { all: false, items: value.items };
}
