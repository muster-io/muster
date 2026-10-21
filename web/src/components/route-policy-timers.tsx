// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "Ack timeout and Reminders" section of the Route editor (C-17.FR-2, C-08.FR-1): the ack timeout on or off with
// its first interval in minutes, the Reminders on or off with their first interval and their cap in hours, and
// auto-unacknowledge, which needs the Reminders. The fields of a switch that is off are hidden. Under the fields the
// times they give are spelled out — only as an explanation of the policy; the server keeps the schedule. A new Route
// starts with the values of its profile; the API keeps them in seconds. "Learn more" sits beside the title.

import type { TFunction } from "i18next";
import type { UseFormRegisterReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { RoutePolicy } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { formatDuration } from "../lib/time";
import { DocsLink } from "./docs-link";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

const MINUTE = 60;
const HOUR = 60 * MINUTE;

/** The notices of one ack timeout (C-17.FR-2): at F, 3F and 7F after it starts. */
const ACK_TIMEOUT_NOTICES = 3;

/** The Reminder times the section spells out. */
const REMINDER_TIMES = 5;

/** A number of a field that may be hidden: an empty field reads as NaN, checked only while its switch is on. */
const amount = z.union([z.number(), z.nan()]);

/** The fields of the section in the form of the Route editor; refineTimers checks them. */
export const timersSchema = {
  ack_timeout_enabled: z.boolean(),
  ack_timeout_first_minutes: amount,
  reminders_enabled: z.boolean(),
  reminders_first_hours: amount,
  reminders_cap_hours: amount,
  auto_unacknowledge: z.boolean(),
};

export interface TimersValues {
  ack_timeout_enabled: boolean;
  ack_timeout_first_minutes: number;
  reminders_enabled: boolean;
  reminders_first_hours: number;
  reminders_cap_hours: number;
  auto_unacknowledge: boolean;
}

/** The pointers of the policy fields in a refusal, by the form field they belong to. */
export const TIMERS_POINTERS: Record<string, keyof TimersValues> = {
  "/policy/ack_timeout/enabled": "ack_timeout_enabled",
  "/policy/ack_timeout/first_interval_seconds": "ack_timeout_first_minutes",
  "/policy/reminders/enabled": "reminders_enabled",
  "/policy/reminders/first_interval_seconds": "reminders_first_hours",
  "/policy/reminders/cap_seconds": "reminders_cap_hours",
  "/policy/auto_unacknowledge": "auto_unacknowledge",
};

function positive(value: number): boolean {
  return Number.isFinite(value) && value > 0;
}

/** An amount in a unit of seconds is at least the 1 second the API takes once rounded to whole seconds. */
function atLeastOneSecond(value: number, unit: number): boolean {
  return Number.isFinite(value) && Math.round(value * unit) >= 1;
}

/** The code of a field of a switch that is on: "required" when empty, "out_of_range" when below 1 second. */
function amountError(value: number, unit: number): string | undefined {
  if (Number.isNaN(value)) {
    return "required";
  }
  return atLeastOneSecond(value, unit) ? undefined : "out_of_range";
}

/**
 * The checks of the section, for the superRefine of the form: the fields of a switch that is on come to at least 1
 * second, and the cap of the Reminders is at least their first interval, as the API checks it. Hidden fields never
 * block a save.
 */
export function refineTimers(v: TimersValues, ctx: z.RefinementCtx): void {
  const add = (field: keyof TimersValues, code: string | undefined) => {
    if (code !== undefined) {
      ctx.addIssue({ code: "custom", path: [field], message: code });
    }
  };
  if (v.ack_timeout_enabled) {
    add("ack_timeout_first_minutes", amountError(v.ack_timeout_first_minutes, MINUTE));
  }
  if (v.reminders_enabled) {
    add("reminders_first_hours", amountError(v.reminders_first_hours, HOUR));
    const cap = amountError(v.reminders_cap_hours, HOUR);
    add(
      "reminders_cap_hours",
      cap ??
        (atLeastOneSecond(v.reminders_first_hours, HOUR) &&
        Math.round(v.reminders_cap_hours * HOUR) < Math.round(v.reminders_first_hours * HOUR)
          ? "out_of_range"
          : undefined),
    );
  }
}

export function timersValues(policy: RoutePolicy): TimersValues {
  return {
    ack_timeout_enabled: policy.ack_timeout.enabled,
    ack_timeout_first_minutes: policy.ack_timeout.first_interval_seconds / MINUTE,
    reminders_enabled: policy.reminders.enabled,
    reminders_first_hours: policy.reminders.first_interval_seconds / HOUR,
    reminders_cap_hours: policy.reminders.cap_seconds / HOUR,
    auto_unacknowledge: policy.auto_unacknowledge,
  };
}

/**
 * The policy with the values of the section; every other policy field stays as it was. The numbers of a switch that
 * is off are kept as stored when they would not pass the API's checks, so that a hidden field never refuses a save.
 */
export function withTimers(policy: RoutePolicy, v: TimersValues): RoutePolicy {
  const ackFirst = Math.round(v.ack_timeout_first_minutes * MINUTE);
  const ackValid = positive(ackFirst);
  const first = Math.round(v.reminders_first_hours * HOUR);
  const cap = Math.round(v.reminders_cap_hours * HOUR);
  const remindersValid = positive(first) && positive(cap) && cap >= first;
  return {
    ...policy,
    ack_timeout: {
      enabled: v.ack_timeout_enabled,
      first_interval_seconds:
        ackValid || v.ack_timeout_enabled ? ackFirst : policy.ack_timeout.first_interval_seconds,
    },
    reminders:
      remindersValid || v.reminders_enabled
        ? { enabled: v.reminders_enabled, first_interval_seconds: first, cap_seconds: cap }
        : { ...policy.reminders, enabled: false },
    auto_unacknowledge: v.auto_unacknowledge,
  };
}

/** When the notices of an ack timeout with the first interval first come after it starts: F, 3F and 7F. */
export function ackTimeoutNoticeTimes(first: number): number[] {
  return Array.from({ length: ACK_TIMEOUT_NOTICES }, (_, i) => (2 ** (i + 1) - 1) * first);
}

/**
 * When the first Reminders come after the acknowledgement: each interval is twice the one before, starting at first,
 * and never longer than cap.
 */
export function reminderTimes(first: number, cap: number, count = REMINDER_TIMES): number[] {
  const out: number[] = [];
  let at = 0;
  for (let k = 0; k < count; k++) {
    at += Math.min(first * 2 ** k, cap);
    out.push(at);
  }
  return out;
}

function errorText(t: TFunction, field: keyof TimersValues, code: string): string {
  if (code !== "out_of_range") {
    return fieldErrorText(t, code);
  }
  switch (field) {
    case "ack_timeout_first_minutes":
      return t("routes.timers.minutesInvalid");
    case "reminders_first_hours":
      return t("routes.timers.hoursInvalid");
    case "reminders_cap_hours":
      return t("routes.timers.capInvalid");
    default:
      return fieldErrorText(t, code);
  }
}

function AmountField({
  id,
  label,
  unit,
  field,
  error,
}: {
  id: string;
  label: string;
  unit: string;
  field: UseFormRegisterReturn;
  error?: string;
}) {
  const described = [`${id}-unit`, error === undefined ? null : `${id}-error`]
    .filter((v) => v !== null)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="number"
          inputMode="decimal"
          min={0}
          step="any"
          className="w-28"
          aria-invalid={error !== undefined}
          aria-describedby={described}
          {...field}
        />
        <span id={`${id}-unit`} className="text-sm text-muted-foreground">
          {unit}
        </span>
      </div>
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

function Switch({
  id,
  label,
  hint,
  field,
  disabled = false,
  extraHint,
  error,
}: {
  id: string;
  label: string;
  hint: string;
  field: UseFormRegisterReturn;
  disabled?: boolean;
  /** A second hint, such as why the switch is disabled. */
  extraHint?: string;
  /** A refusal of the switch, or of a field it hides. */
  error?: string;
}) {
  const described = [
    `${id}-hint`,
    extraHint === undefined ? null : `${id}-extra`,
    error === undefined ? null : `${id}-error`,
  ]
    .filter((v) => v !== null)
    .join(" ");
  return (
    <div className="flex items-start gap-2">
      <input
        id={id}
        type="checkbox"
        role="switch"
        className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50"
        aria-describedby={described}
        aria-invalid={error !== undefined}
        {...field}
        disabled={disabled}
      />
      <div className="flex min-w-0 flex-col gap-0.5">
        <Label htmlFor={id} className="leading-snug">
          {label}
        </Label>
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {hint}
        </p>
        {extraHint !== undefined && (
          <p id={`${id}-extra`} className="text-sm text-muted-foreground">
            {extraHint}
          </p>
        )}
        {error !== undefined && (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

export interface TimersFields {
  ackEnabled: UseFormRegisterReturn;
  ackFirst: UseFormRegisterReturn;
  remindersEnabled: UseFormRegisterReturn;
  remindersFirst: UseFormRegisterReturn;
  remindersCap: UseFormRegisterReturn;
  autoUnacknowledge: UseFormRegisterReturn;
}

export function RoutePolicyTimers({
  id,
  fields,
  values,
  errors,
}: {
  id: string;
  fields: TimersFields;
  /** The current values of the form, which decide what shows. */
  values: TimersValues;
  /** The error codes of the fields. */
  errors: Partial<Record<keyof TimersValues, string>>;
}) {
  const { t, i18n } = useTranslation();
  const number = new Intl.NumberFormat(i18n.resolvedLanguage ?? "en", {
    maximumFractionDigits: 2,
  });
  const fmt = (n: number) => number.format(n);
  const errorOf = (field: keyof TimersValues) => {
    const code = errors[field];
    return code === undefined ? undefined : errorText(t, field, code);
  };
  // A refusal of a field its switch hides shows under the switch, so that it is never out of sight.
  const ackError =
    errorOf("ack_timeout_enabled") ??
    (values.ack_timeout_enabled ? undefined : errorOf("ack_timeout_first_minutes"));
  const remindersError =
    errorOf("reminders_enabled") ??
    (values.reminders_enabled
      ? undefined
      : (errorOf("reminders_first_hours") ?? errorOf("reminders_cap_hours")));
  const notices = positive(values.ack_timeout_first_minutes)
    ? ackTimeoutNoticeTimes(values.ack_timeout_first_minutes)
    : undefined;
  const reminders =
    positive(values.reminders_first_hours) &&
    positive(values.reminders_cap_hours) &&
    values.reminders_cap_hours >= values.reminders_first_hours
      ? reminderTimes(values.reminders_first_hours, values.reminders_cap_hours)
      : undefined;
  return (
    <fieldset
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-4"
      data-testid="route-timers"
    >
      <legend className="mb-2 flex flex-wrap items-baseline gap-x-2 text-base font-semibold">
        <span id={`${id}-title`}>{t("routes.timers.title")}</span>
        <DocsLink to="field.timers" kind="learn_more" about={t("docs.fields.timers")} />
      </legend>
      <div className="flex min-w-0 flex-col gap-3">
        <Switch
          id={`${id}-ack-timeout`}
          label={t("routes.timers.ackTimeout")}
          hint={t("routes.timers.ackTimeoutHint")}
          field={fields.ackEnabled}
          error={ackError}
        />
        {values.ack_timeout_enabled && (
          <div className="flex min-w-0 flex-col gap-2 pl-6">
            <AmountField
              id={`${id}-ack-first`}
              label={t("routes.timers.firstNotice")}
              unit={t("routes.timers.minutes")}
              field={fields.ackFirst}
              error={errorOf("ack_timeout_first_minutes")}
            />
            {notices !== undefined && (
              <p className="text-sm text-muted-foreground" data-testid="ack-timeout-times">
                {t("routes.timers.noticesAt", {
                  first: fmt(notices[0] ?? 0),
                  second: fmt(notices[1] ?? 0),
                  third: fmt(notices[2] ?? 0),
                  count: notices[2] ?? 0,
                })}
              </p>
            )}
          </div>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-3">
        <Switch
          id={`${id}-reminders`}
          label={t("routes.timers.reminders")}
          hint={t("routes.timers.remindersHint")}
          field={fields.remindersEnabled}
          error={remindersError}
        />
        {values.reminders_enabled && (
          <div className="flex min-w-0 flex-col gap-2 pl-6">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <AmountField
                id={`${id}-reminders-first`}
                label={t("routes.timers.firstReminder")}
                unit={t("routes.timers.hours")}
                field={fields.remindersFirst}
                error={errorOf("reminders_first_hours")}
              />
              <AmountField
                id={`${id}-reminders-cap`}
                label={t("routes.timers.cap")}
                unit={t("routes.timers.hours")}
                field={fields.remindersCap}
                error={errorOf("reminders_cap_hours")}
              />
            </div>
            {reminders !== undefined && (
              <p className="text-sm text-muted-foreground" data-testid="reminder-times">
                {t("routes.timers.remindersAt", {
                  first: fmt(reminders[0] ?? 0),
                  second: fmt(reminders[1] ?? 0),
                  third: fmt(reminders[2] ?? 0),
                  fourth: fmt(reminders[3] ?? 0),
                  fifth: fmt(reminders[4] ?? 0),
                  count: reminders[4] ?? 0,
                })}
              </p>
            )}
          </div>
        )}
      </div>
      <Switch
        id={`${id}-auto-unacknowledge`}
        label={t("routes.timers.autoUnacknowledge")}
        hint={t("routes.timers.autoUnacknowledgeHint")}
        field={fields.autoUnacknowledge}
        disabled={!values.reminders_enabled}
        extraHint={values.reminders_enabled ? undefined : t("routes.timers.needsReminders")}
        error={errorOf("auto_unacknowledge")}
      />
    </fieldset>
  );
}

/** The section as the editor shows it without routes:write. */
export function RoutePolicyTimersReadOnly({ policy }: { policy: RoutePolicy }) {
  const { t } = useTranslation();
  const rows: [string, string][] = [
    [
      t("routes.timers.ackTimeout"),
      policy.ack_timeout.enabled
        ? t("routes.timers.readOnlyAck", {
            duration: formatDuration(t, policy.ack_timeout.first_interval_seconds),
          })
        : t("routes.lifecycle.off"),
    ],
    [
      t("routes.timers.reminders"),
      policy.reminders.enabled
        ? t("routes.timers.readOnlyReminders", {
            first: formatDuration(t, policy.reminders.first_interval_seconds),
            cap: formatDuration(t, policy.reminders.cap_seconds),
          })
        : t("routes.lifecycle.off"),
    ],
    [
      t("routes.timers.autoUnacknowledge"),
      policy.auto_unacknowledge ? t("routes.lifecycle.on") : t("routes.lifecycle.off"),
    ],
  ];
  return (
    <section className="flex flex-col gap-2" data-testid="route-timers">
      <h2 className="text-base font-semibold">{t("routes.timers.title")}</h2>
      <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
        {rows.map(([term, value]) => (
          <div key={term} className="contents">
            <dt className="font-medium">{term}</dt>
            <dd className="mb-1 sm:mb-0">{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
