// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "On call now" of a Schedule (C-27.FR-8, FR-9): the person, the Rotation layer whose Shift makes them On-call — or the
// Override — and until when, a mark for each Destination of the Team that a Mention of them cannot reach, and what
// comes next: the next person from a time, or a gap. It reads the Result from now on, whose first segment is the
// current one.

import { useTranslation } from "react-i18next";

import type { ScheduleOnCall } from "../api/gen/model";
import { DocsLink } from "./docs-link";
import { stampNear } from "../lib/schedule-time";
import { personLabel } from "./schedule-view";
import { unreachableText } from "./schedule-warnings";

export function ScheduleOnCallNow({ data, zone }: { data: ScheduleOnCall; zone: string }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = new Date(data.from);
  const current = data.segments.find((s) => new Date(s.end) > now);
  const next =
    current === undefined ? undefined : data.segments[data.segments.indexOf(current) + 1];
  const lasts = current !== undefined && new Date(current.end) < new Date(data.to);
  const marks = data.warnings.filter(
    (w) =>
      w.kind === "unreachable" &&
      w.start !== null &&
      w.end !== null &&
      new Date(w.start) <= now &&
      now < new Date(w.end),
  );
  const via =
    current?.source?.kind === "override"
      ? t("schedule.onCall.override")
      : current?.source?.rotation_layer?.name;
  return (
    <section
      className="flex flex-col gap-2"
      aria-labelledby="schedule-on-call-title"
      data-testid="schedule-on-call-now"
    >
      <h2 id="schedule-on-call-title" className="text-lg font-semibold">
        {t("schedule.onCall.title")}
      </h2>
      {current === undefined || current.user === null ? (
        <p className="text-sm" data-testid="schedule-on-call-person">
          {t("schedule.onCall.nobody")}
          {current !== undefined && lasts && (
            <> {t("schedule.onCall.until", { time: stampNear(current.end, now, zone, locale) })}</>
          )}
        </p>
      ) : (
        <p
          className="flex flex-wrap items-baseline gap-x-2 text-sm"
          data-testid="schedule-on-call-person"
        >
          <span className="text-base font-semibold">{personLabel(current.user)}</span>
          {via !== undefined && <span className="text-muted-foreground">{via}</span>}
          {lasts && (
            <span className="text-muted-foreground">
              {t("schedule.onCall.until", { time: stampNear(current.end, now, zone, locale) })}
            </span>
          )}
        </p>
      )}
      {marks.length > 0 && (
        <div className="flex flex-wrap items-baseline gap-x-3">
          <ul className="flex flex-col gap-0.5" data-testid="schedule-unreachable">
            {marks.map((w) => (
              <li
                key={`${w.destination?.id}-${w.reason}`}
                className="text-sm text-destructive"
                data-reason={w.reason ?? undefined}
              >
                {unreachableText(t, w)}
              </li>
            ))}
          </ul>
          <DocsLink
            to="field.unreachable_on_call"
            kind="learn_more"
            about={t("schedule.onCall.unreachable")}
          />
        </div>
      )}
      {next !== undefined && (
        <p className="text-sm text-muted-foreground" data-testid="schedule-next">
          {next.user === null
            ? t("schedule.onCall.nextGap", { time: stampNear(next.start, now, zone, locale) })
            : t("schedule.onCall.nextPerson", {
                who: personLabel(next.user),
                time: stampNear(next.start, now, zone, locale),
              })}
        </p>
      )}
    </section>
  );
}
