// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The second factor of a sign-in (C-03.FR-10, FR-24): a TOTP code or a recovery code completes a session in the state
// totp_required, then the page the sign-in started from opens.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { useSubmitSessionTotp } from "../api/gen/endpoints/sessions/sessions";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { signOut } from "../components/user-menu";
import { SESSION_QUERY_KEY, problemText, safeReturnTo, withReturnTo } from "../lib/api";

export const Route = createFileRoute("/sign-in/totp")({
  component: SecondFactorPage,
});

const formSchema = z.object({ code: z.string().trim().min(1, "required") });
type FormValues = z.infer<typeof formSchema>;

function SecondFactorPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const returnTo = safeReturnTo(Route.useSearch().return_to);
  const [recovery, setRecovery] = useState(false);
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { code: "" },
  });
  const submit = useSubmitSessionTotp({
    mutation: {
      onSuccess: (session) => {
        queryClient.setQueryData(SESSION_QUERY_KEY, { session, ended: false });
        const next =
          session.state === "totp_enrolment_required"
            ? withReturnTo("/totp-enrolment", returnTo)
            : (returnTo ?? "/");
        void navigate({ href: next, replace: true });
      },
      onError: () => form.resetField("code"),
    },
  });
  const error = form.formState.errors.code;

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h1 className="text-lg">{t("secondFactor.title")}</h1>
        </CardTitle>
        <CardDescription>
          {recovery ? t("secondFactor.recoveryHint") : t("secondFactor.codeHint")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {submit.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, submit.error)}
            </AlertDescription>
          </Alert>
        )}
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit(({ code }) =>
            submit.mutate({
              data: recovery ? { recovery_code: code } : { totp_code: code.replace(/\s/g, "") },
            }),
          )}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="code">
              {recovery ? t("secondFactor.recoveryCode") : t("secondFactor.code")}
            </Label>
            <Input
              id="code"
              autoFocus
              autoComplete={recovery ? "off" : "one-time-code"}
              inputMode={recovery ? "text" : "numeric"}
              autoCapitalize="none"
              spellCheck={false}
              aria-invalid={error !== undefined}
              aria-describedby={error ? "code-error" : undefined}
              {...form.register("code")}
            />
            {error && (
              <p id="code-error" className="text-sm text-destructive">
                {t("fieldErrors.required")}
              </p>
            )}
          </div>
          <Button type="submit" size="lg" disabled={submit.isPending}>
            {t("secondFactor.submit")}
          </Button>
        </form>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button
            variant="link"
            className="px-0"
            onClick={() => {
              setRecovery(!recovery);
              form.reset({ code: "" });
              submit.reset();
            }}
          >
            {recovery ? t("secondFactor.useCode") : t("secondFactor.useRecoveryCode")}
          </Button>
          <Button variant="link" className="px-0" onClick={() => void signOut()}>
            {t("secondFactor.cancel")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
