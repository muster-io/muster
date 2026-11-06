// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Team (C-25.FR-3, FR-4, FR-9; C-25.AC-10): its name, slug and description, "People", "Identity provider groups" and
// "Working hours". Each section saves on its own. The page keeps the version of the Team it was read at (and moves it on
// with each of its own saves), sends it as If-Match with every other field as read, and says so when a newer version
// arrives, so a save over a newer version shows a conflict instead of overwriting it, as the OIDC settings do.
// Without teams:write the page only shows the Team.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";

import { useGetOidcSettings } from "../api/gen/endpoints/oidc/oidc";
import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import {
  deleteTeam,
  getGetTeamQueryKey,
  getListTeamsQueryKey,
  updateTeam,
  useGetTeam,
} from "../api/gen/endpoints/teams/teams";
import type { Team, TeamInput, WorkingHours } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import {
  ClaimEntriesEditor,
  type ClaimEntryRow,
  type EntryErrors,
  newEntryKey,
} from "../components/claim-entries-editor";
import { DocsLink, PageTitle } from "../components/docs-link";
import { TeamForm } from "../components/team-form";
import { TeamPeople } from "../components/team-people";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import {
  type HoursErrors,
  WorkingHoursEditor,
  hoursErrorText,
  workingHoursText,
} from "../components/working-hours-editor";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";

export const Route = createFileRoute("/teams/$teamId")({
  staticData: { shell: true },
  component: TeamPage,
});

/** What the sections share: the version the page was read at, and what a save or a refusal tells the page. */
interface TeamBase {
  base: Team;
  /** The page saved a new version itself; it is the base from now on. */
  saved: (updated: Team) => void;
  /** A save was refused because the Team changed meanwhile (412). */
  conflict: () => void;
}

/**
 * Saves a Team with a change to one part of it: everything else goes as the base has it, the Team matchers and the
 * Destinations of the Team are left out, which keeps them, and the version of the base is the If-Match.
 */
function useTeamSave({ base, saved, conflict }: TeamBase) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (patch: Partial<TeamInput>) => {
      await queryClient.cancelQueries({ queryKey: getGetTeamQueryKey(base.id) });
      const merged: TeamInput = {
        name: base.name,
        slug: base.slug,
        description: base.description,
        own_working_hours: base.own_working_hours,
        working_hours: base.working_hours,
        idp_entries: base.idp_entries,
        ...patch,
      };
      const { working_hours: hours, ...rest } = merged;
      const input: TeamInput = merged.own_working_hours ? { ...rest, working_hours: hours } : rest;
      return updateTeam(base.id, input, { headers: { "If-Match": base.etag } });
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetTeamQueryKey(base.id), updated);
      void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
      saved(updated);
    },
    onError: (err) => {
      if (isStale(err)) {
        conflict();
      }
    },
  });
}

/** The refusals of an update by the field of a section they name, such as /idp_entries or /working_hours. */
function refusals(err: unknown, prefix: string): { pointer: string; code: string }[] {
  if (!isApiError(err)) {
    return [];
  }
  return (err.errors ?? [])
    .filter((e) => e.pointer === prefix || e.pointer.startsWith(`${prefix}/`))
    .map((e) => ({ pointer: e.pointer.slice(prefix.length), code: e.code }));
}

function Section({
  id,
  title,
  docs,
  children,
}: {
  id: string;
  title: string;
  docs?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-3" data-testid={id}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id={id} className="text-lg font-semibold">
          {title}
        </h2>
        {docs}
      </div>
      {children}
    </section>
  );
}

/** The end of a section: a refusal that its fields do not show, Save and "Saved.". */
function SectionBar({
  save,
  saved,
  dirty,
  shown,
  testId,
}: {
  save: ReturnType<typeof useTeamSave>;
  saved: boolean;
  dirty: boolean;
  /** Whether the refusal is shown on the section's fields. */
  shown: boolean;
  testId: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-3">
      {save.isError && !isStale(save.error) && !shown && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">{problemText(t, save.error)}</AlertDescription>
        </Alert>
      )}
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={save.isPending}>
          {t("common.save")}
        </Button>
        <span role="status" className="text-sm text-muted-foreground" data-testid={testId}>
          {saved && !dirty ? t("common.saved") : ""}
        </span>
      </div>
    </div>
  );
}

function rowsOf(team: Team): ClaimEntryRow[] {
  return team.idp_entries.map((e) => ({
    key: newEntryKey(),
    claim: e.claim ?? "",
    group: e.group,
  }));
}

function sameRows(a: readonly ClaimEntryRow[], b: readonly ClaimEntryRow[]): boolean {
  return (
    a.length === b.length &&
    a.every((row, i) => row.group === b[i]?.group && (row.claim ?? "") === (b[i]?.claim ?? ""))
  );
}

/** The longest claim or group of an entry, as the API checks it. */
const ENTRY_MAX = 200;

/** The checks the server repeats on the entries, before they are sent: an empty or a too long group, a too long claim. */
function entryErrors(rows: readonly ClaimEntryRow[]): Map<number, string> {
  const found = new Map<number, string>();
  rows.forEach((row, index) => {
    if (row.group.trim() === "") {
      found.set(index, "required");
    } else if (Array.from(row.group.trim()).length > ENTRY_MAX) {
      found.set(index, "too_long");
    }
  });
  return found;
}

function IdpGroups({ session, canWrite }: { session: TeamBase; canWrite: boolean }) {
  const { t } = useTranslation();
  const team = session.base;
  const canOidc = useCan("oidc:read");
  const settings = useGetOidcSettings({ query: { enabled: canWrite && canOidc } });
  const [initial, setInitial] = useState(() => rowsOf(team));
  const [rows, setRows] = useState(initial);
  const [saved, setSaved] = useState(false);
  const save = useTeamSave(session);
  const dirty = !sameRows(rows, initial);
  // The checks the entries failed before they were sent, until the entries change.
  const [local, setLocal] = useState<ReadonlyMap<number, string>>(new Map());
  const errors = new Map<number, EntryErrors>();
  for (const [index, code] of local) {
    errors.set(index, { group: fieldErrorText(t, code) });
  }
  for (const { pointer, code } of refusals(save.error, "/idp_entries")) {
    const match = /^\/(\d+)\/(claim|group)$/.exec(pointer);
    if (match?.[1] !== undefined) {
      const index = Number(match[1]);
      const field = match[2] === "claim" ? "claim" : "group";
      errors.set(index, { ...errors.get(index), [field]: fieldErrorText(t, code) });
    }
  }
  const shown = errors.size > 0;
  const groupsClaim = settings.data?.groups_claim ?? "groups";
  const docs = <DocsLink to="field.team_idp" kind="learn_more" about={t("docs.fields.teamIdp")} />;
  if (!canWrite) {
    return (
      <Section id="team-idp" title={t("teams.idp.title")} docs={docs}>
        <p className="text-sm text-muted-foreground">{t("teams.idp.hint")}</p>
        {team.idp_entries.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("teams.idp.empty")}</p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm" data-testid="team-idp-entries">
            {team.idp_entries.map((entry) => (
              <li key={`${entry.claim ?? ""}/${entry.group}`} className="wrap-anywhere">
                <span className="text-muted-foreground">
                  {entry.claim === undefined || entry.claim === "" ? groupsClaim : entry.claim}
                </span>{" "}
                <code className="font-mono text-xs">{entry.group}</code>
              </li>
            ))}
          </ul>
        )}
      </Section>
    );
  }
  return (
    <Section id="team-idp" title={t("teams.idp.title")} docs={docs}>
      <form
        noValidate
        className="flex min-w-0 flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          setSaved(false);
          const refused = entryErrors(rows);
          const longClaim = rows.findIndex(
            (r) => Array.from((r.claim ?? "").trim()).length > ENTRY_MAX,
          );
          setLocal(
            longClaim >= 0 && !refused.has(longClaim)
              ? new Map([[longClaim, "too_long"]])
              : refused,
          );
          if (refused.size > 0 || longClaim >= 0) {
            return;
          }
          save.mutate(
            {
              idp_entries: rows.map((r) => {
                const claim = (r.claim ?? "").trim();
                return claim === "" ? { group: r.group.trim() } : { claim, group: r.group.trim() };
              }),
            },
            {
              onSuccess: (updated) => {
                const next = rowsOf(updated);
                setInitial(next);
                setRows(next);
                setSaved(true);
              },
            },
          );
        }}
      >
        <ClaimEntriesEditor
          value={rows}
          onChange={(next) => {
            setRows(next);
            setLocal(new Map());
            setSaved(false);
          }}
          claims={settings.data?.claims_supported ?? []}
          groupsClaim={groupsClaim}
          errors={errors}
          hideLegend
          testId="team-idp-entries"
          texts={{
            legend: t("teams.idp.legend"),
            hint: t("teams.idp.hint"),
            empty: t("teams.idp.empty"),
            add: t("teams.idp.add"),
            claim: (number) => t("teams.idp.claim", { number }),
            group: (number) => t("teams.idp.group", { number }),
            role: () => "",
            remove: (group) => t("teams.idp.remove", { group }),
          }}
        />
        {settings.isError && (
          <p className="text-sm text-destructive" role="alert">
            {problemText(t, settings.error)}
          </p>
        )}
        <SectionBar
          save={save}
          saved={saved}
          dirty={dirty}
          shown={shown}
          testId="team-idp-status"
        />
      </form>
    </Section>
  );
}

/** The checks the server repeats on the Working hours, before they are sent. */
function hoursChecks(hours: WorkingHours): HoursErrors {
  const found: HoursErrors = {};
  if (hours.days.length === 0) {
    found.days = "required";
  }
  if (hours.start === "") {
    found.start = "required";
  }
  if (hours.end === "") {
    found.end = "required";
  } else if (hours.start !== "" && hours.end <= hours.start) {
    found.end = "out_of_range";
  }
  return found;
}

function WorkingHoursSection({ session, canWrite }: { session: TeamBase; canWrite: boolean }) {
  const { t } = useTranslation();
  const team = session.base;
  const canOrganization = useCan("organization:read");
  const organization = useGetOrganization({ query: { enabled: canWrite && canOrganization } });
  const [initial, setInitial] = useState({
    own: team.own_working_hours,
    hours: team.working_hours,
  });
  const [own, setOwn] = useState(initial.own);
  const [hours, setHours] = useState<WorkingHours>(initial.hours);
  const [saved, setSaved] = useState(false);
  const save = useTeamSave(session);
  // The checks the fields failed before they were sent, as codes, until a field changes.
  const [local, setLocal] = useState<HoursErrors>({});
  const dirty =
    own !== initial.own || (own && JSON.stringify(hours) !== JSON.stringify(initial.hours));
  const errors: HoursErrors = {};
  for (const field of ["days", "start", "end", "time_zone"] as const) {
    const code = local[field];
    if (code !== undefined) {
      errors[field] = hoursErrorText(t, code);
    }
  }
  for (const { pointer, code } of refusals(save.error, "/working_hours")) {
    const field = pointer.slice(1);
    if (field === "days" || field === "start" || field === "end" || field === "time_zone") {
      errors[field] ??= hoursErrorText(t, code);
    }
  }
  const shown = Object.keys(errors).length > 0;
  // The Organization's Working hours: read for an Admin, else those the Team follows while it has none of its own;
  // while neither is known, the line says so rather than show the Team's own hours as the Organization's.
  const orgHours: WorkingHours | undefined =
    organization.data?.working_hours ?? (team.own_working_hours ? undefined : team.working_hours);
  const docs = (
    <DocsLink to="field.working_hours" kind="learn_more" about={t("docs.fields.workingHours")} />
  );
  const editor = (
    <WorkingHoursEditor
      id="team-hours"
      own={own}
      onOwnChange={(next) => {
        setOwn(next);
        setLocal({});
        setSaved(false);
      }}
      value={hours}
      onChange={(next) => {
        setHours(next);
        setLocal({});
        setSaved(false);
      }}
      organization={orgHours}
      errors={errors}
      disabled={!canWrite}
    />
  );
  if (!canWrite) {
    return (
      <Section id="team-hours-section" title={t("teams.hours.title")} docs={docs}>
        {team.own_working_hours ? (
          <p className="text-sm" data-testid="working-hours-summary">
            {t("teams.hours.own")}: {workingHoursText(t, team.working_hours)}
          </p>
        ) : (
          <WorkingHoursEditor
            id="team-hours"
            own={false}
            onOwnChange={() => {}}
            value={team.working_hours}
            onChange={() => {}}
            organization={team.working_hours}
            disabled
          />
        )}
      </Section>
    );
  }
  return (
    <Section id="team-hours-section" title={t("teams.hours.title")} docs={docs}>
      <form
        noValidate
        className="flex min-w-0 flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          setSaved(false);
          const checks = own ? hoursChecks(hours) : {};
          setLocal(checks);
          if (Object.keys(checks).length > 0) {
            return;
          }
          save.mutate(
            { own_working_hours: own, ...(own ? { working_hours: hours } : {}) },
            {
              onSuccess: (updated) => {
                setInitial({ own: updated.own_working_hours, hours: updated.working_hours });
                setOwn(updated.own_working_hours);
                setHours(updated.working_hours);
                setSaved(true);
              },
            },
          );
        }}
      >
        {editor}
        <SectionBar
          save={save}
          saved={saved}
          dirty={dirty}
          shown={shown}
          testId="team-hours-status"
        />
      </form>
    </Section>
  );
}

/** "Delete Team": a Team that still has Schedules or Escalation chains is refused and the dialog says so. */
function DeleteTeam({ team, onReload }: { team: Team; onReload: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteTeam(team.id, { headers: { "If-Match": team.etag } }),
    onSuccess: async () => {
      await navigate({ to: "/teams" });
      queryClient.removeQueries({ queryKey: getGetTeamQueryKey(team.id) });
      void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
    },
  });
  const inUse = isApiError(remove.error) && remove.error.code === "team_in_use";
  return (
    <>
      <Button
        type="button"
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("teams.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("teams.delete.title", { name: team.name })}
            </DialogTitle>
            <DialogDescription>{t("teams.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>
                  {inUse
                    ? t("teams.delete.inUse")
                    : isStale(remove.error)
                      ? t("teams.errors.stale")
                      : problemText(t, remove.error)}
                </span>
                {isStale(remove.error) && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setOpen(false);
                      onReload();
                    }}
                  >
                    {t("common.reload")}
                  </Button>
                )}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("teams.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** The sections of a Team page, over the version of the Team the page was read at. */
export function TeamDetails({ team, reload }: { team: Team; reload: () => void }) {
  const { t } = useTranslation();
  const canWrite = useCan("teams:write");
  // The version the sections were read at: a newer one on the server never replaces what the Admin sees unasked.
  const [base, setBase] = useState(team);
  const [conflict, setConflict] = useState(false);
  // The versions this page replaced with its own saves: the read of the Team may still bring one of them for a moment.
  const [superseded, setSuperseded] = useState<ReadonlySet<string>>(new Set());
  const [saved, setSaved] = useState(false);
  const session: TeamBase = {
    base,
    saved: (updated) => {
      setSuperseded((now) => new Set(now).add(base.etag));
      setBase(updated);
      setConflict(false);
    },
    conflict: () => setConflict(true),
  };
  const save = useTeamSave(session);
  const stale = (team.etag !== base.etag && !superseded.has(team.etag)) || conflict;
  return (
    <div className="flex flex-col gap-8">
      {stale && (
        <Alert variant="destructive" data-testid="team-stale">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("teams.errors.stale")}</span>
            <Button type="button" variant="outline" size="sm" onClick={reload}>
              {t("common.reload")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      <Section id="team-details" title={t("teams.details.title")}>
        <TeamForm
          team={base}
          readOnly={!canWrite}
          submitLabel={t("common.save")}
          saved={saved}
          save={async (values) => {
            setSaved(false);
            await save.mutateAsync({
              name: values.name,
              slug: values.slug,
              description: values.description,
            });
            setSaved(true);
          }}
        />
      </Section>
      <TeamPeople teamId={team.id} canWrite={canWrite} />
      <IdpGroups session={session} canWrite={canWrite} />
      <WorkingHoursSection session={session} canWrite={canWrite} />
      {canWrite && (
        <div className="flex">
          <DeleteTeam team={base} onReload={reload} />
        </div>
      )}
    </div>
  );
}

/** The page of a Team by its id: it reads the Team, and a Reload mounts the sections again from a fresh read. */
export function TeamView({ teamId }: { teamId: string }) {
  const { t } = useTranslation();
  const query = useGetTeam(teamId);
  const team = query.data;
  const [generation, setGeneration] = useState(0);
  const [reloadingFrom, setReloadingFrom] = useState<number>();
  const { dataUpdatedAt, isFetching, refetch } = query;
  if (reloadingFrom !== undefined && dataUpdatedAt > reloadingFrom && !isFetching) {
    setReloadingFrom(undefined);
    setGeneration((n) => n + 1);
  }
  // A Reload whose read failed does not wait for a later read, which would mount the sections again unasked.
  if (
    reloadingFrom !== undefined &&
    query.isError &&
    !isFetching &&
    query.errorUpdatedAt > reloadingFrom
  ) {
    setReloadingFrom(undefined);
  }
  const reload = () => {
    setReloadingFrom(dataUpdatedAt);
    void refetch();
  };
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to="/teams" className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("teams.title")}
        </Link>
        <div className="flex min-w-0 flex-col gap-1">
          <PageTitle docs="page.teams">{team?.name ?? t("teams.page.title")}</PageTitle>
          {team !== undefined && (
            <p className="text-sm text-muted-foreground">
              {t("teams.page.slug")} <code className="font-mono text-xs">{team.slug}</code>
            </p>
          )}
        </div>
      </div>
      {query.isError && team !== undefined && (
        <Alert variant="destructive" data-testid="team-read-error">
          <AlertDescription className="text-current">
            {problemText(t, query.error)}
          </AlertDescription>
        </Alert>
      )}
      {team === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <TeamDetails key={generation} team={team} reload={reload} />
      )}
    </div>
  );
}

function TeamPage() {
  const { teamId } = Route.useParams();
  return (
    <RequirePermission permission="teams:read">
      <TeamView teamId={teamId} />
    </RequirePermission>
  );
}
