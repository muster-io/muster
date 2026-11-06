// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The details of a Team (C-25.FR-3): its name, its slug — the value of the Team label that names it, in the format of
// `team.slug_format` — and its description. The form creates a Team and edits one; a refusal of the server shows at the
// field it names, and without teams:write the form only shows the Team.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Team } from "../api/gen/model";
import { applyFieldErrors, fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

const NAME_MAX = 200;
const SLUG_MAX = 63;
const DESCRIPTION_MAX = 2000;
/** The slug format of `team.slug_format`. */
export const SLUG_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;

const schema = z.object({
  name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
  slug: z.string().trim().min(1, "required").regex(SLUG_PATTERN, "invalid_format"),
  description: z.string().max(DESCRIPTION_MAX, "too_long"),
});
export type TeamFormValues = z.infer<typeof schema>;

/** A slug for a name: lowercase, with runs of other characters turned into "-", as long as the format allows. */
export function slugOf(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .replace(/-+$/, "")
    .slice(0, SLUG_MAX);
}

export function teamValues(team: Team | undefined): TeamFormValues {
  return { name: team?.name ?? "", slug: team?.slug ?? "", description: team?.description ?? "" };
}

/** The text of a refusal of a field of the form. */
function fieldText(t: ReturnType<typeof useTranslation>["t"], code: string): string {
  switch (code) {
    case "name_taken":
      return t("teams.form.nameTaken");
    case "slug_taken":
      return t("teams.form.slugTaken");
    default:
      return fieldErrorText(t, code);
  }
}

export interface TeamFormProps {
  /** The Team of an edit. */
  team?: Team;
  readOnly?: boolean;
  submitLabel: string;
  /** Sends the details; a refusal is shown by the form. */
  save: (values: TeamFormValues) => Promise<unknown>;
  onCancel?: () => void;
  /** Shown beside Save once a save has gone through. */
  saved?: boolean;
}

export function TeamForm({
  team,
  readOnly = false,
  submitLabel,
  save,
  onCancel,
  saved = false,
}: TeamFormProps) {
  const { t } = useTranslation();
  const form = useForm<TeamFormValues>({
    resolver: zodResolver(schema),
    defaultValues: teamValues(team),
  });
  const { errors, isDirty } = form.formState;
  // A new Team's slug follows its name until the slug is edited by hand.
  const [slugTouched, setSlugTouched] = useState(team !== undefined);
  const formElement = useRef<HTMLFormElement>(null);
  // After a refused save, the focus goes to the first marked field.
  const [focusRequest, setFocusRequest] = useState(0);
  const submit = useMutation({
    mutationFn: save,
    onSuccess: () => form.reset(form.getValues()),
    onError: (err) => {
      if (isApiError(err)) {
        const fields = ["name", "slug", "description"] as const;
        const pointers = err.errors ?? [];
        if (err.status === 409 && pointers.length === 0 && err.code !== undefined) {
          form.setError(err.code === "slug_taken" ? "slug" : "name", {
            type: err.code,
            message: err.code,
          });
        } else {
          applyFieldErrors(err, form.setError, fields, (code) => code);
        }
        setFocusRequest((n) => n + 1);
      }
    },
  });
  useEffect(() => {
    if (focusRequest > 0) {
      formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [focusRequest]);
  const shownByField =
    isApiError(submit.error) &&
    (submit.error.status === 409 || (submit.error.errors?.length ?? 0) > 0) &&
    !isStale(submit.error);
  const fieldError = (name: keyof TeamFormValues) => {
    const message = errors[name]?.message;
    return message === undefined ? undefined : fieldText(t, message);
  };
  const nameRegistration = form.register("name", {
    onChange: (e: { target: { value: string } }) => {
      if (!slugTouched) {
        form.setValue("slug", slugOf(e.target.value), { shouldDirty: true });
      }
    },
  });
  return (
    <form
      ref={formElement}
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={form.handleSubmit(
        (values) =>
          submit.mutate({ ...values, name: values.name.trim(), slug: values.slug.trim() }),
        () => setFocusRequest((n) => n + 1),
      )}
    >
      <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
        <div className="flex max-w-md min-w-0 flex-col gap-1.5">
          <Label htmlFor="team-name">{t("teams.form.name")}</Label>
          <Input
            id="team-name"
            autoComplete="off"
            maxLength={NAME_MAX}
            aria-invalid={errors.name !== undefined}
            aria-describedby={errors.name === undefined ? undefined : "team-name-error"}
            {...nameRegistration}
          />
          {errors.name !== undefined && (
            <p id="team-name-error" className="text-sm text-destructive">
              {fieldError("name")}
            </p>
          )}
        </div>
        <div className="flex max-w-md min-w-0 flex-col gap-1.5">
          <Label htmlFor="team-slug">{t("teams.form.slug")}</Label>
          <Input
            id="team-slug"
            autoComplete="off"
            spellCheck={false}
            maxLength={SLUG_MAX}
            aria-invalid={errors.slug !== undefined}
            aria-describedby={
              errors.slug === undefined ? "team-slug-hint" : "team-slug-hint team-slug-error"
            }
            {...form.register("slug", {
              onChange: () => {
                setSlugTouched(true);
              },
            })}
          />
          <p id="team-slug-hint" className="text-sm text-muted-foreground">
            {t("teams.form.slugHint")}
          </p>
          {errors.slug !== undefined && (
            <p id="team-slug-error" className="text-sm text-destructive">
              {fieldError("slug")}
            </p>
          )}
        </div>
        <div className="flex max-w-2xl min-w-0 flex-col gap-1.5">
          <Label htmlFor="team-description">{t("teams.form.description")}</Label>
          <textarea
            id="team-description"
            rows={3}
            maxLength={DESCRIPTION_MAX}
            className="min-h-16 w-full rounded-lg border border-input bg-transparent px-2.5 py-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive"
            aria-invalid={errors.description !== undefined}
            aria-describedby={
              errors.description === undefined ? undefined : "team-description-error"
            }
            {...form.register("description")}
          />
          {errors.description !== undefined && (
            <p id="team-description-error" className="text-sm text-destructive">
              {fieldError("description")}
            </p>
          )}
        </div>
      </fieldset>
      {!readOnly && (
        <div className="flex flex-col gap-3">
          {submit.isError && !shownByField && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, submit.error)}
              </AlertDescription>
            </Alert>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={submit.isPending}>
              {submitLabel}
            </Button>
            {onCancel !== undefined && (
              <Button type="button" variant="outline" onClick={onCancel}>
                {t("common.cancel")}
              </Button>
            )}
            <span role="status" className="text-sm text-muted-foreground" data-testid="team-status">
              {saved && !isDirty ? t("common.saved") : ""}
            </span>
          </div>
        </div>
      )}
    </form>
  );
}
