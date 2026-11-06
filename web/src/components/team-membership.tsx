// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Teams of an Alert Group or of an Alert with the rule that brought each (C-25.FR-5, FR-6): "payments — by the Team
// label" or "payments — by the matcher namespace=~\"checkout|billing\"". A Team deleted since keeps its name, marked
// "(deleted)", and is not linked. The Alert Group page, its Alerts, the list and the Alerts view of an Integration use
// these; the names are text from the Teams, never HTML.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import type { TeamRef, TeamRule } from "../api/gen/model";

/** The name of a Team in a sentence: "payments", or "payments (deleted)". */
export function teamNameText(t: TFunction, team: Pick<TeamRef, "name" | "deleted">): string {
  return team.deleted ? t("teams.deletedName", { name: team.name }) : team.name;
}

/** How a Team came to an Alert: "by the Team label", or "by the matcher {matcher}". */
export function teamRuleText(
  t: TFunction,
  rule: TeamRule,
  matcher: string | null | undefined,
): string {
  return rule === "matcher" && matcher
    ? t("teams.rule.matcher", { matcher })
    : t("teams.rule.label");
}

/** The name of a Team, linked to its page unless it is deleted. */
export function TeamLink({ team }: { team: TeamRef }) {
  const { t } = useTranslation();
  if (team.deleted) {
    return <span className="wrap-anywhere">{teamNameText(t, team)}</span>;
  }
  return (
    <Link
      to="/teams/$teamId"
      params={{ teamId: team.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {team.name}
    </Link>
  );
}

/** A Team the object belongs to, with the rule that decided it. */
export interface TeamEntry {
  team: TeamRef;
  rule: TeamRule;
  matcher: string | null;
}

/** One line per Team: its name and its rule; nothing when there are none. */
export function TeamEntries({
  entries,
  testId = "teams",
  className,
}: {
  entries: readonly TeamEntry[];
  testId?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  if (entries.length === 0) {
    return null;
  }
  return (
    <ul className={className ?? "flex flex-col gap-0.5"} data-testid={testId}>
      {entries.map((entry) => (
        <li key={entry.team.id} className="min-w-0 wrap-anywhere" data-testid="team-entry">
          <TeamLink team={entry.team} />
          <span className="text-muted-foreground">
            {" — "}
            {teamRuleText(t, entry.rule, entry.matcher)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** "Teams:" and one line per Team; nothing when there are none. */
export function TeamsLine({
  entries,
  testId,
  className,
}: {
  entries: readonly TeamEntry[];
  testId: string;
  className?: string;
}) {
  const { t } = useTranslation();
  if (entries.length === 0) {
    return null;
  }
  return (
    <div
      className={className ?? "flex min-w-0 flex-wrap gap-x-2 gap-y-0.5 text-sm"}
      data-testid={testId}
    >
      <span className="font-medium">{t("alertGroups.fields.teams")}:</span>{" "}
      <TeamEntries entries={entries} testId={`${testId}-list`} />
    </div>
  );
}
