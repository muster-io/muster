// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "On call now" (C-27.FR-8, FR-9; C-27.AC-6, AC-9): for each Team, the On-call of its main Schedule with the Schedule's
// name and until when, a mark for each messenger in which a Mention of them cannot reach them, "next: gap" when a gap
// follows, and "No Schedule" for a Team without one. It is a section of the Home page, with the switch "All Teams" /
// "My Teams", and of each Team's page, where only that Team's row shows; the Teams list shows the On-call of each Team
// in a column. The times are in the signed-in User's time zone, and the page reads again every half minute, so that
// it follows the business clock.

import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { getGetOnCallNowQueryKey, getOnCallNow } from "../api/gen/endpoints/schedules/schedules";
import type { DestinationType, TeamOnCall } from "../api/gen/model";
import { problemText } from "../lib/api";
import { stamp } from "../lib/schedule-time";
import { useTimeFormat } from "../lib/time";
import { destinationTypeName } from "./destination-health";
import { DocsLink } from "./docs-link";
import { personLabel } from "./schedule-view";
import { Button } from "./ui/button";

/** Which Teams "On call now" lists: every Team, or the Teams of the signed-in User. */
export type OnCallScope = "all" | "mine";

/** How often the lists read "On call now" again, so that they follow the business clock. */
const REFRESH_MS = 30_000;

/** "On call now" of every Team, or of the Teams of the signed-in User. */
export function useOnCallNow(scope: OnCallScope) {
  const params = { teams: scope };
  return useQuery({
    queryKey: getGetOnCallNowQueryKey(params),
    queryFn: ({ signal }) => getOnCallNow(params, { signal }),
    refetchInterval: REFRESH_MS,
    staleTime: 0,
    select: (list): TeamOnCall[] => list.items,
  });
}

/** Each messenger of the unreachable Destinations once, in the order they come. */
function messengersOf(item: TeamOnCall): DestinationType[] {
  return [...new Set(item.unreachable.map((reach) => reach.messenger))];
}

/** Whether a gap follows the person who is On-call now. */
function gapFollows(item: TeamOnCall): boolean {
  return item.user !== null && item.next !== null && item.next.user === null;
}

/** The marks of an On-call: "unreachable in Mattermost" for each messenger, and "next: gap". */
export function OnCallMarks({ item }: { item: TeamOnCall }) {
  const { t } = useTranslation();
  const messengers = messengersOf(item);
  const gap = gapFollows(item);
  if (messengers.length === 0 && !gap) {
    return null;
  }
  return (
    <span className="flex flex-wrap gap-1.5" data-testid="on-call-marks">
      {messengers.map((messenger) => (
        <span
          key={messenger}
          className="rounded-full border border-warning/60 bg-warning-surface px-2 py-px text-xs font-semibold text-foreground"
          data-testid="on-call-unreachable"
        >
          {t("onCallNow.unreachable", { messenger: destinationTypeName(t, messenger) })}
        </span>
      ))}
      {gap && (
        <span
          className="rounded-full bg-destructive/10 px-2 py-px text-xs font-semibold text-destructive"
          data-testid="on-call-gap"
        >
          {t("onCallNow.nextGap")}
        </span>
      )}
    </span>
  );
}

/** The person On-call now, or "Nobody on call" in a gap, or "No Schedule". */
export function OnCallPerson({ item }: { item: TeamOnCall }) {
  const { t } = useTranslation();
  if (item.no_schedule) {
    return <span className="text-muted-foreground">{t("onCallNow.noSchedule")}</span>;
  }
  return item.user === null ? (
    <span className="text-muted-foreground">{t("onCallNow.nobody")}</span>
  ) : (
    <span className="font-medium wrap-anywhere">{personLabel(item.user)}</span>
  );
}

/** One Team's row: the Team, the On-call, the Schedule and until when, and the marks. */
export function OnCallRow({ item, linkTeam = true }: { item: TeamOnCall; linkTeam?: boolean }) {
  const { t, i18n } = useTranslation();
  const { timeZone } = useTimeFormat();
  const locale = i18n.resolvedLanguage ?? "en";
  const team = item.team;
  return (
    <li
      className="flex flex-col gap-1 px-3.5 py-3 not-last:border-b"
      data-testid="on-call-row"
      data-team={team.slug}
    >
      <span className="flex flex-wrap items-baseline gap-x-2">
        {linkTeam ? (
          <Link
            to="/teams/$teamId"
            params={{ teamId: team.id }}
            className="font-semibold wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
          >
            {team.name}
          </Link>
        ) : (
          <b className="font-semibold wrap-anywhere">{team.name}</b>
        )}
        <span aria-hidden="true" className="text-muted-foreground">
          ·
        </span>
        <OnCallPerson item={item} />
      </span>
      {item.schedule !== null && (
        <span className="flex flex-wrap items-baseline gap-x-2 text-sm text-muted-foreground">
          <Link
            to="/schedules/$scheduleId"
            params={{ scheduleId: item.schedule.id }}
            className="text-primary underline-offset-4 hover:underline focus-visible:underline"
          >
            {item.schedule.name}
          </Link>
          {item.until !== null && (
            <span data-testid="on-call-until">
              {t("onCallNow.until", { time: stamp(item.until, timeZone, locale) })}
            </span>
          )}
        </span>
      )}
      <OnCallMarks item={item} />
    </li>
  );
}

function Heading({ id }: { id: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h2 id={id} className="text-lg font-semibold">
        {t("onCallNow.title")}
      </h2>
      <DocsLink to="field.on_call_now" kind="learn_more" about={t("onCallNow.title")} />
    </div>
  );
}

/** The switch "All Teams" / "My Teams" of the Home page. */
function ScopeSwitch({
  scope,
  onChange,
}: {
  scope: OnCallScope;
  onChange: (next: OnCallScope) => void;
}) {
  const { t } = useTranslation();
  return (
    <div role="group" aria-label={t("onCallNow.scope")} className="flex gap-2">
      {(["all", "mine"] as const).map((value) => (
        <Button
          key={value}
          type="button"
          size="sm"
          variant={scope === value ? "default" : "outline"}
          aria-pressed={scope === value}
          onClick={() => onChange(value)}
        >
          {t(value === "all" ? "onCallNow.all" : "onCallNow.mine")}
        </Button>
      ))}
    </div>
  );
}

/** "On call now" of the Home page: every Team by default, or the Teams of the signed-in User. */
export function OnCallNowSection() {
  const { t } = useTranslation();
  const [scope, setScope] = useState<OnCallScope>("all");
  const read = useOnCallNow(scope);
  return (
    <section
      className="flex min-w-0 flex-col gap-3 rounded-lg border bg-card p-5"
      aria-labelledby="on-call-now-title"
      data-testid="on-call-now"
    >
      <Heading id="on-call-now-title" />
      <ScopeSwitch scope={scope} onChange={setScope} />
      {read.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, read.error)}
        </p>
      ) : read.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : read.data.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="on-call-empty">
          {scope === "mine" ? t("onCallNow.emptyMine") : t("onCallNow.empty")}
        </p>
      ) : (
        <ul className="flex flex-col rounded-lg border" data-testid="on-call-list">
          {read.data.map((item) => (
            <OnCallRow key={item.team.id} item={item} />
          ))}
        </ul>
      )}
      <p className="text-sm text-muted-foreground">{t("onCallNow.hint")}</p>
    </section>
  );
}

/** "On call now" of a Team's page: the row of that Team alone. */
export function TeamOnCallNow({ teamId }: { teamId: string }) {
  const { t } = useTranslation();
  const read = useOnCallNow("all");
  const item = read.data?.find((row) => row.team.id === teamId);
  return (
    <section
      className="flex min-w-0 flex-col gap-3"
      aria-labelledby="team-on-call-now-title"
      data-testid="team-on-call-now"
    >
      <Heading id="team-on-call-now-title" />
      {read.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, read.error)}
        </p>
      ) : item === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {read.data === undefined ? t("common.loading") : t("onCallNow.noSchedule")}
        </p>
      ) : (
        <ul className="flex flex-col rounded-lg border">
          <OnCallRow item={item} linkTeam={false} />
        </ul>
      )}
    </section>
  );
}

/** The cell "On call now" of the Teams list: the person or "No Schedule", with the marks. */
export function OnCallCell({ teamId }: { teamId: string }) {
  const read = useOnCallNow("all");
  const item = read.data?.find((row) => row.team.id === teamId);
  if (item === undefined) {
    return null;
  }
  return (
    <span
      className="flex flex-wrap items-baseline gap-x-2 gap-y-1"
      data-testid="team-on-call"
      data-team={item.team.slug}
    >
      <OnCallPerson item={item} />
      <OnCallMarks item={item} />
    </span>
  );
}
