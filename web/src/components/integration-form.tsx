// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The create and edit form of an Integration (C-05.FR-1, C-07.FR-2): the name, the description, the Static labels,
// the duplicate window, pre-filled with integration.duplicate_window, and the Heartbeat: on or off, its timeout in
// minutes, pre-filled with integration.heartbeat_timeout, and the Heartbeat URL with how to pass the token. The
// Connection mode is shown, not chosen: L1 has only "Webhook only", with the precision it gives.

import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { Controller, type UseFormReturn, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Integration, IntegrationInput } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { LABEL_NAME, type LabelRow, LabelsEditor, labelRows, labelsOf } from "./labels-editor";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** integration.duplicate_window, in seconds. */
export const DEFAULT_DUPLICATE_WINDOW_SECONDS = 45;

/** integration.heartbeat_timeout, in seconds. */
export const DEFAULT_HEARTBEAT_TIMEOUT_SECONDS = 300;

/** The longest name of an Integration, in characters, as the API checks it. */
const NAME_MAX = 200;

/** The Connection mode with the precision it gives (C-05.FR-1). The text follows the built-in values
 * processing.gone_min_absence (5 minutes) and processing.stale_after_factor (3). */
export function ConnectionMode({ testId }: { testId?: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-1" data-testid={testId}>
      <span className="font-medium">{t("integrations.connectionMode.webhookOnly")}</span>
      <span className="text-sm text-muted-foreground">
        {t("integrations.connectionMode.precision")}
      </span>
    </div>
  );
}

/** The label of the Connection mode, for lists. */
export function connectionModeLabel(t: TFunction, mode: Integration["connection_mode"]): string {
  return mode === "webhook_only" ? t("integrations.connectionMode.webhookOnly") : mode;
}

/** The mark of the built-in Integration "Muster" (C-06.FR-14). */
export function BuiltinBadge() {
  const { t } = useTranslation();
  return (
    <span
      className="inline-flex w-fit items-center rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap text-muted-foreground"
      data-testid="builtin-badge"
    >
      {t("integrations.builtin.badge")}
    </span>
  );
}

/** The Heartbeat timeout in seconds from the minutes entered, or undefined when it is not a positive number. */
function timeoutSeconds(minutes: string): number | undefined {
  const text = minutes.trim();
  const value = Number(text);
  const seconds = Math.round(value * 60);
  return text === "" || !Number.isFinite(value) || seconds < 1 ? undefined : seconds;
}

/** The stored timeout in minutes, as the form shows it: a whole number, or up to two decimals for odd seconds. */
function timeoutMinutes(seconds: number): string {
  return String(Math.round((seconds / 60) * 100) / 100);
}

const formSchema = z
  .object({
    name: z.string().trim().min(1, "required").max(NAME_MAX, "too_long"),
    description: z.string(),
    labels: z.array(z.object({ key: z.number(), name: z.string(), value: z.string() })),
    duplicate_window_seconds: z.string(),
    heartbeat_enabled: z.boolean(),
    heartbeat_timeout_minutes: z.string(),
  })
  .superRefine((v, ctx) => {
    const seen = new Set<string>();
    v.labels.forEach((row, index) => {
      const name = row.name.trim();
      if (name === "" && row.value === "") {
        return;
      }
      const path = ["labels", index, "name"];
      if (name === "") {
        ctx.addIssue({ code: "custom", path, message: "required" });
      } else if (!LABEL_NAME.test(name)) {
        ctx.addIssue({ code: "custom", path, message: "label_name" });
      } else if (seen.has(name)) {
        ctx.addIssue({ code: "custom", path, message: "label_duplicate" });
      }
      seen.add(name);
    });
    const seconds = Number(v.duplicate_window_seconds);
    if (!Number.isInteger(seconds) || seconds < 1) {
      ctx.addIssue({ code: "custom", path: ["duplicate_window_seconds"], message: "out_of_range" });
    }
    if (v.heartbeat_enabled && timeoutSeconds(v.heartbeat_timeout_minutes) === undefined) {
      ctx.addIssue({
        code: "custom",
        path: ["heartbeat_timeout_minutes"],
        message: "heartbeat_timeout",
      });
    }
  });

type FormValues = z.infer<typeof formSchema>;

/** The form's values for an Integration, or for a new one. */
function formValues(integration: Integration | undefined): FormValues {
  return {
    name: integration?.name ?? "",
    description: integration?.description ?? "",
    labels: labelRows(integration?.static_labels ?? {}),
    duplicate_window_seconds: String(
      integration?.duplicate_window_seconds ?? DEFAULT_DUPLICATE_WINDOW_SECONDS,
    ),
    heartbeat_enabled: integration?.heartbeat.enabled ?? false,
    heartbeat_timeout_minutes: timeoutMinutes(
      integration?.heartbeat.timeout_seconds ?? DEFAULT_HEARTBEAT_TIMEOUT_SECONDS,
    ),
  };
}

/** The input of the form. With the Heartbeat off its timeout is left out, so the stored one stays. */
function inputOf(v: FormValues): IntegrationInput {
  const timeout = v.heartbeat_enabled ? timeoutSeconds(v.heartbeat_timeout_minutes) : undefined;
  return {
    name: v.name.trim(),
    description: v.description.trim(),
    connection_mode: "webhook_only",
    static_labels: labelsOf(v.labels),
    duplicate_window_seconds: Number(v.duplicate_window_seconds),
    heartbeat:
      timeout === undefined
        ? { enabled: v.heartbeat_enabled }
        : { enabled: v.heartbeat_enabled, timeout_seconds: timeout },
  };
}

function errorText(t: TFunction, code: string | undefined): string {
  switch (code) {
    case "label_name":
      return t("labels.errors.invalidName");
    case "label_duplicate":
      return t("labels.errors.duplicate");
    case "out_of_range":
      return t("integrations.errors.duplicateWindow");
    case "heartbeat_timeout":
      return t("heartbeat.errors.timeout");
    case "name_taken":
      return t("integrations.errors.nameTaken");
    case "invalid_format":
      return t("fieldErrors.invalidFormat");
    default:
      return fieldErrorText(t, code ?? "");
  }
}

/** The index of the row a pointer such as "/static_labels/cluster" names (RFC 6901 escaping). */
function labelIndex(rows: readonly LabelRow[], pointer: string): number {
  const name = pointer.slice("/static_labels/".length).replaceAll("~1", "/").replaceAll("~0", "~");
  return name === "" ? -1 : rows.findIndex((r) => r.name.trim() === name);
}

/**
 * The Heartbeat section: the switch, and while it is on the timeout in minutes and the Heartbeat URL, which a new
 * Integration gets when it is created.
 */
function HeartbeatSection({
  form,
  url,
  error,
}: {
  form: UseFormReturn<FormValues>;
  url: string | undefined;
  error: string | undefined;
}) {
  const { t } = useTranslation();
  const enabled = useWatch({ control: form.control, name: "heartbeat_enabled" });
  return (
    <fieldset className="flex flex-col gap-3" aria-describedby="integration-heartbeat-hint">
      <legend className="mb-2 text-sm font-medium">{t("heartbeat.title")}</legend>
      <p id="integration-heartbeat-hint" className="text-sm text-muted-foreground">
        {t("heartbeat.form.hint")}
      </p>
      <div className="flex items-center gap-2">
        <input
          id="integration-heartbeat-enabled"
          type="checkbox"
          role="switch"
          className="size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
          {...form.register("heartbeat_enabled")}
        />
        <Label htmlFor="integration-heartbeat-enabled">{t("heartbeat.form.enabled")}</Label>
      </div>
      {enabled && (
        <>
          <div className="flex flex-col gap-2">
            <Label htmlFor="integration-heartbeat-timeout">{t("heartbeat.form.timeout")}</Label>
            <div className="flex items-center gap-2">
              <Input
                id="integration-heartbeat-timeout"
                type="number"
                inputMode="decimal"
                step="any"
                className="w-28"
                aria-invalid={error !== undefined}
                aria-describedby={
                  error
                    ? "integration-heartbeat-timeout-unit integration-heartbeat-timeout-error integration-heartbeat-timeout-hint"
                    : "integration-heartbeat-timeout-unit integration-heartbeat-timeout-hint"
                }
                {...form.register("heartbeat_timeout_minutes")}
              />
              <span
                id="integration-heartbeat-timeout-unit"
                className="text-sm text-muted-foreground"
              >
                {t("heartbeat.form.minutesUnit")}
              </span>
            </div>
            {error && (
              <p id="integration-heartbeat-timeout-error" className="text-sm text-destructive">
                {error}
              </p>
            )}
            <p id="integration-heartbeat-timeout-hint" className="text-sm text-muted-foreground">
              {t("heartbeat.form.timeoutHint")}
            </p>
          </div>
          <div className="flex flex-col gap-2">
            {url ? (
              <Label htmlFor="integration-heartbeat-url">{t("heartbeat.form.url")}</Label>
            ) : (
              <span className="text-sm font-medium">{t("heartbeat.form.url")}</span>
            )}
            {url ? (
              <Input
                id="integration-heartbeat-url"
                readOnly
                value={url}
                spellCheck={false}
                className="max-w-md font-mono text-xs"
                aria-describedby="integration-heartbeat-url-hint"
              />
            ) : (
              <p className="text-sm text-muted-foreground">{t("heartbeat.form.urlAfterCreate")}</p>
            )}
            <p id="integration-heartbeat-url-hint" className="text-sm text-muted-foreground">
              {t("heartbeat.form.urlHint")}
            </p>
          </div>
        </>
      )}
    </fieldset>
  );
}

/**
 * The form. save sends the input (with If-Match on an update); a refusal with field errors puts them on their fields, a
 * stale save (412) offers onReload.
 */
export function IntegrationForm({
  integration,
  submitLabel,
  save,
  stale,
  onReload,
  onDirtyChange,
  onCancel,
}: {
  integration?: Integration;
  submitLabel: string;
  save: (input: IntegrationInput) => Promise<unknown>;
  /** A newer version than the form's was read while the form had changes. */
  stale?: boolean;
  onReload?: () => void;
  /** Told whether the form has changes, so that a newer version replaces an untouched form. */
  onDirtyChange?: (dirty: boolean) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: formValues(integration),
  });
  const submit = useMutation({
    mutationFn: (input: IntegrationInput) => save(input),
    onMutate: () => setUnmatched([]),
    onError: (err) => {
      if (!isApiError(err)) {
        return;
      }
      if (err.status === 409 && err.code === "name_taken") {
        form.setError("name", { type: "name_taken", message: "name_taken" });
        return;
      }
      const rest: string[] = [];
      for (const item of err.errors ?? []) {
        const pointer = item.pointer;
        if (pointer === "/name") {
          form.setError("name", { type: item.code, message: item.code });
        } else if (pointer === "/description") {
          form.setError("description", { type: item.code, message: item.code });
        } else if (pointer === "/duplicate_window_seconds") {
          form.setError("duplicate_window_seconds", { type: item.code, message: item.code });
        } else if (pointer === "/heartbeat/timeout_seconds") {
          form.setError("heartbeat_timeout_minutes", {
            type: item.code,
            message: item.code === "out_of_range" ? "heartbeat_timeout" : item.code,
          });
        } else if (pointer.startsWith("/static_labels/")) {
          const index = labelIndex(form.getValues("labels"), pointer);
          if (index >= 0) {
            form.setError(`labels.${index}.name`, {
              type: item.code,
              message: item.code === "invalid_format" ? "label_name" : item.code,
            });
          } else {
            rest.push(item.code);
          }
        } else {
          rest.push(item.code);
        }
      }
      setUnmatched(rest);
    },
  });
  const dirty = form.formState.isDirty;
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  const errors = form.formState.errors;
  const fieldHandled =
    isApiError(submit.error) &&
    ((submit.error.status === 409 && submit.error.code === "name_taken") ||
      (Boolean(submit.error.errors?.length) && unmatched.length === 0));
  const showStale = (stale === true && !submit.isPending) || isStale(submit.error);
  return (
    <form
      noValidate
      className="flex flex-col gap-6"
      onSubmit={form.handleSubmit((v) => submit.mutate(inputOf(v)))}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor="integration-name">{t("integrations.fields.name")}</Label>
        <Input
          id="integration-name"
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          className="max-w-md"
          aria-invalid={errors.name !== undefined}
          aria-describedby={errors.name ? "integration-name-error" : "integration-name-hint"}
          {...form.register("name")}
        />
        {errors.name ? (
          <p id="integration-name-error" className="text-sm text-destructive">
            {errorText(t, errors.name.message)}
          </p>
        ) : (
          <p id="integration-name-hint" className="text-sm text-muted-foreground">
            {t("integrations.form.nameHint")}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="integration-description">{t("integrations.fields.description")}</Label>
        <textarea
          id="integration-description"
          rows={3}
          className="w-full max-w-md min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive dark:bg-input/30"
          aria-invalid={errors.description !== undefined}
          aria-describedby={errors.description ? "integration-description-error" : undefined}
          {...form.register("description")}
        />
        {errors.description && (
          <p id="integration-description-error" className="text-sm text-destructive">
            {errorText(t, errors.description.message)}
          </p>
        )}
      </div>
      <fieldset className="flex flex-col gap-2" aria-describedby="integration-labels-hint">
        <legend className="mb-2 text-sm font-medium">
          {t("integrations.fields.staticLabels")}
        </legend>
        <p id="integration-labels-hint" className="text-sm text-muted-foreground">
          {t("integrations.form.staticLabelsHint")}
        </p>
        <Controller
          control={form.control}
          name="labels"
          render={({ field }) => (
            <LabelsEditor
              id="integration-labels"
              rows={field.value}
              onChange={(rows) => {
                // Errors are shown by position: a removed row would leave them on the row after it.
                if (rows.length < field.value.length) {
                  form.clearErrors("labels");
                }
                field.onChange(rows);
              }}
              errors={field.value.map((_, index) => {
                const message = errors.labels?.[index]?.name?.message;
                return message === undefined ? undefined : errorText(t, message);
              })}
            />
          )}
        />
      </fieldset>
      <div className="flex flex-col gap-2">
        <Label htmlFor="integration-duplicate-window">
          {t("integrations.fields.duplicateWindow")}
        </Label>
        <div className="flex items-center gap-2">
          <Input
            id="integration-duplicate-window"
            type="number"
            inputMode="numeric"
            min={1}
            step={1}
            className="w-28"
            aria-invalid={errors.duplicate_window_seconds !== undefined}
            aria-describedby={
              errors.duplicate_window_seconds
                ? "integration-duplicate-window-error integration-duplicate-window-hint"
                : "integration-duplicate-window-hint"
            }
            {...form.register("duplicate_window_seconds")}
          />
          <span className="text-sm text-muted-foreground">
            {t("integrations.form.secondsUnit")}
          </span>
        </div>
        {errors.duplicate_window_seconds && (
          <p id="integration-duplicate-window-error" className="text-sm text-destructive">
            {errorText(t, errors.duplicate_window_seconds.message)}
          </p>
        )}
        <p id="integration-duplicate-window-hint" className="text-sm text-muted-foreground">
          {t("integrations.form.duplicateWindowHint")}
        </p>
      </div>
      <HeartbeatSection
        form={form}
        url={integration?.heartbeat.url}
        error={
          errors.heartbeat_timeout_minutes
            ? errorText(t, errors.heartbeat_timeout_minutes.message)
            : undefined
        }
      />
      <div className="flex flex-col gap-2">
        <span className="text-sm font-medium">{t("integrations.fields.connectionMode")}</span>
        <ConnectionMode testId="integration-connection-mode" />
      </div>
      {showStale && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("integrations.errors.stale")}</span>
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
              ? unmatched.map((code) => errorText(t, code)).join(" ")
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
