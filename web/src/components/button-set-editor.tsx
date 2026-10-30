// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The button set of a Destination or of the Organization (C-28.FR-8, FR-10): the choice between following the
// Organization ("As in the Organization" / "Own for this Destination") or showing every button ("All buttons" / a list
// on the Organization's page), a tick box for Acknowledge, Resolve, each Snooze duration of the Organization's Routes
// and both Snooze presets, and the preview of what a firing and an acknowledged Root message show. The preview is
// computed here from the ticks, so that it follows them before anything is saved (lib/button-sets.ts).

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import { useListRouteProfiles, useListRoutes } from "../api/gen/endpoints/routes/routes";
import type { ButtonSetItem, Organization, Weekday } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import {
  type ButtonSetValue,
  type PreviewStatus,
  type RouteDurations,
  type ShownSet,
  buttonDuration,
  itemKey,
  offeredDurations,
  offeredItems,
  orderedItems,
  organizationButtons,
  previewButtons,
  previewDurations,
  previewLabel,
  shownSet,
  snoozeItem,
} from "../lib/button-sets";
import { snoozePresetLabel } from "../lib/commands";
import { useCan } from "./app-shell";
import { DocsLink } from "./docs-link";
import { profileKind } from "./profile-picker";

const DEFAULT_WORKDAY_START = "09:00";
const DEFAULT_WEEK_START: Weekday = "monday";

/** What the editor reads of the Organization: the start of the working day, the first day of the week and its set. */
export interface OrganizationSettings {
  workdayStart: string;
  weekStart: Weekday;
  buttons: ShownSet;
}

function settingsOf(org: Organization | undefined): OrganizationSettings {
  const value = organizationButtons(org?.buttons);
  return {
    workdayStart: org?.workday_start ?? DEFAULT_WORKDAY_START,
    weekStart: org?.week_start ?? DEFAULT_WEEK_START,
    buttons: shownSet(value),
  };
}

/** What the editor reads of the Routes: the durations the tick boxes offer, each Route's own, and whether they load. */
export interface RouteReads {
  /** The Organization's Routes' distinct durations, else the Paging profile's: the tick boxes. */
  offered: number[];
  routes: RouteDurations[];
  loading: boolean;
}

export function useRouteReads(): RouteReads {
  const canRoutes = useCan("routes:read");
  const routes = useListRoutes(undefined, { query: { enabled: canRoutes } });
  const profiles = useListRouteProfiles({ query: { enabled: canRoutes } });
  const paging = profiles.data?.items.find((p) => profileKind(p.id) === "paging");
  const own = (routes.data?.items ?? []).map((r) => ({
    destinationIds: r.destination_ids,
    durations: r.policy.snooze_durations_seconds,
  }));
  return {
    offered: offeredDurations(
      own.map((r) => r.durations),
      paging?.policy.snooze_durations_seconds ?? [],
    ),
    routes: own,
    loading: routes.isLoading || profiles.isLoading,
  };
}

export interface ButtonSetEditorProps {
  /** The base of the ids, unique on the page. */
  id: string;
  /** A Destination chooses between the Organization's set and its own; the Organization between all and a list. */
  kind: "destination" | "organization";
  value: ButtonSetValue;
  onChange: (next: ButtonSetValue) => void;
  /** The name of the Destination in the heading of the preview. */
  destinationName?: string;
  /** The stored Destination, whose Routes' durations the preview shows; a new Destination has none. */
  destinationId?: string;
  /** The Organization as the page read it; without it the editor reads it itself. */
  organization?: Organization;
  /** The code of a refusal of the set. */
  error?: string;
  disabled?: boolean;
}

/** The Snooze durations the items name, after those already named. */
function namedDurations(items: readonly ButtonSetItem[], earlier: readonly number[]): number[] {
  const seconds = items.flatMap((i) =>
    i.button === "snooze" && i.snooze_seconds ? [i.snooze_seconds] : [],
  );
  return [...new Set([...earlier, ...seconds])];
}

function itemLabel(
  t: ReturnType<typeof useTranslation>["t"],
  item: ButtonSetItem,
  settings: OrganizationSettings,
): string {
  switch (item.button) {
    case "acknowledge":
      return t("buttons.items.acknowledge");
    case "resolve":
      return t("buttons.items.resolve");
    case "snooze":
      return t("buttons.items.snooze", {
        duration: buttonDuration(t, item.snooze_seconds ?? 0),
      });
    default:
      return snoozePresetLabel(
        t,
        item.preset ?? "tomorrow",
        settings.workdayStart,
        settings.weekStart,
      );
  }
}

function Preview({
  id,
  status,
  shown,
  durations,
  settings,
}: {
  id: string;
  status: PreviewStatus;
  shown: ShownSet;
  durations: readonly number[];
  settings: OrganizationSettings;
}) {
  const { t } = useTranslation();
  const buttons = previewButtons(status, shown, durations);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <h4 id={`${id}-${status}-title`} className="text-sm font-medium">
        {status === "firing" ? t("buttons.firing") : t("buttons.acknowledged")}
      </h4>
      {buttons.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid={`button-set-preview-${status}`}>
          {t("buttons.none")}
        </p>
      ) : (
        <ul
          className="flex min-w-0 flex-wrap gap-1.5"
          data-testid={`button-set-preview-${status}`}
          aria-labelledby={`${id}-${status}-title`}
        >
          {buttons.map((b) => (
            <li
              key={b.key}
              className="rounded-md border bg-muted px-2 py-0.5 text-sm wrap-anywhere"
            >
              {previewLabel(t, b, settings.workdayStart, settings.weekStart)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function ButtonSetEditor({
  id,
  kind,
  value,
  onChange,
  destinationName,
  destinationId,
  organization,
  error,
  disabled = false,
}: ButtonSetEditorProps) {
  const { t } = useTranslation();
  const read = useGetOrganization({ query: { enabled: organization === undefined } });
  const settings = settingsOf(organization ?? read.data);
  const reads = useRouteReads();
  const durations = reads.offered;
  const isDestination = kind === "destination";
  // A Destination's Root message takes the durations of its Route; the Organization's set applies to every Route.
  const shownDurations = isDestination
    ? previewDurations(reads.routes, destinationId, durations)
    : durations;
  // A duration that the set named once stays a tick box while the editor is open, so that unticking can be undone.
  const [named, setNamed] = useState(() => namedDurations(value.items, []));
  const offered = offeredItems(durations, namedDurations(value.items, named).map(snoozeItem));
  const ticked = new Set(value.items.map(itemKey));
  // The Organization's own page previews what it is set to, not what was saved.
  const shown = isDestination ? shownSet(value, settings.buttons) : shownSet(value);
  const heading = isDestination
    ? t("buttons.preview", { destination: destinationName || t("buttons.thisDestination") })
    : t("buttons.previewOrganization");

  const follow = (follows: boolean) => {
    if (follows === value.follows) {
      return;
    }
    if (follows) {
      onChange({ follows: true, items: [] });
      return;
    }
    // Its own set starts as what the Destination shows now, so that an untick takes one button away.
    const now: ShownSet = isDestination ? settings.buttons : { all: true };
    onChange({
      follows: false,
      items: now.all
        ? offeredItems(durations, [])
        : orderedItems(now.items.filter((i) => i.button !== "snooze" || i.snooze_seconds)),
    });
  };
  const toggle = (item: ButtonSetItem, on: boolean) => {
    setNamed(namedDurations(value.items, named));
    const rest = value.items.filter((i) => itemKey(i) !== itemKey(item));
    onChange({ follows: false, items: orderedItems(on ? [...rest, item] : rest) });
  };

  const followsLabel = isDestination ? t("buttons.source.organization") : t("buttons.source.all");
  const ownLabel = isDestination ? t("buttons.source.own") : t("buttons.source.list");
  const errorId = `${id}-error`;
  return (
    <fieldset
      aria-labelledby={`${id}-title`}
      aria-describedby={error === undefined ? `${id}-hint` : `${id}-hint ${errorId}`}
      className="flex min-w-0 flex-col gap-3"
      data-testid="button-set"
      disabled={disabled}
    >
      <legend className="mb-2 flex flex-wrap items-baseline gap-x-2 text-base font-semibold">
        <span id={`${id}-title`}>
          {isDestination ? t("buttons.title") : t("buttons.titleOrganization")}
        </span>
        <DocsLink to="field.buttons" kind="learn_more" about={t("docs.fields.buttons")} />
      </legend>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {isDestination ? t("buttons.hint") : t("buttons.hintOrganization")}
      </p>
      <div
        role="radiogroup"
        aria-label={isDestination ? t("buttons.title") : t("buttons.titleOrganization")}
        className="flex min-w-0 flex-col gap-2"
        data-testid="button-set-source"
        data-value={value.follows ? (isDestination ? "organization" : "all") : "own"}
      >
        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            name={`${id}-source`}
            className="size-4 shrink-0 accent-primary"
            checked={value.follows}
            onChange={() => follow(true)}
          />
          <span className="min-w-0">{followsLabel}</span>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            name={`${id}-source`}
            className="size-4 shrink-0 accent-primary"
            checked={!value.follows}
            disabled={reads.loading && value.follows}
            onChange={() => follow(false)}
          />
          <span className="min-w-0">{ownLabel}</span>
        </label>
      </div>
      {!value.follows && (
        <ul
          className="grid min-w-0 gap-x-4 gap-y-2 sm:grid-cols-2 xl:grid-cols-3"
          aria-label={t("buttons.items.list")}
        >
          {offered.map((item) => {
            const key = itemKey(item);
            return (
              <li key={key}>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    className="size-4 shrink-0 accent-primary"
                    checked={ticked.has(key)}
                    data-testid={`button-set-item-${key}`}
                    onChange={(e) => toggle(item, e.target.checked)}
                  />
                  <span className="min-w-0 wrap-anywhere">{itemLabel(t, item, settings)}</span>
                </label>
              </li>
            );
          })}
        </ul>
      )}
      {error !== undefined && (
        <p
          id={errorId}
          tabIndex={-1}
          data-focus-problem="true"
          className="text-sm text-destructive"
          data-testid="button-set-error"
        >
          {fieldErrorText(t, error)}
        </p>
      )}
      <div
        className="flex min-w-0 flex-col gap-2 rounded-lg border p-3"
        data-testid="button-set-preview"
      >
        <h3 className="text-sm font-semibold">{heading}</h3>
        <Preview
          id={id}
          status="firing"
          shown={shown}
          durations={shownDurations}
          settings={settings}
        />
        <Preview
          id={id}
          status="acknowledged"
          shown={shown}
          durations={shownDurations}
          settings={settings}
        />
      </div>
    </fieldset>
  );
}
