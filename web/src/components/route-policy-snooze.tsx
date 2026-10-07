// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze durations section of the Route editor (C-10.FR-6, C-08.FR-1): the quick durations that the Snooze dialog
// and the messengers offer, as a list with add and remove, sent as policy.snooze_durations_seconds. A new Route starts
// with the durations of its profile.

import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { fieldErrorText } from "../lib/api";
import { snoozeDurationLabel } from "../lib/commands";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const UNIT_NAMES = ["minutes", "hours", "days"] as const;
type Unit = (typeof UNIT_NAMES)[number];
const UNITS: Record<Unit, number> = { minutes: 60, hours: 60 * 60, days: 24 * 60 * 60 };

function unitLabel(t: TFunction, unit: Unit): string {
  switch (unit) {
    case "minutes":
      return t("routes.snooze.minutes");
    case "hours":
      return t("routes.snooze.hours");
    default:
      return t("routes.snooze.days");
  }
}

/** The durations in ascending order, each once. */
export function sortedDurations(durations: readonly number[]): number[] {
  return [...new Set(durations)].toSorted((a, b) => a - b);
}

/** The pointer of the durations in a refusal of a save. */
export const SNOOZE_POINTER = "/policy/snooze_durations_seconds";

export function RoutePolicySnooze({
  id,
  value,
  onChange,
  error,
}: {
  id: string;
  value: readonly number[];
  onChange: (next: number[]) => void;
  /** The error code of a refusal about the durations. */
  error?: string;
}) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState("");
  const [unit, setUnit] = useState<Unit>("hours");
  const [problem, setProblem] = useState<"invalid" | "duplicate" | null>(null);
  const add = () => {
    const n = Number(amount);
    if (amount.trim() === "" || !Number.isInteger(n) || n < 1) {
      setProblem("invalid");
      return;
    }
    const seconds = n * UNITS[unit];
    if (value.includes(seconds)) {
      setProblem("duplicate");
      return;
    }
    setProblem(null);
    setAmount("");
    onChange(sortedDurations([...value, seconds]));
  };
  const message =
    problem === "invalid"
      ? t("routes.snooze.invalid")
      : problem === "duplicate"
        ? t("routes.snooze.duplicate")
        : error === undefined
          ? undefined
          : fieldErrorText(t, error);
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" data-testid="route-snooze">
      <legend className="mb-2 text-base font-semibold">{t("routes.snooze.title")}</legend>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("routes.snooze.hint")}
      </p>
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("routes.snooze.none")}</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("routes.snooze.list")}>
          {value.map((seconds) => {
            const label = snoozeDurationLabel(t, seconds);
            return (
              <li
                key={seconds}
                className="inline-flex items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 text-sm"
                data-testid="snooze-duration"
              >
                <span className="whitespace-nowrap">{label}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={t("routes.snooze.remove", { duration: label })}
                  onClick={() => onChange(value.filter((s) => s !== seconds))}
                >
                  <XIcon aria-hidden="true" />
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      <div className="flex min-w-0 flex-wrap items-end gap-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-amount`}>{t("routes.snooze.duration")}</Label>
          <Input
            id={`${id}-amount`}
            type="number"
            inputMode="numeric"
            min={1}
            step={1}
            className="w-24"
            value={amount}
            aria-invalid={message !== undefined}
            aria-describedby={message === undefined ? `${id}-hint` : `${id}-hint ${id}-error`}
            onChange={(e) => {
              setAmount(e.target.value);
              setProblem(null);
            }}
            onKeyDown={(e) => {
              // Enter adds the duration instead of saving the Route.
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-unit`}>{t("routes.snooze.unit")}</Label>
          <NativeSelect
            id={`${id}-unit`}
            value={unit}
            onChange={(e) => {
              const next = UNIT_NAMES.find((u) => u === e.target.value);
              if (next !== undefined) {
                setUnit(next);
              }
            }}
          >
            {UNIT_NAMES.map((u) => (
              <NativeSelectOption key={u} value={u}>
                {unitLabel(t, u)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <Button type="button" variant="outline" onClick={add}>
          {t("routes.snooze.add")}
        </Button>
      </div>
      {message !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {message}
        </p>
      )}
    </fieldset>
  );
}

/** The section as the editor shows it without routes:write. */
export function RoutePolicySnoozeReadOnly({ durations }: { durations: readonly number[] }) {
  const { t } = useTranslation();
  return (
    <section className="flex flex-col gap-2" data-testid="route-snooze">
      <h2 className="text-base font-semibold">{t("routes.snooze.title")}</h2>
      <p className="text-sm">
        {durations.length === 0
          ? t("routes.snooze.none")
          : sortedDurations(durations)
              .map((s) => snoozeDurationLabel(t, s))
              .join(", ")}
      </p>
    </section>
  );
}
