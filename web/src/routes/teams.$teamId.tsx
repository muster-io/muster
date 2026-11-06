// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Team (C-25.FR-3, FR-4, FR-9; C-25.AC-10): its name, slug and description, "People", "Identity provider groups" and
// "Working hours". Each section saves on its own with the version of the Team that was read last as If-Match, so a save
// over a newer version shows a conflict instead of overwriting it. Without teams:write the page only shows the Team.

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
} from "../components/working-hours-editor";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";

export const Route = createFileRoute("/teams/$teamId")({
  staticData: { shell: true },
  component: TeamPage,
});

/**
 * Saves a Team with a change to one part of it: everything else goes as it was read, the Team matchers and the
 * Destinations of the Team are left out, which keeps them, and the version read last is the If-Match.
 */
function useTeamSave(team: Team) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (patch: Partial<TeamInput>) => {
      await queryClient.cancelQueries({ queryKey: getGetTeamQueryKey(team.id) });
      const merged: TeamInput = {
        name: team.name,
        slug: team.slug,
        description: team.description,
        own_working_hours: team.own_working_hours,
        working_hours: team.working_hours,
        idp_entries: team.idp_entries,
        ...patch,
      };
      const { working_hours: hours, ...rest } = merged;
      const input: TeamInput = merged.own_working_hours ? { ...rest, working_hours: hours } : rest;
      return updateTeam(team.id, input, { headers: { "If-Match": team.etag } });
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetTeamQueryKey(team.id), updated);
      void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
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

/** The end of a section: the conflict with its Reload, another refusal, Save and "Saved.". */
function SectionBar({
  save,
  saved,
  dirty,
  shown,
  testId,
  onReload,
}: {
  save: ReturnType<typeof useTeamSave>;
  saved: boolean;
  dirty: boolean;
  /** Whether the refusal is shown on the section's fields. */
  shown: boolean;
  testId: string;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-3">
      {isStale(save.error) && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("teams.errors.stale")}</span>
            <Button type="button" variant="outline" size="sm" onClick={onReload}>
              {t("common.reload")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
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

function IdpGroups({
  team,
  canWrite,
  onReload,
}: {
  team: Team;
  canWrite: boolean;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  const canOidc = useCan("oidc:read");
  const settings = useGetOidcSettings({ query: { enabled: canWrite && canOidc } });
  const [initial, setInitial] = useState(() => rowsOf(team));
  const [rows, setRows] = useState(initial);
  const [saved, setSaved] = useState(false);
  const save = useTeamSave(team);
  const dirty = !sameRows(rows, initial);
  const errors = new Map<number, EntryErrors>();
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
        <SectionBar
          save={save}
          saved={saved}
          dirty={dirty}
          shown={shown}
          testId="team-idp-status"
          onReload={onReload}
        />
      </form>
    </Section>
  );
}

function WorkingHoursSection({
  team,
  canWrite,
  onReload,
}: {
  team: Team;
  canWrite: boolean;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  const canOrganization = useCan("organization:read");
  const organization = useGetOrganization({ query: { enabled: canWrite && canOrganization } });
  const [initial, setInitial] = useState({
    own: team.own_working_hours,
    hours: team.working_hours,
  });
  const [own, setOwn] = useState(initial.own);
  const [hours, setHours] = useState<WorkingHours>(initial.hours);
  const [saved, setSaved] = useState(false);
  const save = useTeamSave(team);
  const dirty =
    own !== initial.own || (own && JSON.stringify(hours) !== JSON.stringify(initial.hours));
  const errors: HoursErrors = {};
  for (const { pointer, code } of refusals(save.error, "/working_hours")) {
    const field = pointer.slice(1);
    if (field === "days" || field === "start" || field === "end" || field === "time_zone") {
      errors[field] = hoursErrorText(t, code);
    }
  }
  const shown = Object.keys(errors).length > 0;
  // The Organization's Working hours: read for an Admin, else those the Team follows while it has none of its own.
  const orgHours: WorkingHours | undefined = organization.data?.working_hours;
  const fallback = team.own_working_hours ? undefined : team.working_hours;
  const docs = (
    <DocsLink to="field.working_hours" kind="learn_more" about={t("docs.fields.workingHours")} />
  );
  const editor = (
    <WorkingHoursEditor
      id="team-hours"
      own={own}
      onOwnChange={(next) => {
        setOwn(next);
        setSaved(false);
      }}
      value={hours}
      onChange={(next) => {
        setHours(next);
        setSaved(false);
      }}
      organization={orgHours ?? fallback ?? team.working_hours}
      errors={errors}
      disabled={!canWrite}
    />
  );
  if (!canWrite) {
    return (
      <Section id="team-hours-section" title={t("teams.hours.title")} docs={docs}>
        {team.own_working_hours ? (
          <p className="text-sm" data-testid="working-hours-summary">
            {t("teams.hours.own")}: {team.working_hours.start}–{team.working_hours.end},{" "}
            {team.working_hours.time_zone}
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
          onReload={onReload}
        />
      </form>
    </Section>
  );
}

/** "Delete Team": a Team that still has Schedules or Escalation chains is refused and the dialog says so. */
function DeleteTeam({ team }: { team: Team }) {
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
              <AlertDescription className="text-current">
                {inUse
                  ? t("teams.delete.inUse")
                  : isStale(remove.error)
                    ? t("teams.errors.stale")
                    : problemText(t, remove.error)}
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

function TeamDetails({
  team,
  generation,
  reload,
}: {
  team: Team;
  /** Changes when the sections are mounted again from a version read afresh. */
  generation: number;
  reload: () => void;
}) {
  const { t } = useTranslation();
  const canWrite = useCan("teams:write");
  const [saved, setSaved] = useState(false);
  const save = useTeamSave(team);
  return (
    <div className="flex flex-col gap-8">
      <Section id="team-details" title={t("teams.details.title")}>
        <TeamForm
          key={`details-${generation}`}
          team={team}
          readOnly={!canWrite}
          submitLabel={t("common.save")}
          saved={saved}
          onReload={reload}
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
      <IdpGroups
        key={`idp-${generation}-${team.id}`}
        team={team}
        canWrite={canWrite}
        onReload={reload}
      />
      <WorkingHoursSection
        key={`hours-${generation}-${team.id}`}
        team={team}
        canWrite={canWrite}
        onReload={reload}
      />
      {canWrite && (
        <div className="flex">
          <DeleteTeam team={team} />
        </div>
      )}
    </div>
  );
}

function TeamPage() {
  const { t } = useTranslation();
  const { teamId } = Route.useParams();
  const query = useGetTeam(teamId);
  const team = query.data;
  // A Reload reads the Team again and then mounts the sections again from it, losing their changes.
  const [generation, setGeneration] = useState(0);
  const [reloadingFrom, setReloadingFrom] = useState<number>();
  const { dataUpdatedAt, isFetching, refetch } = query;
  if (reloadingFrom !== undefined && dataUpdatedAt > reloadingFrom && !isFetching) {
    setReloadingFrom(undefined);
    setGeneration((n) => n + 1);
  }
  const reload = () => {
    setReloadingFrom(dataUpdatedAt);
    void refetch();
  };
  return (
    <RequirePermission permission="teams:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <Link
            to="/teams"
            className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
          >
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
        {team === undefined ? (
          <p className="text-sm text-muted-foreground" role="status">
            {query.isError ? problemText(t, query.error) : t("common.loading")}
          </p>
        ) : (
          <TeamDetails team={team} generation={generation} reload={reload} />
        )}
      </div>
    </RequirePermission>
  );
}
