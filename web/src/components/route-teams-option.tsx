// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Also to the Teams from the label {team label}" of the Route editor (C-26.FR-2, FR-12) and, under it, "Run the Teams'
// Escalation chains", which stays off-limits while the option is off. The hint says that a new Team only adds channels
// and removes none. When the option is on and the Group key lacks the Team label, the Group key shows the hint that
// Alerts of different Teams with the same key share one Alert Group, with "Add {team label} to the key"
// (reference.md, banners). Without routes:write the option shows as text.

import { InfoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { DocsLink } from "./docs-link";
import { Button } from "./ui/button";
import { Label } from "./ui/label";

const SWITCH =
  "mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50";

export interface RouteTeamsOptionProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  /** The Team label of the Organization (`organization.team_label`). */
  teamLabel: string;
  deliverToTeams: boolean;
  runTeamChains: boolean;
  onDeliverToTeamsChange?: (next: boolean) => void;
  onRunTeamChainsChange?: (next: boolean) => void;
  /** The error codes of a refused save, as texts. */
  errors?: { deliverToTeams?: string; runTeamChains?: string };
  readOnly?: boolean;
}

export function RouteTeamsOption({
  id,
  teamLabel,
  deliverToTeams,
  runTeamChains,
  onDeliverToTeamsChange,
  onRunTeamChainsChange,
  errors = {},
  readOnly = false,
}: RouteTeamsOptionProps) {
  const { t } = useTranslation();
  const hintId = `${id}-deliver-hint`;
  if (readOnly) {
    return (
      <section className="flex min-w-0 flex-col gap-1" data-testid="route-teams-option">
        <h3 className="text-sm font-medium">{t("routes.teams.deliver", { label: teamLabel })}</h3>
        <p className="text-sm" data-testid="route-deliver-to-teams">
          {deliverToTeams ? t("routes.lifecycle.on") : t("routes.lifecycle.off")}
        </p>
        <p className="text-sm text-muted-foreground">{t("routes.teams.hint")}</p>
        {deliverToTeams && (
          <p className="text-sm">
            {t("routes.teams.runChains")}:{" "}
            <span data-testid="route-run-team-chains">
              {runTeamChains ? t("routes.lifecycle.on") : t("routes.lifecycle.off")}
            </span>
          </p>
        )}
      </section>
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="route-teams-option">
      <div className="flex items-start gap-2">
        <input
          id={`${id}-deliver`}
          type="checkbox"
          role="switch"
          className={SWITCH}
          data-testid="route-deliver-to-teams"
          checked={deliverToTeams}
          aria-describedby={hintId}
          onChange={(e) => onDeliverToTeamsChange?.(e.target.checked)}
        />
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <Label htmlFor={`${id}-deliver`} className="leading-snug">
              {t("routes.teams.deliver", { label: teamLabel })}
            </Label>
            <DocsLink
              to="field.deliver_to_teams"
              kind="learn_more"
              about={t("docs.fields.deliverToTeams")}
            />
          </div>
          <p id={hintId} className="text-sm text-muted-foreground">
            {t("routes.teams.hint")}
          </p>
          {errors.deliverToTeams !== undefined && (
            <p className="text-sm text-destructive">{errors.deliverToTeams}</p>
          )}
        </div>
      </div>
      <div className="ml-6 flex items-start gap-2">
        <input
          id={`${id}-run-chains`}
          type="checkbox"
          role="switch"
          className={SWITCH}
          data-testid="route-run-team-chains"
          checked={runTeamChains}
          disabled={!deliverToTeams}
          onChange={(e) => onRunTeamChainsChange?.(e.target.checked)}
        />
        <div className="flex min-w-0 flex-col gap-1">
          <Label htmlFor={`${id}-run-chains`} className="leading-snug">
            {t("routes.teams.runChains")}
          </Label>
          {errors.runTeamChains !== undefined && (
            <p className="text-sm text-destructive">{errors.runTeamChains}</p>
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * The hint under the Group key: with "Also to the Teams from the label" on and the Team label not in the key, Alerts of
 * different Teams with the same key share one Alert Group. Nothing shows otherwise.
 */
export function SharedKeyHint({
  teamLabel,
  groupKey,
  deliverToTeams,
  onAdd,
}: {
  teamLabel: string;
  groupKey: readonly string[];
  deliverToTeams: boolean;
  /** Adds the Team label to the key. */
  onAdd: () => void;
}) {
  const { t } = useTranslation();
  if (!deliverToTeams || groupKey.includes(teamLabel)) {
    return null;
  }
  return (
    <div
      className="flex flex-col items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm text-foreground"
      role="status"
      data-testid="shared-key-hint"
    >
      <p className="flex items-start gap-2">
        <InfoIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
        />
        <span className="min-w-0 wrap-anywhere">
          {t("routes.teams.sharedKey", { label: teamLabel })}
        </span>
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onAdd}>
        {t("routes.teams.addToKey", { label: teamLabel })}
      </Button>
    </div>
  );
}
