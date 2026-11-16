// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the statistics page (C-09.FR-30): Routes, Integrations, Severity level, Urgent, Team with "My Teams",
// Reopened, label Matchers and "Acknowledged first by", the pickers of the Alert Group list. They apply to every view
// and live in the URL. The list's other filters — resolved by, Owner, Assignee, snoozed with no end, Delivery problem and
// Unclaimed — are not offered. "Acknowledged first by" names a User only (C-09.FR-27): "Anyone", "Me" and the Users of
// the directory, never a Service account.

import { useId } from "react";
import { useTranslation } from "react-i18next";

import { useListIntegrations } from "../api/gen/endpoints/integrations/integrations";
import { useListRoutes } from "../api/gen/endpoints/routes/routes";
import { SeverityLevel } from "../api/gen/model";
import {
  ME,
  STATISTICS_FILTER_KEYS,
  type StatisticsSearch,
  activeFilterCount,
} from "../lib/statistics-search";
import { MultiPicker, OnePicker, flag, fromFlag, severityChoices } from "./alert-group-filters";
import { useCan } from "./app-shell";
import { LabelMatchersInput } from "./label-matchers-input";
import { UserFilter } from "./owner-filter";
import { TeamFilter } from "./team-filter";
import { Button } from "./ui/button";

export interface StatisticsFiltersProps {
  search: StatisticsSearch;
  onChange: (patch: Partial<StatisticsSearch>) => void;
  /** Applies new Matchers after the server checked them; a refusal is thrown and shown under the field. */
  applyMatchers: (next: string[]) => Promise<void>;
  /** The error of the statistics, shown under the label Matchers when it is about them. */
  problem?: unknown;
}

export function StatisticsFilters({
  search,
  onChange,
  applyMatchers,
  problem,
}: StatisticsFiltersProps) {
  const { t } = useTranslation();
  const id = useId();
  const canRoutes = useCan("routes:read");
  const canIntegrations = useCan("integrations:read");
  const routes = useListRoutes(undefined, { query: { enabled: canRoutes } });
  const integrations = useListIntegrations({ limit: 500 }, { query: { enabled: canIntegrations } });
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="statistics-filters">
      {(canRoutes || search.route !== undefined) && (
        <MultiPicker
          testId="filter-route"
          label={t("alertGroups.filters.route")}
          placeholder={t("alertGroups.filters.addRoute")}
          choices={(routes.data?.items ?? []).map((r) => ({ value: r.id, label: r.name }))}
          value={search.route ?? []}
          onChange={(next) => onChange({ route: next.length > 0 ? next : undefined })}
        />
      )}
      {(canIntegrations || search.integration !== undefined) && (
        <MultiPicker
          testId="filter-integration"
          label={t("alertGroups.filters.integration")}
          placeholder={t("alertGroups.filters.addIntegration")}
          choices={(integrations.data?.items ?? []).map((i) => ({ value: i.id, label: i.name }))}
          value={search.integration ?? []}
          onChange={(next) => onChange({ integration: next.length > 0 ? next : undefined })}
        />
      )}
      <MultiPicker
        testId="filter-severity"
        label={t("alertGroups.filters.severity")}
        placeholder={t("alertGroups.filters.addSeverity")}
        choices={severityChoices(t)}
        value={search.severity ?? []}
        onChange={(next) =>
          onChange({
            severity:
              next.length > 0
                ? Object.values(SeverityLevel).filter((l) => next.includes(l))
                : undefined,
          })
        }
      />
      <OnePicker
        label={t("alertGroups.filters.urgent")}
        choices={[
          { value: "", label: t("alertGroups.filters.any") },
          { value: "yes", label: t("alertGroups.filters.urgentOnly") },
          { value: "no", label: t("alertGroups.filters.notUrgent") },
        ]}
        value={flag(search.urgent)}
        onChange={(next) => onChange({ urgent: fromFlag(next) })}
      />
      <TeamFilter value={search.team} onChange={(team) => onChange({ team })} />
      <OnePicker
        label={t("alertGroups.filters.reopened")}
        choices={[
          { value: "", label: t("alertGroups.filters.any") },
          { value: "yes", label: t("alertGroups.filters.reopenedOnly") },
          { value: "no", label: t("alertGroups.filters.neverReopened") },
        ]}
        value={flag(search.reopened)}
        onChange={(next) => onChange({ reopened: fromFlag(next) })}
      />
      <LabelMatchersInput
        id={`${id}-matchers`}
        value={search.label ?? []}
        onChange={applyMatchers}
        problem={problem}
      />
      <UserFilter
        value={search.first_acknowledged_by}
        onChange={(next) => onChange({ first_acknowledged_by: next })}
        label={t("statistics.filters.firstAcknowledgedBy")}
        searchLabel={t("alertGroups.filters.ownerSearch")}
        specials={[{ value: ME, label: t("statistics.filters.me") }]}
        testId="filter-first-acknowledged-by"
      />
      {activeFilterCount(search) > 0 && (
        <div>
          <Button
            variant="outline"
            onClick={() =>
              onChange(Object.fromEntries(STATISTICS_FILTER_KEYS.map((k) => [k, undefined])))
            }
          >
            {t("alertGroups.filters.clear")}
          </Button>
        </div>
      )}
    </div>
  );
}
