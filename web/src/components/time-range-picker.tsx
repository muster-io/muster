// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The time range of the Alert Group list (C-09.FR-13): the presets and "Custom" as a native select, and the custom
// range as native date and time inputs in the user's time zone (D250: native controls, nothing that injects styles).

import { TZDate } from "@date-fns/tz";
import type { TFunction } from "i18next";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import {
  type AlertGroupSearch,
  DEFAULT_TIME_RANGE,
  TIME_RANGES,
  type TimeRange,
  rangeParams,
} from "../lib/alert-group-search";
import { useTimeFormat } from "../lib/time";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

function rangeLabel(t: TFunction, range: TimeRange): string {
  switch (range) {
    case "1h":
      return t("alertGroups.range.lastHour");
    case "24h":
      return t("alertGroups.range.last24Hours");
    case "7d":
      return t("alertGroups.range.last7Days");
    case "30d":
      return t("alertGroups.range.last30Days");
    default:
      return t("alertGroups.range.custom");
  }
}

/** An instant as a datetime-local value (YYYY-MM-DDTHH:MM) in the time zone. */
export function toLocalInput(iso: string, timeZone: string): string {
  const parts = new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone,
  }).formatToParts(new Date(iso));
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((p) => p.type === type)?.value ?? "00";
  return `${part("year")}-${part("month")}-${part("day")}T${part("hour")}:${part("minute")}`;
}

/** A datetime-local value in the time zone as an instant, or undefined when it is not a whole value. */
export function fromLocalInput(value: string, timeZone: string): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (m === null) {
    return undefined;
  }
  const [, y, mo, d, h, mi] = m.map(Number);
  return new TZDate(y ?? 0, (mo ?? 1) - 1, d ?? 1, h ?? 0, mi ?? 0, timeZone).toISOString();
}

export function TimeRangePicker({
  search,
  onChange,
}: {
  search: AlertGroupSearch;
  onChange: (patch: Partial<AlertGroupSearch>) => void;
}) {
  const { t } = useTranslation();
  const { timeZone } = useTimeFormat();
  const id = useId();
  const range = search.range ?? DEFAULT_TIME_RANGE;
  const select = (next: TimeRange) => {
    if (next !== "custom") {
      onChange({
        range: next === DEFAULT_TIME_RANGE ? undefined : next,
        from: undefined,
        to: undefined,
      });
      return;
    }
    // The custom range starts as the range shown, so that the inputs are never empty.
    const now = Date.now();
    const shown = rangeParams(search, now);
    onChange({
      range: "custom",
      from: shown.from ?? new Date(now - 7 * 24 * 60 * 60 * 1000).toISOString(),
      to: shown.to ?? new Date(now).toISOString(),
    });
  };
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-range`}>{t("alertGroups.range.label")}</Label>
        <NativeSelect
          id={`${id}-range`}
          className="w-full"
          value={range}
          onChange={(e) => {
            const next = TIME_RANGES.find((r) => r === e.target.value);
            if (next !== undefined) {
              select(next);
            }
          }}
        >
          {TIME_RANGES.map((r) => (
            <NativeSelectOption key={r} value={r}>
              {rangeLabel(t, r)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {range === "custom" && (
        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
          {(["from", "to"] as const).map((bound) => (
            <div key={bound} className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-${bound}`}>
                {bound === "from" ? t("alertGroups.range.from") : t("alertGroups.range.to")}
              </Label>
              <Input
                id={`${id}-${bound}`}
                type="datetime-local"
                className="min-w-0"
                value={search[bound] ? toLocalInput(search[bound], timeZone) : ""}
                onChange={(e) => {
                  const instant = fromLocalInput(e.target.value, timeZone);
                  if (instant !== undefined || e.target.value === "") {
                    onChange({ [bound]: instant });
                  }
                }}
              />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
