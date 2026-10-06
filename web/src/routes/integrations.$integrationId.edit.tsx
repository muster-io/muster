// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Editing an Integration (C-05.FR-1). The form keeps the version it was read at and sends it as If-Match: a newer
// version replaces an untouched form, while a form with changes keeps them and its save is refused (412) with "Someone
// else changed this integration. Reload to see the changes."

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetIntegrationQueryKey,
  getGetIntegrationQueryOptions,
  getListIntegrationsQueryKey,
  updateIntegration,
  useGetIntegration,
} from "../api/gen/endpoints/integrations/integrations";
import type { Integration } from "../api/gen/model";
import { RequirePermission } from "../components/app-shell";
import { IntegrationForm } from "../components/integration-form";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/integrations/$integrationId/edit")({
  staticData: { shell: true },
  component: EditIntegrationPage,
});

function EditForm({ current }: { current: Integration }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // A newer version (another Admin, another tab) replaces an untouched form.
  if (current.etag !== base.etag && !dirty) {
    setBase(current);
  }
  const page = { to: "/integrations/$integrationId", params: { integrationId: base.id } } as const;
  return (
    <>
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      <IntegrationForm
        key={base.etag}
        integration={base}
        submitLabel={t("common.save")}
        stale={current.etag !== base.etag}
        onDirtyChange={setDirty}
        save={async (input) => {
          await queryClient.cancelQueries({ queryKey: getGetIntegrationQueryKey(base.id) });
          const updated = await updateIntegration(base.id, input, {
            headers: { "If-Match": base.etag ?? "" },
          });
          queryClient.setQueryData(getGetIntegrationQueryKey(updated.id), updated);
          void queryClient.invalidateQueries({ queryKey: getListIntegrationsQueryKey() });
          await navigate(page);
        }}
        onReload={() => {
          setReloadError(null);
          queryClient
            .fetchQuery({ ...getGetIntegrationQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              setDirty(false);
              setBase(fresh);
            })
            .catch((err: unknown) => setReloadError(err));
        }}
        onCancel={() => void navigate(page)}
      />
    </>
  );
}

function EditIntegration({ integrationId }: { integrationId: string }) {
  const { t } = useTranslation();
  const query = useGetIntegration(integrationId);
  const integration = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/integrations/$integrationId"
          params={{ integrationId }}
          className={buttonVariants({ variant: "link", className: "w-fit max-w-full px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          <span className="truncate">{integration?.name ?? t("integrations.page.title")}</span>
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("integrations.edit.title")}</h1>
      </div>
      {integration === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : integration.builtin ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("integrations.errors.builtin")}
        </p>
      ) : (
        <EditForm current={integration} />
      )}
    </div>
  );
}

function EditIntegrationPage() {
  const { integrationId } = Route.useParams();
  return (
    <RequirePermission permission="integrations:write">
      <EditIntegration integrationId={integrationId} />
    </RequirePermission>
  );
}
