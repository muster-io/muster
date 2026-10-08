// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The limiter of a Connection or a Destination (C-11, C-13.FR-1): at most `limit` requests per `per_seconds` seconds.
// The form keeps both numbers as typed text and sends limiterInput(values) once limiterErrors finds nothing.

import { useTranslation } from "react-i18next";

import type { Limiter } from "../api/gen/model";
import { Input } from "./ui/input";

export interface LimiterValues {
  limit: string;
  per_seconds: string;
}

export type LimiterFieldName = keyof LimiterValues;

export function limiterValues(limiter: Limiter): LimiterValues {
  return { limit: String(limiter.limit), per_seconds: String(limiter.per_seconds) };
}

/** A whole number of at least 1, or undefined. */
function positive(text: string): number | undefined {
  const value = text.trim();
  if (!/^\d{1,9}$/.test(value)) {
    return undefined;
  }
  const n = Number(value);
  return n >= 1 ? n : undefined;
}

/** The checks the server repeats: both numbers are whole and at least 1. Returns the field errors as codes. */
export function limiterErrors(values: LimiterValues): Partial<Record<LimiterFieldName, string>> {
  const errors: Partial<Record<LimiterFieldName, string>> = {};
  for (const field of ["limit", "per_seconds"] as const) {
    if (values[field].trim() === "") {
      errors[field] = "required";
    } else if (positive(values[field]) === undefined) {
      errors[field] = "invalid_format";
    }
  }
  return errors;
}

/** The limiter of a request; call it only after limiterErrors found nothing. */
export function limiterInput(values: LimiterValues): Limiter {
  return { limit: positive(values.limit) ?? 1, per_seconds: positive(values.per_seconds) ?? 1 };
}

export interface LimiterFieldProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  value: LimiterValues;
  onChange: (value: LimiterValues) => void;
  /** The hint under the inputs. */
  hint?: string;
  /** Field errors, already translated; an error of the whole limiter goes on both. */
  errors?: Partial<Record<LimiterFieldName, string>>;
  disabled?: boolean;
}

export function LimiterField({
  id,
  value,
  onChange,
  hint,
  errors = {},
  disabled = false,
}: LimiterFieldProps) {
  const { t } = useTranslation();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const messages = [...new Set([errors.limit, errors.per_seconds].filter((e) => e !== undefined))];
  const described = (field: LimiterFieldName) =>
    [hint === undefined ? null : hintId, errors[field] === undefined ? null : errorId]
      .filter(Boolean)
      .join(" ") || undefined;
  return (
    <fieldset className="flex min-w-0 flex-col gap-2" disabled={disabled}>
      <legend className="mb-2 text-sm leading-none font-medium">{t("limiter.title")}</legend>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Input
          id={`${id}-limit`}
          inputMode="numeric"
          className="w-20"
          aria-label={t("limiter.limit")}
          aria-invalid={errors.limit !== undefined}
          aria-describedby={described("limit")}
          value={value.limit}
          onChange={(e) => onChange({ ...value, limit: e.target.value })}
        />
        <span aria-hidden="true">{t("limiter.per")}</span>
        <Input
          id={`${id}-per-seconds`}
          inputMode="numeric"
          className="w-20"
          aria-label={t("limiter.perSeconds")}
          aria-invalid={errors.per_seconds !== undefined}
          aria-describedby={described("per_seconds")}
          value={value.per_seconds}
          onChange={(e) => onChange({ ...value, per_seconds: e.target.value })}
        />
        <span aria-hidden="true">{t("limiter.seconds")}</span>
      </div>
      {hint !== undefined && (
        <p id={hintId} className="text-sm text-muted-foreground">
          {hint}
        </p>
      )}
      {messages.length > 0 && (
        <p id={errorId} className="text-sm text-destructive">
          {messages.join(" ")}
        </p>
      )}
    </fieldset>
  );
}
