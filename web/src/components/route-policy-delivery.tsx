// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Delivery section of the Route editor (C-08.FR-1, the C-11 fields of the Route): the Thread batching window in
// seconds and the Storm threshold in new Alert Groups per minute. A new Route starts with the values of its profile.

import type { TFunction } from "i18next";
import type { UseFormRegisterReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { RoutePolicy } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { formatDuration } from "../lib/time";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** The fields of the section in the form of the Route editor; an empty field reads as NaN, which is "required". */
export const deliverySchema = {
  thread_batching_window_seconds: z
    .number({ error: "required" })
    .int("out_of_range")
    .min(0, "out_of_range"),
  storm_threshold: z.number({ error: "required" }).int("out_of_range").min(1, "out_of_range"),
};

export interface DeliveryValues {
  thread_batching_window_seconds: number;
  storm_threshold: number;
}

/** The pointers of the policy fields in a refusal, by the form field they belong to. */
export const DELIVERY_POINTERS: Record<string, keyof DeliveryValues> = {
  "/policy/thread_batching_window_seconds": "thread_batching_window_seconds",
  "/policy/storm_threshold": "storm_threshold",
};

export function deliveryValues(policy: RoutePolicy): DeliveryValues {
  return {
    thread_batching_window_seconds: policy.thread_batching_window_seconds,
    storm_threshold: policy.storm_threshold,
  };
}

/** The policy with the values of the section; every other policy field stays as it was. */
export function withDelivery(policy: RoutePolicy, v: DeliveryValues): RoutePolicy {
  return {
    ...policy,
    thread_batching_window_seconds: v.thread_batching_window_seconds,
    storm_threshold: v.storm_threshold,
  };
}

function errorText(t: TFunction, field: keyof DeliveryValues, code: string): string {
  if (code === "required") {
    return fieldErrorText(t, code);
  }
  return field === "storm_threshold"
    ? t("routes.delivery.thresholdInvalid")
    : t("routes.delivery.windowInvalid");
}

function NumberField({
  id,
  label,
  unit,
  hint,
  min,
  field,
  error,
}: {
  id: string;
  label: string;
  unit: string;
  hint: string;
  min: number;
  field: UseFormRegisterReturn;
  error?: string;
}) {
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
          min={min}
          step={1}
          className="w-28"
          aria-invalid={error !== undefined}
          aria-describedby={described}
          {...field}
        />
        <span id={`${id}-unit`} className="text-sm text-muted-foreground">
          {unit}
        </span>
      </div>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {hint}
      </p>
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

export function RoutePolicyDelivery({
  id,
  batchingWindow,
  threshold,
  errors,
}: {
  id: string;
  batchingWindow: UseFormRegisterReturn;
  threshold: UseFormRegisterReturn;
  /** The error codes of the fields. */
  errors: Partial<Record<keyof DeliveryValues, string>>;
}) {
  const { t } = useTranslation();
  const text = (field: keyof DeliveryValues) => {
    const code = errors[field];
    return code === undefined ? undefined : errorText(t, field, code);
  };
  return (
    <fieldset className="flex min-w-0 flex-col gap-4" data-testid="route-delivery">
      <legend className="mb-2 text-base font-semibold">{t("routes.delivery.title")}</legend>
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <NumberField
          id={`${id}-window`}
          label={t("routes.delivery.window")}
          unit={t("routes.delivery.seconds")}
          hint={t("routes.delivery.windowHint")}
          min={0}
          field={batchingWindow}
          error={text("thread_batching_window_seconds")}
        />
        <NumberField
          id={`${id}-threshold`}
          label={t("routes.delivery.threshold")}
          unit={t("routes.delivery.perMinute")}
          hint={t("routes.delivery.thresholdHint")}
          min={1}
          field={threshold}
          error={text("storm_threshold")}
        />
      </div>
    </fieldset>
  );
}

/** The section as the editor shows it without routes:write. */
export function RoutePolicyDeliveryReadOnly({ policy }: { policy: RoutePolicy }) {
  const { t } = useTranslation();
  const rows: [string, string][] = [
    [t("routes.delivery.window"), formatDuration(t, policy.thread_batching_window_seconds)],
    [
      t("routes.delivery.threshold"),
      t("routes.delivery.thresholdValue", { count: policy.storm_threshold }),
    ],
  ];
  return (
    <section className="flex flex-col gap-2" data-testid="route-delivery">
      <h2 className="text-base font-semibold">{t("routes.delivery.title")}</h2>
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
