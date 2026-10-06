// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Audit log (C-03.FR-15): entries newest first for a time range, by default the last 7 days, filtered by actor,
// action and resource, with the filters in the URL. Each row shows the actor, the Transport and the diff, in which a
// Secret appears only as changed (C-03.FR-21).

import { createFileRoute, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { getListAuditLogQueryKey, listAuditLog } from "../api/gen/endpoints/audit-log/audit-log";
import { useListUsers } from "../api/gen/endpoints/users/users";
import type { AuditActor, AuditEntry, ListAuditLogParams, Transport } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { AuditDiff } from "../components/audit-diff";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { dayIn, startOfDayIn, useTimeFormat } from "../lib/time";

const DAY = /^\d{4}-\d{2}-\d{2}$/;

const searchSchema = z.object({
  from: z.string().regex(DAY).optional().catch(undefined),
  to: z.string().regex(DAY).optional().catch(undefined),
  actor: z.string().min(1).optional().catch(undefined),
  action: z.string().min(1).optional().catch(undefined),
  resource_type: z.string().min(1).optional().catch(undefined),
  resource_id: z.string().min(1).optional().catch(undefined),
});
type AuditSearch = z.infer<typeof searchSchema>;

export const Route = createFileRoute("/admin/audit-log")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: AuditLogPage,
});

/** The default time range: the last 7 days. */
const DEFAULT_DAYS = 7;

/** Action types to suggest; the field takes any other, as every capability adds its own. */
const KNOWN_ACTIONS = [
  "oidc_settings.updated",
  "organization.updated",
  "session.signed_in",
  "session.signed_out",
  "session.sign_in_failed",
  "session.second_factor_failed",
  "session.oidc_refused",
  "session.ended_all",
  "totp.enrolled",
  "totp.removed",
  "totp.reset",
  "totp.recovery_codes_regenerated",
  "user.created",
  "user.updated",
  "user.role_changed",
  "user.role_sync_kept_admin",
  "user.disabled",
  "user.enabled",
  "user.deleted",
  "user.converted_to_local",
  "user.oidc_linked",
  "user.oidc_link_refused",
  "user.oidc_refused",
  "user.password_changed",
  "user.password_reset",
  "user.password_set",
  "user.password_setup_link_created",
  "user.profile_updated",
];

const KNOWN_RESOURCES = ["user", "oidc_settings", "organization"];

function transportLabel(t: TFunction, transport: Transport): string {
  switch (transport) {
    case "ui":
      return t("audit.transport.ui");
    case "api":
      return t("audit.transport.api");
    case "mattermost":
      return "Mattermost";
    case "telegram":
      return "Telegram";
    case "cli":
      return t("audit.transport.cli");
    default:
      return t("audit.transport.system");
  }
}

function actorText(t: TFunction, actor: AuditActor): string {
  switch (actor.kind) {
    case "user":
      return actor.name;
    case "service_account":
      return t("audit.actor.serviceAccount", { name: actor.name });
    case "system":
      return t("audit.actor.system");
    case "bootstrap":
      return t("audit.actor.bootstrap");
    default:
      return t("audit.actor.cli", { name: actor.name });
  }
}

/** The text filters, applied together on Enter or with "Apply". */
function TextFilters({
  search,
  apply,
}: {
  search: AuditSearch;
  apply: (patch: Partial<AuditSearch>) => void;
}) {
  const { t } = useTranslation();
  const [action, setAction] = useState(search.action ?? "");
  const [resource, setResource] = useState(search.resource_type ?? "");
  // A change of the URL (back, a link, "Clear filters") fills the fields again.
  const [shown, setShown] = useState([search.action, search.resource_type]);
  if (shown[0] !== search.action || shown[1] !== search.resource_type) {
    setShown([search.action, search.resource_type]);
    setAction(search.action ?? "");
    setResource(search.resource_type ?? "");
  }
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const type = resource.trim() === "" ? undefined : resource.trim();
    apply({
      action: action.trim() === "" ? undefined : action.trim(),
      resource_type: type,
      // A resource id belongs to its type.
      resource_id: type === search.resource_type ? search.resource_id : undefined,
    });
  };
  return (
    <form className="contents" onSubmit={submit}>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="audit-action">{t("audit.filters.action")}</Label>
        <Input
          id="audit-action"
          list="audit-actions"
          value={action}
          spellCheck={false}
          autoComplete="off"
          placeholder="user.created"
          onChange={(e) => setAction(e.target.value)}
        />
        <datalist id="audit-actions">
          {KNOWN_ACTIONS.map((a) => (
            <option key={a} value={a} />
          ))}
        </datalist>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="audit-resource">{t("audit.filters.resource")}</Label>
        <Input
          id="audit-resource"
          list="audit-resources"
          value={resource}
          spellCheck={false}
          autoComplete="off"
          placeholder="user"
          onChange={(e) => setResource(e.target.value)}
        />
        <datalist id="audit-resources">
          {KNOWN_RESOURCES.map((r) => (
            <option key={r} value={r} />
          ))}
        </datalist>
      </div>
      <div className="flex items-end">
        <Button type="submit" variant="outline">
          {t("audit.filters.apply")}
        </Button>
      </div>
    </form>
  );
}

function ActorFilter({
  value,
  onChange,
}: {
  value: string | undefined;
  onChange: (actor: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const users = useListUsers({ limit: 500 });
  const items = users.data?.items ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor="audit-actor">{t("audit.filters.actor")}</Label>
      <NativeSelect
        id="audit-actor"
        className="w-full"
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        <NativeSelectOption value="">{t("audit.filters.anyActor")}</NativeSelectOption>
        {value !== undefined && !items.some((u) => u.id === value) && (
          <NativeSelectOption value={value}>{value}</NativeSelectOption>
        )}
        {items.map((u) => (
          <NativeSelectOption key={u.id} value={u.id}>
            {u.name === u.login ? u.name : `${u.name} (${u.login})`}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

function Filters({ search, defaultFrom }: { search: AuditSearch; defaultFrom: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate({ from: Route.fullPath });
  const canListUsers = useCan("users:read");
  const apply = (patch: Partial<AuditSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const filtered = Object.values(search).some((v) => v !== undefined);
  return (
    <div className="flex flex-col gap-3" role="search" aria-label={t("audit.filters.label")}>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[repeat(3,minmax(0,1fr))_minmax(0,1.5fr)_minmax(0,1fr)_auto]">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="audit-from">{t("audit.filters.from")}</Label>
          <Input
            id="audit-from"
            type="date"
            value={search.from ?? defaultFrom}
            max={search.to}
            onChange={(e) => apply({ from: DAY.test(e.target.value) ? e.target.value : undefined })}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="audit-to">{t("audit.filters.to")}</Label>
          <Input
            id="audit-to"
            type="date"
            value={search.to ?? ""}
            min={search.from ?? defaultFrom}
            onChange={(e) => apply({ to: DAY.test(e.target.value) ? e.target.value : undefined })}
          />
        </div>
        {canListUsers ? (
          <ActorFilter value={search.actor} onChange={(actor) => apply({ actor })} />
        ) : (
          <div />
        )}
        <TextFilters search={search} apply={apply} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {search.resource_id !== undefined && (
          <span className="inline-flex items-center gap-1 rounded-md border bg-muted/50 py-0.5 pr-0.5 pl-2 text-sm">
            {t("audit.filters.resourceId", { id: search.resource_id })}
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t("audit.filters.clearResourceId")}
              onClick={() => apply({ resource_id: undefined })}
            >
              <XIcon aria-hidden="true" />
            </Button>
          </span>
        )}
        {filtered && (
          <Button
            variant="link"
            className="px-0"
            onClick={() =>
              void navigate({
                search: {},
                replace: true,
              })
            }
          >
            {t("audit.filters.clear")}
          </Button>
        )}
      </div>
    </div>
  );
}

function AtCell({ row }: { row: AuditEntry }) {
  const { dateTime } = useTimeFormat();
  return <time dateTime={row.at}>{dateTime(row.at)}</time>;
}

function ActorCell({ row }: { row: AuditEntry }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col">
      <span data-testid="audit-actor">{actorText(t, row.actor)}</span>
      {row.token_name && (
        <span className="text-muted-foreground">
          {t("audit.actor.token", { name: row.token_name })}
        </span>
      )}
    </div>
  );
}

function ActionCell({ row }: { row: AuditEntry }) {
  return <code className="font-mono text-xs">{row.action}</code>;
}

function ResourceCell({ row }: { row: AuditEntry }) {
  const { t } = useTranslation();
  const navigate = useNavigate({ from: Route.fullPath });
  if (!row.resource_type) {
    return <span className="text-muted-foreground">—</span>;
  }
  const name = row.resource_name ?? row.resource_id;
  return (
    <div className="flex flex-col items-start">
      <span className="text-muted-foreground">{row.resource_type}</span>
      {row.resource_id ? (
        <Button
          variant="link"
          className="h-auto p-0 text-left whitespace-normal"
          title={t("audit.filters.byResource", { name })}
          onClick={() =>
            void navigate({
              search: (prev) => ({
                ...prev,
                resource_type: row.resource_type ?? undefined,
                resource_id: row.resource_id ?? undefined,
              }),
              replace: true,
            })
          }
        >
          {name}
        </Button>
      ) : (
        name && <span>{name}</span>
      )}
    </div>
  );
}

function DiffCell({ row }: { row: AuditEntry }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-1">
      <AuditDiff diff={row.diff} resourceType={row.resource_type} />
      {row.details !== undefined && Object.keys(row.details).length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer text-muted-foreground">{t("audit.details")}</summary>
          <pre className="mt-1 font-mono break-all whitespace-pre-wrap">
            {JSON.stringify(row.details, null, 2)}
          </pre>
        </details>
      )}
    </div>
  );
}

function AuditTable({ search, defaultFrom }: { search: AuditSearch; defaultFrom: Date }) {
  const { t } = useTranslation();
  const { timeZone } = useTimeFormat();
  const params: ListAuditLogParams = useMemo(
    () => ({
      from: (search.from ? startOfDayIn(search.from, timeZone) : defaultFrom).toISOString(),
      to: search.to ? startOfDayIn(search.to, timeZone, 1).toISOString() : undefined,
      actor: search.actor,
      action: search.action,
      resource_type: search.resource_type,
      resource_id: search.resource_id,
    }),
    [search, timeZone, defaultFrom],
  );
  const list = useCursorList<AuditEntry>(getListAuditLogQueryKey(params), (cursor, signal) =>
    listAuditLog({ ...params, cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<AuditEntry>[] => [
      { id: "at", header: t("audit.columns.at"), className: "whitespace-nowrap", Cell: AtCell },
      { id: "actor", header: t("audit.columns.actor"), className: "min-w-32", Cell: ActorCell },
      { id: "action", header: t("audit.columns.action"), Cell: ActionCell },
      {
        id: "resource",
        header: t("audit.columns.resource"),
        className: "min-w-32",
        Cell: ResourceCell,
      },
      {
        id: "transport",
        header: t("audit.columns.transport"),
        text: (e) => transportLabel(t, e.transport),
      },
      { id: "diff", header: t("audit.columns.diff"), className: "min-w-64", Cell: DiffCell },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("audit.title")}
      columns={columns}
      list={list}
      rowId={(e) => e.id}
      empty={t("audit.empty")}
    />
  );
}

function AuditLogView() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const { timeZone } = useTimeFormat();
  // The default range starts at the beginning of the day 7 days before the page opened, as the From field shows it.
  const [defaultFrom] = useState(() =>
    startOfDayIn(
      dayIn(new Date(Date.now() - DEFAULT_DAYS * 24 * 60 * 60 * 1000), timeZone),
      timeZone,
    ),
  );
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight">{t("audit.title")}</h1>
        <p className="text-muted-foreground">{t("audit.hint")}</p>
      </div>
      <Filters search={search} defaultFrom={dayIn(defaultFrom, timeZone)} />
      <AuditTable search={search} defaultFrom={defaultFrom} />
    </div>
  );
}

function AuditLogPage() {
  return (
    <RequirePermission permission="audit-log:read">
      <AuditLogView />
    </RequirePermission>
  );
}
