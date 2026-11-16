// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Owner filter of the Alert Group list (C-10.FR-13, C-09.FR-13): anyone, "Mine" (owner=me), "Nobody"
// (owner=none) or a user picked from the user directory, sent as the user's public_id. A search field narrows the
// users of the native select (D250); a deleted user reads "(deactivated)". The "Mine" quick filter beside the list
// sets the same value.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { getListUserDirectoryQueryKey, listUserDirectory } from "../api/gen/endpoints/users/users";
import type { UserRef } from "../api/gen/model";
import { OWNER_ME, OWNER_NONE } from "../lib/alert-group-search";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** How long the search waits for typing to pause. */
const SEARCH_DELAY_MS = 300;
/** The users a search offers at most. */
const DIRECTORY_LIMIT = 100;

/** A user of the directory as a choice: the name, the login when it differs, "(deactivated)" for a deleted user. */
export function directoryLabel(t: TFunction, user: UserRef): string {
  const name =
    user.login !== undefined && user.login !== "" && user.login !== user.name
      ? t("alertGroups.filters.userWithLogin", { name: user.name, login: user.login })
      : user.name;
  return user.deactivated ? t("alertGroups.deactivated", { name }) : name;
}

function useDirectory(q: string) {
  const params = q === "" ? { limit: DIRECTORY_LIMIT } : { limit: DIRECTORY_LIMIT, q };
  return useQuery({
    queryKey: getListUserDirectoryQueryKey(params),
    queryFn: ({ signal }) => listUserDirectory(params, { signal }),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  });
}

/** A choice that is not a user and still names who is meant: its value goes into the URL as it is. */
export interface SpecialChoice {
  value: string;
  label: string;
}

/**
 * A picker of a user of the directory, with the special choices `specials` after "Anyone". The Owner filter takes "Mine"
 * and "Nobody"; "Acknowledged first by" of the statistics page takes "Me" and no Service accounts, as the directory
 * lists Users only.
 */
export function UserFilter({
  value,
  onChange,
  label,
  searchLabel,
  specials,
  testId,
}: {
  value: string | undefined;
  onChange: (next: string | undefined) => void;
  label: string;
  searchLabel: string;
  specials: readonly SpecialChoice[];
  testId: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQ(text.trim()), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [text]);
  // The first page names the chosen user while a search shows others.
  const all = useDirectory("");
  const found = useDirectory(q);
  const users = found.data?.items ?? [];
  const special = value === undefined || specials.some((c) => c.value === value);
  const chosen =
    special || users.some((u) => u.id === value)
      ? undefined
      : ([...(all.data?.items ?? []), ...users].find((u) => u.id === value) ?? {
          id: value,
          name: t("alertGroups.filters.ownerUnknown"),
          deactivated: false,
        });
  return (
    <div className="flex min-w-0 flex-col gap-1.5" data-testid={testId}>
      <Label htmlFor={`${id}-owner`}>{label}</Label>
      <Input
        id={`${id}-search`}
        type="search"
        value={text}
        autoComplete="off"
        aria-label={searchLabel}
        placeholder={searchLabel}
        onChange={(e) => setText(e.target.value)}
      />
      <NativeSelect
        id={`${id}-owner`}
        className="w-full"
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        <NativeSelectOption value="">{t("alertGroups.filters.anyone")}</NativeSelectOption>
        {specials.map((c) => (
          <NativeSelectOption key={c.value} value={c.value}>
            {c.label}
          </NativeSelectOption>
        ))}
        {chosen !== undefined && (
          <NativeSelectOption value={chosen.id}>{directoryLabel(t, chosen)}</NativeSelectOption>
        )}
        {users.map((u) => (
          <NativeSelectOption key={u.id} value={u.id}>
            {directoryLabel(t, u)}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      {q !== "" && found.data !== undefined && users.length === 0 && (
        <p className="text-sm text-muted-foreground" role="status">
          {t("alertGroups.filters.ownerNoMatch")}
        </p>
      )}
    </div>
  );
}

export function OwnerFilter({
  value,
  onChange,
}: {
  value: string | undefined;
  onChange: (next: string | undefined) => void;
}) {
  const { t } = useTranslation();
  return (
    <UserFilter
      value={value}
      onChange={onChange}
      label={t("alertGroups.filters.owner")}
      searchLabel={t("alertGroups.filters.ownerSearch")}
      specials={[
        { value: OWNER_ME, label: t("alertGroups.filters.mine") },
        { value: OWNER_NONE, label: t("alertGroups.filters.nobody") },
      ]}
      testId="filter-owner"
    />
  );
}

/** The "Mine" quick filter: the Alert Groups the signed-in user owns, as the Owner filter's "Mine". */
export function MineToggle({
  value,
  onChange,
}: {
  value: string | undefined;
  onChange: (next: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const on = value === OWNER_ME;
  return (
    <Button
      variant={on ? "default" : "outline"}
      aria-pressed={on}
      onClick={() => onChange(on ? undefined : OWNER_ME)}
      data-testid="filter-mine"
    >
      {t("alertGroups.filters.mine")}
    </Button>
  );
}
