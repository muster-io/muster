// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create integration" (C-05.FR-1): the form; the new Integration's page opens after it is created.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  createIntegration,
  getGetIntegrationQueryKey,
  getListIntegrationsQueryKey,
} from "../api/gen/endpoints/integrations/integrations";
import { RequirePermission } from "../components/app-shell";
import { IntegrationForm } from "../components/integration-form";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/integrations/new")({
  staticData: { shell: true },
  component: NewIntegrationPage,
});

function NewIntegration() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/integrations"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("integrations.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("integrations.create.title")}</h1>
      </div>
      <IntegrationForm
        submitLabel={t("integrations.create.submit")}
        save={async (input) => {
          const created = await createIntegration(input);
          queryClient.setQueryData(getGetIntegrationQueryKey(created.id), created);
          void queryClient.invalidateQueries({ queryKey: getListIntegrationsQueryKey() });
          await navigate({
            to: "/integrations/$integrationId",
            params: { integrationId: created.id },
          });
        }}
        onCancel={() => void navigate({ to: "/integrations" })}
      />
    </div>
  );
}

function NewIntegrationPage() {
  return (
    <RequirePermission permission="integrations:write">
      <NewIntegration />
    </RequirePermission>
  );
}
