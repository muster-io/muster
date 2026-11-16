// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The two-week view of a Schedule (C-27.FR-4, FR-5, FR-7): columns per day in the Schedule's time zone, a row per
// Rotation layer, top first, with its Shifts — those of a person who may not be Paged hatched — a row of Overrides and
// the row "Result — who is On-call", whose gaps are red and whose segments are buttons that the Override form takes,
// a mark for now, and "Previous week", "Today" and "Next week". Positions follow real time, so a day of a change of
// the clocks is narrower or wider than the others.

import type { TFunction } from "i18next";
import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type {
  NotPageableReason,
  OnCallSegment,
  ScheduleOnCall,
  ScheduleOverride,
  UserRef,
} from "../api/gen/model";
import { type ViewRange, dayHeading, fractionOf, span, within } from "../lib/schedule-time";
import { Button } from "./ui/button";
import { cn } from "./ui/utils";

/** "@login" of a person, or the name when there is no login. */
export function personLabel(user: Pick<UserRef, "login" | "name">): string {
  return user.login !== undefined && user.login !== "" ? `@${user.login}` : user.name;
}

/** A hue for a person, the same on every row and every visit. */
function hueOf(id: string): number {
  let hash = 0;
  for (const char of id) {
    hash = (hash * 31 + char.charCodeAt(0)) % 360;
  }
  return hash;
}

function personColors(id: string): { backgroundColor: string; color: string } {
  return { backgroundColor: `oklch(0.82 0.09 ${hueOf(id)})`, color: "oklch(0.2 0 0)" };
}

/** Why a Shift is skipped, as its accessible name says it. */
function skippedText(t: TFunction, reason: NotPageableReason): string {
  switch (reason) {
    case "disabled":
      return t("schedule.view.skippedDisabled");
    case "viewer":
      return t("schedule.view.skippedViewer");
    default:
      return t("schedule.view.skippedNotInTeam");
  }
}

const HATCH = "repeating-linear-gradient(135deg, transparent 0 5px, oklch(0.2 0 0 / 0.35) 5px 7px)";
const ROW = "h-9";

interface Placed {
  left: string;
  width: string;
}

/** The place of a time range on the track, as percentages. */
function placed(start: string, end: string, range: ViewRange): Placed | null {
  const from = fractionOf(start, range);
  const to = fractionOf(end, range);
  if (to <= from) {
    return null;
  }
  return { left: `${from * 100}%`, width: `${(to - from) * 100}%` };
}

export interface ScheduleViewProps {
  zone: string;
  data: ScheduleOnCall;
  range: ViewRange;
  /** The business clock's now, for the mark; null while it is not known. */
  now: Date | null;
  onPrevious: () => void;
  onToday: () => void;
  onNext: () => void;
  /** Called with the segment of the Result a person picks; left out for those who cannot make an Override. */
  onPick?: (segment: OnCallSegment) => void;
  picked?: OnCallSegment | null;
  /** The days shown are those of the week before, until the Result of this one has been read. */
  loading?: boolean;
}

function Track({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("relative", ROW, className)}>{children}</div>;
}

function OverrideBlock({
  override,
  range,
  zone,
  locale,
  label,
}: {
  override: ScheduleOverride;
  range: ViewRange;
  zone: string;
  locale: string;
  label: string;
}) {
  const place = placed(override.start, override.end, range);
  if (place === null) {
    return null;
  }
  return (
    <div
      className="absolute inset-y-0.5 overflow-hidden rounded-sm border border-foreground/40 px-1 text-xs leading-8 whitespace-nowrap"
      style={{ ...place, ...personColors(override.user_id) }}
      title={label}
      role="img"
      aria-label={label}
      data-testid="schedule-override-block"
    >
      {override.replaces_user !== null && `${personLabel(override.replaces_user)} → `}
      {personLabel(override.user)}
      <span className="sr-only">{span(override.start, override.end, zone, locale)}</span>
    </div>
  );
}

export function ScheduleView({
  zone,
  data,
  range,
  now,
  onPrevious,
  onToday,
  onNext,
  onPick,
  picked = null,
  loading = false,
}: ScheduleViewProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  // Rotation layers are listed from the lowest; the view shows the top one first.
  const layers = data.rotation_layers.toReversed();
  const nowFraction = now !== null && within(now, range) ? fractionOf(now, range) : null;
  return (
    <section className="flex min-w-0 flex-col gap-3" data-testid="schedule-view">
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onPrevious}>
          <ChevronLeftIcon aria-hidden="true" />
          {t("schedule.view.previous")}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onToday}>
          {t("schedule.view.today")}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onNext}>
          {t("schedule.view.next")}
          <ChevronRightIcon aria-hidden="true" />
        </Button>
        <span className="text-sm text-muted-foreground" data-testid="schedule-view-range">
          {dayHeading(range.days[0]?.day ?? "", locale)} –{" "}
          {dayHeading(range.days.at(-1)?.day ?? "", locale)}
        </span>
      </div>
      <div
        className={cn("overflow-x-auto rounded-lg border", loading && "opacity-50")}
        aria-busy={loading}
        data-testid="schedule-view-scroll"
      >
        <div className="flex min-w-[64rem]">
          <div className="sticky left-0 z-20 flex w-32 shrink-0 flex-col gap-1 border-r bg-background p-1">
            <div className="h-6" />
            {layers.map((layer) => (
              <div
                key={layer.rotation_layer.id}
                className={cn(ROW, "flex items-center text-sm font-medium wrap-anywhere")}
                data-testid="schedule-row-label"
              >
                {layer.rotation_layer.name}
              </div>
            ))}
            <div
              className={cn(ROW, "flex items-center text-sm font-medium")}
              data-testid="schedule-row-label"
            >
              {t("schedule.view.overrides")}
            </div>
            <div
              className={cn(ROW, "flex items-center text-sm font-semibold")}
              data-testid="schedule-row-label"
            >
              {t("schedule.view.result")}
            </div>
          </div>
          <div className="relative flex min-w-0 flex-1 flex-col gap-1 p-1">
            <div className="relative h-6" data-testid="schedule-days">
              {range.days.map((day) => {
                const place = placed(day.start.toISOString(), day.end.toISOString(), range);
                return (
                  <div
                    key={day.day}
                    className="absolute inset-y-0 truncate border-l px-1 text-xs text-muted-foreground"
                    style={place ?? undefined}
                    data-day={day.day}
                  >
                    {dayHeading(day.day, locale)}
                  </div>
                );
              })}
            </div>
            {layers.map((layer) => (
              <Track key={layer.rotation_layer.id} className="rounded-sm bg-muted/40">
                <div data-testid="schedule-layer-row" data-layer={layer.rotation_layer.name}>
                  {layer.shifts.map((shift) => {
                    const place = placed(shift.start, shift.end, range);
                    if (place === null) {
                      return null;
                    }
                    const name = `${personLabel(shift.user)}, ${span(shift.start, shift.end, zone, locale)}`;
                    return (
                      <div
                        key={`${shift.user.id}-${shift.start}`}
                        className="absolute inset-y-0.5 overflow-hidden rounded-sm border border-foreground/30 px-1 text-xs leading-8 whitespace-nowrap"
                        style={{
                          ...place,
                          ...personColors(shift.user.id),
                          ...(shift.skipped === null ? {} : { backgroundImage: HATCH }),
                        }}
                        title={name}
                        role="img"
                        aria-label={
                          shift.skipped === null
                            ? name
                            : `${name}, ${skippedText(t, shift.skipped)}`
                        }
                        data-testid="schedule-shift"
                        data-skipped={shift.skipped ?? undefined}
                      >
                        {personLabel(shift.user)}
                      </div>
                    );
                  })}
                </div>
              </Track>
            ))}
            <Track className="rounded-sm bg-muted/40">
              <div data-testid="schedule-override-row">
                {data.overrides.map((override) => (
                  <OverrideBlock
                    key={override.id}
                    override={override}
                    range={range}
                    zone={zone}
                    locale={locale}
                    label={`${personLabel(override.user)}, ${span(override.start, override.end, zone, locale)}`}
                  />
                ))}
              </div>
            </Track>
            <Track className="rounded-sm bg-muted/40">
              <div data-testid="schedule-result-row">
                {data.segments.map((segment) => {
                  const place = placed(segment.start, segment.end, range);
                  if (place === null) {
                    return null;
                  }
                  const person = segment.user;
                  const gap = person === null;
                  const when = span(segment.start, segment.end, zone, locale);
                  const name =
                    person === null
                      ? t("schedule.view.gapAt", { when })
                      : `${personLabel(person)}, ${when}`;
                  const isPicked =
                    picked !== null && picked.start === segment.start && picked.end === segment.end;
                  // A segment that has ended is the past, which an Override may not rewrite.
                  const pickable =
                    onPick !== undefined && (now === null || new Date(segment.end) > now);
                  const classes = cn(
                    "absolute inset-y-0.5 overflow-hidden rounded-sm border px-1 text-left text-xs leading-8 whitespace-nowrap",
                    gap
                      ? "border-destructive bg-destructive/25 text-destructive"
                      : "border-foreground/30",
                    pickable &&
                      "cursor-pointer outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    isPicked && "ring-2 ring-primary ring-offset-1",
                  );
                  const style = person === null ? place : { ...place, ...personColors(person.id) };
                  const text = person === null ? t("schedule.view.gap") : personLabel(person);
                  if (!pickable) {
                    return (
                      <div
                        key={`${segment.start}-${segment.end}`}
                        className={classes}
                        style={style}
                        title={name}
                        role="img"
                        aria-label={name}
                        data-testid="schedule-segment"
                        data-start={segment.start}
                        data-end={segment.end}
                        data-gap={gap ? "true" : undefined}
                      >
                        {text}
                      </div>
                    );
                  }
                  return (
                    <button
                      key={`${segment.start}-${segment.end}`}
                      type="button"
                      className={classes}
                      style={style}
                      title={name}
                      aria-label={name}
                      aria-pressed={isPicked}
                      onClick={() => onPick?.(segment)}
                      data-testid="schedule-segment"
                      data-start={segment.start}
                      data-end={segment.end}
                      data-gap={gap ? "true" : undefined}
                    >
                      {text}
                    </button>
                  );
                })}
              </div>
            </Track>
            {nowFraction !== null && (
              <div
                className="pointer-events-none absolute inset-y-0 z-10 w-0.5 bg-primary"
                style={{ left: `calc(0.25rem + (100% - 0.5rem) * ${nowFraction})` }}
                role="img"
                aria-label={t("schedule.view.now")}
                data-testid="schedule-now"
              >
                <span className="absolute top-0 left-1 rounded-sm bg-primary px-1 text-[0.65rem] leading-4 text-primary-foreground">
                  {t("schedule.view.now")}
                </span>
              </div>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}
