// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The password setup page (C-03.FR-26). The link carries its token in the URL fragment, which no server sees: the page
// reads it, removes it from the address bar and posts it only together with the new password.

import { zodResolver } from "@hookform/resolvers/zod";
import { Link, createFileRoute } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { useCompletePasswordSetup } from "../api/gen/endpoints/sessions/sessions";
import { AuthLayout } from "../components/app-shell";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { applyFieldErrors, fieldErrorText, isApiError, problemText } from "../lib/api";

export const Route = createFileRoute("/password-setup")({
  component: PasswordSetupPage,
});

/** auth.password_min_length, a built-in value (defaults.md); the server enforces it with too_short. */
const MIN_PASSWORD_LENGTH = 12;

let takenToken: string | undefined;

/**
 * Takes the token out of "#token=…" and clears the fragment, so that it stays out of the history and bookmarks. The
 * token is kept for the next call, as React may run a state initializer twice.
 */
function takeToken(): string | undefined {
  const token = new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token");
  if (window.location.hash !== "") {
    window.history.replaceState(
      window.history.state,
      "",
      window.location.pathname + window.location.search,
    );
  }
  if (token !== null && token !== "") {
    takenToken = token;
  }
  return takenToken;
}

const formSchema = z
  .object({ password: z.string().min(1, "required"), repeat: z.string().min(1, "required") })
  .refine((v) => v.password === v.repeat, { path: ["repeat"], message: "mismatch" });
type FormValues = z.infer<typeof formSchema>;

/** The text of a refused link (410 link_expired or link_used, 404 for an unknown token). */
function linkErrorText(t: TFunction, err: unknown): string | undefined {
  if (!isApiError(err)) {
    return undefined;
  }
  if (err.status === 410 && err.code === "link_expired") {
    return t("passwordSetup.linkExpired");
  }
  if (err.status === 410) {
    return t("passwordSetup.linkUsed");
  }
  if (err.status === 404) {
    return t("passwordSetup.linkInvalid");
  }
  return undefined;
}

function PasswordSetupPage() {
  const { t } = useTranslation();
  const [token, setToken] = useState(takeToken);
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { password: "", repeat: "" },
  });
  const setup = useCompletePasswordSetup({
    mutation: {
      onSuccess: () => {
        takenToken = undefined;
      },
      onError: (err) => {
        if (isApiError(err)) {
          applyFieldErrors(err, form.setError, ["password"], (code) => fieldErrorText(t, code));
        }
      },
    },
  });
  const { reset: resetSetup } = setup;
  const { reset: resetForm } = form;
  // A second link opened in the same tab changes only the fragment, which does not load the page again.
  useEffect(() => {
    const onHash = () => {
      if (window.location.hash.includes("token=")) {
        setToken(takeToken());
        resetSetup();
        resetForm();
      }
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [resetSetup, resetForm]);
  const errors = form.formState.errors;
  const linkError =
    token === undefined ? t("passwordSetup.linkInvalid") : linkErrorText(t, setup.error);
  const fieldError = isApiError(setup.error) && setup.error.status === 422;

  return (
    <AuthLayout>
      <Card>
        <CardHeader>
          <CardTitle>
            <h1 className="text-lg">{t("passwordSetup.title")}</h1>
          </CardTitle>
          {!setup.isSuccess && linkError === undefined && (
            <CardDescription>{t("passwordSetup.hint")}</CardDescription>
          )}
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {setup.isSuccess ? (
            <>
              <Alert>
                <AlertDescription className="text-current">
                  {t("passwordSetup.done")}
                </AlertDescription>
              </Alert>
              <Link to="/sign-in" className={buttonVariants({ size: "lg" })}>
                {t("signIn.submit")}
              </Link>
            </>
          ) : linkError !== undefined ? (
            <Alert variant="destructive">
              <AlertDescription className="text-current">{linkError}</AlertDescription>
            </Alert>
          ) : (
            <form
              noValidate
              className="flex flex-col gap-4"
              onSubmit={form.handleSubmit(({ password }) =>
                setup.mutate({ data: { token: token ?? "", password } }),
              )}
            >
              {setup.isError && !fieldError && (
                <Alert variant="destructive">
                  <AlertDescription className="text-current">
                    {problemText(t, setup.error)}
                  </AlertDescription>
                </Alert>
              )}
              <div className="flex flex-col gap-2">
                <Label htmlFor="new-password">{t("passwordSetup.password")}</Label>
                <Input
                  id="new-password"
                  type="password"
                  autoComplete="new-password"
                  aria-invalid={errors.password !== undefined}
                  aria-describedby={
                    errors.password ? "new-password-hint new-password-error" : "new-password-hint"
                  }
                  {...form.register("password")}
                />
                <p id="new-password-hint" className="text-sm text-muted-foreground">
                  {t("passwordSetup.minLength", { count: MIN_PASSWORD_LENGTH })}
                </p>
                {errors.password && (
                  <p id="new-password-error" className="text-sm text-destructive">
                    {fieldErrorText(t, errors.password.message ?? "")}
                  </p>
                )}
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="repeat-password">{t("passwordSetup.repeat")}</Label>
                <Input
                  id="repeat-password"
                  type="password"
                  autoComplete="new-password"
                  aria-invalid={errors.repeat !== undefined}
                  aria-describedby={errors.repeat ? "repeat-password-error" : undefined}
                  {...form.register("repeat")}
                />
                {errors.repeat && (
                  <p id="repeat-password-error" className="text-sm text-destructive">
                    {fieldErrorText(t, errors.repeat.message ?? "")}
                  </p>
                )}
              </div>
              <Button type="submit" size="lg" disabled={setup.isPending}>
                {t("passwordSetup.submit")}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </AuthLayout>
  );
}
