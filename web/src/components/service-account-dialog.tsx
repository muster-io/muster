// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create service account" (C-04.FR-2): a name and a Role. The web session may give any Role; a token may only give a
// Role whose Permissions it holds (C-04.FR-7), which the API checks. The new account's page opens next, for its tokens.

import { zodResolver } from "@hookform/resolvers/zod";
import type { TFunction } from "i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getGetServiceAccountQueryKey,
  getListServiceAccountsQueryKey,
  useCreateServiceAccount,
} from "../api/gen/endpoints/api-tokens/api-tokens";
import { CreateServiceAccountBody } from "../api/gen/zod/api-tokens/api-tokens.zod";
import { applyFieldErrors, fieldErrorText, isApiError, problemText } from "../lib/api";
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
import { roleLabel, useRoleNames } from "./user-create-dialog";

/** The longest name of a Service account, in characters, as the API checks it. */
const NAME_MAX = 200;

const createSchema = CreateServiceAccountBody.extend({
  name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
});
type CreateValues = z.infer<typeof createSchema>;

const FIELDS = ["name", "role"] as const;

/** The text of a field error of a Service account form. */
export function serviceAccountFieldText(t: TFunction, code: string): string {
  switch (code) {
    case "name_taken":
      return t("serviceAccounts.errors.nameTaken");
    case "permission_not_held":
      return t("serviceAccounts.errors.roleNotHeld");
    default:
      return fieldErrorText(t, code);
  }
}

function CreateForm() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const roles = useRoleNames();
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const form = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
    defaultValues: { name: "", role: "viewer" },
  });
  const create = useCreateServiceAccount({
    mutation: {
      onSuccess: (account) => {
        queryClient.setQueryData(getGetServiceAccountQueryKey(account.id), account);
        void queryClient.invalidateQueries({ queryKey: getListServiceAccountsQueryKey() });
        void navigate({
          to: "/admin/service-accounts/$serviceAccountId",
          params: { serviceAccountId: account.id },
        });
      },
      onError: (err) => {
        if (!isApiError(err)) {
          return;
        }
        if (err.code === "name_taken") {
          form.setError("name", { type: "name_taken", message: "name_taken" });
          setUnmatched([]);
          return;
        }
        setUnmatched(applyFieldErrors(err, form.setError, FIELDS, (code) => code));
      },
    },
  });
  const errors = form.formState.errors;
  const handled =
    isApiError(create.error) &&
    (create.error.code === "name_taken" ||
      (Boolean(create.error.errors?.length) && unmatched.length === 0));
  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={form.handleSubmit((data) => create.mutate({ data }))}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor="create-sa-name">{t("serviceAccounts.fields.name")}</Label>
        <Input
          id="create-sa-name"
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          aria-invalid={errors.name !== undefined}
          aria-describedby={errors.name ? "create-sa-name-error" : undefined}
          {...form.register("name")}
        />
        {errors.name && (
          <p id="create-sa-name-error" className="text-sm text-destructive">
            {serviceAccountFieldText(t, errors.name.message ?? "")}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="create-sa-role">{t("serviceAccounts.fields.role")}</Label>
        <NativeSelect
          id="create-sa-role"
          className="w-full"
          aria-invalid={errors.role !== undefined}
          aria-describedby={errors.role ? "create-sa-role-error" : "create-sa-role-hint"}
          {...form.register("role")}
        >
          {roles.map((role) => (
            <NativeSelectOption key={role} value={role}>
              {roleLabel(t, role)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        {errors.role ? (
          <p id="create-sa-role-error" className="text-sm text-destructive">
            {serviceAccountFieldText(t, errors.role.message ?? "")}
          </p>
        ) : (
          <p id="create-sa-role-hint" className="text-sm text-muted-foreground">
            {t("serviceAccounts.fields.roleHint")}
          </p>
        )}
      </div>
      {create.isError && !handled && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {unmatched.length > 0
              ? unmatched.map((code) => serviceAccountFieldText(t, code)).join(" ")
              : problemText(t, create.error)}
          </AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
        <Button type="submit" disabled={create.isPending}>
          {t("serviceAccounts.create.submit")}
        </Button>
      </DialogFooter>
    </form>
  );
}

export function ServiceAccountDialog() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(true)}>{t("serviceAccounts.create.start")}</Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")} className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("serviceAccounts.create.title")}</DialogTitle>
            <DialogDescription>{t("serviceAccounts.create.hint")}</DialogDescription>
          </DialogHeader>
          <CreateForm />
        </DialogContent>
      </Dialog>
    </>
  );
}
