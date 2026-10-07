// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze dialog (C-10.FR-6): the Route's Snooze durations as quick choices ("1 h", "4 h", "24 h"), "Until" with
// native date and time inputs in the user's time zone (D250), sent as `until` in UTC, and "No end", which shows the
// warning of reference.md and enables "Snooze" only once "I understand" is ticked (sent as `{"no_end": true}`). A bulk
// Snooze has no Route, so it offers "Until" and "No end" only. A phone shows the dialog as a full-screen sheet.

import { TZDate } from "@date-fns/tz";
import { TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import type { SnoozeRequest } from "../api/gen/model";
import { SHEET, snoozeDurationLabel } from "../lib/commands";
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

/** The choice of the dialog: a quick duration in seconds, an end of the user's choosing, or no end. */
export type SnoozeChoice =
  | { kind: "duration"; seconds: number }
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

/** The request of a choice at an instant, or undefined while it cannot be sent. */
export function snoozeRequest(
  choice: SnoozeChoice,
  until: Date | undefined,
  understood: boolean,
  now: number,
): SnoozeRequest | undefined {
  switch (choice.kind) {
    case "duration":
      return { until: new Date(now + choice.seconds * 1000).toISOString() };
    case "until":
      return until === undefined || until.getTime() <= now
        ? undefined
        : { until: until.toISOString() };
    default:
      return understood ? { no_end: true } : undefined;
  }
}

const choiceKey = (c: SnoozeChoice) => (c.kind === "duration" ? `d${c.seconds}` : c.kind);

function ChoicePill({
  name,
  checked,
  onSelect,
  children,
  testId,
}: {
  name: string;
  checked: boolean;
  onSelect: () => void;
  children: ReactNode;
  testId: string;
}) {
  return (
    <label
      className={cn(
        "inline-flex h-9 cursor-pointer items-center rounded-lg border px-3 text-sm font-medium whitespace-nowrap has-focus-visible:ring-3 has-focus-visible:ring-ring/50",
        checked
          ? "border-primary bg-primary text-primary-foreground"
          : "bg-background hover:bg-muted",
      )}
      data-testid={testId}
    >
      <input type="radio" name={name} className="sr-only" checked={checked} onChange={onSelect} />
      {children}
    </label>
  );
}

function SnoozeForm({
  durations,
  bulk,
  pending,
  error,
  onSnooze,
}: {
  durations: readonly number[];
  bulk: boolean;
  pending: boolean;
  error?: ReactNode;
  onSnooze: (request: SnoozeRequest) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const { timeZone } = useTimeFormat();
  const quick = [...new Set(durations)].filter((s) => s > 0).toSorted((a, b) => a - b);
  const first = quick[0];
  const [choice, setChoice] = useState<SnoozeChoice>(
    first === undefined ? { kind: "until" } : { kind: "duration", seconds: first },
  );
  const [initial] = useState(() => dateTimeParts(defaultUntil(Date.now()), timeZone));
  const [date, setDate] = useState(initial.date);
  const [time, setTime] = useState(initial.time);
  const [understood, setUnderstood] = useState(false);
  const until = instantOf(date, time, timeZone);
  const now = useNow();
  const request = snoozeRequest(choice, until, understood, now);
  const untilInvalid = choice.kind === "until" && request === undefined;
  const choices: { choice: SnoozeChoice; label: string }[] = [
    ...quick.map((seconds) => ({
      choice: { kind: "duration", seconds } as const,
      label: snoozeDurationLabel(t, seconds),
    })),
    { choice: { kind: "until" }, label: t("commands.snooze.until") },
    { choice: { kind: "no_end" }, label: t("commands.snooze.noEnd") },
  ];
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        // The quick durations count from the moment of the press.
        const sent = snoozeRequest(choice, until, understood, Date.now());
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
              testId={`snooze-choice-${choiceKey(c.choice)}`}
            >
              {c.label}
            </ChoicePill>
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
        {loading ? (
          <p className="text-sm text-muted-foreground" role="status">
            {t("common.loading")}
          </p>
        ) : (
          <SnoozeForm
            durations={durations}
            bulk={bulk}
            pending={pending}
            error={error}
            onSnooze={onSnooze}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
