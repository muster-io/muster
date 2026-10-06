// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sign-in page (C-03.FR-24, FR-25): the local form, the OIDC button while OIDC is enabled, and the texts of the
// errors the OIDC callback redirects with. Its child /sign-in/totp asks for the second factor in the same frame.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { Outlet, createFileRoute, useChildMatches, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { useCreateSession, useGetSignInOptions } from "../api/gen/endpoints/sessions/sessions";
import { CreateSessionBody } from "../api/gen/zod/sessions/sessions.zod";
import { AuthLayout } from "../components/app-shell";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import {
  SESSION_ENDED,
  SESSION_QUERY_KEY,
  problemText,
  safeReturnTo,
  withReturnTo,
} from "../lib/api";

const searchSchema = z.object({
  error: z.string().optional().catch(undefined),
  reason: z.string().optional().catch(undefined),
  return_to: z.string().optional().catch(undefined),
});

export const Route = createFileRoute("/sign-in")({
  validateSearch: searchSchema,
  component: SignInRoute,
});

/** The text of an error code the OIDC callback redirects with (C-03.FR-7, FR-25, FR-28). */
function callbackErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "no_access":
      return t("signIn.errors.noAccess");
    case "login_taken":
      return t("signIn.errors.loginTaken");
    case "account_disabled":
      return t("signIn.errors.accountDisabled");
    case "oidc_disabled":
      return t("signIn.errors.oidcDisabled");
    case "invalid_request":
      return t("signIn.errors.invalidRequest");
    case "idp_error":
      return t("signIn.errors.idpError");
    default:
      return t("signIn.errors.unknown");
  }
}

function SignInRoute() {
  const childMatched = useChildMatches({ select: (matches) => matches.length > 0 });
  return <AuthLayout>{childMatched ? <Outlet /> : <SignInForm />}</AuthLayout>;
}

const formSchema = CreateSessionBody.pick({ login: true, password: true }).extend({
  login: z.string().trim().min(1, "required"),
  password: z.string().min(1, "required"),
});
type FormValues = z.infer<typeof formSchema>;

function SignInForm() {
  const { t } = useTranslation();
  const search = Route.useSearch();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const options = useGetSignInOptions();
  const returnTo = safeReturnTo(search.return_to);
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { login: "", password: "" },
  });
  const signIn = useCreateSession({
    mutation: {
      onSuccess: (session) => {
        queryClient.setQueryData(SESSION_QUERY_KEY, { session, ended: false });
        const next =
          session.state === "totp_required"
            ? withReturnTo("/sign-in/totp", returnTo)
            : session.state === "totp_enrolment_required"
              ? withReturnTo("/totp-enrolment", returnTo)
              : (returnTo ?? "/");
        void navigate({ href: next, replace: true });
      },
      onError: () => form.resetField("password"),
    },
  });

  const oidc = options.data?.oidc;
  const oidcHref = withReturnTo("/api/v1/sessions/oidc/start", returnTo);
  const message =
    search.error !== undefined
      ? callbackErrorText(t, search.error)
      : search.reason === SESSION_ENDED
        ? t("signIn.sessionEnded")
        : undefined;
  const errors = form.formState.errors;

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h1 className="text-lg">{t("signIn.title")}</h1>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {message !== undefined && !signIn.isError && (
          <Alert variant={search.error !== undefined ? "destructive" : "default"}>
            <AlertDescription className="text-current">{message}</AlertDescription>
          </Alert>
        )}
        {signIn.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, signIn.error)}
            </AlertDescription>
          </Alert>
        )}
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit((values) => signIn.mutate({ data: values }))}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="login">{t("signIn.login")}</Label>
            <Input
              id="login"
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              aria-invalid={errors.login !== undefined}
              aria-describedby={errors.login ? "login-error" : undefined}
              {...form.register("login")}
            />
            {errors.login && (
              <p id="login-error" className="text-sm text-destructive">
                {t("fieldErrors.required")}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="password">{t("signIn.password")}</Label>
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              aria-invalid={errors.password !== undefined}
              aria-describedby={errors.password ? "password-error" : undefined}
              {...form.register("password")}
            />
            {errors.password && (
              <p id="password-error" className="text-sm text-destructive">
                {t("fieldErrors.required")}
              </p>
            )}
          </div>
          <Button type="submit" size="lg" disabled={signIn.isPending}>
            {t("signIn.submit")}
          </Button>
        </form>
        {oidc?.enabled === true && (
          <>
            <div
              className="flex items-center gap-3 text-xs text-muted-foreground"
              aria-hidden="true"
            >
              <span className="h-px flex-1 bg-border" />
              {t("signIn.or")}
              <span className="h-px flex-1 bg-border" />
            </div>
            <a href={oidcHref} className={buttonVariants({ variant: "outline", size: "lg" })}>
              {t("signIn.withProvider", { provider: oidc.display_name ?? "OIDC" })}
            </a>
          </>
        )}
      </CardContent>
    </Card>
  );
}
