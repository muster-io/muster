// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The form of a Link rule (C-12.FR-9, AC-6): its name, its Matchers with the Matcher builder, its scope — the Alert
// Group, or each value of a label — and its URL template, whose preview renders the link against a chosen sample with
// the Lookup tables of the Organization. The built-in "Explore" rule keeps its name and scope and cannot be deleted
// (builtin_immutable); its Matchers and URL template can be edited. A save sends the version the form read as
// If-Match. Without link-rules:write the form only shows the rule.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  deleteLinkRule,
  getGetLinkRuleQueryKey,
  getListLinkRulesQueryKey,
} from "../api/gen/endpoints/links/links";
import type { LinkRule, LinkRuleBase, LinkRuleScope, ProblemError } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { useCan } from "./app-shell";
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
import { DEFAULT_SAMPLE, type Sample, SamplePicker } from "./sample-picker";
import { TemplateEditor } from "./template-editor";
import { TemplatePreview, previewErrors, useTemplatePreview } from "./template-preview";
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

const ID = "link-rule";
const NAME_MAX = 200;

const formSchema = z
  .object({
    name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
    matchers: z.array(
      z.object({
        key: z.number(),
        label: z.string(),
        op: z.enum(["=", "!=", "=~", "!~"]),
        value: z.string(),
      }),
    ),
    scope: z.enum(["alert_group", "label_value"]),
    scope_label: z.string(),
    url_template: z.string(),
  })
  .superRefine((v, ctx) => {
    v.matchers.forEach((row, index) => {
      if (!isEmptyRow(row) && row.label.trim() === "") {
        ctx.addIssue({ code: "custom", path: ["matchers", index, "label"], message: "required" });
      }
    });
    if (v.scope === "label_value" && v.scope_label.trim() === "") {
      ctx.addIssue({ code: "custom", path: ["scope_label"], message: "required" });
    }
    if (v.url_template.trim() === "") {
      ctx.addIssue({ code: "custom", path: ["url_template"], message: "required" });
    }
  });

type FormValues = z.infer<typeof formSchema>;

function formValues(rule: LinkRule | undefined): FormValues {
  return {
    name: rule?.name ?? "",
    matchers: matcherRows(rule?.matchers ?? []),
    scope: rule?.scope.type ?? "alert_group",
    scope_label: rule?.scope.label ?? "",
    url_template: rule?.url_template ?? "",
  };
}

function scopeOf(v: FormValues): LinkRuleScope {
  return v.scope === "label_value"
    ? { type: "label_value", label: v.scope_label.trim() }
    : { type: "alert_group" };
}

/** The scope of a rule, as the list shows it. */
export function scopeText(t: TFunction, scope: LinkRuleScope): string {
  return scope.type === "label_value"
    ? t("linkRules.scope.labelValueOf", { label: scope.label ?? "" })
    : t("linkRules.scope.alertGroup");
}

/** The mark of the built-in "Explore" rule. */
export function BuiltinBadge() {
  const { t } = useTranslation();
  return (
    <span
      className="inline-flex w-fit items-center rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap text-muted-foreground"
      data-testid="builtin-badge"
    >
      {t("linkRules.builtin")}
    </span>
  );
}

function errorText(t: TFunction, code: string | undefined): string | undefined {
  switch (code) {
    case undefined:
      return undefined;
    case "name_taken":
      return t("linkRules.errors.nameTaken");
    default:
      return fieldErrorText(t, code);
  }
}

function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

export interface LinkRuleFormProps {
  /** The stored rule of an edit. */
  rule?: LinkRule;
  readOnly?: boolean;
  submitLabel: string;
  save: (input: LinkRuleBase) => Promise<unknown>;
  stale?: boolean;
  onReload?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  onCancel: () => void;
}

export function LinkRuleForm({
  rule,
  readOnly = false,
  submitLabel,
  save,
  stale = false,
  onReload,
  onDirtyChange,
  onCancel,
}: LinkRuleFormProps) {
  const { t } = useTranslation();
  const builtin = rule?.builtin === true;
  const canPreview = useCan("templates:preview");
  const [serverMatcherErrors, setServerMatcherErrors] = useState<Map<number, MatcherRowErrors>>(
    () => new Map(),
  );
  const [templateErrors, setTemplateErrors] = useState<ProblemError[]>([]);
  const [unmatched, setUnmatched] = useState<string[]>([]);
  // An error of the scope other than its label, such as an unknown type.
  const [scopeError, setScopeError] = useState<string>();
  const [sample, setSample] = useState<Sample>(DEFAULT_SAMPLE);
  const [defaultValues] = useState(() => formValues(rule));
  const form = useForm<FormValues>({ resolver: zodResolver(formSchema), defaultValues });
  const rows = useWatch({ control: form.control, name: "matchers" });
  const scope = useWatch({ control: form.control, name: "scope" });
  const template = useWatch({ control: form.control, name: "url_template" });
  const preview = useTemplatePreview(
    { kind: "link_rule", template, sample },
    canPreview && template !== "",
  );

  const submit = useMutation({
    mutationFn: ({ input }: { input: LinkRuleBase; sent: MatcherRow[] }) => save(input),
    onMutate: () => {
      setServerMatcherErrors(new Map());
      setTemplateErrors([]);
      setUnmatched([]);
      setScopeError(undefined);
    },
    onError: (err, { sent }) => {
      if (!isApiError(err)) {
        return;
      }
      if (err.status === 409 && err.code === "name_taken") {
        form.setError("name", { type: "name_taken", message: "name_taken" }, { shouldFocus: true });
        return;
      }
      const byRow = matcherErrors(err, sent);
      setServerMatcherErrors(byRow);
      const rest: string[] = [];
      const atTemplate: ProblemError[] = [];
      for (const item of err.errors ?? []) {
        if (item.pointer === "/name") {
          form.setError("name", { type: item.code, message: item.code }, { shouldFocus: true });
        } else if (item.pointer === "/scope/label") {
          form.setError(
            "scope_label",
            { type: item.code, message: item.code },
            { shouldFocus: true },
          );
        } else if (item.pointer.startsWith("/scope")) {
          setScopeError(item.code);
        } else if (item.pointer === "/url_template") {
          atTemplate.push(item);
        } else if (!item.pointer.startsWith("/matchers/")) {
          rest.push(item.code);
        }
      }
      setTemplateErrors(atTemplate);
      setUnmatched(rest);
      if (atTemplate.length > 0) {
        document.getElementById(`${ID}-url-template`)?.focus();
      } else {
        const first = [...byRow.entries()][0];
        if (first !== undefined) {
          const field = (["label", "op", "value"] as const).find((f) => first[1][f] !== undefined);
          document.getElementById(`${ID}-matchers-${first[0]}-${field ?? "label"}`)?.focus();
        }
      }
    },
  });

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
  const shownTemplateErrors =
    templateErrors.length > 0
      ? templateErrors
      : template === ""
        ? []
        : previewErrors(preview, template);
  const isBuiltinRefusal =
    isApiError(submit.error) &&
    submit.error.status === 409 &&
    submit.error.code === "builtin_immutable";
  const fieldHandled =
    isApiError(submit.error) &&
    ((submit.error.status === 409 && submit.error.code === "name_taken") ||
      (Boolean(submit.error.errors?.length) && unmatched.length === 0));
  const showStale = (stale && !submit.isPending) || isOutdated(submit.error);

  const previewPanel = canPreview && (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex min-w-0 flex-col gap-1">
        <Label htmlFor={`${ID}-sample`} className="text-xs text-muted-foreground">
          {t("templates.sample.label")}
        </Label>
        <SamplePicker
          id={`${ID}-sample`}
          value={sample}
          onChange={setSample}
          defaultLabel={t("templates.sample.example")}
        />
      </div>
      {template === "" ? (
        <p className="text-sm text-muted-foreground">{t("linkRules.form.previewEmpty")}</p>
      ) : (
        <TemplatePreview id={`${ID}-preview`} kind="link_rule" preview={preview} />
      )}
    </div>
  );

  if (readOnly) {
    return (
      <div className="flex flex-col gap-6" data-testid="link-rule-read-only">
        <p className="text-sm text-muted-foreground">{t("linkRules.form.readOnly")}</p>
        <div className="flex flex-col gap-1">
          <span className="text-sm font-medium">{t("linkRules.fields.matchers")}</span>
          <MatcherBuilder
            id={`${ID}-matchers`}
            rows={rows}
            onChange={() => {}}
            errors={new Map()}
            readOnly
          />
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-sm font-medium">{t("linkRules.fields.scope")}</span>
          <span className="text-sm">{rule === undefined ? "" : scopeText(t, rule.scope)}</span>
        </div>
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">{t("linkRules.fields.urlTemplate")}</span>
          <TemplateEditor
            id={`${ID}-url-template`}
            label={t("linkRules.fields.urlTemplate")}
            value={rule?.url_template ?? ""}
            readOnly
          />
        </div>
        {previewPanel}
        <div>
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("linkRules.form.back")}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={form.handleSubmit((v) =>
        submit.mutate({
          input: {
            // The built-in rule keeps its name and scope.
            name: builtin && rule !== undefined ? rule.name : v.name.trim(),
            matchers: matchersOf(v.matchers),
            scope: builtin && rule !== undefined ? rule.scope : scopeOf(v),
            url_template: v.url_template,
          },
          sent: sentRows(v.matchers),
        }),
      )}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${ID}-name`}>{t("linkRules.fields.name")}</Label>
        <Input
          id={`${ID}-name`}
          autoComplete="off"
          maxLength={NAME_MAX}
          readOnly={builtin}
          className="max-w-md read-only:bg-muted/40"
          aria-invalid={errors.name !== undefined}
          aria-describedby={
            [builtin ? `${ID}-builtin-hint` : null, errors.name ? `${ID}-name-error` : null]
              .filter((v) => v !== null)
              .join(" ") || undefined
          }
          {...form.register("name")}
        />
        {builtin && (
          <p id={`${ID}-builtin-hint`} className="text-sm text-muted-foreground">
            {t("linkRules.form.builtinHint")}
          </p>
        )}
        {errors.name && (
          <p id={`${ID}-name-error`} className="text-sm text-destructive">
            {errorText(t, errors.name.message)}
          </p>
        )}
      </div>
      <fieldset className="flex min-w-0 flex-col gap-2" aria-describedby={`${ID}-matchers-hint`}>
        <legend className="mb-2 text-sm font-medium">{t("linkRules.fields.matchers")}</legend>
        <p id={`${ID}-matchers-hint`} className="text-sm text-muted-foreground">
          {t("linkRules.form.matchersHint")}
        </p>
        <Controller
          control={form.control}
          name="matchers"
          render={({ field }) => (
            <MatcherBuilder
              id={`${ID}-matchers`}
              rows={field.value}
              errors={matcherErrorMap}
              onChange={(next: MatcherRow[]) => {
                if (next.length < field.value.length) {
                  form.clearErrors("matchers");
                }
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
      </fieldset>
      <fieldset className="flex min-w-0 flex-col gap-2" disabled={builtin}>
        <legend className="mb-2 text-sm font-medium">{t("linkRules.fields.scope")}</legend>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            value="alert_group"
            className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            {...form.register("scope")}
          />
          {t("linkRules.scope.alertGroup")}
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            value="label_value"
            className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            {...form.register("scope")}
          />
          {t("linkRules.scope.labelValue")}
        </label>
        {scope === "label_value" && (
          <div className="flex flex-col gap-2 pl-6">
            <Label htmlFor={`${ID}-scope-label`}>{t("linkRules.fields.scopeLabel")}</Label>
            <Input
              id={`${ID}-scope-label`}
              autoComplete="off"
              spellCheck={false}
              className="max-w-xs font-mono"
              aria-invalid={errors.scope_label !== undefined}
              aria-describedby={`${ID}-scope-hint${errors.scope_label ? ` ${ID}-scope-error` : ""}`}
              {...form.register("scope_label")}
            />
            <p id={`${ID}-scope-hint`} className="text-sm text-muted-foreground">
              {t("linkRules.form.scopeHint")}
            </p>
            {errors.scope_label && (
              <p id={`${ID}-scope-error`} className="text-sm text-destructive">
                {errors.scope_label.message === "required"
                  ? t("linkRules.errors.scopeLabelRequired")
                  : fieldErrorText(t, errors.scope_label.message ?? "")}
              </p>
            )}
          </div>
        )}
        {scopeError !== undefined && (
          <p className="text-sm text-destructive">{fieldErrorText(t, scopeError)}</p>
        )}
      </fieldset>
      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor={`${ID}-url-template`}>{t("linkRules.fields.urlTemplate")}</Label>
        <p id={`${ID}-url-template-hint`} className="text-sm text-muted-foreground">
          {t("linkRules.form.urlTemplateHint")}
        </p>
        <Controller
          control={form.control}
          name="url_template"
          render={({ field }) => (
            <TemplateEditor
              id={`${ID}-url-template`}
              label={t("linkRules.fields.urlTemplate")}
              describedBy={`${ID}-url-template-hint`}
              value={field.value}
              minRows={2}
              errors={
                errors.url_template
                  ? [
                      {
                        pointer: "/url_template",
                        code: "required",
                        detail: t("linkRules.errors.urlTemplateRequired"),
                      },
                    ]
                  : shownTemplateErrors
              }
              inputRef={field.ref}
              onChange={(next) => {
                setTemplateErrors([]);
                form.clearErrors("url_template");
                field.onChange(next);
              }}
            />
          )}
        />
        {previewPanel}
      </div>
      {showStale && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>
              {isOutdated(submit.error)
                ? t("linkRules.errors.stale")
                : t("linkRules.errors.changedElsewhere")}
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
      {submit.isError && !fieldHandled && !isOutdated(submit.error) && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {isBuiltinRefusal
              ? t("linkRules.errors.builtin")
              : unmatched.length > 0
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

/** "Delete" of a Link rule; the built-in rule has none, and the server refuses it anyway (builtin_immutable). */
export function LinkRuleDeleteDialog({ rule }: { rule: LinkRule }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteLinkRule(rule.id, { headers: { "If-Match": rule.etag ?? "" } }),
    onSuccess: async () => {
      await navigate({ to: "/admin/organization/link-rules" });
      queryClient.removeQueries({ queryKey: getGetLinkRuleQueryKey(rule.id) });
      void queryClient.invalidateQueries({ queryKey: getListLinkRulesQueryKey() });
    },
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetLinkRuleQueryKey(rule.id) });
      }
    },
  });
  if (rule.builtin) {
    return null;
  }
  return (
    <>
      <Button
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("linkRules.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("linkRules.delete.title", { name: rule.name })}
            </DialogTitle>
            <DialogDescription>{t("linkRules.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isApiError(remove.error) && remove.error.code === "builtin_immutable"
                  ? t("linkRules.errors.builtin")
                  : isStale(remove.error)
                    ? t("linkRules.errors.stale")
                    : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("linkRules.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
