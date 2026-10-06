// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Users (C-03.FR-3, C-03.FR-29): the list with Role, source, sign-in method, last sign-in, TOTP and status, filtered by
// a search and by Role, status and source, with the filters in the URL; "Create user" for users:write.

import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { getListUsersQueryKey, listUsers } from "../api/gen/endpoints/users/users";
import type { ListUsersParams, User } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import {
  UserCreateDialog,
  methodLabel,
  roleLabel,
  sourceLabel,
  statusLabel,
  useRoleNames,
} from "../components/user-create-dialog";
import { useTimeFormat } from "../lib/time";

const searchSchema = z.object({
  q: z.string().optional().catch(undefined),
  role: z.enum(["admin", "responder", "viewer"]).optional().catch(undefined),
  status: z.enum(["active", "disabled", "deleted"]).optional().catch(undefined),
  source: z.enum(["local", "oidc", "bootstrap"]).optional().catch(undefined),
});
type UsersSearch = z.infer<typeof searchSchema>;

export const Route = createFileRoute("/admin/users/")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: UsersPage,
});

/** How long the search waits for typing to pause before it changes the URL. */
const SEARCH_DELAY_MS = 300;

function Filters({ search }: { search: UsersSearch }) {
  const { t } = useTranslation();
  const navigate = useNavigate({ from: Route.fullPath });
  const roles = useRoleNames();
  const [q, setQ] = useState(search.q ?? "");
  const [shown, setShown] = useState(search.q);
  const set = (patch: Partial<UsersSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  // The search box writes the URL once typing pauses; a change of the URL (back, a link) fills it again.
  if (search.q !== shown) {
    setShown(search.q);
    if ((search.q ?? "") !== q.trim()) {
      setQ(search.q ?? "");
    }
  }
  useEffect(() => {
    const value = q.trim() === "" ? undefined : q.trim();
    if (value === search.q) {
      return undefined;
    }
    const timer = setTimeout(
      () => void navigate({ search: (prev) => ({ ...prev, q: value }), replace: true }),
      SEARCH_DELAY_MS,
    );
    return () => clearTimeout(timer);
  }, [q, search.q, navigate]);
  return (
    <div
      className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr_1fr]"
      role="search"
      aria-label={t("users.filters.label")}
    >
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="users-q">{t("users.filters.search")}</Label>
        <Input
          id="users-q"
          type="search"
          value={q}
          placeholder={t("users.filters.searchHint")}
          onChange={(e) => setQ(e.target.value)}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="users-role">{t("users.fields.role")}</Label>
        <NativeSelect
          id="users-role"
          className="w-full"
          value={search.role ?? ""}
          onChange={(e) => set({ role: roles.find((r) => r === e.target.value) })}
        >
          <NativeSelectOption value="">{t("users.filters.any")}</NativeSelectOption>
          {roles.map((role) => (
            <NativeSelectOption key={role} value={role}>
              {roleLabel(t, role)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="users-status">{t("users.fields.status")}</Label>
        <NativeSelect
          id="users-status"
          className="w-full"
          value={search.status ?? ""}
          onChange={(e) =>
            set({
              status: (["active", "disabled", "deleted"] as const).find(
                (s) => s === e.target.value,
              ),
            })
          }
        >
          <NativeSelectOption value="">{t("users.filters.any")}</NativeSelectOption>
          <NativeSelectOption value="active">{statusLabel(t, "active")}</NativeSelectOption>
          <NativeSelectOption value="disabled">{statusLabel(t, "disabled")}</NativeSelectOption>
          <NativeSelectOption value="deleted">{statusLabel(t, "deleted")}</NativeSelectOption>
        </NativeSelect>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="users-source">{t("users.fields.source")}</Label>
        <NativeSelect
          id="users-source"
          className="w-full"
          value={search.source ?? ""}
          onChange={(e) =>
            set({
              source: (["local", "oidc", "bootstrap"] as const).find((s) => s === e.target.value),
            })
          }
        >
          <NativeSelectOption value="">{t("users.filters.any")}</NativeSelectOption>
          <NativeSelectOption value="local">{sourceLabel(t, "local")}</NativeSelectOption>
          <NativeSelectOption value="oidc">{sourceLabel(t, "oidc")}</NativeSelectOption>
          <NativeSelectOption value="bootstrap">{sourceLabel(t, "bootstrap")}</NativeSelectOption>
        </NativeSelect>
      </div>
    </div>
  );
}

function NameCell({ row }: { row: User }) {
  return (
    <div className="flex flex-col">
      <Link
        to="/admin/users/$userId"
        params={{ userId: row.id }}
        className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline"
      >
        {row.name}
      </Link>
      <span className="text-muted-foreground">{row.login}</span>
    </div>
  );
}

function LastSignInCell({ row }: { row: User }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  return row.last_sign_in_at ? (
    <time dateTime={row.last_sign_in_at}>{dateTime(row.last_sign_in_at)}</time>
  ) : (
    <span className="text-muted-foreground">{t("users.never")}</span>
  );
}

function UsersTable({ search }: { search: UsersSearch }) {
  const { t } = useTranslation();
  const params: ListUsersParams = useMemo(
    () => ({ q: search.q, role: search.role, status: search.status, source: search.source }),
    [search.q, search.role, search.status, search.source],
  );
  const list = useCursorList<User>(getListUsersQueryKey(params), (cursor, signal) =>
    listUsers({ ...params, cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<User>[] => [
      { id: "name", header: t("users.fields.name"), className: "min-w-44", Cell: NameCell },
      { id: "role", header: t("users.fields.role"), text: (u) => roleLabel(t, u.role) },
      { id: "source", header: t("users.fields.source"), text: (u) => sourceLabel(t, u.source) },
      {
        id: "method",
        header: t("users.fields.method"),
        text: (u) => methodLabel(t, u.sign_in_method),
      },
      {
        id: "lastSignIn",
        header: t("users.fields.lastSignIn"),
        className: "whitespace-nowrap",
        Cell: LastSignInCell,
      },
      {
        id: "totp",
        header: t("users.fields.totp"),
        text: (u) => (u.totp_enabled ? t("users.totp.on") : t("users.totp.off")),
      },
      { id: "status", header: t("users.fields.status"), text: (u) => statusLabel(t, u.status) },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("users.title")}
      columns={columns}
      list={list}
      rowId={(u) => u.id}
      empty={t("users.empty")}
    />
  );
}

function UsersPage() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const canWrite = useCan("users:write");
  return (
    <RequirePermission permission="users:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-2xl font-semibold tracking-tight">{t("users.title")}</h1>
          {canWrite && <UserCreateDialog />}
        </div>
        <Filters search={search} />
        <UsersTable search={search} />
      </div>
    </RequirePermission>
  );
}
