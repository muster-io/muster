// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create user" (C-03.FR-3): a local user with a name, a login, an optional email and a Role. Muster answers with a
// single-use password setup link, which the dialog then shows once. The texts of Roles, sources, statuses and sign-in
// methods that the user pages share live here too.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getListUsersQueryKey,
  useCreateUser,
  useListRoles,
} from "../api/gen/endpoints/users/users";
import type {
  OidcUnmatchedRole,
  RoleName,
  User,
  UserCreated,
  UserSource,
  UserStatus,
} from "../api/gen/model";
import { CreateUserBody } from "../api/gen/zod/users/users.zod";
import { applyFieldErrors, fieldErrorText, isApiError, problemText } from "../lib/api";
import { SetupLinkContent } from "./setup-link-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export const ROLES: readonly RoleName[] = ["admin", "responder", "viewer"];

export function roleLabel(t: TFunction, role: RoleName | OidcUnmatchedRole): string {
  switch (role) {
    case "admin":
      return t("roles.admin");
    case "responder":
      return t("roles.responder");
    case "viewer":
      return t("roles.viewer");
    default:
      return t("roles.none");
  }
}

export function sourceLabel(t: TFunction, source: UserSource): string {
  switch (source) {
    case "local":
      return t("users.source.local");
    case "oidc":
      return t("users.source.oidc");
    default:
      return t("users.source.bootstrap");
  }
}

export function statusLabel(t: TFunction, status: UserStatus): string {
  switch (status) {
    case "active":
      return t("users.status.active");
    case "disabled":
      return t("users.status.disabled");
    default:
      return t("users.status.deleted");
  }
}

export function methodLabel(t: TFunction, method: User["sign_in_method"]): string {
  return method === "oidc" ? t("users.method.oidc") : t("users.method.local");
}

/** The Roles to choose from: those listRoles returns, in its order, else the three built-in ones. */
export function useRoleNames(): readonly RoleName[] {
  const { data } = useListRoles();
  return data?.items.map((r) => r.name) ?? ROLES;
}

const createSchema = CreateUserBody.extend({
  name: z.string().trim().min(1, "required"),
  login: z.string().trim().min(1, "required"),
  email: z.string().trim(),
});
type CreateValues = z.infer<typeof createSchema>;

const FIELDS = ["name", "login", "email", "role"] as const;

function CreateForm({ onCreated }: { onCreated: (created: UserCreated) => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const roles = useRoleNames();
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const form = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: "", login: "", email: "", role: "responder" },
  });
  const create = useCreateUser({
    mutation: {
      onSuccess: (created) => {
        void queryClient.invalidateQueries({ queryKey: getListUsersQueryKey() });
        onCreated(created);
      },
      onError: (err) => {
        if (!isApiError(err)) {
          return;
        }
        if (err.code === "name_taken") {
          form.setError("login", { type: "name_taken", message: "name_taken" });
          setUnmatched([]);
          return;
        }
        setUnmatched(applyFieldErrors(err, form.setError, FIELDS, (code) => code));
      },
    },
  });
  const errors = form.formState.errors;
  const message = (code: string | undefined) =>
    code === "name_taken" ? t("errors.loginTaken") : fieldErrorText(t, code ?? "");
  // Field errors show on their fields; codes that match no field show as a whole.
  const handled =
    isApiError(create.error) &&
    (create.error.code === "name_taken" ||
      (Boolean(create.error.errors?.length) && unmatched.length === 0));
  const field = (name: "name" | "login" | "email", label: string, type = "text") => (
    <div className="flex flex-col gap-2">
      <Label htmlFor={`create-user-${name}`}>{label}</Label>
      <Input
        id={`create-user-${name}`}
        type={type}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={errors[name] !== undefined}
        aria-describedby={errors[name] ? `create-user-${name}-error` : undefined}
        {...form.register(name)}
      />
      {errors[name] && (
        <p id={`create-user-${name}-error`} className="text-sm text-destructive">
          {message(errors[name]?.message)}
        </p>
      )}
    </div>
  );
  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={form.handleSubmit(({ name, login, email, role }) =>
        create.mutate({ data: { name, login, role, email: email === "" ? null : email } }),
      )}
    >
      {field("name", t("users.fields.name"))}
      {field("login", t("users.fields.login"))}
      {field("email", t("users.fields.emailOptional"), "email")}
      <div className="flex flex-col gap-2">
        <Label htmlFor="create-user-role">{t("users.fields.role")}</Label>
        <NativeSelect
          id="create-user-role"
          className="w-full"
          aria-invalid={errors.role !== undefined}
          aria-describedby={errors.role ? "create-user-role-error" : undefined}
          {...form.register("role")}
        >
          {roles.map((role) => (
            <NativeSelectOption key={role} value={role}>
              {roleLabel(t, role)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {errors.role && (
          <p id="create-user-role-error" className="text-sm text-destructive">
            {message(errors.role.message)}
          </p>
        )}
      </div>
      {create.isError && !handled && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {unmatched.length > 0
              ? unmatched.map((code) => fieldErrorText(t, code)).join(" ")
              : problemText(t, create.error)}
          </AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
        <Button type="submit" disabled={create.isPending}>
          {t("users.create.submit")}
        </Button>
      </DialogFooter>
    </form>
  );
}

export function UserCreateDialog() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [created, setCreated] = useState<UserCreated | null>(null);
  return (
    <>
      <Button onClick={() => setOpen(true)}>{t("users.create.start")}</Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) {
            setCreated(null);
          }
        }}
      >
        <DialogContent closeLabel={t("common.close")} className="sm:max-w-lg">
          {created === null ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("users.create.title")}</DialogTitle>
                <DialogDescription>{t("users.create.hint")}</DialogDescription>
              </DialogHeader>
              <CreateForm onCreated={setCreated} />
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{t("setupLink.title")}</DialogTitle>
                <DialogDescription>
                  {t("users.create.created", { name: created.user.name })}
                </DialogDescription>
              </DialogHeader>
              <SetupLinkContent link={created.password_setup_link} userName={created.user.name} />
              <DialogFooter>
                <DialogClose render={<Button />}>{t("common.done")}</DialogClose>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
