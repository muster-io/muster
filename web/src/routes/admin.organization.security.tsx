// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Security (C-03.FR-20): the "TOTP required" policy. The update sends every other field as it was read,
// with the version the form was read at as If-Match, so a save over a newer version shows a conflict instead of
// overwriting it. S-056 adds the token grace period and the Keyring here.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  getGetOrganizationQueryKey,
  getGetOrganizationQueryOptions,
  updateOrganization,
  useGetOrganization,
} from "../api/gen/endpoints/organization/organization";
import type { Organization, OrganizationInput, TotpPolicy } from "../api/gen/model";
import { UpdateOrganizationBody } from "../api/gen/zod/organization/organization.zod";
import { RequirePermission } from "../components/app-shell";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { isStale, problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/security")({
  staticData: { shell: true },
  component: SecurityPage,
});

// The generated body is the intersection of the base and the input; the policy is a field of the base.
const schema = UpdateOrganizationBody.def.left.pick({ totp_required: true });
type SecurityValues = { totp_required: TotpPolicy };

const POLICIES: readonly TotpPolicy[] = ["nobody", "local_users", "everyone"];

/** The Organization as an update: everything as read, the stored Secrets kept, the policy as chosen. */
function organizationInput(org: Organization, policy: TotpPolicy): OrganizationInput {
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  return { ...rest, totp_required: policy, outgoing_heartbeat: { proxy } };
}

function SecurityForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  // The version the form was read at: a newer one on the server never replaces what the Admin sees unasked.
  const [base, setBase] = useState(organization);
  const [saved, setSaved] = useState(false);
  const form = useForm<SecurityValues>({
    resolver: zodResolver(schema),
    defaultValues: { totp_required: organization.totp_required },
  });
  const { isDirty } = form.formState;
  const save = useMutation({
    mutationFn: (policy: TotpPolicy) =>
      updateOrganization(organizationInput(base, policy), {
        headers: { "If-Match": base.etag },
      }),
    // A read still on its way (a live hint) must not bring back the version this save replaces.
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetOrganizationQueryKey() }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetOrganizationQueryKey(), updated);
      setBase(updated);
      form.reset({ totp_required: updated.totp_required });
      setSaved(true);
    },
  });
  const reload = () => {
    save.reset();
    setSaved(false);
    void queryClient
      .fetchQuery({ ...getGetOrganizationQueryOptions(), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset({ totp_required: fresh.totp_required });
      });
  };
  const newer = organization.etag !== base.etag && !save.isPending;
  const label = (policy: TotpPolicy) => {
    switch (policy) {
      case "nobody":
        return t("security.totp.nobody");
      case "local_users":
        return t("security.totp.localUsers");
      default:
        return t("security.totp.everyone");
    }
  };
  const hint = (policy: TotpPolicy) => {
    switch (policy) {
      case "nobody":
        return t("security.totp.nobodyHint");
      case "local_users":
        return t("security.totp.localUsersHint");
      default:
        return t("security.totp.everyoneHint");
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("security.totp.title")}</h2>
        </CardTitle>
        <CardDescription>{t("security.totp.hint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit(({ totp_required }) => {
            setSaved(false);
            save.mutate(totp_required);
          })}
        >
          <fieldset className="flex flex-col gap-3">
            <legend className="mb-2 text-sm font-medium">{t("security.totp.legend")}</legend>
            {POLICIES.map((policy) => (
              <div key={policy} className="flex items-start gap-2">
                <input
                  id={`totp-policy-${policy}`}
                  type="radio"
                  value={policy}
                  className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
                  aria-describedby={`totp-policy-${policy}-hint`}
                  {...form.register("totp_required")}
                />
                <div className="flex flex-col gap-0.5">
                  <label htmlFor={`totp-policy-${policy}`} className="text-sm font-medium">
                    {label(policy)}
                  </label>
                  <p id={`totp-policy-${policy}-hint`} className="text-sm text-muted-foreground">
                    {hint(policy)}
                  </p>
                </div>
              </div>
            ))}
          </fieldset>
          {(newer || isStale(save.error)) && (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>{t("errors.stale")}</span>
                <Button variant="outline" size="sm" onClick={reload}>
                  {t("common.reload")}
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {save.isError && !isStale(save.error) && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, save.error)}
              </AlertDescription>
            </Alert>
          )}
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={save.isPending}>
              {t("common.save")}
            </Button>
            <span
              role="status"
              className="text-sm text-muted-foreground"
              data-testid="security-status"
            >
              {saved && !isDirty ? t("common.saved") : ""}
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function SecurityView() {
  const { t } = useTranslation();
  const query = useGetOrganization();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight">{t("security.title")}</h1>
        <p className="text-muted-foreground">{t("security.hint")}</p>
      </div>
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="max-w-2xl">
          <SecurityForm organization={query.data} />
        </div>
      )}
    </div>
  );
}

function SecurityPage() {
  return (
    <RequirePermission permission="organization:write">
      <SecurityView />
    </RequirePermission>
  );
}
