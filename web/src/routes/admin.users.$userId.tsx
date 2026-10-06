// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A user's page (C-03.FR-3, FR-11, FR-13, FR-29): the details, and for users:write the edit of name, email and Role,
// disable and enable, reset TOTP, a new password setup link, convert to local and delete. S-052 adds the user's
// Account links here.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  deleteUser,
  getGetUserQueryKey,
  getGetUserQueryOptions,
  getListUsersQueryKey,
  updateUser,
  useConvertUserToLocal,
  useCreatePasswordSetupLink,
  useDisableUser,
  useEnableUser,
  useGetUser,
  useResetUserTotp,
} from "../api/gen/endpoints/users/users";
import type { PasswordSetupLink, User } from "../api/gen/model";
import { UpdateUserBody } from "../api/gen/zod/users/users.zod";
import { RequirePermission, useCan } from "../components/app-shell";
import { SetupLinkDialog } from "../components/setup-link-dialog";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
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
import {
  methodLabel,
  roleLabel,
  sourceLabel,
  statusLabel,
  useRoleNames,
} from "../components/user-create-dialog";
import {
  SESSION_QUERY_KEY,
  applyFieldErrors,
  fieldErrorText,
  isApiError,
  isStale,
  problemText,
  useSession,
} from "../lib/api";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/admin/users/$userId")({
  staticData: { shell: true },
  component: UserPage,
});

/**
 * Keeps the cached user and the list in step with a changed user, and reads the session again when the Admin changed
 * themselves, so that the navigation follows a changed Role.
 */
function useStoreUser() {
  const queryClient = useQueryClient();
  const session = useSession();
  return (user: User) => {
    queryClient.setQueryData(getGetUserQueryKey(user.id), user);
    void queryClient.invalidateQueries({ queryKey: getListUsersQueryKey() });
    if (user.id === session?.user.id) {
      void queryClient.invalidateQueries({ queryKey: SESSION_QUERY_KEY });
    }
  };
}

function Details({ user }: { user: User }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const rows: [string, ReactNode, string][] = [
    [t("users.fields.login"), user.login, "login"],
    [t("users.fields.email"), user.email ?? "—", "email"],
    [t("users.fields.role"), roleLabel(t, user.role), "role"],
    [t("users.fields.source"), sourceLabel(t, user.source), "source"],
    [t("users.fields.method"), methodLabel(t, user.sign_in_method), "method"],
    [t("users.fields.status"), statusLabel(t, user.status), "status"],
    [t("users.fields.totp"), user.totp_enabled ? t("users.totp.on") : t("users.totp.off"), "totp"],
    [
      t("users.fields.lastSignIn"),
      user.last_sign_in_at ? dateTime(user.last_sign_in_at) : t("users.never"),
      "last-sign-in",
    ],
    [t("users.fields.created"), dateTime(user.created_at), "created"],
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("users.page.details")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
          {rows.map(([label, value, id]) => (
            <div key={id} className="contents">
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="min-w-0 break-words" data-testid={`user-${id}`}>
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

const editSchema = UpdateUserBody.extend({
  name: z.string().trim().min(1, "required"),
  email: z.string().trim(),
});
type EditValues = z.infer<typeof editSchema>;

function editValues(user: User): EditValues {
  return { name: user.name, email: user.email ?? "", role: user.role };
}

/**
 * Name, email and Role. The form keeps the version it was read at and sends it as If-Match; a newer version (an action
 * on this page, another Admin) replaces an untouched form, while an edited one keeps the edit and a save over the
 * newer version is refused.
 */
function EditCard({ user }: { user: User }) {
  const { t } = useTranslation();
  const store = useStoreUser();
  const queryClient = useQueryClient();
  const roles = useRoleNames();
  const [base, setBase] = useState(user);
  const [saved, setSaved] = useState(false);
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const form = useForm<EditValues>({
    resolver: zodResolver(editSchema),
    values: editValues(base),
  });
  const { isDirty } = form.formState;
  const update = useMutation({
    mutationFn: (data: EditValues) =>
      updateUser(
        base.id,
        { name: data.name, role: data.role, email: data.email === "" ? null : data.email },
        { headers: { "If-Match": base.etag } },
      ),
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetUserQueryKey(base.id) }),
    onSuccess: (updated) => {
      setBase(updated);
      store(updated);
      setSaved(true);
    },
    onError: (err) => {
      setUnmatched(
        isApiError(err)
          ? applyFieldErrors(err, form.setError, ["name", "email", "role"], (code) => code)
          : [],
      );
    },
  });
  if (user.etag !== base.etag && !isDirty && !update.isPending) {
    setBase(user);
  }
  const newer = user.etag !== base.etag && !update.isPending;
  const errors = form.formState.errors;
  const reload = () => {
    update.reset();
    void queryClient
      .fetchQuery({ ...getGetUserQueryOptions(base.id), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset(editValues(fresh));
      });
  };
  const field = (name: "name" | "email", label: string, type = "text") => (
    <div className="flex flex-col gap-2">
      <Label htmlFor={`user-edit-${name}`}>{label}</Label>
      <Input
        id={`user-edit-${name}`}
        type={type}
        className="max-w-sm"
        autoComplete="off"
        aria-invalid={errors[name] !== undefined}
        aria-describedby={errors[name] ? `user-edit-${name}-error` : undefined}
        {...form.register(name)}
      />
      {errors[name] && (
        <p id={`user-edit-${name}-error`} className="text-sm text-destructive">
          {fieldErrorText(t, errors[name]?.message ?? "")}
        </p>
      )}
    </div>
  );
  // A refusal with field errors shows on the fields; codes that match no field show as a whole.
  const handled =
    isApiError(update.error) && Boolean(update.error.errors?.length) && unmatched.length === 0;
  const stale = newer || isStale(update.error);
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("users.page.edit")}</h2>
        </CardTitle>
        <CardDescription>{t("users.page.editHint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit((values) => {
            setSaved(false);
            update.mutate(values);
          })}
        >
          {field("name", t("users.fields.name"))}
          {field("email", t("users.fields.email"), "email")}
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-role">{t("users.fields.role")}</Label>
            <NativeSelect
              id="user-edit-role"
              className="w-full max-w-sm"
              disabled={user.role_locked === true}
              aria-invalid={errors.role !== undefined}
              aria-describedby={
                [
                  user.role_locked === true ? "user-edit-role-hint" : null,
                  errors.role ? "user-edit-role-error" : null,
                ]
                  .filter(Boolean)
                  .join(" ") || undefined
              }
              {...form.register("role")}
            >
              {roles.map((role) => (
                <NativeSelectOption key={role} value={role}>
                  {roleLabel(t, role)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            {user.role_locked === true && (
              <p id="user-edit-role-hint" className="text-sm text-muted-foreground">
                {t("users.page.roleLocked")}
              </p>
            )}
            {errors.role && (
              <p id="user-edit-role-error" className="text-sm text-destructive">
                {fieldErrorText(t, errors.role.message ?? "")}
              </p>
            )}
          </div>
          {stale && (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>{t("errors.staleUser")}</span>
                <Button variant="outline" size="sm" onClick={reload}>
                  {t("common.reload")}
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {update.isError && !handled && !isStale(update.error) && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {unmatched.length > 0
                  ? unmatched.map((code) => fieldErrorText(t, code)).join(" ")
                  : problemText(t, update.error)}
              </AlertDescription>
            </Alert>
          )}
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={update.isPending}>
              {t("common.save")}
            </Button>
            <span role="status" className="text-sm text-muted-foreground">
              {saved && !isDirty ? t("common.saved") : ""}
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

/** A dialog that asks before an action that cannot simply be undone. */
function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirm,
  pending,
  error,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  confirm: string;
  pending: boolean;
  error: unknown;
  onConfirm: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {error !== null && error !== undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {isStale(error) ? t("errors.staleUser") : problemText(t, error)}
            </AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button variant="destructive" disabled={pending} onClick={onConfirm}>
            {confirm}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type Asking = "delete" | "resetTotp" | "convert" | null;

function ActionsCard({ user }: { user: User }) {
  const { t } = useTranslation();
  const store = useStoreUser();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [asking, setAsking] = useState<Asking>(null);
  const [link, setLink] = useState<PasswordSetupLink | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const onUser = { mutation: { onSuccess: store } };
  const disable = useDisableUser(onUser);
  const enable = useEnableUser(onUser);
  const resetTotp = useResetUserTotp({
    mutation: {
      onSuccess: () => {
        setAsking(null);
        setNotice(t("users.actions.totpReset"));
        void queryClient.invalidateQueries({ queryKey: getGetUserQueryKey(user.id) });
        void queryClient.invalidateQueries({ queryKey: getListUsersQueryKey() });
      },
    },
  });
  const convert = useConvertUserToLocal({
    mutation: {
      onSuccess: (created) => {
        setAsking(null);
        store(created.user);
        setLink(created.password_setup_link);
      },
    },
  });
  const newLink = useCreatePasswordSetupLink({ mutation: { onSuccess: setLink } });
  const remove = useMutation({
    mutationFn: () => deleteUser(user.id, { headers: { "If-Match": user.etag } }),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: getGetUserQueryKey(user.id) });
      void queryClient.invalidateQueries({ queryKey: getListUsersQueryKey() });
      void navigate({ to: "/admin/users" });
    },
    // A newer version was read meanwhile: the page reads it again, and a second Delete sends it.
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetUserQueryKey(user.id) });
      }
    },
  });
  const failed = [disable, enable, newLink].find((m) => m.isError);
  const busy = [disable, enable, newLink, resetTotp, convert, remove].some((m) => m.isPending);
  const ask = (what: Asking) => {
    for (const m of [resetTotp, convert, remove]) {
      m.reset();
    }
    setNotice(null);
    setAsking(what);
  };
  const run = (mutate: () => void) => {
    for (const m of [disable, enable, newLink]) {
      m.reset();
    }
    setNotice(null);
    mutate();
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("users.page.actions")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-2">
          {user.status === "active" ? (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => run(() => disable.mutate({ userId: user.id }))}
            >
              {t("users.actions.disable")}
            </Button>
          ) : (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => run(() => enable.mutate({ userId: user.id }))}
            >
              {t("users.actions.enable")}
            </Button>
          )}
          {user.totp_enabled && (
            <Button variant="outline" disabled={busy} onClick={() => ask("resetTotp")}>
              {t("users.actions.resetTotp")}
            </Button>
          )}
          {user.sign_in_method === "local" ? (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => run(() => newLink.mutate({ userId: user.id }))}
            >
              {t("users.actions.newSetupLink")}
            </Button>
          ) : (
            <Button variant="outline" disabled={busy} onClick={() => ask("convert")}>
              {t("users.actions.convert")}
            </Button>
          )}
          <Button variant="destructive" disabled={busy} onClick={() => ask("delete")}>
            {t("users.actions.delete")}
          </Button>
        </div>
        {failed?.error !== undefined && failed.error !== null && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, failed.error)}
            </AlertDescription>
          </Alert>
        )}
        <p role="status" className="text-sm text-muted-foreground empty:hidden">
          {notice ?? ""}
        </p>
        <ConfirmDialog
          open={asking === "resetTotp"}
          onOpenChange={(open) => setAsking(open ? "resetTotp" : null)}
          title={t("users.actions.resetTotp")}
          description={t("users.actions.resetTotpConfirm", { name: user.name })}
          confirm={t("users.actions.resetTotp")}
          pending={resetTotp.isPending}
          error={resetTotp.error}
          onConfirm={() => resetTotp.mutate({ userId: user.id })}
        />
        <ConfirmDialog
          open={asking === "convert"}
          onOpenChange={(open) => setAsking(open ? "convert" : null)}
          title={t("users.actions.convert")}
          description={t("users.actions.convertConfirm", { name: user.name })}
          confirm={t("users.actions.convert")}
          pending={convert.isPending}
          error={convert.error}
          onConfirm={() => convert.mutate({ userId: user.id })}
        />
        <ConfirmDialog
          open={asking === "delete"}
          onOpenChange={(open) => setAsking(open ? "delete" : null)}
          title={t("users.actions.deleteTitle", { name: user.name })}
          description={t("users.actions.deleteConfirm", { pseudonym: `deleted-user-${user.id}` })}
          confirm={t("users.actions.delete")}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate()}
        />
        <SetupLinkDialog link={link} userName={user.name} onClose={() => setLink(null)} />
      </CardContent>
    </Card>
  );
}

function UserView({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("users:write");
  const query = useGetUser(userId);
  const user = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/admin/users"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("users.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight break-words">
          {user?.name ?? t("users.page.title")}
        </h1>
      </div>
      {user === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="grid gap-6 lg:grid-cols-2">
          <Details user={user} />
          {canWrite && user.status !== "deleted" && <EditCard key={user.id} user={user} />}
          {canWrite && user.status !== "deleted" && <ActionsCard user={user} />}
        </div>
      )}
    </div>
  );
}

function UserPage() {
  const { userId } = Route.useParams();
  return (
    <RequirePermission permission="users:read">
      <UserView userId={userId} />
    </RequirePermission>
  );
}
