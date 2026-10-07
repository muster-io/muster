// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Route editor (C-08.FR-1, FR-2, FR-4, FR-5): the name, the description, the Matchers (none for the Default
// route), the urgent mark, the Group key with its preview and the Lifecycle section (C-09.FR-4, FR-5, FR-9). The other
// fields of a Route — its Destinations and the policy fields of later capabilities — are not shown yet: they travel
// unchanged from the profile of a new Route, or from the stored Route, with every save. Without routes:write the
// editor only shows the Route.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Route, RouteInput } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { GroupKeyEditor } from "./group-key-editor";
import { GroupKeyPreviewPanel, type PreviewQuery } from "./group-key-preview";
import {
  MatcherBuilder,
  type MatcherRow,
  type MatcherRowErrors,
  isEmptyRow,
  matcherErrors,
  matcherRows,
  matchersOf,
  sentRows,
} from "./matcher-builder";
import {
  LIFECYCLE_POINTERS,
  RoutePolicyLifecycle,
  RoutePolicyLifecycleReadOnly,
  lifecycleSchema,
  lifecycleValues,
  withLifecycle,
} from "./route-policy-lifecycle";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** The longest name of a Route, in characters, as the API checks it. */
const NAME_MAX = 200;

const ID = "route";

const formSchema = z
  .object({
    name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
    description: z.string(),
    urgent: z.boolean(),
    matchers: z.array(
      z.object({
        key: z.number(),
        label: z.string(),
        op: z.enum(["=", "!=", "=~", "!~"]),
        value: z.string(),
      }),
    ),
    group_key: z.array(z.string()),
    ...lifecycleSchema,
  })
  .superRefine((v, ctx) => {
    v.matchers.forEach((row, index) => {
      if (!isEmptyRow(row) && row.label.trim() === "") {
        ctx.addIssue({ code: "custom", path: ["matchers", index, "label"], message: "required" });
      }
    });
  });

type FormValues = z.infer<typeof formSchema>;

function formValues(base: RouteInput): FormValues {
  return {
    name: base.name,
    description: base.description ?? "",
    urgent: base.urgent,
    matchers: matcherRows(base.matchers),
    group_key: [...base.group_key],
    ...lifecycleValues(base.policy),
  };
}

/** The Group key with a label name still typed in the field and not added yet, when it can be added. */
function withDraft(key: readonly string[], draft: string): string[] {
  const name = draft.trim();
  return name === "" || key.includes(name) ? [...key] : [...key, name];
}

/**
 * The input of a save: the fields of the form, and the Destinations and policy fields unchanged from the values the
 * form started from.
 */
function inputOf(base: RouteInput, v: FormValues, draft: string, isDefault: boolean): RouteInput {
  return {
    name: v.name.trim(),
    description: v.description.trim(),
    urgent: v.urgent,
    matchers: isDefault ? [] : matchersOf(v.matchers),
    group_key: withDraft(v.group_key, draft),
    destination_ids: [...base.destination_ids],
    policy: withLifecycle(base.policy, v),
  };
}

function nameErrorText(t: TFunction, code: string | undefined): string {
  switch (code) {
    case "name_taken":
      return t("routes.errors.nameTaken");
    default:
      return fieldErrorText(t, code ?? "");
  }
}

function groupKeyErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "duplicate":
      return t("routes.groupKey.duplicate");
    case "invalid_format":
      return t("routes.groupKey.invalid");
    default:
      return fieldErrorText(t, code);
  }
}

/** The error of the Group key in a refusal, under the pointer of a save or of a preview. */
function groupKeyError(t: TFunction, err: unknown): string | undefined {
  if (!isApiError(err)) {
    return undefined;
  }
  const item = err.errors?.find(
    (e) => e.pointer.startsWith("/group_key") || e.pointer.startsWith("/proposed_group_key"),
  );
  return item === undefined ? undefined : groupKeyErrorText(t, item.code);
}

/** A labelled read-only value of the editor without routes:write. */
function ReadOnlyField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm font-medium">{label}</span>
      {children}
    </div>
  );
}

export interface RouteFormProps {
  /** The values the form starts from: the chosen profile's for a new Route, the stored Route's for an edit. */
  base: RouteInput;
  /** The stored Route of an edit. */
  route?: Route;
  readOnly?: boolean;
  submitLabel: string;
  save: (input: RouteInput) => Promise<unknown>;
  /** A newer version than the form's was read while the form had changes. */
  stale?: boolean;
  onReload?: () => void;
  /** Told whether the form has changes, so that a newer version replaces an untouched form. */
  onDirtyChange?: (dirty: boolean) => void;
  onCancel: () => void;
  /** Puts the focus on the name once the form shows, as after the choice of a profile. */
  autoFocus?: boolean;
}

export function RouteForm({
  base,
  route,
  readOnly = false,
  submitLabel,
  save,
  stale,
  onReload,
  onDirtyChange,
  onCancel,
  autoFocus = false,
}: RouteFormProps) {
  const { t } = useTranslation();
  const isDefault = route?.is_default === true;
  const [serverMatcherErrors, setServerMatcherErrors] = useState<Map<number, MatcherRowErrors>>(
    () => new Map(),
  );
  const [serverGroupKeyError, setServerGroupKeyError] = useState<string>();
  const [unmatched, setUnmatched] = useState<string[]>([]);
  // A label name typed in the Group key field: Save and Preview take it as if it had been added.
  const [keyDraft, setKeyDraft] = useState("");
  // The Matcher rows a preview was asked with, so that its errors land on the rows they were sent from.
  const previewSent = useRef<MatcherRow[]>([]);
  const [defaultValues] = useState(() => formValues(base));
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues,
  });
  const rows = useWatch({ control: form.control, name: "matchers" });
  const groupKey = useWatch({ control: form.control, name: "group_key" });

  /**
   * Puts the errors of a refusal on the Matchers, by the rows the request was sent with, and on the Group key; returns
   * whether any landed there.
   */
  const applyFieldProblem = (err: unknown, sent: readonly MatcherRow[]): boolean => {
    const byRow = matcherErrors(err, sent);
    setServerMatcherErrors(byRow);
    const keyError = groupKeyError(t, err);
    setServerGroupKeyError(keyError);
    const first = [...byRow.entries()][0];
    if (first !== undefined) {
      const field = (["label", "op", "value"] as const).find((f) => first[1][f] !== undefined);
      document.getElementById(`${ID}-matchers-${first[0]}-${field ?? "label"}`)?.focus();
    } else if (keyError !== undefined) {
      document.getElementById(`${ID}-group-key`)?.focus();
    }
    return byRow.size > 0 || keyError !== undefined;
  };

  const submit = useMutation({
    mutationFn: ({ input }: { input: RouteInput; sent: MatcherRow[] }) => save(input),
    onMutate: () => {
      setUnmatched([]);
      setServerMatcherErrors(new Map());
      setServerGroupKeyError(undefined);
    },
    onError: (err, { sent }) => {
      if (!isApiError(err)) {
        return;
      }
      if (err.status === 409 && err.code === "name_taken") {
        form.setError("name", { type: "name_taken", message: "name_taken" }, { shouldFocus: true });
        return;
      }
      applyFieldProblem(err, sent);
      const rest: string[] = [];
      for (const item of err.errors ?? []) {
        const lifecycleField = LIFECYCLE_POINTERS[item.pointer];
        if (item.pointer === "/name") {
          form.setError("name", { type: item.code, message: item.code }, { shouldFocus: true });
        } else if (lifecycleField !== undefined) {
          form.setError(
            lifecycleField,
            { type: item.code, message: item.code },
            { shouldFocus: true },
          );
        } else if (item.pointer === "/description") {
          form.setError("description", { type: item.code, message: item.code });
        } else if (
          !item.pointer.startsWith("/matchers/") &&
          !item.pointer.startsWith("/group_key")
        ) {
          rest.push(item.code);
        }
      }
      setUnmatched(rest);
    },
  });

  useEffect(() => {
    if (autoFocus && !readOnly) {
      form.setFocus("name");
    }
  }, [autoFocus, readOnly, form]);

  const dirty = form.formState.isDirty;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const errors = form.formState.errors;
  const matcherErrorMap = new Map(serverMatcherErrors);
  rows.forEach((row, index) => {
    const code = errors.matchers?.[index]?.label?.message;
    if (code !== undefined) {
      matcherErrorMap.set(row.key, { ...matcherErrorMap.get(row.key), label: { code } });
    }
  });
  const fieldHandled =
    isApiError(submit.error) &&
    ((submit.error.status === 409 && submit.error.code === "name_taken") ||
      (Boolean(submit.error.errors?.length) && unmatched.length === 0));
  const showStale = (stale === true && !submit.isPending) || isStale(submit.error);
  const preview: PreviewQuery = {
    route_id: route?.id,
    matchers: isDefault ? undefined : matchersOf(rows),
    proposed_group_key: withDraft(groupKey, keyDraft),
  };

  if (readOnly) {
    return (
      <div className="flex flex-col gap-6" data-testid="route-read-only">
        <p className="text-sm text-muted-foreground">{t("routes.form.readOnly")}</p>
        <ReadOnlyField label={t("routes.fields.name")}>
          <span className="break-words" data-testid="route-name">
            {base.name}
          </span>
        </ReadOnlyField>
        {base.description ? (
          <ReadOnlyField label={t("routes.fields.description")}>
            <p className="text-sm break-words whitespace-pre-wrap">{base.description}</p>
          </ReadOnlyField>
        ) : null}
        <ReadOnlyField label={t("routes.fields.matchers")}>
          {isDefault ? (
            <p className="text-sm text-muted-foreground">{t("routes.form.defaultMatchers")}</p>
          ) : (
            <MatcherBuilder
              id={`${ID}-matchers`}
              rows={rows}
              onChange={() => {}}
              errors={new Map()}
              readOnly
            />
          )}
        </ReadOnlyField>
        <ReadOnlyField label={t("routes.fields.urgent")}>
          <span className="text-sm">
            {base.urgent ? t("routes.form.urgentOn") : t("routes.form.urgentOff")}
          </span>
        </ReadOnlyField>
        <div className="flex flex-col gap-1">
          <span id={`${ID}-group-key-title`} className="text-sm font-medium">
            {t("routes.fields.groupKey")}
          </span>
          <GroupKeyEditor
            id={`${ID}-group-key`}
            labelledBy={`${ID}-group-key-title`}
            value={base.group_key}
            onChange={() => {}}
            readOnly
          />
        </div>
        <RoutePolicyLifecycleReadOnly policy={base.policy} />
        <div>
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("routes.form.back")}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <form
      noValidate
      className="flex flex-col gap-6"
      onSubmit={form.handleSubmit((v) =>
        submit.mutate({ input: inputOf(base, v, keyDraft, isDefault), sent: sentRows(v.matchers) }),
      )}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${ID}-name`}>{t("routes.fields.name")}</Label>
        <Input
          id={`${ID}-name`}
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          className="max-w-md"
          aria-invalid={errors.name !== undefined}
          aria-describedby={errors.name ? `${ID}-name-error` : undefined}
          {...form.register("name")}
        />
        {errors.name && (
          <p id={`${ID}-name-error`} className="text-sm text-destructive">
            {nameErrorText(t, errors.name.message)}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${ID}-description`}>{t("routes.fields.description")}</Label>
        <textarea
          id={`${ID}-description`}
          rows={2}
          className="w-full max-w-md min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive dark:bg-input/30"
          aria-invalid={errors.description !== undefined}
          aria-describedby={errors.description ? `${ID}-description-error` : undefined}
          {...form.register("description")}
        />
        {errors.description && (
          <p id={`${ID}-description-error`} className="text-sm text-destructive">
            {fieldErrorText(t, errors.description.message ?? "")}
          </p>
        )}
      </div>
      <fieldset className="flex min-w-0 flex-col gap-2" aria-describedby={`${ID}-matchers-hint`}>
        <legend className="mb-2 text-sm font-medium">{t("routes.fields.matchers")}</legend>
        <p id={`${ID}-matchers-hint`} className="text-sm text-muted-foreground">
          {isDefault ? t("routes.form.defaultMatchers") : t("routes.matchers.hint")}
        </p>
        {!isDefault && (
          <Controller
            control={form.control}
            name="matchers"
            render={({ field }) => (
              <MatcherBuilder
                id={`${ID}-matchers`}
                rows={field.value}
                errors={matcherErrorMap}
                onChange={(next: MatcherRow[]) => {
                  // Errors of the form are shown by position: a removed row would leave them on the row after it.
                  if (next.length < field.value.length) {
                    form.clearErrors("matchers");
                  }
                  // A row that changes loses the server's error, which was about its old value.
                  setServerMatcherErrors((prev) => {
                    const kept = new Map(prev);
                    for (const row of next) {
                      const old = field.value.find((r) => r.key === row.key);
                      if (old !== undefined && old !== row) {
                        kept.delete(row.key);
                      }
                    }
                    return kept;
                  });
                  field.onChange(next);
                }}
              />
            )}
          />
        )}
      </fieldset>
      <div className="flex items-start gap-2">
        <input
          id={`${ID}-urgent`}
          type="checkbox"
          role="switch"
          className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          {...form.register("urgent")}
        />
        <Label htmlFor={`${ID}-urgent`} className="leading-snug">
          {t("routes.form.urgent")}
        </Label>
      </div>
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-2">
          <Label id={`${ID}-group-key-title`} htmlFor={`${ID}-group-key`}>
            {t("routes.fields.groupKey")}
          </Label>
          <Controller
            control={form.control}
            name="group_key"
            render={({ field }) => (
              <GroupKeyEditor
                id={`${ID}-group-key`}
                labelledBy={`${ID}-group-key-title`}
                value={field.value}
                error={serverGroupKeyError}
                draft={keyDraft}
                onDraftChange={setKeyDraft}
                onChange={(next) => {
                  setServerGroupKeyError(undefined);
                  field.onChange(next);
                }}
              />
            )}
          />
        </div>
        <GroupKeyPreviewPanel
          query={preview}
          currentKey={route?.group_key}
          onProblem={(err) => {
            if (err === null) {
              previewSent.current = sentRows(form.getValues("matchers"));
              setServerMatcherErrors(new Map());
              setServerGroupKeyError(undefined);
            } else {
              applyFieldProblem(err, previewSent.current);
            }
          }}
        />
      </div>
      <RoutePolicyLifecycle
        id={`${ID}-lifecycle`}
        reopenWindow={form.register("reopen_window_minutes", { valueAsNumber: true })}
        gracePeriod={form.register("grace_period_minutes", { valueAsNumber: true })}
        urgentRise={form.register("urgent_rise_removes_ack")}
        errors={{
          reopen_window_minutes: errors.reopen_window_minutes?.message,
          grace_period_minutes: errors.grace_period_minutes?.message,
          urgent_rise_removes_ack: errors.urgent_rise_removes_ack?.message,
        }}
      />
      {showStale && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>
              {isStale(submit.error)
                ? t("routes.errors.stale")
                : t("routes.errors.changedElsewhere")}
            </span>
            {onReload && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  submit.reset();
                  onReload();
                }}
              >
                {t("common.reload")}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      {submit.isError && !fieldHandled && !isStale(submit.error) && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {unmatched.length > 0
              ? unmatched.map((code) => fieldErrorText(t, code)).join(" ")
              : problemText(t, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={submit.isPending}>
          {submitLabel}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
