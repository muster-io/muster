// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Message section of the Route editor (C-08.FR-1; C-12.FR-2, FR-3, FR-5, AC-2): the language of the messages
// (policy.language) and three templates — "Root message", "Alert line" and "Ack timeout notice" (policy.templates;
// the notice is used from the ack timeout's story on) — each "Built-in" (null) or "Custom" (a string). "Custom" starts
// from the source of the built-in template in the Route's language, which previewTemplate returns for an empty
// template. Under each editor its preview renders against the chosen sample, refreshed as the text changes, and the
// editor marks the errors of the preview or of a refused save at their line and column.

import { useMutation } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { previewTemplate } from "../api/gen/endpoints/templates/templates";
import type { Language, ProblemError, RoutePolicy, RouteTemplates } from "../api/gen/model";
import { fieldErrorText, problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { DEFAULT_SAMPLE, type Sample, SamplePicker } from "./sample-picker";
import { TemplateEditor, templateName } from "./template-editor";
import {
  FormatSwitch,
  type PreviewFormat,
  TemplatePreview,
  previewErrors,
  useTemplatePreview,
} from "./template-preview";
import { Alert, AlertDescription } from "./ui/alert";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The templates of a Route, in the order of the section. */
export const MESSAGE_TEMPLATES = ["root_message", "line", "ack_timeout_notice"] as const;
export type MessageTemplate = (typeof MESSAGE_TEMPLATES)[number];

/** The values of the section in the form of the Route editor. */
export interface MessageValues {
  language: Language;
  templates: Record<MessageTemplate, string | null>;
}

export function messageValues(policy: RoutePolicy): MessageValues {
  return {
    language: policy.language,
    templates: {
      root_message: policy.templates.root_message ?? null,
      line: policy.templates.line ?? null,
      ack_timeout_notice: policy.templates.ack_timeout_notice ?? null,
    },
  };
}

/** The policy with the values of the section; every other policy field stays as it was. */
export function withMessage(policy: RoutePolicy, v: MessageValues): RoutePolicy {
  const templates: RouteTemplates = { ...v.templates };
  return { ...policy, language: v.language, templates };
}

/** The pointer of the language in a refusal. */
export const LANGUAGE_POINTER = "/policy/language";

/** The template a pointer of a refusal names, such as /policy/templates/line. */
export function templateOfPointer(pointer: string): MessageTemplate | undefined {
  const name = /^\/policy\/templates\/([a-z_]+)$/.exec(pointer)?.[1];
  return MESSAGE_TEMPLATES.find((m) => m === name);
}

function templateHint(t: TFunction, name: MessageTemplate): string {
  switch (name) {
    case "root_message":
      return t("routes.message.rootMessageHint");
    case "line":
      return t("routes.message.lineHint");
    default:
      return t("routes.message.ackTimeoutNoticeHint");
  }
}

/** A language by its own name, as the profile offers it. */
function languageName(language: Language): string {
  return language === "ru" ? "Русский" : "English";
}

/** One template: Built-in or Custom, its editor and its preview. */
function TemplateField({
  id,
  name,
  value,
  onChange,
  errors,
  routeId,
  language,
  readOnly,
}: {
  id: string;
  name: MessageTemplate;
  value: string | null;
  onChange: (value: string | null) => void;
  /** The errors of a refused save, shown in place of those of the preview until the text changes. */
  errors?: readonly ProblemError[];
  routeId?: string;
  language: Language;
  readOnly: boolean;
}) {
  const { t } = useTranslation();
  const canPreview = useCan("templates:preview");
  const [sample, setSample] = useState<Sample>(DEFAULT_SAMPLE);
  const [format, setFormat] = useState<PreviewFormat>("markdown");
  // The custom text kept while "Built-in" is chosen, so that switching back restores it.
  const [draft, setDraft] = useState<string | null>(null);
  const custom = value !== null;
  const template = value ?? "";
  const preview = useTemplatePreview(
    { kind: name, template, routeId, language, sample, format },
    canPreview,
  );
  // The language the built-in source is wanted in; it may change while the source is read.
  const wanted = useRef(language);
  useEffect(() => {
    wanted.current = language;
  });
  const builtin = useMutation({
    mutationFn: (lang: Language) =>
      previewTemplate({ kind: name, template: "", route_id: routeId ?? null, language: lang }),
    onSuccess: (result, lang) => {
      if (lang !== wanted.current) {
        builtin.mutate(wanted.current);
        return;
      }
      onChange(result.source ?? "");
    },
    // Without the source the template stays built in rather than an empty custom one.
    onError: () => onChange(null),
  });
  const shownErrors =
    errors !== undefined && errors.length > 0
      ? errors
      : custom
        ? previewErrors(preview, template)
        : [];
  const title = templateName(t, name);
  const choose = (next: "builtin" | "custom") => {
    if (next === "builtin" && custom) {
      setDraft(value);
      onChange(null);
    } else if (next === "custom" && !custom) {
      if (draft !== null) {
        onChange(draft);
      } else {
        // Custom at once, with the editor locked until the built-in source fills it.
        onChange("");
        builtin.mutate(language);
      }
    }
  };
  return (
    <section
      className="flex min-w-0 flex-col gap-2 border-t pt-4 first:border-t-0 first:pt-0"
      aria-labelledby={`${id}-title`}
      data-testid={`template-${name}`}
    >
      <h3 id={`${id}-title`} className="text-sm font-semibold">
        {title}
      </h3>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {templateHint(t, name)}
      </p>
      {readOnly ? (
        <p className="text-sm">
          {custom ? t("routes.message.custom") : t("routes.message.builtin")}
        </p>
      ) : (
        <fieldset className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <legend className="sr-only">{t("routes.message.source", { template: title })}</legend>
          {(["builtin", "custom"] as const).map((option) => (
            <label key={option} className="flex items-center gap-1.5 text-sm">
              <input
                type="radio"
                name={`${id}-source`}
                value={option}
                checked={(option === "custom") === custom}
                disabled={builtin.isPending}
                className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
                onChange={() => choose(option)}
              />
              {option === "custom" ? t("routes.message.custom") : t("routes.message.builtin")}
            </label>
          ))}
        </fieldset>
      )}
      {builtin.isError && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, builtin.error)}
          </AlertDescription>
        </Alert>
      )}
      {custom && (
        <TemplateEditor
          id={`${id}-editor`}
          label={title}
          describedBy={`${id}-hint`}
          value={template}
          readOnly={readOnly}
          disabled={builtin.isPending}
          errors={shownErrors}
          onChange={(next) => onChange(next)}
        />
      )}
      {canPreview && (
        <div className="flex min-w-0 flex-col gap-2">
          <div className="flex min-w-0 flex-wrap items-end gap-x-4 gap-y-2">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <Label htmlFor={`${id}-sample`} className="text-xs text-muted-foreground">
                {t("templates.sample.label")}
              </Label>
              <SamplePicker
                id={`${id}-sample`}
                value={sample}
                onChange={setSample}
                routeId={routeId}
                defaultLabel={t("templates.sample.routeRecent")}
              />
            </div>
            <FormatSwitch name={`${id}-format`} value={format} onChange={setFormat} />
          </div>
          <TemplatePreview id={`${id}-preview`} kind={name} preview={preview} />
        </div>
      )}
    </section>
  );
}

export function RoutePolicyMessage({
  id,
  routeId,
  value,
  onChange,
  errors,
  languageError,
  readOnly = false,
}: {
  id: string;
  /** The stored Route of an edit, whose recent Stored Snapshots the previews render against. */
  routeId?: string;
  value: MessageValues;
  onChange?: (value: MessageValues) => void;
  /** The errors of a refused save, by template. */
  errors?: Partial<Record<MessageTemplate, readonly ProblemError[]>>;
  languageError?: string;
  readOnly?: boolean;
}) {
  const { t } = useTranslation();
  const languages: Language[] = ["en", "ru"];
  return (
    <fieldset className="flex min-w-0 flex-col gap-4" data-testid="route-message">
      <legend className="mb-2 text-base font-semibold">{t("routes.message.title")}</legend>
      <div className="flex flex-col gap-2">
        {readOnly ? (
          <>
            <span className="text-sm font-medium">{t("routes.message.language")}</span>
            <span className="text-sm" data-testid="route-language">
              {languageName(value.language)}
            </span>
          </>
        ) : (
          <Label htmlFor={`${id}-language`}>{t("routes.message.language")}</Label>
        )}
        {!readOnly && (
          <NativeSelect
            id={`${id}-language`}
            className="w-48"
            value={value.language}
            aria-invalid={languageError !== undefined}
            aria-describedby={languageError === undefined ? undefined : `${id}-language-error`}
            onChange={(e) => {
              const language = languages.find((l) => l === e.target.value);
              if (language !== undefined) {
                onChange?.({ ...value, language });
              }
            }}
          >
            {languages.map((language) => (
              <NativeSelectOption key={language} value={language}>
                {languageName(language)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        )}
        {languageError !== undefined && (
          <p id={`${id}-language-error`} className="text-sm text-destructive">
            {fieldErrorText(t, languageError)}
          </p>
        )}
      </div>
      {MESSAGE_TEMPLATES.map((name) => (
        <TemplateField
          key={name}
          id={`${id}-${name}`}
          name={name}
          value={value.templates[name]}
          errors={errors?.[name]}
          routeId={routeId}
          language={value.language}
          readOnly={readOnly}
          onChange={(next) =>
            onChange?.({ ...value, templates: { ...value.templates, [name]: next } })
          }
        />
      ))}
    </fieldset>
  );
}
