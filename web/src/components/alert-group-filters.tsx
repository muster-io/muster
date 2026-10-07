// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the Alert Group list (C-09.FR-13, C-10.FR-13): Route and Integration (several of each), Severity
// level, Urgent, resolved by a person or by the system with its reason, Reopened, the Owner, "Snoozed with no end",
// and label Matchers; on a desktop also the label
// columns. All combine, and all live in the URL. The pickers are native selects (D250); a picker that takes several
// values adds each chosen one as a chip that removes it again.

import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import {
  getAlertGroupCounts,
  getGetAlertGroupCountsQueryKey,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import { useListIntegrations } from "../api/gen/endpoints/integrations/integrations";
import { useListRoutes } from "../api/gen/endpoints/routes/routes";
import { ResolveReason, ResolverKind, SeverityLevel } from "../api/gen/model";
import {
  type AlertGroupSearch,
  FILTER_KEYS,
  activeFilterCount,
  compact,
  countParams,
} from "../lib/alert-group-search";
import { useCan } from "./app-shell";
import { reasonLabel, severityLabel } from "./integration-alerts";
import { LabelColumnsPicker } from "./label-columns-picker";
import { LabelMatchersInput } from "./label-matchers-input";
import { OwnerFilter } from "./owner-filter";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** A choice of a picker: its value and what it shows. */
interface Choice {
  value: string;
  label: string;
}

/** A picker of several values: a native select adds one, a chip removes it; the statistics page uses it too. */
export function MultiPicker({
  label,
  placeholder,
  choices,
  value,
  onChange,
  testId,
}: {
  label: string;
  placeholder: string;
  choices: readonly Choice[];
  value: readonly string[];
  onChange: (next: string[]) => void;
  testId: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const rest = choices.filter((c) => !value.includes(c.value));
  const labelOf = (v: string) => choices.find((c) => c.value === v)?.label ?? v;
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-testid={testId}>
      <Label htmlFor={id}>{label}</Label>
      {value.length > 0 && (
        <ul
          className="flex flex-wrap gap-1.5"
          aria-label={t("alertGroups.filters.chosen", { label })}
        >
          {value.map((v) => (
            <li
              key={v}
              className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 text-xs"
            >
              <span className="min-w-0 wrap-anywhere">{labelOf(v)}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t("alertGroups.filters.remove", { value: labelOf(v) })}
                onClick={() => onChange(value.filter((x) => x !== v))}
              >
                <XIcon aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <NativeSelect
        id={id}
        className="w-full"
        value=""
        disabled={rest.length === 0}
        onChange={(e) => {
          if (e.target.value !== "") {
            onChange([...value, e.target.value]);
          }
        }}
      >
        <NativeSelectOption value="">{placeholder}</NativeSelectOption>
        {rest.map((c) => (
          <NativeSelectOption key={c.value} value={c.value}>
            {c.label}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

/** A picker of one value or none ("any"), as a native select. */
function OnePicker<T extends string>({
  label,
  choices,
  value,
  onChange,
}: {
  label: string;
  choices: readonly { value: T | ""; label: string }[];
  value: T | undefined;
  onChange: (next: T | undefined) => void;
}) {
  const id = useId();
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <NativeSelect
        id={id}
        className="w-full"
        value={value ?? ""}
        onChange={(e) => {
          const next = choices.find((c) => c.value === e.target.value)?.value;
          onChange(next === "" || next === undefined ? undefined : next);
        }}
      >
        {choices.map((c) => (
          <NativeSelectOption key={c.value} value={c.value}>
            {c.label}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

/** A filter of a boolean as a native select: any, yes or no. */
function flag(value: boolean | undefined): "yes" | "no" | undefined {
  return value === undefined ? undefined : value ? "yes" : "no";
}

function fromFlag(value: "yes" | "no" | undefined): boolean | undefined {
  return value === undefined ? undefined : value === "yes";
}

function severityChoices(t: TFunction): Choice[] {
  return Object.values(SeverityLevel).map((level) => ({
    value: level,
    label: severityLabel(t, level),
  }));
}

export interface AlertGroupFiltersProps {
  search: AlertGroupSearch;
  onChange: (patch: Partial<AlertGroupSearch>) => void;
  /** The error of the list, shown under the label Matchers when it is about them. */
  problem?: unknown;
  /** Shows the label columns, which only the desktop table has. */
  showColumns?: boolean;
}

export function AlertGroupFilters({
  search,
  onChange,
  problem,
  showColumns = false,
}: AlertGroupFiltersProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canRoutes = useCan("routes:read");
  const canIntegrations = useCan("integrations:read");
  const routes = useListRoutes({ query: { enabled: canRoutes } });
  const integrations = useListIntegrations({ limit: 500 }, { query: { enabled: canIntegrations } });
  const id = useId();

  // New Matchers are checked by the server first, through the counts the list reads anyway; a refused one leaves the
  // URL, and the rows, as they were, and the field shows why.
  const applyMatchers = async (next: string[]) => {
    const params = compact(countParams({ ...search, label: next }, Date.now()));
    await queryClient.fetchQuery({
      queryKey: getGetAlertGroupCountsQueryKey(params),
      queryFn: ({ signal }) => getAlertGroupCounts(params, { signal }),
    });
    onChange({ label: next.length > 0 ? next : undefined });
  };

  const reasons = Object.values(ResolveReason);
  return (
    <div className="flex min-w-0 flex-col gap-4" data-testid="alert-group-filters">
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
      <OnePicker
        label={t("alertGroups.filters.resolvedBy")}
        choices={[
          { value: "", label: t("alertGroups.filters.anyone") },
          { value: ResolverKind.user, label: t("alertGroups.filters.byPerson") },
          { value: ResolverKind.system, label: t("alertGroups.filters.bySystem") },
        ]}
        value={search.resolved_by}
        onChange={(next) =>
          onChange({
            resolved_by: next,
            resolve_reason: next === "system" ? search.resolve_reason : undefined,
          })
        }
      />
      {search.resolved_by === "system" && (
        <OnePicker
          label={t("alertGroups.filters.reason")}
          choices={[
            { value: "", label: t("alertGroups.filters.anyReason") },
            ...reasons.map((r) => ({ value: r, label: reasonLabel(t, r) })),
          ]}
          value={search.resolve_reason}
          onChange={(next) => onChange({ resolve_reason: next })}
        />
      )}
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
      <OwnerFilter value={search.owner} onChange={(owner) => onChange({ owner })} />
      <div className="flex items-start gap-2">
        <input
          id={`${id}-no-end`}
          type="checkbox"
          className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          checked={search.snoozed_no_end === true}
          onChange={(e) => onChange({ snoozed_no_end: e.target.checked ? true : undefined })}
        />
        <Label htmlFor={`${id}-no-end`} className="leading-snug">
          {t("alertGroups.filters.snoozedNoEnd")}
        </Label>
      </div>
      <LabelMatchersInput
        id={`${id}-matchers`}
        value={search.label ?? []}
        onChange={applyMatchers}
        problem={problem}
      />
      {showColumns && (
        <LabelColumnsPicker
          value={search.columns ?? []}
          onChange={(next) => onChange({ columns: next.length > 0 ? next : undefined })}
        />
      )}
      {activeFilterCount(search) > 0 && (
        <div>
          <Button
            variant="outline"
            onClick={() => onChange(Object.fromEntries(FILTER_KEYS.map((k) => [k, undefined])))}
          >
            {t("alertGroups.filters.clear")}
          </Button>
        </div>
      )}
    </div>
  );
}
