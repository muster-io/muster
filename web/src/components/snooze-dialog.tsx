// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze dialog (C-10.FR-6, FR-18): the Route's Snooze durations as quick choices ("1 h", "4 h", "24 h"), the
// Snooze presets "Until tomorrow 09:00" and "Until Monday 09:00" from the Organization's settings, each with the end the
// server computes for the user's time zone when the dialog opens (sent as `{"preset", "time_zone"}`), "Until" with
// native date and time inputs in the user's time zone (D250), sent as `until` in UTC, and "No end", which shows the
// warning of reference.md and enables "Snooze" only once "I understand" is ticked (sent as `{"no_end": true}`). A bulk
// Snooze has no Route, so it offers no durations. A phone shows the dialog as a full-screen sheet.

import { TZDate } from "@date-fns/tz";
import { TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { useListSnoozePresets } from "../api/gen/endpoints/alert-groups/alert-groups";
import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import { SnoozePreset, type SnoozeRequest, type Weekday } from "../api/gen/model";
import { SHEET, snoozeDurationLabel, snoozePresetLabel } from "../lib/commands";
import { useTimeFormat } from "../lib/time";
import { useNow } from "./relative-time";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { cn } from "./ui/utils";

/** The choice of the dialog: a quick duration in seconds, a Snooze preset, an end of the user's choosing, or no end. */
export type SnoozeChoice =
  | { kind: "duration"; seconds: number }
  | { kind: "preset"; preset: SnoozePreset }
  | { kind: "until" }
  | { kind: "no_end" };

/** The date (YYYY-MM-DD) and time (HH:MM) of an instant in the time zone, for the native inputs. */
export function dateTimeParts(instant: Date, timeZone: string): { date: string; time: string } {
  const parts = new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone,
  }).formatToParts(instant);
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((p) => p.type === type)?.value ?? "00";
  return {
    date: `${part("year")}-${part("month")}-${part("day")}`,
    time: `${part("hour")}:${part("minute")}`,
  };
}

/** The instant of a date and a time in the time zone, or undefined while either is incomplete. */
export function instantOf(date: string, time: string, timeZone: string): Date | undefined {
  const d = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  const h = /^(\d{2}):(\d{2})$/.exec(time);
  if (d === null || h === null) {
    return undefined;
  }
  const [, y, mo, day] = d.map(Number);
  const [, hour, minute] = h.map(Number);
  return new Date(
    new TZDate(y ?? 0, (mo ?? 1) - 1, day ?? 1, hour ?? 0, minute ?? 0, timeZone).getTime(),
  );
}

/** The end a new "Until" starts with: an hour from now, on the next quarter hour. */
function defaultUntil(now: number): Date {
  const quarter = 15 * 60 * 1000;
  return new Date(Math.ceil((now + 60 * 60 * 1000) / quarter) * quarter);
}

/**
 * The request of a choice at an instant, or undefined while it cannot be sent. A preset names the user's time zone, in
 * which the server computes its end.
 */
export function snoozeRequest(
  choice: SnoozeChoice,
  until: Date | undefined,
  understood: boolean,
  now: number,
  timeZone: string,
): SnoozeRequest | undefined {
  switch (choice.kind) {
    case "duration":
      return { until: new Date(now + choice.seconds * 1000).toISOString() };
    case "preset":
      return { preset: choice.preset, time_zone: timeZone };
    case "until":
      return until === undefined || until.getTime() <= now
        ? undefined
        : { until: until.toISOString() };
    default:
      return understood ? { no_end: true } : undefined;
  }
}

function choiceKey(c: SnoozeChoice): string {
  switch (c.kind) {
    case "duration":
      return `d${c.seconds}`;
    case "preset":
      return c.preset;
    default:
      return c.kind;
  }
}

/** A choice of the dialog; a preset's pill has its end on a second line, which the radio names as its description. */
function ChoicePill({
  name,
  checked,
  onSelect,
  label,
  detail,
  testId,
  detailTestId,
}: {
  name: string;
  checked: boolean;
  onSelect: () => void;
  label: string;
  detail?: string;
  testId: string;
  detailTestId?: string;
}) {
  const id = useId();
  return (
    <label
      className={cn(
        "inline-flex min-h-9 max-w-full cursor-pointer flex-col justify-center rounded-lg border px-3 py-1 text-sm font-medium has-focus-visible:ring-3 has-focus-visible:ring-ring/50",
        checked
          ? "border-primary bg-primary text-primary-foreground"
          : "bg-background hover:bg-muted",
      )}
      data-testid={testId}
    >
      <input
        type="radio"
        name={name}
        className="sr-only"
        checked={checked}
        onChange={onSelect}
        aria-labelledby={`${id}-label`}
        aria-describedby={detail === undefined ? undefined : `${id}-detail`}
      />
      <span id={`${id}-label`} className="whitespace-nowrap">
        {label}
      </span>
      {detail !== undefined && (
        <span
          id={`${id}-detail`}
          className={cn(
            "text-xs font-normal",
            checked ? "text-primary-foreground/80" : "text-muted-foreground",
          )}
          data-testid={detailTestId}
        >
          {detail}
        </span>
      )}
    </label>
  );
}

/** A Snooze preset as the dialog offers it: its label and, once the server has answered, the end it would set. */
interface PresetChoice {
  preset: SnoozePreset;
  label: string;
  /** The end as the user reads it, "Ends Oct 20, 2026, 09:00"; undefined while it is read or when it cannot be. */
  end?: string;
}

function SnoozeForm({
  durations,
  presets,
  bulk,
  pending,
  error,
  onSnooze,
}: {
  durations: readonly number[];
  presets: readonly PresetChoice[];
  bulk: boolean;
  pending: boolean;
  error?: ReactNode;
  onSnooze: (request: SnoozeRequest) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const { timeZone } = useTimeFormat();
  const quick = [...new Set(durations)].filter((s) => s > 0).toSorted((a, b) => a - b);
  const choices: { choice: SnoozeChoice; label: string; detail?: string }[] = [
    ...quick.map((seconds) => ({
      choice: { kind: "duration", seconds } as const,
      label: snoozeDurationLabel(t, seconds),
    })),
    ...presets.map((p) => ({
      choice: { kind: "preset", preset: p.preset } as const,
      label: p.label,
      detail: p.end,
    })),
    { choice: { kind: "until" }, label: t("commands.snooze.until") },
    { choice: { kind: "no_end" }, label: t("commands.snooze.noEnd") },
  ];
  // The first choice is chosen: the shortest duration, else the first preset.
  const [choice, setChoice] = useState<SnoozeChoice>(() => choices[0]?.choice ?? { kind: "until" });
  const [initial] = useState(() => dateTimeParts(defaultUntil(Date.now()), timeZone));
  const [date, setDate] = useState(initial.date);
  const [time, setTime] = useState(initial.time);
  const [understood, setUnderstood] = useState(false);
  const until = instantOf(date, time, timeZone);
  const now = useNow();
  const request = snoozeRequest(choice, until, understood, now, timeZone);
  const untilInvalid = choice.kind === "until" && request === undefined;
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        // The quick durations count from the moment of the press.
        const sent = snoozeRequest(choice, until, understood, Date.now(), timeZone);
        if (sent !== undefined && !pending) {
          onSnooze(sent);
        }
      }}
    >
      <fieldset className="flex min-w-0 flex-col gap-2">
        <legend className="mb-2 text-sm font-medium">{t("commands.snooze.for")}</legend>
        <div className="flex flex-wrap gap-2">
          {choices.map((c) => (
            <ChoicePill
              key={choiceKey(c.choice)}
              name={`${id}-choice`}
              checked={choiceKey(c.choice) === choiceKey(choice)}
              onSelect={() => setChoice(c.choice)}
              label={c.label}
              detail={c.detail}
              testId={`snooze-choice-${choiceKey(c.choice)}`}
              detailTestId={`snooze-preset-end-${choiceKey(c.choice)}`}
            />
          ))}
        </div>
      </fieldset>
      {choice.kind === "until" && (
        <div className="flex min-w-0 flex-col gap-2">
          <div className="grid min-w-0 grid-cols-2 gap-3">
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-date`}>{t("commands.snooze.date")}</Label>
              <Input
                id={`${id}-date`}
                type="date"
                className="min-w-0"
                value={date}
                aria-invalid={untilInvalid}
                aria-describedby={`${id}-zone${untilInvalid ? ` ${id}-past` : ""}`}
                onChange={(e) => setDate(e.target.value)}
              />
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-time`}>{t("commands.snooze.time")}</Label>
              <Input
                id={`${id}-time`}
                type="time"
                className="min-w-0"
                value={time}
                aria-invalid={untilInvalid}
                aria-describedby={`${id}-zone${untilInvalid ? ` ${id}-past` : ""}`}
                onChange={(e) => setTime(e.target.value)}
              />
            </div>
          </div>
          <p id={`${id}-zone`} className="text-xs text-muted-foreground wrap-anywhere">
            {t("commands.snooze.timeZone", { zone: timeZone })}
          </p>
          {untilInvalid && (
            <p id={`${id}-past`} className="text-sm text-destructive">
              {until === undefined ? t("commands.snooze.incomplete") : t("commands.snooze.past")}
            </p>
          )}
        </div>
      )}
      {choice.kind === "no_end" && (
        <div className="flex min-w-0 flex-col gap-3">
          <Alert data-testid="snooze-no-end-warning">
            <TriangleAlertIcon aria-hidden="true" />
            <AlertDescription className="text-foreground">
              {bulk ? t("commands.snooze.noEndWarningBulk") : t("commands.snooze.noEndWarning")}
            </AlertDescription>
          </Alert>
          <div className="flex items-start gap-2">
            <input
              id={`${id}-understood`}
              type="checkbox"
              className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
              checked={understood}
              onChange={(e) => setUnderstood(e.target.checked)}
            />
            <Label htmlFor={`${id}-understood`} className="leading-snug">
              {t("commands.snooze.understood")}
            </Label>
          </div>
        </div>
      )}
      {error}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" type="button" />}>
          {t("common.cancel")}
        </DialogClose>
        <Button type="submit" disabled={request === undefined || pending} aria-busy={pending}>
          {t("commands.buttons.snooze")}
        </Button>
      </DialogFooter>
    </form>
  );
}

export interface SnoozeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  /** The Route's Snooze durations in seconds; none for a bulk Snooze. */
  durations?: readonly number[];
  /** The Route's durations are still being read; the form waits for them, so that it never starts over. */
  loading?: boolean;
  /** A Snooze of several Alert Groups, whose warning speaks of them all. */
  bulk?: boolean;
  pending: boolean;
  error?: ReactNode;
  onSnooze: (request: SnoozeRequest) => void;
}

/**
 * The content of the open dialog, mounted only while it is open: each opening reads the ends of the presets afresh, for
 * the user's time zone and on the server's clock. The labels come from the Organization's settings, and from the
 * settings the ends were computed with once those are read. The form waits for the Organization as it waits for the
 * Route's durations, so that it never starts over; without either answer the presets have no label and are left out.
 */
function SnoozeContent({
  durations,
  loading,
  bulk,
  pending,
  error,
  onSnooze,
}: {
  durations: readonly number[];
  loading: boolean;
  bulk: boolean;
  pending: boolean;
  error?: ReactNode;
  onSnooze: (request: SnoozeRequest) => void;
}) {
  const { t } = useTranslation();
  const { timeZone, dateTime } = useTimeFormat();
  const organization = useGetOrganization();
  const ends = useListSnoozePresets(
    { time_zone: timeZone },
    { query: { staleTime: 0, gcTime: 0 } },
  );
  if (loading || organization.isPending) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {t("common.loading")}
      </p>
    );
  }
  const settings: { workday_start: string; week_start: Weekday } | undefined =
    ends.data ?? organization.data;
  const presets: PresetChoice[] =
    settings === undefined
      ? []
      : Object.values(SnoozePreset).map((preset) => {
          const until = ends.data?.items.find((i) => i.preset === preset)?.until;
          return {
            preset,
            label: snoozePresetLabel(t, preset, settings.workday_start, settings.week_start),
            end:
              until === undefined
                ? undefined
                : t("commands.snooze.presetEnds", { time: dateTime(until) }),
          };
        });
  return (
    <SnoozeForm
      durations={durations}
      presets={presets}
      bulk={bulk}
      pending={pending}
      error={error}
      onSnooze={onSnooze}
    />
  );
}

export function SnoozeDialog({
  open,
  onOpenChange,
  title,
  durations = [],
  loading = false,
  bulk = false,
  pending,
  error,
  onSnooze,
}: SnoozeDialogProps) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")} className={SHEET} data-testid="snooze-dialog">
        <DialogHeader>
          <DialogTitle className="pr-8 wrap-anywhere">{title}</DialogTitle>
          <DialogDescription>{t("commands.snooze.description")}</DialogDescription>
        </DialogHeader>
        <SnoozeContent
          durations={durations}
          loading={loading}
          bulk={bulk}
          pending={pending}
          error={error}
          onSnooze={onSnooze}
        />
      </DialogContent>
    </Dialog>
  );
}
