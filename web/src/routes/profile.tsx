// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The profile (C-03.FR-12): name, language and time zone, the sign-in method with "Link OIDC" (C-03.FR-29), the
// password of an account that has one, the sessions, TOTP and the Personal access tokens (C-04). Account links join it
// later.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getGetMeQueryKey,
  getListMySessionsQueryKey,
  useChangePassword,
  useGetMe,
  useStartOidcLink,
  useUpdateMe,
} from "../api/gen/endpoints/profile/profile";
import { useGetSignInOptions } from "../api/gen/endpoints/sessions/sessions";
import type { Me, MeUpdate, User } from "../api/gen/model";
import { UpdateMeBody } from "../api/gen/zod/profile/profile.zod";
import { ProfileSessions } from "../components/profile-sessions";
import { ProfileTokens } from "../components/profile-tokens";
import { ProfileTotp } from "../components/profile-totp";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { applyLanguage } from "../i18n";
import {
  SESSION_QUERY_KEY,
  type SessionRead,
  applyFieldErrors,
  fieldErrorText,
  isApiError,
  problemText,
} from "../lib/api";
import { browserTimeZone, timeZones } from "../lib/time";

const searchSchema = z.object({ error: z.string().optional().catch(undefined) });

export const Route = createFileRoute("/profile")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: ProfilePage,
});

/** The text of an error the OIDC link callback redirects with. */
function linkErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "identity_linked_elsewhere":
      return t("profile.link.errors.identityLinkedElsewhere");
    case "no_access":
      return t("profile.link.errors.noAccess");
    case "oidc_disabled":
      return t("profile.link.errors.oidcDisabled");
    case "invalid_request":
      return t("profile.link.errors.invalidRequest");
    case "idp_error":
      return t("profile.link.errors.idpError");
    default:
      return t("profile.link.errors.unknown");
  }
}

/** Keeps the cached profile and session in step with an updated user, so the shell follows at once. */
function useStoreMe() {
  const queryClient = useQueryClient();
  return (me: Me) => {
    queryClient.setQueryData(getGetMeQueryKey(), me);
    queryClient.setQueryData<SessionRead>(SESSION_QUERY_KEY, (old) =>
      old?.session ? { ...old, session: { ...old.session, user: me.user } } : old,
    );
    applyLanguage(me.user.language);
  };
}

const nameSchema = UpdateMeBody.pick({ name: true }).extend({
  name: z.string().trim().min(1, "required"),
});
type NameValues = z.infer<typeof nameSchema>;

function DetailsCard({ user }: { user: User }) {
  const { t } = useTranslation();
  const store = useStoreMe();
  const [saved, setSaved] = useState(false);
  const form = useForm<NameValues>({
    resolver: zodResolver(nameSchema),
    defaultValues: { name: user.name },
  });
  const update = useUpdateMe({
    mutation: {
      onSuccess: (me) => {
        store(me);
        setSaved(true);
      },
      onError: (err) => {
        if (isApiError(err)) {
          applyFieldErrors(err, form.setError, ["name"], (code) => fieldErrorText(t, code));
        }
      },
    },
  });
  const error = form.formState.errors.name;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("profile.details.title")}</h2>
        </CardTitle>
        <CardDescription>{t("profile.details.login", { login: user.login })}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex flex-col gap-3"
          onSubmit={form.handleSubmit(({ name }) => {
            setSaved(false);
            update.mutate({ data: { name } });
          })}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="profile-name">{t("profile.details.name")}</Label>
            <Input
              id="profile-name"
              className="max-w-sm"
              autoComplete="name"
              aria-invalid={error !== undefined}
              aria-describedby={error ? "profile-name-error" : undefined}
              {...form.register("name")}
            />
            {error && (
              <p id="profile-name-error" className="text-sm text-destructive">
                {fieldErrorText(t, error.message ?? "")}
              </p>
            )}
          </div>
          {update.isError && !(isApiError(update.error) && update.error.errors?.length) && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, update.error)}
              </AlertDescription>
            </Alert>
          )}
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={update.isPending}>
              {t("common.save")}
            </Button>
            <span
              role="status"
              data-testid="details-status"
              className="text-sm text-muted-foreground"
            >
              {saved ? t("common.saved") : ""}
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function PreferencesCard({ user }: { user: User }) {
  const { t } = useTranslation();
  const store = useStoreMe();
  const zones = useMemo(() => {
    const known = timeZones();
    // A stored zone that the browser lists under another name stays selectable.
    return user.time_zone && !known.includes(user.time_zone) ? [user.time_zone, ...known] : known;
  }, [user.time_zone]);
  const [saved, setSaved] = useState(false);
  const update = useUpdateMe({
    mutation: {
      onSuccess: (me) => {
        store(me);
        setSaved(true);
      },
    },
  });
  const save = (change: Omit<MeUpdate, "name">) => {
    setSaved(false);
    update.mutate({ data: { name: user.name, ...change } });
  };
  const browserZone = browserTimeZone();
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("profile.preferences.title")}</h2>
        </CardTitle>
        <CardDescription>{t("profile.preferences.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label htmlFor="profile-language">{t("profile.preferences.language")}</Label>
          <NativeSelect
            id="profile-language"
            value={user.language ?? ""}
            aria-busy={update.isPending}
            onChange={(e) =>
              save({
                language: e.target.value === "ru" ? "ru" : e.target.value === "en" ? "en" : null,
              })
            }
          >
            <NativeSelectOption value="">
              {t("profile.preferences.browserLanguage")}
            </NativeSelectOption>
            {/* Each language is named in itself. */}
            <NativeSelectOption value="en" lang="en">
              English
            </NativeSelectOption>
            <NativeSelectOption value="ru" lang="ru">
              Русский
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="profile-time-zone">{t("profile.preferences.timeZone")}</Label>
          <NativeSelect
            id="profile-time-zone"
            className="w-full max-w-sm"
            value={user.time_zone ?? ""}
            aria-busy={update.isPending}
            onChange={(e) => save({ time_zone: e.target.value === "" ? null : e.target.value })}
          >
            <NativeSelectOption value="">
              {t("profile.preferences.browserTimeZone", { zone: browserZone })}
            </NativeSelectOption>
            {zones.map((zone) => (
              <NativeSelectOption key={zone} value={zone}>
                {zone}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        {update.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, update.error)}
            </AlertDescription>
          </Alert>
        )}
        <span
          role="status"
          data-testid="preferences-status"
          className="text-sm text-muted-foreground"
        >
          {saved ? t("common.saved") : ""}
        </span>
      </CardContent>
    </Card>
  );
}

function SignInMethodCard({ user, linkError }: { user: User; linkError?: string }) {
  const { t } = useTranslation();
  const options = useGetSignInOptions();
  const [warning, setWarning] = useState(false);
  const link = useStartOidcLink({
    mutation: {
      onSuccess: ({ authorization_url: url }) => {
        if (/^https?:\/\//i.test(url)) {
          window.location.assign(url);
        }
      },
    },
  });
  const oidc = user.sign_in_method === "oidc";
  const canLink = !oidc && options.data?.oidc.enabled === true;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("profile.method.title")}</h2>
        </CardTitle>
        <CardDescription data-testid="sign-in-method">
          {oidc ? t("profile.method.oidcLong") : t("profile.method.localLong")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {linkError !== undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {linkErrorText(t, linkError)}
            </AlertDescription>
          </Alert>
        )}
        {canLink && (
          <div>
            <Button variant="outline" onClick={() => setWarning(true)}>
              {t("profile.link.start")}
            </Button>
          </div>
        )}
        <Dialog open={warning} onOpenChange={setWarning}>
          <DialogContent closeLabel={t("common.close")}>
            <DialogHeader>
              <DialogTitle>{t("profile.link.start")}</DialogTitle>
              <DialogDescription>{t("profile.link.warning")}</DialogDescription>
            </DialogHeader>
            {link.isError && (
              <Alert variant="destructive">
                <AlertDescription className="text-current">
                  {problemText(t, link.error)}
                </AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
              <Button disabled={link.isPending || link.isSuccess} onClick={() => link.mutate()}>
                {t("common.continue")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}

const passwordSchema = z
  .object({
    current_password: z.string().min(1, "required"),
    new_password: z.string().min(1, "required"),
    repeat: z.string().min(1, "required"),
  })
  .refine((v) => v.new_password === v.repeat, { path: ["repeat"], message: "mismatch" });
type PasswordValues = z.infer<typeof passwordSchema>;

function PasswordCard() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const form = useForm<PasswordValues>({
    resolver: zodResolver(passwordSchema),
    defaultValues: { current_password: "", new_password: "", repeat: "" },
  });
  const change = useChangePassword({
    mutation: {
      onSuccess: () => {
        form.reset();
        void queryClient.invalidateQueries({ queryKey: getListMySessionsQueryKey() });
      },
      onError: (err) => {
        if (!isApiError(err)) {
          return;
        }
        if (err.status === 401) {
          form.setError("current_password", {
            type: "invalid_credentials",
            message: "wrong_password",
          });
        }
        applyFieldErrors(err, form.setError, ["current_password", "new_password"], (code) =>
          fieldErrorText(t, code),
        );
      },
    },
  });
  const errors = form.formState.errors;
  const handled =
    isApiError(change.error) &&
    (change.error.status === 401 || Boolean(change.error.errors?.length));
  const message = (m: string | undefined) =>
    m === "wrong_password" ? t("profile.password.wrong") : fieldErrorText(t, m ?? "");
  const field = (name: keyof PasswordValues, id: string, label: string, autoComplete: string) => (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="password"
        className="max-w-sm"
        autoComplete={autoComplete}
        aria-invalid={errors[name] !== undefined}
        aria-describedby={errors[name] ? `${id}-error` : undefined}
        {...form.register(name)}
      />
      {errors[name] && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {message(errors[name]?.message)}
        </p>
      )}
    </div>
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("profile.password.title")}</h2>
        </CardTitle>
        <CardDescription>{t("profile.password.hint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex flex-col gap-3"
          onSubmit={form.handleSubmit(({ current_password, new_password }) =>
            change.mutate({ data: { current_password, new_password } }),
          )}
        >
          {field(
            "current_password",
            "current-password",
            t("profile.password.current"),
            "current-password",
          )}
          {field("new_password", "new-password", t("profile.password.new"), "new-password")}
          {field("repeat", "repeat-new-password", t("profile.password.repeat"), "new-password")}
          {change.isError && !handled && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, change.error)}
              </AlertDescription>
            </Alert>
          )}
          {change.isSuccess && (
            <Alert>
              <AlertDescription className="text-current">
                {t("profile.password.changed")}
              </AlertDescription>
            </Alert>
          )}
          <div>
            <Button type="submit" disabled={change.isPending}>
              {t("profile.password.submit")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function ProfilePage() {
  const { t } = useTranslation();
  const { error } = Route.useSearch();
  const me = useGetMe();
  const user = me.data?.user;
  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t("profile.title")}</h1>
      {user === undefined ? (
        <p className="text-sm text-muted-foreground">
          {me.isError ? problemText(t, me.error) : t("common.loading")}
        </p>
      ) : (
        <div className="grid gap-6 lg:grid-cols-2">
          <DetailsCard key={user.id} user={user} />
          <PreferencesCard user={user} />
          <SignInMethodCard user={user} linkError={error} />
          {user.sign_in_method === "local" && <PasswordCard />}
          <ProfileTotp hasPassword={user.sign_in_method === "local"} />
          <ProfileSessions />
          <ProfileTokens user={user} held={me.data?.permissions ?? []} />
        </div>
      )}
    </div>
  );
}
