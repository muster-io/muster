// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "People" of a Team (C-25.FR-3, C-25.AC-10): each person with their Role, how they are in the Team — "added by hand"
// or the identity provider group that matched, or both — and the messengers they linked. "Remove" takes away only the
// source "added by hand", so it shows only for people added by hand; "Add person" picks a User. A person in the Team
// only through the identity provider leaves it when no entry matches them at a later sign-in or re-check. Without
// teams:write the table is all there is.

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { PlusIcon } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  addTeamUser,
  getGetTeamQueryKey,
  getListTeamUsersQueryKey,
  getListTeamsQueryKey,
  removeTeamUser,
  useListTeamUsers,
} from "../api/gen/endpoints/teams/teams";
import { getListUsersQueryKey, listUsers } from "../api/gen/endpoints/users/users";
import type { ConnectionType, TeamUser } from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { roleLabel } from "./user-create-dialog";

/** How long the search waits for typing to pause. */
const SEARCH_DELAY_MS = 300;
/** The Users a search offers at most. */
const USERS_LIMIT = 100;

/** The name of the messenger of a Connection type. */
export function messengerName(t: TFunction, type: ConnectionType): string {
  switch (type) {
    case "mattermost":
      return "Mattermost";
    case "telegram":
      return "Telegram";
    case "slack":
      return t("connections.types.slack");
    default:
      return t("connections.types.bridge");
  }
}

function SourceCell({ row }: { row: TeamUser }) {
  const { t } = useTranslation();
  const byHand = row.sources.includes("by_hand");
  const idp = row.sources.includes("idp");
  return (
    <ul className="flex flex-col gap-0.5" data-testid="team-person-source">
      {byHand && <li>{t("teams.people.byHand")}</li>}
      {idp &&
        row.idp_entries.map((entry) => (
          <li
            key={`${entry.claim ?? ""}/${entry.group}`}
            className="wrap-anywhere"
            title={
              entry.claim === undefined || entry.claim === ""
                ? undefined
                : t("teams.people.claimTitle", { claim: entry.claim })
            }
          >
            {t("teams.people.fromGroup")} <code className="font-mono text-xs">{entry.group}</code>
          </li>
        ))}
      {idp && row.idp_entries.length === 0 && <li>{t("teams.people.fromIdp")}</li>}
    </ul>
  );
}

function PersonName({ row }: { row: TeamUser }) {
  const { t } = useTranslation();
  const login = row.user.login;
  return (
    <span className="wrap-anywhere">
      <span className="font-medium">
        {row.user.deactivated
          ? t("teams.people.deactivated", { name: row.user.name })
          : row.user.name}
      </span>
      {login !== undefined && login !== "" && login !== row.user.name && (
        <span className="text-muted-foreground"> · {login}</span>
      )}
    </span>
  );
}

function Messengers({ row }: { row: TeamUser }) {
  const { t } = useTranslation();
  if (row.linked_messengers.length === 0) {
    return <span className="text-muted-foreground">{t("teams.people.notLinked")}</span>;
  }
  return (
    <ul className="flex flex-col gap-0.5">
      {row.linked_messengers.map((m) => (
        <li key={`${m.type}/${m.connection.id}`} className="wrap-anywhere">
          {messengerName(t, m.type)} · {m.connection.name}
        </li>
      ))}
    </ul>
  );
}

function AddPerson({
  teamId,
  members,
  onDone,
}: {
  teamId: string;
  /** The Users already added by hand. */
  members: ReadonlySet<string>;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const id = useId();
  const [text, setText] = useState("");
  const [q, setQ] = useState("");
  const [userId, setUserId] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQ(text.trim()), SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [text]);
  const params = q === "" ? { limit: USERS_LIMIT } : { limit: USERS_LIMIT, q };
  const users = useQuery({
    queryKey: getListUsersQueryKey(params),
    queryFn: ({ signal }) => listUsers(params, { signal }),
    placeholderData: keepPreviousData,
  });
  const add = useMutation({
    mutationFn: (user: string) => addTeamUser(teamId, { user_id: user }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: getListTeamUsersQueryKey(teamId) }),
        queryClient.invalidateQueries({ queryKey: getGetTeamQueryKey(teamId) }),
        queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() }),
      ]);
      onDone();
    },
  });
  const choices = (users.data?.items ?? []).filter(
    (u) => !members.has(u.id) && u.status !== "deleted",
  );
  const unknown = isApiError(add.error) && add.error.errors?.some((e) => e.code === "unknown_id");
  return (
    <form
      className="flex flex-col gap-3 rounded-lg border p-3"
      aria-label={t("teams.people.add")}
      onSubmit={(e) => {
        e.preventDefault();
        if (userId !== "") {
          add.mutate(userId);
        }
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-search`}>{t("teams.people.search")}</Label>
          <Input
            id={`${id}-search`}
            type="search"
            value={text}
            autoComplete="off"
            placeholder={t("teams.people.searchHint")}
            onChange={(e) => setText(e.target.value)}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-user`}>{t("teams.people.user")}</Label>
          <NativeSelect
            id={`${id}-user`}
            className="w-full"
            value={userId}
            aria-invalid={unknown}
            onChange={(e) => setUserId(e.target.value)}
          >
            <NativeSelectOption value="">{t("teams.people.chooseUser")}</NativeSelectOption>
            {choices.map((u) => (
              <NativeSelectOption key={u.id} value={u.id}>
                {u.login !== "" && u.login !== u.name ? `${u.name} (${u.login})` : u.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      </div>
      {users.isError && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, users.error)}
        </p>
      )}
      {add.isError && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {unknown ? t("teams.people.unknownUser") : problemText(t, add.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={userId === "" || add.isPending}>
          {t("teams.people.addSubmit")}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

export function TeamPeople({ teamId, canWrite }: { teamId: string; canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const people = useListTeamUsers(teamId);
  const [adding, setAdding] = useState(false);
  const remove = useMutation({
    mutationFn: (userId: string) => removeTeamUser(teamId, userId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: getListTeamUsersQueryKey(teamId) }),
        queryClient.invalidateQueries({ queryKey: getGetTeamQueryKey(teamId) }),
        queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() }),
      ]);
    },
  });
  const rows = people.data?.items ?? [];
  const byHand = new Set(rows.filter((r) => r.sources.includes("by_hand")).map((r) => r.user.id));
  return (
    <section
      aria-labelledby="team-people-title"
      className="flex min-w-0 flex-col gap-3"
      data-testid="team-people"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="team-people-title" className="text-lg font-semibold">
          {t("teams.people.title")}
        </h2>
        {canWrite && !adding && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <PlusIcon aria-hidden="true" />
            {t("teams.people.add")}
          </Button>
        )}
      </div>
      {canWrite && adding && (
        <AddPerson teamId={teamId} members={byHand} onDone={() => setAdding(false)} />
      )}
      {remove.isError && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, remove.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="relative overflow-x-auto rounded-lg border">
        <table
          className="w-full border-collapse text-left text-sm"
          aria-label={t("teams.people.title")}
        >
          <thead className="bg-muted/50">
            <tr>
              <th
                scope="col"
                className="px-3 py-2 font-medium whitespace-nowrap text-muted-foreground"
              >
                {t("teams.people.user")}
              </th>
              <th
                scope="col"
                className="px-3 py-2 font-medium whitespace-nowrap text-muted-foreground"
              >
                {t("teams.people.role")}
              </th>
              <th
                scope="col"
                className="px-3 py-2 font-medium whitespace-nowrap text-muted-foreground"
              >
                {t("teams.people.source")}
              </th>
              <th
                scope="col"
                className="px-3 py-2 font-medium whitespace-nowrap text-muted-foreground"
              >
                {t("teams.people.messengers")}
              </th>
              {canWrite && (
                <th
                  scope="col"
                  className="px-3 py-2 font-medium whitespace-nowrap text-muted-foreground"
                >
                  <span className="sr-only">{t("teams.people.actions")}</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.user.id} className="border-t align-top" data-testid="team-person">
                <td className="min-w-32 px-3 py-2">
                  <PersonName row={row} />
                </td>
                <td className="px-3 py-2">{roleLabel(t, row.role)}</td>
                <td className="min-w-40 px-3 py-2">
                  <SourceCell row={row} />
                </td>
                <td className="min-w-32 px-3 py-2">
                  <Messengers row={row} />
                </td>
                {canWrite && (
                  <td className="px-3 py-2">
                    {row.sources.includes("by_hand") && (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={remove.isPending}
                        aria-label={t("teams.people.removeNamed", { name: row.user.name })}
                        onClick={() => remove.mutate(row.user.id)}
                      >
                        {t("teams.people.remove")}
                      </Button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {rows.length === 0 && (
          <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
            {people.isPending
              ? t("common.loading")
              : people.isError
                ? problemText(t, people.error)
                : t("teams.people.empty")}
          </p>
        )}
      </div>
    </section>
  );
}
