// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "New Team" (C-25.FR-3): the name, the slug and the description; the Team page opens after it is created, where its
// people, identity provider groups and Working hours are set. A new Team follows the Organization's Working hours.
// "Create Team" beside an unknown value of the Team label (C-25.FR-7) opens it with `slug=<value>`, which the slug and
// the name start with, or, for a value that is no valid slug, with `value=<value>`: an empty slug and a note that such
// Alerts reach a Team only through a Team matcher.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  createTeam,
  getListTeamLabelValuesQueryKey,
  getListTeamsQueryKey,
} from "../api/gen/endpoints/teams/teams";
import { RequirePermission } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { matcherText } from "../components/matcher-builder";
import { TeamForm } from "../components/team-form";
import { useTeamLabel } from "../components/team-label-values";
import { buttonVariants } from "../components/ui/button";

/** A value the URL may have parsed as a number, such as a slug of 42, kept as typed. */
const text = z
  .preprocess(
    (v) => (typeof v === "number" || typeof v === "boolean" ? String(v) : v),
    z.string().min(1),
  )
  .optional()
  .catch(undefined);

const searchSchema = z.object({ slug: text, value: text });

export const Route = createFileRoute("/teams/new")({
  staticData: { shell: true },
  validateSearch: searchSchema,
  component: NewTeamPage,
});

/** The note under the title for a value that is no slug: only a Team matcher can pick such Alerts. */
function ValueNote({ value }: { value: string }) {
  const { t } = useTranslation();
  const { label } = useTeamLabel();
  if (label === undefined) {
    return null;
  }
  return (
    <p
      className="max-w-2xl text-sm text-muted-foreground wrap-anywhere"
      data-testid="team-value-note"
    >
      {t("teams.create.valueNote", { value, matcher: matcherText({ label, op: "=", value }) })}
    </p>
  );
}

function NewTeam() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { slug, value } = Route.useSearch();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to="/teams" className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("teams.title")}
        </Link>
        <PageTitle docs="page.teams">{t("teams.create.title")}</PageTitle>
        {value !== undefined && slug === undefined && <ValueNote value={value} />}
      </div>
      <TeamForm
        initial={slug === undefined ? undefined : { name: slug, slug }}
        submitLabel={t("common.save")}
        onCancel={() => void navigate({ to: "/teams" })}
        save={async (values) => {
          const team = await createTeam({
            name: values.name,
            slug: values.slug,
            description: values.description,
            own_working_hours: false,
          });
          void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
          void queryClient.invalidateQueries({ queryKey: getListTeamLabelValuesQueryKey() });
          await navigate({ to: "/teams/$teamId", params: { teamId: team.id } });
        }}
      />
    </div>
  );
}

function NewTeamPage() {
  return (
    <RequirePermission permission="teams:write">
      <NewTeam />
    </RequirePermission>
  );
}
