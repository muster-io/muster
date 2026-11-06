// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The choices of a Team in the lists and forms of what a Team owns (C-25.FR-2, C-25.AC-11, C-26.FR-7): the filter
// "Team" of the Routes and Destinations lists — "All", "My Teams" (the Teams of the signed-in User) and each Team,
// whose value goes into the list's search parameter `team` — and the field "Owning Team" of the Route and Destination
// forms. The owning Team is for ownership and for the filter of the UI; it does not affect delivery.

import { useQuery } from "@tanstack/react-query";
import { useId } from "react";
import { useTranslation } from "react-i18next";

import { getListTeamsQueryKey, listTeams } from "../api/gen/endpoints/teams/teams";
import type { Team, TeamRef } from "../api/gen/model";
import { problemText } from "../lib/api";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The value of the search parameter `team` for the Teams of the signed-in User; the API takes it as it is. */
export const MY_TEAMS = "mine";

/** The most Teams one read brings, which is the API's largest page. */
const TEAMS_LIMIT = 500;

/** The Teams by name, read once for every select of the page. */
export function useTeamList() {
  const params = { limit: TEAMS_LIMIT };
  return useQuery({
    queryKey: getListTeamsQueryKey(params),
    queryFn: ({ signal }) => listTeams(params, { signal }),
    staleTime: 30_000,
    select: (list): Team[] => list.items,
  });
}

export function TeamFilter({
  value,
  onChange,
}: {
  /** The search parameter `team`: undefined for all, MY_TEAMS or the id of a Team. */
  value: string | undefined;
  onChange: (next: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const teams = useTeamList();
  const listed = teams.data ?? [];
  const unknown =
    value !== undefined && value !== MY_TEAMS && !listed.some((team) => team.id === value);
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-testid="filter-team">
      <Label htmlFor={id}>{t("teams.filter.label")}</Label>
      <NativeSelect
        id={id}
        className="w-full sm:w-64"
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        <NativeSelectOption value="">{t("teams.filter.all")}</NativeSelectOption>
        <NativeSelectOption value={MY_TEAMS}>{t("teams.filter.mine")}</NativeSelectOption>
        {unknown && <NativeSelectOption value={value}>{value}</NativeSelectOption>}
        {listed.map((team) => (
          <NativeSelectOption key={team.id} value={team.id}>
            {team.name}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      {teams.isError && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, teams.error)}
        </p>
      )}
    </div>
  );
}

/** "Owning Team": the Teams and "None", with the hint that it does not affect delivery. */
export function OwningTeamField({
  id,
  value,
  onChange,
  current,
  disabled = false,
  error,
}: {
  id: string;
  /** The id of the owning Team, empty for none. */
  value: string;
  onChange: (next: string) => void;
  /** The owning Team as stored, which stays selectable while the list does not name it. */
  current?: TeamRef | null;
  disabled?: boolean;
  /** The text of the field's refusal. */
  error?: string;
}) {
  const { t } = useTranslation();
  const teams = useTeamList();
  const listed = teams.data ?? [];
  const missing =
    current != null && !current.deleted && !listed.some((team) => team.id === current.id);
  const described = [`${id}-hint`, error === undefined ? null : `${id}-error`]
    .filter((v) => v !== null)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-testid="owning-team">
      <Label htmlFor={id}>{t("teams.owner.label")}</Label>
      <NativeSelect
        id={id}
        className="w-full sm:w-64"
        value={value}
        disabled={disabled}
        aria-invalid={error !== undefined}
        aria-describedby={described}
        onChange={(e) => onChange(e.target.value)}
      >
        <NativeSelectOption value="">{t("teams.owner.none")}</NativeSelectOption>
        {missing && <NativeSelectOption value={current.id}>{current.name}</NativeSelectOption>}
        {listed.map((team) => (
          <NativeSelectOption key={team.id} value={team.id}>
            {team.name}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("teams.owner.hint")}
      </p>
      {error !== undefined && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
