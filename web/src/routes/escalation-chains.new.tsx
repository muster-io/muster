// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "New chain" (C-32.FR-1, FR-3): the name, the owning Team — the Team the page was opened from —, the steps. The
// chain's page opens after it is created. Only Admins (escalation-chains:write).

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  createEscalationChain,
  getListEscalationChainsQueryKey,
} from "../api/gen/endpoints/escalation-chains/escalation-chains";
import { getListTeamsQueryKey } from "../api/gen/endpoints/teams/teams";
import { RequirePermission } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { EscalationChainForm } from "../components/escalation-chain-form";
import { buttonVariants } from "../components/ui/button";

const searchSchema = z.object({
  team: z.string().min(1).optional().catch(undefined),
});

export const Route = createFileRoute("/escalation-chains/new")({
  staticData: { shell: true },
  validateSearch: searchSchema,
  component: NewChainPage,
});

function NewChain() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { team } = Route.useSearch();
  const back = team === undefined || team === "mine" ? {} : { team };
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/escalation-chains"
          search={back}
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("escalation.title")}
        </Link>
        <PageTitle docs="page.escalation_chain">{t("escalation.create.title")}</PageTitle>
      </div>
      <EscalationChainForm
        {...(team === undefined || team === "mine" ? {} : { initialTeam: team })}
        submitLabel={t("common.save")}
        onCancel={() => void navigate({ to: "/escalation-chains", search: back })}
        save={async (input) => {
          const created = await createEscalationChain(input);
          void queryClient.invalidateQueries({ queryKey: getListEscalationChainsQueryKey() });
          void queryClient.invalidateQueries({ queryKey: getListTeamsQueryKey() });
          await navigate({ to: "/escalation-chains/$chainId", params: { chainId: created.id } });
          return created;
        }}
      />
    </div>
  );
}

function NewChainPage() {
  return (
    <RequirePermission permission="escalation-chains:write">
      <NewChain />
    </RequirePermission>
  );
}
