// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "New Team" (C-25.FR-3): the name, the slug and the description; the Team page opens after it is created, where its
// people, identity provider groups and Working hours are set. A new Team follows the Organization's Working hours.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { createTeam, getListTeamsQueryKey } from "../api/gen/endpoints/teams/teams";
import { RequirePermission } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { TeamForm } from "../components/team-form";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/teams/new")({
  staticData: { shell: true },
  component: NewTeamPage,
});

function NewTeam() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to="/teams" className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("teams.title")}
        </Link>
        <PageTitle docs="page.teams">{t("teams.create.title")}</PageTitle>
      </div>
      <TeamForm
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
