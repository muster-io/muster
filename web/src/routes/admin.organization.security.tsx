// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Security (C-03.FR-20, C-04.FR-8): the "TOTP required" policy and the token grace for OIDC users
// without an offline token, in days. The update sends every other field as it was read, with the version the form was
// read at as If-Match, so a save over a newer version shows a conflict instead of overwriting it.

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Organization, TotpPolicy } from "../api/gen/model";
import {
  Field,
  OrganizationPage,
  SaveBar,
  errorText,
  useOrganizationForm,
} from "../components/organization-form";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";

export const Route = createFileRoute("/admin/organization/security")({
  staticData: { shell: true },
  component: SecurityPage,
});

const DAY_SECONDS = 86_400;
const MAX_GRACE_DAYS = 36_500;

// The grace field holds whole days under the name of the API field, so that a refusal at /oidc_token_grace_seconds
// shows at the field.
const schema = z.object({
  totp_required: z.enum(["nobody", "local_users", "everyone"]),
  oidc_token_grace_seconds: z
    .number({ error: "required" })
    .int("invalid_format")
    .min(1, "out_of_range")
    .max(MAX_GRACE_DAYS, "out_of_range"),
});
type SecurityValues = z.infer<typeof schema>;

const POLICIES: readonly TotpPolicy[] = ["nobody", "local_users", "everyone"];

/** The grace in whole days as the form shows it; a stored value that is not whole days is rounded up for display. */
function graceDays(seconds: number): number {
  return Math.ceil(seconds / DAY_SECONDS);
}

function SecurityForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const { form, formRef, onSubmit, state } = useOrganizationForm<SecurityValues>({
    organization,
    values: (org) => ({
      totp_required: org.totp_required,
      oidc_token_grace_seconds: graceDays(org.oidc_token_grace_seconds),
    }),
    // A grace the Admin did not change is sent as stored, so a value that is not whole days is never rewritten unasked.
    patch: (v, base) => ({
      totp_required: v.totp_required,
      oidc_token_grace_seconds:
        v.oidc_token_grace_seconds === graceDays(base.oidc_token_grace_seconds)
          ? base.oidc_token_grace_seconds
          : v.oidc_token_grace_seconds * DAY_SECONDS,
    }),
    fields: () => ["totp_required", "oidc_token_grace_seconds"],
    resolver: zodResolver(schema),
  });
  const { errors } = form.formState;
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
    <form ref={formRef} noValidate className="flex flex-col gap-6" onSubmit={onSubmit}>
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("security.totp.title")}</h2>
          </CardTitle>
          <CardDescription>{t("security.totp.hint")}</CardDescription>
        </CardHeader>
        <CardContent>
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
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>{t("security.tokenGrace.title")}</h2>
          </CardTitle>
          <CardDescription>{t("security.tokenGrace.hint")}</CardDescription>
        </CardHeader>
        <CardContent>
          <Field
            id="oidc-token-grace"
            label={t("security.tokenGrace.label")}
            error={errorText(t, errors.oidc_token_grace_seconds)}
          >
            {(control) => (
              <Input
                {...control}
                type="number"
                inputMode="numeric"
                min={1}
                max={MAX_GRACE_DAYS}
                step={1}
                className="max-w-40"
                {...form.register("oidc_token_grace_seconds", { valueAsNumber: true })}
              />
            )}
          </Field>
        </CardContent>
      </Card>
      <SaveBar state={state} testId="security-status" />
    </form>
  );
}

function SecurityPage() {
  const { t } = useTranslation();
  return (
    <OrganizationPage title={t("security.title")} hint={t("security.hint")}>
      {(organization) => <SecurityForm organization={organization} />}
    </OrganizationPage>
  );
}
