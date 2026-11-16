// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Rotation layers" of a Schedule (C-27.FR-2, FR-13): the layers top first — the upper one wins where it has a Shift —,
// each with a name, the people who take Shifts in turn from the Team, the length of a Shift (a day, a week, or a
// number of hours, which the API takes as `shift_seconds`), the handover time and, for weeks, day, the start date and
// the optional limits of days and time of day; a "to" earlier than "from" crosses midnight and reads "until {to} the
// next day". Admins edit them; everyone else reads the summary. A person the server refuses as `not_pageable` is
// marked at the person.

import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowDownIcon, ArrowUpIcon, PlusIcon, Trash2Icon, XIcon } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import { getListTeamUsersQueryKey, listTeamUsers } from "../api/gen/endpoints/teams/teams";
import {
  type NotPageableReason,
  type RotationLayer,
  type RotationLayerInput,
  ShiftLength,
  type TeamUser,
  type UserRef,
  Weekday,
} from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { personLabel } from "./schedule-view";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { weekdayName, weekdayShort } from "./working-hours-editor";

const NAME_MAX = 200;
/** The longest Shift of hours: 28 days. */
export const MAX_SHIFT_HOURS = 672;
/** The most Rotation layers of a Schedule. */
export const MAX_LAYERS = 20;
/** The most people in the turn of one Rotation layer. */
export const MAX_PEOPLE = 100;

const WEEKDAYS: readonly Weekday[] = Object.values(Weekday);

/** A Rotation layer as the form holds it; `key` tells layers apart while they are moved. */
export interface LayerValues {
  key: string;
  /** The id of a stored Rotation layer, which keeps its identity; undefined for a new one. */
  id?: string;
  name: string;
  userIds: string[];
  shiftLength: ShiftLength;
  /** The length of a Shift of hours, as typed. */
  hours: string;
  handoverTime: string;
  handoverDay: Weekday;
  startDate: string;
  limited: boolean;
  limitDays: Weekday[];
  limitStart: string;
  limitEnd: string;
}

/** The people of a Team whom a Shift may name: everyone but the Viewers, who may not be Paged. */
export function useTeamPeople(teamId: string): {
  people: UserRef[];
  members: TeamUser[];
  isError: boolean;
} {
  const query = useQuery({
    queryKey: getListTeamUsersQueryKey(teamId),
    queryFn: ({ signal }) => listTeamUsers(teamId, { signal }),
    enabled: teamId !== "",
    staleTime: 30_000,
  });
  const members = query.data?.items ?? [];
  const people = members.filter((m) => m.role !== "viewer").map((m) => m.user);
  return { people, members, isError: query.isError };
}

let keyCounter = 0;
export function newLayerKey(): string {
  keyCounter += 1;
  return `layer-${keyCounter}`;
}

export function newLayer(name: string, startDate: string, userIds: string[] = []): LayerValues {
  return {
    key: newLayerKey(),
    name,
    userIds,
    shiftLength: "week",
    hours: "12",
    handoverTime: "10:00",
    handoverDay: "monday",
    startDate,
    limited: false,
    limitDays: [],
    limitStart: "",
    limitEnd: "",
  };
}

/** The form values of a stored Rotation layer. */
export function layerValues(layer: RotationLayer): LayerValues {
  return {
    key: newLayerKey(),
    id: layer.id,
    name: layer.name,
    userIds: layer.user_ids,
    shiftLength: layer.shift_length,
    hours: layer.shift_seconds === undefined ? "12" : String(layer.shift_seconds / 3600),
    handoverTime: layer.handover_time,
    handoverDay: layer.handover_day ?? "monday",
    startDate: layer.start_date,
    limited: layer.limits !== null,
    limitDays: layer.limits?.days ?? [],
    limitStart: layer.limits?.start ?? "",
    limitEnd: layer.limits?.end ?? "",
  };
}

/** The Rotation layers of the API, lowest first, from the form's, top first. */
export function layersInput(layers: readonly LayerValues[]): RotationLayerInput[] {
  return layers.toReversed().map((l): RotationLayerInput => {
    const input: RotationLayerInput = {
      ...(l.id === undefined ? {} : { id: l.id }),
      name: l.name.trim(),
      user_ids: l.userIds,
      shift_length: l.shiftLength,
      handover_time: l.handoverTime,
      start_date: l.startDate,
      limits: l.limited
        ? {
            days: WEEKDAYS.filter((d) => l.limitDays.includes(d)),
            ...(l.limitStart !== "" && l.limitEnd !== ""
              ? { start: l.limitStart, end: l.limitEnd }
              : {}),
          }
        : null,
    };
    if (l.shiftLength === "hours") {
      input.shift_seconds = Math.round(Number(l.hours) * 3600);
    }
    if (l.shiftLength === "week") {
      input.handover_day = l.handoverDay;
    }
    return input;
  });
}

/** A refusal at a field of a Rotation layer: the code, and for `not_pageable` why the person may not be Paged. */
export interface FieldIssue {
  code: string;
  reason?: NotPageableReason;
}

/** The issues by field: "{layer}.name", "{layer}.user_ids", "{layer}.user_ids.{person}", "{layer}.limits" … */
export type LayerIssues = Record<string, FieldIssue>;

/** The client-side checks of the layers, in the form's order; the API checks again. */
export function checkLayers(layers: readonly LayerValues[]): LayerIssues {
  const issues: LayerIssues = {};
  layers.forEach((l, i) => {
    if (l.name.trim() === "") {
      issues[`${i}.name`] = { code: "required" };
    } else if (l.name.length > NAME_MAX) {
      issues[`${i}.name`] = { code: "too_long" };
    }
    if (l.userIds.length === 0) {
      issues[`${i}.user_ids`] = { code: "required" };
    }
    if (l.shiftLength === "hours") {
      const hours = Number(l.hours);
      if (!Number.isInteger(hours) || hours < 1 || hours > MAX_SHIFT_HOURS) {
        issues[`${i}.shift_seconds`] = { code: "out_of_range" };
      }
    }
    if (l.handoverTime === "") {
      issues[`${i}.handover_time`] = { code: "required" };
    }
    if (l.startDate === "") {
      issues[`${i}.start_date`] = { code: "required" };
    }
    if (l.limited) {
      if (l.limitDays.length === 0) {
        issues[`${i}.limits`] = { code: "required" };
      } else if ((l.limitStart === "") !== (l.limitEnd === "")) {
        issues[`${i}.limits`] = { code: "invalid" };
      }
    }
  });
  return issues;
}

/** The fields of a Rotation layer that the editor shows a refusal at. */
const SHOWN_FIELDS = ["name", "user_ids", "shift_seconds", "handover_time", "start_date", "limits"];

/**
 * The refusals of the API at the Rotation layers, by the form's index: the pointers count from the lowest layer, the
 * form from the top. Returns the issues, and the codes of the refusals that name no field the editor shows.
 */
export function layerIssuesOf(
  errors: readonly { pointer: string; code: string; reason?: NotPageableReason }[],
  count: number,
): { issues: LayerIssues; rest: string[] } {
  const issues: LayerIssues = {};
  const rest: string[] = [];
  for (const e of errors) {
    const match = /^\/rotation_layers\/(\d+)\/([^/]+)(?:\/(\d+))?/.exec(e.pointer);
    if (match === null) {
      rest.push(e.code);
      continue;
    }
    const [, index = "0", field = "", person] = match;
    if (!SHOWN_FIELDS.includes(field)) {
      rest.push(e.code);
      continue;
    }
    const ui = count - 1 - Number(index);
    const key = person === undefined ? `${ui}.${field}` : `${ui}.${field}.${person}`;
    issues[key] = e.reason === undefined ? { code: e.code } : { code: e.code, reason: e.reason };
  }
  return { issues, rest };
}

/** Why a person may not be Paged, as a refusal at a person reads. */
export function notPageableText(t: TFunction, reason: NotPageableReason | undefined): string {
  switch (reason) {
    case "viewer":
      return t("schedule.notPageable.viewer");
    case "disabled":
      return t("schedule.notPageable.disabled");
    case "not_in_team":
      return t("schedule.notPageable.notInTeam");
    default:
      return t("schedule.notPageable.other");
  }
}

function issueText(t: TFunction, issue: FieldIssue): string {
  return issue.code === "not_pageable"
    ? notPageableText(t, issue.reason)
    : fieldErrorText(t, issue.code);
}

function ErrorLine({ id, issue }: { id: string; issue: FieldIssue | undefined }) {
  const { t } = useTranslation();
  if (issue === undefined) {
    return null;
  }
  return (
    <p id={id} className="text-sm text-destructive">
      {issueText(t, issue)}
    </p>
  );
}

/** "Every day" text of the limits as the summary reads it: "Mon–Fri 11:00–19:00", "until 09:00 the next day". */
export function limitsText(t: TFunction, limits: RotationLayer["limits"]): string | undefined {
  if (limits === null) {
    return undefined;
  }
  const days = WEEKDAYS.filter((d) => limits.days.includes(d));
  const first = WEEKDAYS.indexOf(days[0] ?? "monday");
  const last = WEEKDAYS.indexOf(days.at(-1) ?? "monday");
  const dayText =
    days.length >= 3 && last - first + 1 === days.length
      ? `${weekdayShort(t, days[0] ?? "monday")}–${weekdayShort(t, days.at(-1) ?? "monday")}`
      : days.map((d) => weekdayShort(t, d)).join(", ");
  if (limits.start === undefined || limits.end === undefined) {
    return dayText;
  }
  const times =
    limits.end < limits.start
      ? t("schedule.layers.crossesMidnight", { from: limits.start, to: limits.end })
      : `${limits.start}–${limits.end}`;
  return `${dayText} ${times}`;
}

/** The length of a Shift as the summary reads it. */
export function shiftText(
  t: TFunction,
  layer: Pick<RotationLayer, "shift_length" | "shift_seconds">,
): string {
  switch (layer.shift_length) {
    case "day":
      return t("schedule.layers.shiftDay");
    case "week":
      return t("schedule.layers.shiftWeek");
    default:
      return t("schedule.layers.shiftHours", { count: (layer.shift_seconds ?? 0) / 3600 });
  }
}

/** The Rotation layers as everyone reads them: top first, each with its limits, Shift length, handover and people. */
export function RotationLayersSummary({ layers }: { layers: readonly RotationLayer[] }) {
  const { t } = useTranslation();
  const top = layers.toReversed();
  if (top.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("schedule.layers.none")}</p>;
  }
  return (
    <ol className="flex flex-col gap-3" data-testid="rotation-layers-summary">
      {top.map((layer) => {
        const limits = limitsText(t, layer.limits);
        return (
          <li
            key={layer.id}
            className="flex flex-col gap-1 rounded-lg border p-3 text-sm"
            data-testid="rotation-layer-summary"
          >
            <span className="font-medium wrap-anywhere">{layer.name}</span>
            <span className="text-muted-foreground">
              {[
                shiftText(t, layer),
                layer.shift_length === "week" && layer.handover_day !== undefined
                  ? t("schedule.layers.handoverOnDay", {
                      day: weekdayName(t, layer.handover_day),
                      time: layer.handover_time,
                    })
                  : t("schedule.layers.handoverAt", { time: layer.handover_time }),
                limits,
              ]
                .filter((x) => x !== undefined)
                .join(" · ")}
            </span>
            <span className="wrap-anywhere">
              {layer.users.map((u) => personLabel(u)).join(" → ")}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

export interface RotationLayersEditorProps {
  layers: LayerValues[];
  onChange: (layers: LayerValues[]) => void;
  /** Everyone a layer may name: the people of the Team and those the layers keep. */
  people: readonly UserRef[];
  /** The people of the Team, whom "Add person" offers. */
  team: readonly UserRef[];
  issues: LayerIssues;
  /** The start date of a new layer, today in the Schedule's zone. */
  today: string;
  disabled?: boolean;
}

export function RotationLayersEditor({
  layers,
  onChange,
  people,
  team,
  issues,
  today,
  disabled = false,
}: RotationLayersEditorProps) {
  const { t } = useTranslation();
  const set = (i: number, patch: Partial<LayerValues>) =>
    onChange(layers.map((l, at) => (at === i ? { ...l, ...patch } : l)));
  const move = (i: number, by: number) => {
    const next = [...layers];
    const [item] = next.splice(i, 1);
    if (item !== undefined) {
      next.splice(i + by, 0, item);
      onChange(next);
    }
  };
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="rotation-layers-editor">
      {layers.map((layer, i) => (
        <LayerCard
          key={layer.key}
          index={i}
          count={layers.length}
          layer={layer}
          people={people}
          team={team}
          issues={issues}
          disabled={disabled}
          onPatch={(patch) => set(i, patch)}
          onMove={(by) => move(i, by)}
          onRemove={() => onChange(layers.filter((_, at) => at !== i))}
        />
      ))}
      {layers.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("schedule.layers.none")}</p>
      )}
      <div>
        <Button
          type="button"
          variant="outline"
          disabled={disabled || layers.length >= MAX_LAYERS}
          onClick={() =>
            onChange([
              newLayer(t("schedule.layers.defaultName", { number: layers.length + 1 }), today),
              ...layers,
            ])
          }
        >
          <PlusIcon aria-hidden="true" />
          {t("schedule.layers.add")}
        </Button>
      </div>
    </div>
  );
}

function LayerCard({
  index,
  count,
  layer,
  people,
  team,
  issues,
  disabled,
  onPatch,
  onMove,
  onRemove,
}: {
  index: number;
  count: number;
  layer: LayerValues;
  people: readonly UserRef[];
  team: readonly UserRef[];
  issues: LayerIssues;
  disabled: boolean;
  onPatch: (patch: Partial<LayerValues>) => void;
  onMove: (by: number) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const issue = (field: string) => issues[`${index}.${field}`];
  const named = layer.name.trim() === "" ? t("schedule.layers.unnamed") : layer.name;
  const byId = new Map(people.map((p) => [p.id, p]));
  const pickable = team.filter((u) => !layer.userIds.includes(u.id));
  const crosses =
    layer.limited &&
    layer.limitStart !== "" &&
    layer.limitEnd !== "" &&
    layer.limitEnd < layer.limitStart;
  const movePerson = (a: number, b: number) => {
    const next = [...layer.userIds];
    const [item] = next.splice(a, 1);
    if (item !== undefined) {
      next.splice(b, 0, item);
      onPatch({ userIds: next });
    }
  };
  return (
    <fieldset
      disabled={disabled}
      className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
      data-testid="rotation-layer"
    >
      <legend className="sr-only">{named}</legend>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium text-muted-foreground">
          {index === 0
            ? t("schedule.layers.position.top", { number: index + 1 })
            : t("schedule.layers.position.other", { number: index + 1 })}
        </span>
        <div className="flex items-center gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === 0}
            aria-label={t("schedule.layers.moveUp", { name: named })}
            onClick={() => onMove(-1)}
          >
            <ArrowUpIcon aria-hidden="true" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === count - 1}
            aria-label={t("schedule.layers.moveDown", { name: named })}
            onClick={() => onMove(1)}
          >
            <ArrowDownIcon aria-hidden="true" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={t("schedule.layers.remove", { name: named })}
            onClick={onRemove}
          >
            <Trash2Icon aria-hidden="true" />
          </Button>
        </div>
      </div>
      <div className="flex max-w-md min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-name`}>{t("schedule.layers.name")}</Label>
        <Input
          id={`${id}-name`}
          autoComplete="off"
          maxLength={NAME_MAX}
          value={layer.name}
          aria-invalid={issue("name") !== undefined}
          aria-describedby={issue("name") === undefined ? undefined : `${id}-name-error`}
          onChange={(e) => onPatch({ name: e.target.value })}
        />
        <ErrorLine id={`${id}-name-error`} issue={issue("name")} />
      </div>
      <div className="flex min-w-0 flex-col gap-1.5" data-testid="rotation-layer-people">
        <Label htmlFor={`${id}-add-person`}>{t("schedule.layers.people")}</Label>
        {layer.userIds.length > 0 && (
          <ol className="flex flex-col gap-1">
            {layer.userIds.map((userId, p) => {
              const person = byId.get(userId);
              const refused = issue(`user_ids.${p}`);
              const label = person === undefined ? userId : personLabel(person);
              return (
                <li key={`${userId}-${p}`} className="flex flex-col gap-0.5">
                  <div className="flex items-center gap-1">
                    <span className="w-6 text-sm text-muted-foreground">{p + 1}.</span>
                    <span
                      className="min-w-0 flex-1 text-sm wrap-anywhere"
                      data-testid="rotation-layer-person"
                    >
                      {label}
                    </span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      disabled={p === 0}
                      aria-label={t("schedule.layers.personUp", { name: label })}
                      onClick={() => movePerson(p, p - 1)}
                    >
                      <ArrowUpIcon aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      disabled={p === layer.userIds.length - 1}
                      aria-label={t("schedule.layers.personDown", { name: label })}
                      onClick={() => movePerson(p, p + 1)}
                    >
                      <ArrowDownIcon aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={t("schedule.layers.personRemove", { name: label })}
                      onClick={() =>
                        onPatch({ userIds: layer.userIds.filter((_, at) => at !== p) })
                      }
                    >
                      <XIcon aria-hidden="true" />
                    </Button>
                  </div>
                  <ErrorLine id={`${id}-person-${p}-error`} issue={refused} />
                </li>
              );
            })}
          </ol>
        )}
        <NativeSelect
          id={`${id}-add-person`}
          className="w-full sm:w-64"
          value=""
          disabled={layer.userIds.length >= MAX_PEOPLE}
          aria-invalid={issue("user_ids") !== undefined}
          aria-describedby={issue("user_ids") === undefined ? undefined : `${id}-people-error`}
          onChange={(e) => {
            if (e.target.value !== "") {
              onPatch({ userIds: [...layer.userIds, e.target.value] });
            }
          }}
        >
          <NativeSelectOption value="">{t("schedule.layers.addPerson")}</NativeSelectOption>
          {pickable.map((u) => (
            <NativeSelectOption key={u.id} value={u.id}>
              {personLabel(u)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <ErrorLine id={`${id}-people-error`} issue={issue("user_ids")} />
      </div>
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-shift`}>{t("schedule.layers.shiftLength")}</Label>
          <NativeSelect
            id={`${id}-shift`}
            className="w-40"
            value={layer.shiftLength}
            onChange={(e) => {
              const value = Object.values(ShiftLength).find((s) => s === e.target.value);
              if (value !== undefined) {
                onPatch({ shiftLength: value });
              }
            }}
          >
            <NativeSelectOption value="day">{t("schedule.layers.day")}</NativeSelectOption>
            <NativeSelectOption value="week">{t("schedule.layers.week")}</NativeSelectOption>
            <NativeSelectOption value="hours">{t("schedule.layers.hours")}</NativeSelectOption>
          </NativeSelect>
        </div>
        {layer.shiftLength === "hours" && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-hours`}>{t("schedule.layers.hoursField")}</Label>
            <Input
              id={`${id}-hours`}
              type="number"
              inputMode="numeric"
              min={1}
              max={MAX_SHIFT_HOURS}
              step={1}
              className="w-28"
              value={layer.hours}
              aria-invalid={issue("shift_seconds") !== undefined}
              aria-describedby={
                issue("shift_seconds") === undefined ? undefined : `${id}-hours-error`
              }
              onChange={(e) => onPatch({ hours: e.target.value })}
            />
            <ErrorLine id={`${id}-hours-error`} issue={issue("shift_seconds")} />
          </div>
        )}
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-handover`}>{t("schedule.layers.handoverTime")}</Label>
          <Input
            id={`${id}-handover`}
            type="time"
            className="w-32"
            value={layer.handoverTime}
            aria-invalid={issue("handover_time") !== undefined}
            onChange={(e) => onPatch({ handoverTime: e.target.value })}
          />
          <ErrorLine id={`${id}-handover-error`} issue={issue("handover_time")} />
        </div>
        {layer.shiftLength === "week" && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-day`}>{t("schedule.layers.handoverDay")}</Label>
            <NativeSelect
              id={`${id}-day`}
              className="w-40"
              value={layer.handoverDay}
              onChange={(e) => {
                const day = WEEKDAYS.find((d) => d === e.target.value);
                if (day !== undefined) {
                  onPatch({ handoverDay: day });
                }
              }}
            >
              {WEEKDAYS.map((d) => (
                <NativeSelectOption key={d} value={d}>
                  {weekdayName(t, d)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
        )}
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-start`}>{t("schedule.layers.startDate")}</Label>
          <Input
            id={`${id}-start`}
            type="date"
            className="w-44"
            value={layer.startDate}
            aria-invalid={issue("start_date") !== undefined}
            onChange={(e) => onPatch({ startDate: e.target.value })}
          />
          <ErrorLine id={`${id}-start-error`} issue={issue("start_date")} />
        </div>
      </div>
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex items-center gap-2">
          <input
            id={`${id}-limited`}
            type="checkbox"
            className="size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            checked={layer.limited}
            onChange={(e) =>
              onPatch({
                limited: e.target.checked,
                limitDays:
                  e.target.checked && layer.limitDays.length === 0
                    ? [...WEEKDAYS]
                    : layer.limitDays,
              })
            }
          />
          <Label htmlFor={`${id}-limited`}>{t("schedule.layers.limit")}</Label>
        </div>
        {layer.limited && (
          <div className="flex min-w-0 flex-col gap-2 pl-6" data-testid="rotation-layer-limits">
            <div
              role="group"
              aria-label={t("schedule.layers.limitDays")}
              className="flex flex-wrap gap-x-3 gap-y-1"
            >
              {WEEKDAYS.map((d) => (
                <label key={d} className="flex items-center gap-1 text-sm">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={layer.limitDays.includes(d)}
                    onChange={(e) =>
                      onPatch({
                        limitDays: e.target.checked
                          ? [...layer.limitDays, d]
                          : layer.limitDays.filter((x) => x !== d),
                      })
                    }
                  />
                  {weekdayShort(t, d)}
                </label>
              ))}
            </div>
            <div className="flex flex-wrap items-end gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-from`}>{t("schedule.layers.limitFrom")}</Label>
                <Input
                  id={`${id}-from`}
                  type="time"
                  className="w-32"
                  value={layer.limitStart}
                  onChange={(e) => onPatch({ limitStart: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-to`}>{t("schedule.layers.limitTo")}</Label>
                <Input
                  id={`${id}-to`}
                  type="time"
                  className="w-32"
                  value={layer.limitEnd}
                  onChange={(e) => onPatch({ limitEnd: e.target.value })}
                />
              </div>
            </div>
            {crosses && (
              <p className="text-sm text-muted-foreground" data-testid="rotation-layer-crosses">
                {t("schedule.layers.untilNextDay", { to: layer.limitEnd })}
              </p>
            )}
            <ErrorLine id={`${id}-limits-error`} issue={issue("limits")} />
          </div>
        )}
      </div>
    </fieldset>
  );
}
