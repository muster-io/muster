// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Lifecycle section of the Route editor (C-09.FR-4, FR-5, FR-9; C-08.FR-1): the Reopen window and the Grace period
// in minutes, and whether a rise to Urgent removes the acknowledgement. A new Route starts with the values of its
// profile; the API keeps them in seconds.

import type { TFunction } from "i18next";
import type { UseFormRegisterReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { RoutePolicy } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { formatDuration } from "../lib/time";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** A number of minutes, 0 or more; an empty field reads as NaN, which is "required". */
const minutes = z.number({ error: "required" }).min(0, "out_of_range");

/** The fields of the section in the form of the Route editor. */
export const lifecycleSchema = {
  reopen_window_minutes: minutes,
  grace_period_minutes: minutes,
  urgent_rise_removes_ack: z.boolean(),
};

export interface LifecycleValues {
  reopen_window_minutes: number;
  grace_period_minutes: number;
  urgent_rise_removes_ack: boolean;
}

/** The pointers of the policy fields in a refusal, by the form field they belong to. */
export const LIFECYCLE_POINTERS: Record<string, keyof LifecycleValues> = {
  "/policy/reopen_window_seconds": "reopen_window_minutes",
  "/policy/grace_period_seconds": "grace_period_minutes",
  "/policy/urgent_rise_removes_ack": "urgent_rise_removes_ack",
};

export function lifecycleValues(policy: RoutePolicy): LifecycleValues {
  return {
    reopen_window_minutes: policy.reopen_window_seconds / 60,
    grace_period_minutes: policy.grace_period_seconds / 60,
    urgent_rise_removes_ack: policy.urgent_rise_removes_ack,
  };
}

/** The policy with the values of the section; every other policy field stays as it was. */
export function withLifecycle(policy: RoutePolicy, v: LifecycleValues): RoutePolicy {
  return {
    ...policy,
    reopen_window_seconds: Math.round(v.reopen_window_minutes * 60),
    grace_period_seconds: Math.round(v.grace_period_minutes * 60),
    urgent_rise_removes_ack: v.urgent_rise_removes_ack,
  };
}

function minutesErrorText(t: TFunction, code: string): string {
  return code === "required" ? fieldErrorText(t, code) : t("routes.lifecycle.minutesInvalid");
}

function MinutesField({
  id,
  label,
  hint,
  field,
  error,
}: {
  id: string;
  label: string;
  hint: string;
  field: UseFormRegisterReturn;
  error?: string;
}) {
  const { t } = useTranslation();
  const described = [`${id}-unit`, `${id}-hint`, error === undefined ? null : `${id}-error`]
    .filter((v) => v !== null)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="number"
          inputMode="numeric"
          min={0}
          step={1}
          className="w-28"
          aria-invalid={error !== undefined}
          aria-describedby={described}
          {...field}
        />
        <span id={`${id}-unit`} className="text-sm text-muted-foreground">
          {t("routes.lifecycle.minutes")}
        </span>
      </div>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {hint}
      </p>
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {minutesErrorText(t, error)}
        </p>
      )}
    </div>
  );
}

export function RoutePolicyLifecycle({
  id,
  reopenWindow,
  gracePeriod,
  urgentRise,
  errors,
}: {
  id: string;
  reopenWindow: UseFormRegisterReturn;
  gracePeriod: UseFormRegisterReturn;
  urgentRise: UseFormRegisterReturn;
  /** The error codes of the fields. */
  errors: Partial<Record<keyof LifecycleValues, string>>;
}) {
  const { t } = useTranslation();
  return (
    <fieldset className="flex min-w-0 flex-col gap-4" data-testid="route-lifecycle">
      <legend className="mb-2 text-base font-semibold">{t("routes.lifecycle.title")}</legend>
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <MinutesField
          id={`${id}-reopen-window`}
          label={t("routes.lifecycle.reopenWindow")}
          hint={t("routes.lifecycle.reopenWindowHint")}
          field={reopenWindow}
          error={errors.reopen_window_minutes}
        />
        <MinutesField
          id={`${id}-grace-period`}
          label={t("routes.lifecycle.gracePeriod")}
          hint={t("routes.lifecycle.gracePeriodHint")}
          field={gracePeriod}
          error={errors.grace_period_minutes}
        />
      </div>
      <div className="flex items-start gap-2">
        <input
          id={`${id}-urgent-rise`}
          type="checkbox"
          role="switch"
          className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          {...urgentRise}
        />
        <Label htmlFor={`${id}-urgent-rise`} className="leading-snug">
          {t("routes.lifecycle.urgentRise")}
        </Label>
      </div>
      {errors.urgent_rise_removes_ack !== undefined && (
        <p className="text-sm text-destructive">
          {fieldErrorText(t, errors.urgent_rise_removes_ack)}
        </p>
      )}
    </fieldset>
  );
}

/** The section as the editor shows it without routes:write. */
export function RoutePolicyLifecycleReadOnly({ policy }: { policy: RoutePolicy }) {
  const { t } = useTranslation();
  const rows: [string, string][] = [
    [t("routes.lifecycle.reopenWindow"), formatDuration(t, policy.reopen_window_seconds)],
    [t("routes.lifecycle.gracePeriod"), formatDuration(t, policy.grace_period_seconds)],
    [
      t("routes.lifecycle.urgentRise"),
      policy.urgent_rise_removes_ack ? t("routes.lifecycle.on") : t("routes.lifecycle.off"),
    ],
  ];
  return (
    <section className="flex flex-col gap-2" data-testid="route-lifecycle">
      <h2 className="text-base font-semibold">{t("routes.lifecycle.title")}</h2>
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
