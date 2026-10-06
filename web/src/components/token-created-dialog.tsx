// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Creating an API token (C-04.FR-3): the dialog takes the name, the optional expiry and, for a Personal access token,
// the Permissions, then shows the value once with a copy button. The value lives only in this dialog's state: the
// request's result keeps the token without it, closing the dialog drops it, and no list ever shows it again.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import { addDays } from "date-fns";
import { CheckIcon, CopyIcon, TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { Permission } from "../api/gen/model";
import { applyFieldErrors, fieldErrorText, isApiError, problemText } from "../lib/api";
import { dayIn, startOfDayIn, useTimeFormat } from "../lib/time";
import { PermissionPicker } from "./permission-picker";
import { COPIED_MS, copyField } from "./setup-link-dialog";
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
import { cn } from "./ui/utils";

/** The longest name of a token, in characters, as the API checks it. */
const NAME_MAX = 200;

/** The quick choices of the expiry, in days from today. */
const EXPIRY_DAYS = [30, 90, 365] as const;

/** What the forms send: the API's create body, the expiry as an instant. */
export interface TokenInput {
  name: string;
  expires_at: string | null;
  permissions: Permission[];
}

/** The warning on a token without an expiry date, in the form and in the lists (C-04.FR-3). */
export function NeverExpires({ id, className }: { id?: string; className?: string }) {
  const { t } = useTranslation();
  return (
    <p id={id} className={cn("flex items-start gap-1.5 text-sm", className)}>
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <span>{t("tokens.neverExpires")}</span>
    </p>
  );
}

/**
 * The expiry date: a day in the profile's time zone, or none. The token stops working when that day starts there; the
 * quick choices pick the day 30, 90 or 365 days from today.
 */
function ExpiryField({
  id,
  value,
  onChange,
  error,
}: {
  id: string;
  value: string;
  onChange: (day: string) => void;
  error?: string;
}) {
  const { t } = useTranslation();
  const { timeZone } = useTimeFormat();
  // The day the form opened on; the quick choices count from it.
  const [now] = useState(() => new Date());
  const choice = (days: number) => dayIn(addDays(now, days), timeZone);
  const described = [value === "" ? `${id}-never` : null, error ? `${id}-error` : null]
    .filter(Boolean)
    .join(" ");
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{t("tokens.fields.expiry")}</Label>
      <div className="flex flex-wrap gap-2" role="group" aria-label={t("tokens.expiry.choices")}>
        {EXPIRY_DAYS.map((days) => (
          <Button
            key={days}
            type="button"
            size="sm"
            variant={value === choice(days) ? "secondary" : "outline"}
            aria-pressed={value === choice(days)}
            onClick={() => onChange(choice(days))}
          >
            {t("tokens.expiry.days", { count: days })}
          </Button>
        ))}
        <Button
          type="button"
          size="sm"
          variant={value === "" ? "secondary" : "outline"}
          aria-pressed={value === ""}
          onClick={() => onChange("")}
        >
          {t("tokens.expiry.none")}
        </Button>
      </div>
      <Input
        id={id}
        type="date"
        className="w-full max-w-48"
        value={value}
        min={choice(1)}
        aria-invalid={error !== undefined}
        aria-describedby={described || undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {value === "" && <NeverExpires id={`${id}-never`} />}
      {error && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

/** The value of a created token with its copy button, and the warning that it is shown once. */
export function TokenValue({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-2">
        <Label htmlFor="token-value">{t("tokens.created.value")}</Label>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id="token-value"
            readOnly
            value={value}
            className="font-mono text-xs"
            spellCheck={false}
            autoComplete="off"
            onFocus={(e) => e.currentTarget.select()}
            ref={field}
            autoFocus
            data-testid="token-value"
          />
          <Button
            variant="outline"
            onClick={() => {
              void copyField(value, field.current).then((done) => {
                setCopied(done);
                if (done) {
                  setTimeout(() => setCopied(false), COPIED_MS);
                }
              });
            }}
          >
            {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
            {copied ? t("setupLink.copied") : t("setupLink.copy")}
          </Button>
          <span role="status" className="sr-only">
            {copied ? t("setupLink.copied") : ""}
          </span>
        </div>
      </div>
      <p className="flex items-start gap-1.5 text-sm font-medium">
        <TriangleAlertIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
        />
        <span>{t("tokens.created.once")}</span>
      </p>
    </div>
  );
}

const formSchema = z.object({
  name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
  expires_at: z.string(),
  permissions: z.array(z.enum(Permission)),
});
type FormValues = z.infer<typeof formSchema>;

const FIELDS = ["name", "expires_at", "permissions"] as const;

function TokenForm({
  held,
  create,
  onCreated,
}: {
  held: readonly Permission[] | undefined;
  create: (input: TokenInput) => Promise<{ value: string }>;
  onCreated: (value: string, name: string) => void;
}) {
  const { t } = useTranslation();
  const { timeZone } = useTimeFormat();
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const form = useForm<FormValues>({
    resolver: zodResolver(
      held === undefined
        ? formSchema
        : formSchema.refine((v) => v.permissions.length > 0, {
            path: ["permissions"],
            message: "permissions_required",
          }),
    ),
    defaultValues: { name: "", expires_at: "", permissions: [] },
  });
  // The request's result keeps nothing: the value goes straight to the dialog's state.
  const submit = useMutation({
    mutationFn: async (input: TokenInput) => {
      const { value } = await create(input);
      onCreated(value, input.name);
    },
    onError: (err) => {
      setUnmatched(
        isApiError(err) ? applyFieldErrors(err, form.setError, FIELDS, (code) => code) : [],
      );
    },
  });
  const message = (code: string | undefined) => {
    switch (code) {
      case "permissions_required":
        return t("tokens.errors.permissionsRequired");
      case "permission_not_held":
        return t("tokens.errors.permissionNotHeld");
      case "out_of_range":
        return t("tokens.errors.expiryPast");
      default:
        return fieldErrorText(t, code ?? "");
    }
  };
  const errors = form.formState.errors;
  const handled =
    isApiError(submit.error) && Boolean(submit.error.errors?.length) && unmatched.length === 0;
  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={form.handleSubmit((v) =>
        submit.mutate({
          name: v.name,
          expires_at:
            v.expires_at === "" ? null : startOfDayIn(v.expires_at, timeZone).toISOString(),
          permissions: v.permissions,
        }),
      )}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor="token-name">{t("tokens.fields.name")}</Label>
        <Input
          id="token-name"
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          aria-invalid={errors.name !== undefined}
          aria-describedby={errors.name ? "token-name-error" : "token-name-hint"}
          {...form.register("name")}
        />
        {errors.name ? (
          <p id="token-name-error" className="text-sm text-destructive">
            {message(errors.name.message)}
          </p>
        ) : (
          <p id="token-name-hint" className="text-sm text-muted-foreground">
            {t("tokens.fields.nameHint")}
          </p>
        )}
      </div>
      <Controller
        control={form.control}
        name="expires_at"
        render={({ field, fieldState }) => (
          <ExpiryField
            id="token-expiry"
            value={field.value}
            onChange={field.onChange}
            error={fieldState.error ? message(fieldState.error.message) : undefined}
          />
        )}
      />
      {held !== undefined && (
        <Controller
          control={form.control}
          name="permissions"
          render={({ field, fieldState }) => (
            <PermissionPicker
              id="token-permissions"
              held={held}
              value={field.value}
              onChange={field.onChange}
              error={fieldState.error ? message(fieldState.error.message) : undefined}
            />
          )}
        />
      )}
      {submit.isError && !handled && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {unmatched.length > 0
              ? unmatched.map((code) => message(code)).join(" ")
              : problemText(t, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
        <Button type="submit" disabled={submit.isPending}>
          {t("tokens.create.submit")}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * "Create token": the form, then the value once. held names the Permissions to choose from (a Personal access token);
 * without it the form has none (a Service account token acts with its account's Role).
 */
export function TokenCreateDialog({
  description,
  held,
  create,
  onCreated,
}: {
  description: ReactNode;
  held?: readonly Permission[];
  create: (input: TokenInput) => Promise<{ value: string }>;
  onCreated: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [created, setCreated] = useState<{ value: string; name: string } | null>(null);
  // Each opening and closing starts a new round. A value that arrives in a later round than the form that asked for it
  // (the dialog was closed while the request was on its way) is dropped; the list still shows the token to revoke.
  const round = useRef(0);
  const asked = round.current;
  return (
    <>
      <Button
        onClick={() => {
          round.current += 1;
          setOpen(true);
        }}
      >
        {t("tokens.create.start")}
      </Button>
      <Dialog
        open={open}
        disablePointerDismissal={created !== null}
        onOpenChange={(next) => {
          round.current += 1;
          setOpen(next);
          if (!next) {
            setCreated(null);
          }
        }}
      >
        <DialogContent
          closeLabel={t("common.close")}
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg"
        >
          {created === null ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.create.title")}</DialogTitle>
                <DialogDescription>{description}</DialogDescription>
              </DialogHeader>
              <TokenForm
                held={held}
                create={create}
                onCreated={(value, name) => {
                  if (round.current === asked) {
                    setCreated({ value, name });
                  }
                  onCreated();
                }}
              />
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.created.title")}</DialogTitle>
                <DialogDescription>
                  {t("tokens.created.hint", { name: created.name })}
                </DialogDescription>
              </DialogHeader>
              <TokenValue value={created.value} />
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
