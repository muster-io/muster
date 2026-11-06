// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Working hours of a Team (C-25.FR-9): "Organization default: Mon–Fri 09:00–18:00, Europe/Berlin" until "Own" is
// chosen, then the days, "From", "To" and the time zone. The Organization's Working hours (C-20.FR-1) edit their days
// and end on Organization → General with the same list of days. A refusal of the server shows at its field.

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { Weekday, type WorkingHours } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { unreachable } from "./destination-test-panel";
import { TimeZonePicker } from "./time-zone-picker";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** The days of the week from Monday, as the lists show them. */
export const WEEKDAYS: readonly Weekday[] = Object.values(Weekday);

/** The full name of a day of the week. */
export function weekdayName(t: TFunction, day: Weekday): string {
  switch (day) {
    case "monday":
      return t("weekdays.monday");
    case "tuesday":
      return t("weekdays.tuesday");
    case "wednesday":
      return t("weekdays.wednesday");
    case "thursday":
      return t("weekdays.thursday");
    case "friday":
      return t("weekdays.friday");
    case "saturday":
      return t("weekdays.saturday");
    case "sunday":
      return t("weekdays.sunday");
    default:
      return unreachable(day);
  }
}

/** The short name of a day of the week, such as "Mon". */
export function weekdayShort(t: TFunction, day: Weekday): string {
  switch (day) {
    case "monday":
      return t("weekdaysShort.monday");
    case "tuesday":
      return t("weekdaysShort.tuesday");
    case "wednesday":
      return t("weekdaysShort.wednesday");
    case "thursday":
      return t("weekdaysShort.thursday");
    case "friday":
      return t("weekdaysShort.friday");
    case "saturday":
      return t("weekdaysShort.saturday");
    case "sunday":
      return t("weekdaysShort.sunday");
    default:
      return unreachable(day);
  }
}

/** The days as a person reads them: "Mon–Fri" for a run of three or more, else the names with commas. */
export function workingDaysText(t: TFunction, days: readonly Weekday[]): string {
  const sorted = WEEKDAYS.filter((d) => days.includes(d));
  if (sorted.length === 0) {
    return t("teams.hours.noDays");
  }
  const first = WEEKDAYS.indexOf(sorted[0] ?? "monday");
  const last = WEEKDAYS.indexOf(sorted.at(-1) ?? "monday");
  if (sorted.length >= 3 && last - first + 1 === sorted.length) {
    return `${weekdayShort(t, sorted[0] ?? "monday")}–${weekdayShort(t, sorted.at(-1) ?? "monday")}`;
  }
  return sorted.map((d) => weekdayShort(t, d)).join(", ");
}

/** The Working hours in one line, such as "Mon–Fri 09:00–18:00, Europe/Berlin". */
export function workingHoursText(t: TFunction, hours: WorkingHours): string {
  return `${workingDaysText(t, hours.days)} ${hours.start}–${hours.end}, ${hours.time_zone}`;
}

/** The days of the week to tick, in a fieldset of its own. */
export function WeekdayChecklist({
  legend,
  value,
  onChange,
  error,
  id,
  disabled = false,
}: {
  legend: string;
  value: readonly Weekday[];
  onChange: (days: Weekday[]) => void;
  error?: string;
  id: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <fieldset
      className="flex min-w-0 flex-col gap-2"
      disabled={disabled}
      aria-describedby={error === undefined ? undefined : `${id}-error`}
      data-testid={`${id}-days`}
    >
      <legend className="mb-1 text-sm font-medium">{legend}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {WEEKDAYS.map((day) => (
          <label key={day} className="flex items-center gap-1.5 text-sm">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={value.includes(day)}
              aria-invalid={error !== undefined}
              onChange={(e) =>
                onChange(WEEKDAYS.filter((d) => (d === day ? e.target.checked : value.includes(d))))
              }
            />
            {weekdayShort(t, day)}
          </label>
        ))}
      </div>
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </fieldset>
  );
}

/** The text of a refusal of a field of the Working hours: an end that is not later than the start says so. */
export function hoursErrorText(t: TFunction, code: string): string {
  return code === "out_of_range" ? t("teams.hours.endAfterStart") : fieldErrorText(t, code);
}

/** The refusals of the fields of the Working hours, already translated. */
export interface HoursErrors {
  days?: string;
  start?: string;
  end?: string;
  time_zone?: string;
}

export interface WorkingHoursEditorProps {
  id: string;
  /** Whether the Team has Working hours of its own. */
  own: boolean;
  onOwnChange: (own: boolean) => void;
  /** The Working hours to edit with "Own"; they start as the effective ones, the Organization's. */
  value: WorkingHours;
  onChange: (next: WorkingHours) => void;
  /** The Working hours in effect without "Own": the Organization's; undefined while they are not known. */
  organization: WorkingHours | undefined;
  errors?: HoursErrors;
  disabled?: boolean;
}

export function WorkingHoursEditor({
  id,
  own,
  onOwnChange,
  value,
  onChange,
  organization,
  errors,
  disabled = false,
}: WorkingHoursEditorProps) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-4">
      {own ? (
        <p className="text-sm text-muted-foreground" data-testid="working-hours-summary">
          {workingHoursText(t, value)}
        </p>
      ) : organization === undefined ? (
        <p className="text-sm text-muted-foreground" data-testid="working-hours-summary">
          {t("teams.hours.organizationUnknown")}
        </p>
      ) : (
        <p className="text-sm" data-testid="working-hours-summary">
          {t("teams.hours.organizationDefault", {
            days: workingDaysText(t, organization.days),
            from: organization.start,
            to: organization.end,
            zone: organization.time_zone,
          })}
        </p>
      )}
      <label className="flex w-fit items-center gap-2 text-sm font-medium">
        <input
          type="checkbox"
          className="size-4 accent-primary"
          checked={own}
          disabled={disabled}
          onChange={(e) => onOwnChange(e.target.checked)}
        />
        {t("teams.hours.own")}
      </label>
      {own && (
        <div className="flex min-w-0 flex-col gap-4">
          <WeekdayChecklist
            id={`${id}-days`}
            legend={t("teams.hours.days")}
            value={value.days}
            error={errors?.days}
            disabled={disabled}
            onChange={(days) => onChange({ ...value, days })}
          />
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-start`}>{t("teams.hours.from")}</Label>
              <Input
                id={`${id}-start`}
                type="time"
                className="min-w-0"
                value={value.start}
                disabled={disabled}
                aria-invalid={errors?.start !== undefined}
                aria-describedby={errors?.start === undefined ? undefined : `${id}-start-error`}
                onChange={(e) => onChange({ ...value, start: e.target.value })}
              />
              {errors?.start !== undefined && (
                <p id={`${id}-start-error`} className="text-sm text-destructive">
                  {errors.start}
                </p>
              )}
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-end`}>{t("teams.hours.to")}</Label>
              <Input
                id={`${id}-end`}
                type="time"
                className="min-w-0"
                value={value.end}
                disabled={disabled}
                aria-invalid={errors?.end !== undefined}
                aria-describedby={errors?.end === undefined ? undefined : `${id}-end-error`}
                onChange={(e) => onChange({ ...value, end: e.target.value })}
              />
              {errors?.end !== undefined && (
                <p id={`${id}-end-error`} className="text-sm text-destructive">
                  {errors.end}
                </p>
              )}
            </div>
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-zone`}>{t("teams.hours.timeZone")}</Label>
            <TimeZonePicker
              id={`${id}-zone`}
              value={value.time_zone}
              invalid={errors?.time_zone !== undefined}
              describedBy={errors?.time_zone === undefined ? undefined : `${id}-zone-error`}
              onChange={(time_zone) => onChange({ ...value, time_zone })}
            />
            {errors?.time_zone !== undefined && (
              <p id={`${id}-zone-error`} className="text-sm text-destructive">
                {errors.time_zone}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
