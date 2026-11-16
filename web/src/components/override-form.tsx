// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Override form (C-27.FR-3, FR-6, C-27.AC-2): "I take the Shift of…" or "I give my Shift to…" on a segment picked in
// the row "Result" of the view, then who takes it, and from and to within that segment — the form never sends a span
// outside it. It sends `user_id` (who takes the Shift), `replaces_user_id` (the segment's person, if any) and the span;
// an Override of exactly the segment is the one that keeps its start and end as they came. A refusal of the server —
// `not_in_team`, `not_pageable`, a span out of range — shows its message in the form. The same file has the form that
// changes an Override: its end, and while it has not begun its people and start.

import { useMutation } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createScheduleOverride,
  updateScheduleOverride,
} from "../api/gen/endpoints/schedules/schedules";
import type {
  NotPageableReason,
  OnCallSegment,
  ScheduleOverride,
  ScheduleOverrideInput,
  UserRef,
} from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { clock, fromLocalInput, stamp, toLocalInput } from "../lib/schedule-time";
import { notPageableText } from "./rotation-layers-editor";
import { personLabel } from "./schedule-view";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export type OverrideMode = "take" | "give";

/** The refusal of a save by field: "user_id", "start", "end", or "" for the form as a whole. */
interface Refusal {
  field: "user_id" | "replaces_user_id" | "start" | "end" | "";
  code: string;
  reason?: NotPageableReason;
}

function refusalOf(err: unknown): Refusal | null {
  if (!isApiError(err)) {
    return null;
  }
  const first = err.errors?.[0];
  if (first !== undefined) {
    const field = first.pointer.slice(1);
    if (
      field === "user_id" ||
      field === "replaces_user_id" ||
      field === "start" ||
      field === "end"
    ) {
      return first.reason === undefined
        ? { field, code: first.code }
        : { field, code: first.code, reason: first.reason };
    }
  }
  return { field: "", code: err.code ?? "" };
}

/** What the form says about a refusal of the server. */
function refusalText(
  t: ReturnType<typeof useTranslation>["t"],
  refusal: Refusal,
  err: unknown,
): string {
  if (refusal.code === "not_in_team") {
    return t("schedule.override.notInTeam");
  }
  if (refusal.code === "not_pageable") {
    return notPageableText(t, refusal.reason);
  }
  if (refusal.code === "override_ended") {
    return t("schedule.override.ended");
  }
  if (refusal.field !== "") {
    return fieldErrorText(t, refusal.code);
  }
  return problemText(t, err);
}

export interface OverrideFormProps {
  scheduleId: string;
  zone: string;
  /** The business clock's now. */
  now: Date;
  /** The signed-in User, who takes the Shift in "I take the Shift of…". */
  me: UserRef;
  /** The people of the Team who may take a Shift. */
  people: readonly UserRef[];
  /** The segment of the row "Result" the person picked, if any. */
  segment: OnCallSegment | null;
  onCancel: () => void;
  onSaved: () => void;
}

/**
 * The span an Override on the segment may cover: from its start — or from now, for a segment that has begun — to its
 * end. It is taken once, when the segment is picked, so that the fields do not fall outside it when the clock moves on.
 */
function boundsOf(segment: OnCallSegment | null, now: Date): { first: Date; last: Date } | null {
  if (segment === null) {
    return null;
  }
  const start = new Date(segment.start);
  return { first: start < now ? now : start, last: new Date(segment.end) };
}

/** "11:00" for a time on one day, "Wed 11:00" when the span crosses midnight. */
function spanTexts(
  from: Date,
  to: Date,
  zone: string,
  locale: string,
): { from: string; to: string } {
  const sameDay = toLocalInput(from, zone).slice(0, 10) === toLocalInput(to, zone).slice(0, 10);
  return sameDay
    ? { from: clock(from, zone, locale), to: clock(to, zone, locale) }
    : { from: stamp(from, zone, locale), to: stamp(to, zone, locale) };
}

export function OverrideForm({
  scheduleId,
  zone,
  now,
  me,
  people,
  segment,
  onCancel,
  onSaved,
}: OverrideFormProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const [mode, setMode] = useState<OverrideMode>("take");
  const [taker, setTaker] = useState("");
  // The span the segment gives, and what the fields hold: a field left as it came keeps the exact instant.
  const [bounds, setBounds] = useState(() => boundsOf(segment, now));
  const first = bounds?.first ?? null;
  const last = bounds?.last ?? null;
  const [from, setFrom] = useState(first === null ? "" : toLocalInput(first, zone));
  const [to, setTo] = useState(last === null ? "" : toLocalInput(last, zone));
  const [picked, setPicked] = useState(segment);
  const submit = useMutation({
    mutationFn: (input: ScheduleOverrideInput) => createScheduleOverride(scheduleId, input),
    onSuccess: onSaved,
  });
  // Another segment picked in the view starts the fields again, and forgets what the server said about the last one.
  if (segment !== picked) {
    const next = boundsOf(segment, now);
    setPicked(segment);
    setBounds(next);
    setFrom(next === null ? "" : toLocalInput(next.first, zone));
    setTo(next === null ? "" : toLocalInput(next.last, zone));
    submit.reset();
  }
  const refusal = refusalOf(submit.error);
  const fromAt =
    first !== null && from === toLocalInput(first, zone) ? first : fromLocalInput(from, zone);
  const toAt = last !== null && to === toLocalInput(last, zone) ? last : fromLocalInput(to, zone);
  const outside =
    first !== null &&
    last !== null &&
    fromAt !== null &&
    toAt !== null &&
    (fromAt < first || toAt > last || toAt <= fromAt);
  const who = mode === "take" ? me.id : taker;
  // The refusals of the fields the form shows are placed at them; any other is shown as a whole.
  const placed =
    refusal !== null &&
    (refusal.field === "start" ||
      refusal.field === "end" ||
      (refusal.field === "user_id" && mode === "give"));
  const ready = segment !== null && fromAt !== null && toAt !== null && !outside && who !== "";
  const options = people.filter((p) => p.id !== segment?.user?.id);
  const texts =
    fromAt !== null && toAt !== null ? spanTexts(fromAt, toAt, zone, locale) : { from: "", to: "" };
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4 rounded-lg border p-3"
      data-testid="override-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready || fromAt === null || toAt === null) {
          return;
        }
        submit.mutate({
          user_id: who,
          replaces_user_id: segment?.user?.id ?? null,
          start: fromAt.toISOString(),
          end: toAt.toISOString(),
        });
      }}
    >
      <h3 className="text-base font-semibold">{t("schedule.override.title")}</h3>
      <fieldset className="flex flex-wrap gap-x-6 gap-y-2">
        <legend className="sr-only">{t("schedule.override.mode")}</legend>
        {(["take", "give"] as const).map((m) => (
          <label key={m} className="flex items-center gap-2 text-sm">
            <input
              type="radio"
              name={`${id}-mode`}
              className="size-4 accent-primary"
              checked={mode === m}
              onChange={() => {
                setMode(m);
                submit.reset();
              }}
            />
            {m === "take" ? t("schedule.override.take") : t("schedule.override.give")}
          </label>
        ))}
      </fieldset>
      <p className="text-sm text-muted-foreground" data-testid="override-hint">
        {segment === null ? t("schedule.override.hintEmpty") : t("schedule.override.hint", texts)}
      </p>
      {segment !== null && (
        <p className="text-sm" data-testid="override-segment">
          {t("schedule.override.segment", {
            person: segment.user === null ? t("schedule.view.gap") : personLabel(segment.user),
            when: `${stamp(segment.start, zone, locale)} – ${stamp(segment.end, zone, locale)}`,
          })}
        </p>
      )}
      {mode === "give" ? (
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-who`}>{t("schedule.override.who")}</Label>
          <NativeSelect
            id={`${id}-who`}
            className="w-full sm:w-64"
            value={taker}
            aria-invalid={refusal?.field === "user_id"}
            aria-describedby={refusal?.field === "user_id" ? `${id}-who-error` : undefined}
            onChange={(e) => setTaker(e.target.value)}
          >
            <NativeSelectOption value="">{t("schedule.override.choosePerson")}</NativeSelectOption>
            {options.map((p) => (
              <NativeSelectOption key={p.id} value={p.id}>
                {personLabel(p)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          {refusal?.field === "user_id" && (
            <p id={`${id}-who-error`} className="text-sm text-destructive">
              {refusalText(t, refusal, submit.error)}
            </p>
          )}
        </div>
      ) : (
        <p className="text-sm" data-testid="override-taker">
          {t("schedule.override.takerYou", { who: personLabel(me) })}
        </p>
      )}
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-from`}>{t("schedule.override.from")}</Label>
          <Input
            id={`${id}-from`}
            type="datetime-local"
            className="w-56"
            disabled={segment === null}
            min={first === null ? undefined : toLocalInput(first, zone)}
            max={last === null ? undefined : toLocalInput(last, zone)}
            value={from}
            aria-invalid={outside || refusal?.field === "start"}
            onChange={(e) => setFrom(e.target.value)}
          />
          {refusal?.field === "start" && (
            <p className="text-sm text-destructive">{refusalText(t, refusal, submit.error)}</p>
          )}
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-to`}>{t("schedule.override.to")}</Label>
          <Input
            id={`${id}-to`}
            type="datetime-local"
            className="w-56"
            disabled={segment === null}
            min={first === null ? undefined : toLocalInput(first, zone)}
            max={last === null ? undefined : toLocalInput(last, zone)}
            value={to}
            aria-invalid={outside || refusal?.field === "end"}
            onChange={(e) => setTo(e.target.value)}
          />
          {refusal?.field === "end" && (
            <p className="text-sm text-destructive">{refusalText(t, refusal, submit.error)}</p>
          )}
        </div>
      </div>
      {outside && (
        <p className="text-sm text-destructive" role="alert" data-testid="override-outside">
          {t("schedule.override.outside")}
        </p>
      )}
      {submit.isError && refusal !== null && !placed && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {refusalText(t, refusal, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={!ready || submit.isPending}>
          {t("common.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

export interface OverrideEditFormProps {
  scheduleId: string;
  zone: string;
  now: Date;
  override: ScheduleOverride;
  people: readonly UserRef[];
  onCancel: () => void;
  onSaved: () => void;
  /** Reads the Override again, which a change another person made meanwhile asks for. */
  onReload: () => void;
}

/**
 * Changes an Override: its people and its start while it has not begun, its end always — an Override that has begun
 * keeps its past as it was, so the API takes only a new end.
 */
export function OverrideEditForm({
  scheduleId,
  zone,
  now,
  override,
  people,
  onCancel,
  onSaved,
  onReload,
}: OverrideEditFormProps) {
  const { t } = useTranslation();
  const id = useId();
  const begun = new Date(override.start) <= now;
  const [taker, setTaker] = useState(override.user_id);
  const [replaces, setReplaces] = useState(override.replaces_user_id ?? "");
  const [from, setFrom] = useState(toLocalInput(new Date(override.start), zone));
  const [to, setTo] = useState(toLocalInput(new Date(override.end), zone));
  const submit = useMutation({
    mutationFn: (input: ScheduleOverrideInput) =>
      updateScheduleOverride(scheduleId, override.id, input, {
        headers: { "If-Match": override.etag },
      }),
    onSuccess: onSaved,
  });
  const refusal = refusalOf(submit.error);
  const fromAt =
    from === toLocalInput(new Date(override.start), zone)
      ? new Date(override.start)
      : fromLocalInput(from, zone);
  const toAt =
    to === toLocalInput(new Date(override.end), zone)
      ? new Date(override.end)
      : fromLocalInput(to, zone);
  const valid = fromAt !== null && toAt !== null && toAt > fromAt && toAt > now;
  const listed = people.some((p) => p.id === override.user_id)
    ? people
    : [override.user, ...people];
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
      data-testid="override-edit-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (!valid || fromAt === null || toAt === null) {
          return;
        }
        submit.mutate({
          user_id: taker,
          replaces_user_id: replaces === "" ? null : replaces,
          start: fromAt.toISOString(),
          end: toAt.toISOString(),
        });
      }}
    >
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-taker`}>{t("schedule.override.who")}</Label>
          <NativeSelect
            id={`${id}-taker`}
            className="w-56"
            value={taker}
            disabled={begun}
            aria-invalid={refusal?.field === "user_id"}
            onChange={(e) => setTaker(e.target.value)}
          >
            {listed.map((p) => (
              <NativeSelectOption key={p.id} value={p.id}>
                {personLabel(p)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-replaces`}>{t("schedule.override.replaces")}</Label>
          <NativeSelect
            id={`${id}-replaces`}
            className="w-56"
            value={replaces}
            disabled={begun}
            onChange={(e) => setReplaces(e.target.value)}
          >
            <NativeSelectOption value="">{t("schedule.override.nobody")}</NativeSelectOption>
            {override.replaces_user !== null &&
              !people.some((p) => p.id === override.replaces_user_id) && (
                <NativeSelectOption value={override.replaces_user.id}>
                  {personLabel(override.replaces_user)}
                </NativeSelectOption>
              )}
            {people.map((p) => (
              <NativeSelectOption key={p.id} value={p.id}>
                {personLabel(p)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-from`}>{t("schedule.override.from")}</Label>
          <Input
            id={`${id}-from`}
            type="datetime-local"
            className="w-56"
            disabled={begun}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-to`}>{t("schedule.override.to")}</Label>
          <Input
            id={`${id}-to`}
            type="datetime-local"
            className="w-56"
            value={to}
            aria-invalid={!valid || refusal?.field === "end"}
            onChange={(e) => setTo(e.target.value)}
          />
        </div>
      </div>
      {submit.isError && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {isStale(submit.error)
              ? t("schedule.override.stale")
              : refusalText(t, refusal ?? { field: "", code: "" }, submit.error)}
          </AlertDescription>
          {(isStale(submit.error) || refusal?.code === "override_ended") && (
            <Button type="button" variant="outline" size="sm" onClick={onReload}>
              {t("common.reload")}
            </Button>
          )}
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={!valid || submit.isPending}>
          {t("common.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
