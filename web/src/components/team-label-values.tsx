// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What the Teams page says about the Team label (C-25.FR-7, C-25.AC-10): the label and the rule that an Alert belongs to
// a Team by its value or by the Team's matchers, with a link to change the label in Organization → General; and
// "Unknown values of {label}" — the values that Alerts brought without a Team of that name — each with when it was
// last seen and "Create Team", which opens the new Team form with the value as the slug, or, for a value no slug can
// take, with the note that only a Team matcher picks such Alerts. The list is the server's: a value that a Team
// matcher happens to pick still shows, since the server does not evaluate matchers for it.

import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import { useListTeamLabelValues } from "../api/gen/endpoints/teams/teams";
import type { TeamLabelValue } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { DocsLink } from "./docs-link";
import { useCan } from "./app-shell";
import { elapsedSeconds, formatElapsed, useNow } from "./relative-time";
import { buttonVariants } from "./ui/button";

/** The Team label of the Organization, once it is known; undefined while it is read or when the read failed. */
export function useTeamLabel() {
  const organization = useGetOrganization();
  return { label: organization.data?.team_label, query: organization };
}

/** The Team label, the hint about what makes an Alert belong to a Team, and the link to change the label. */
export function TeamLabelLine() {
  const { t } = useTranslation();
  const canChange = useCan("organization:write");
  const { label, query } = useTeamLabel();
  return (
    <div className="flex min-w-0 flex-col gap-1.5 rounded-lg border p-3" data-testid="team-label">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="text-sm font-medium">{t("teams.label.title")}</span>
        <code className="font-mono text-sm wrap-anywhere" data-testid="team-label-name">
          {label ?? "…"}
        </code>
        {canChange && (
          <Link
            to="/admin/organization/general"
            className={buttonVariants({ variant: "link", className: "h-auto p-0" })}
          >
            {t("teams.label.change")}
          </Link>
        )}
        <DocsLink to="field.team_label" kind="learn_more" about={t("docs.fields.teamLabel")} />
      </div>
      <p className="text-sm text-muted-foreground">{t("teams.label.hint")}</p>
      {query.isError && (
        <p className="text-sm text-destructive" role="alert" data-testid="team-label-error">
          {problemText(t, query.error)}
        </p>
      )}
    </div>
  );
}

function LastSeen({ value }: { value: TeamLabelValue }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const at = useNow();
  const ago = formatElapsed(t, elapsedSeconds(value.last_seen_at, undefined, at));
  return (
    <time dateTime={value.last_seen_at} className="text-xs text-muted-foreground">
      {t("teams.values.lastSeen", { time: dateTime(value.last_seen_at), ago })}
    </time>
  );
}

function ValueRow({
  value,
  label,
  days,
  canCreate,
}: {
  value: TeamLabelValue;
  label: string;
  days: number;
  canCreate: boolean;
}) {
  const { t } = useTranslation();
  return (
    <li
      className="flex min-w-0 flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t py-2.5 first:border-t-0"
      data-testid="team-label-value"
    >
      <div className="flex min-w-0 flex-col gap-0.5">
        <code className="font-mono text-sm wrap-anywhere">
          {label}={value.value}
        </code>
        <span className="flex flex-wrap gap-x-3 gap-y-0.5">
          <LastSeen value={value} />
          <span className="text-xs text-muted-foreground">
            {t("teams.values.alerts", { count: value.alert_count, days })}
          </span>
        </span>
      </div>
      {canCreate && (
        <Link
          to="/teams/new"
          search={value.valid_slug ? { slug: value.value } : { value: value.value }}
          className={buttonVariants({ variant: "outline", size: "sm" })}
          aria-label={t("teams.values.createFor", { value: value.value })}
        >
          {t("teams.values.create")}
        </Link>
      )}
    </li>
  );
}

/** "Unknown values of {label}"; nothing while there are none and nothing went wrong. */
export function TeamLabelValues() {
  const { t } = useTranslation();
  const canCreate = useCan("teams:write");
  const query = useListTeamLabelValues();
  if (query.isError && query.data === undefined) {
    return (
      <p className="text-sm text-destructive" role="alert">
        {problemText(t, query.error)}
      </p>
    );
  }
  const data = query.data;
  if (data === undefined || data.items.length === 0) {
    return null;
  }
  return (
    <section
      aria-labelledby="team-label-values-title"
      className="flex min-w-0 flex-col gap-2"
      data-testid="team-label-values"
    >
      <h2 id="team-label-values-title" className="text-lg font-semibold wrap-anywhere">
        {t("teams.values.title", { label: data.label })}
      </h2>
      <p className="text-sm text-muted-foreground">
        {t("teams.values.banner")}{" "}
        <DocsLink
          to="fix.unknown_team_values"
          kind="how_to_fix"
          about={t("docs.fixes.unknownTeamValues")}
        />
      </p>
      {query.isError && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, query.error)}
        </p>
      )}
      <ul className="rounded-lg border px-3">
        {data.items.map((value) => (
          <ValueRow
            key={value.value}
            value={value}
            label={data.label}
            days={data.window_days}
            canCreate={canCreate}
          />
        ))}
      </ul>
    </section>
  );
}
