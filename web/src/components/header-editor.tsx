// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The headers of an outgoing webhook request (C-15.FR-1, FR-3, FR-10), and the one-line template fields they share
// with the URLs: a header is a name and a value, a Go template that may read Secrets as {{ .Secrets.<name> }}. A
// refused save shows its problem at the field, with the line and column of a template error; a template that reads
// .Secrets or .Response other than by a literal name is refused (D291). The stored Destination warns where a header
// named Authorization or the URL's query or user information holds a literal value, while the field still holds it.

import type { TFunction } from "i18next";
import { PlusIcon, TriangleAlertIcon, XIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { HeaderTemplate, ProblemError } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import type { FieldProblem } from "./destination-form";
import { positionedErrorText } from "./template-editor";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** What the fields of an outgoing webhook show beside their values, by the JSON pointer of the request body. */
export interface FieldNotes {
  /** The problem of the last refused save at a pointer. */
  problem: (pointer: string) => FieldProblem | undefined;
  /** Whether the stored Destination warns of a literal credential at a pointer that still holds the stored value. */
  literal: (pointer: string) => boolean;
  /** An extracted value the template src at a pointer reads that no extraction rule its request may read gives. */
  unreadable: (pointer: string, src: string) => string | undefined;
}

/** The reference of a Secret or an extracted value in a template, such as {{ .Secrets.token }}. */
export function reference(field: "Secrets" | "Response", name: string): string {
  return `{{ .${field}.${name} }}`;
}

const HEADER_NAME = /\/headers\/\d+\/name$/;
const RULE_NAME = /\/extract\/\d+\/name$/;
const RULE_PATH = /\/extract\/\d+\/path$/;
const URL_FIELD = /\/url$/;
const HEADER_VALUE = /\/headers\/\d+\/value$/;

/**
 * A problem at a pointer as the template editor shows it: the parser's message for a template that does not parse,
 * and the texts of the other codes in the reader's language. A template error keeps its line and column. missing is
 * the extracted value the template reads that its request may not read, which the dry run refuses without a position.
 */
export function shownProblem(
  t: TFunction,
  pointer: string,
  problem: FieldProblem,
  missing?: string,
): ProblemError {
  const error: ProblemError = { pointer, code: problem.code };
  if (problem.line !== undefined) {
    error.line = problem.line;
  }
  if (problem.column !== undefined) {
    error.column = problem.column;
  }
  if (problem.code === "template_syntax" || problem.code === "unknown_function") {
    if (problem.detail !== undefined) {
      error.detail = problem.detail;
    }
    return error;
  }
  const invalid = problem.code === "invalid_format";
  if (invalid && problem.line !== undefined) {
    // The only refusal with a position besides the parser's: .Secrets or .Response read other than by name.
    error.detail = t("destinations.webhook.errors.reference", {
      secret: reference("Secrets", "<name>"),
      response: reference("Response", "<name>"),
    });
  } else if (problem.code === "reserved") {
    error.detail = t("destinations.webhook.errors.reservedHeader");
  } else if (invalid && HEADER_NAME.test(pointer)) {
    error.detail = t("destinations.webhook.errors.headerName");
  } else if (invalid && RULE_NAME.test(pointer)) {
    error.detail = t("destinations.webhook.errors.ruleName");
  } else if (invalid && RULE_PATH.test(pointer)) {
    error.detail = t("destinations.webhook.errors.jsonPath");
  } else if (invalid && missing !== undefined) {
    error.detail = t("destinations.webhook.errors.missingValue", {
      ref: reference("Response", missing),
    });
  } else if (invalid && URL_FIELD.test(pointer)) {
    error.detail = t("destinations.webhook.errors.url");
  } else if (invalid && HEADER_VALUE.test(pointer)) {
    error.detail = t("destinations.webhook.errors.lineBreak");
  } else {
    error.detail = fieldErrorText(t, problem.code);
  }
  return error;
}

/** The text of a problem at a pointer, with its line and column; src is the template it is about. */
export function problemText(
  t: TFunction,
  pointer: string,
  problem: FieldProblem,
  src: string,
  missing?: string,
): string {
  return positionedErrorText(t, shownProblem(t, pointer, problem, missing), src);
}

/**
 * Stable keys of the rows of a list, such as headers: removing a row keeps the keys, and so the elements and the focus,
 * of the rows after it. A list replaced from outside keeps the keys of its leading rows.
 */
export function useRowKeys(length: number): {
  keys: readonly number[];
  add: () => void;
  remove: (index: number) => void;
} {
  const [state, setState] = useState(() => ({
    keys: Array.from({ length }, (_, i) => i),
    next: length,
  }));
  let { keys, next } = state;
  if (keys.length !== length) {
    keys = keys.slice(0, length);
    while (keys.length < length) {
      keys = [...keys, next];
      next += 1;
    }
    setState({ keys, next });
  }
  return {
    keys,
    add: () => setState((s) => ({ keys: [...s.keys, s.next], next: s.next + 1 })),
    remove: (index) =>
      setState((s) => ({ keys: s.keys.filter((_, i) => i !== index), next: s.next })),
  };
}

/** The warning of a literal credential at a field. */
export function LiteralWarning({ id }: { id: string }) {
  const { t } = useTranslation();
  return (
    <p id={id} className="flex items-start gap-1.5 text-sm" data-testid="literal-credential">
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <span>{t("destinations.webhook.literalCredential")}</span>
    </p>
  );
}

/** The problem of a list as a whole, such as too many headers. */
export function ListProblem({
  id,
  pointer,
  notes,
}: {
  id: string;
  pointer: string;
  notes: FieldNotes;
}) {
  const { t } = useTranslation();
  const problem = notes.problem(pointer);
  // A refused save focuses the first marked field; a problem of a whole list takes the focus itself.
  return problem === undefined ? null : (
    <p
      id={id}
      tabIndex={-1}
      data-focus-problem="true"
      className="text-sm wrap-anywhere text-destructive outline-none"
      data-testid={`${id}-text`}
    >
      {problemText(t, pointer, problem, "")}
    </p>
  );
}

export interface TemplateInputProps {
  id: string;
  /** The visible label; without it, the accessible name. */
  label: string;
  hideLabel?: boolean;
  value: string;
  onChange: (value: string) => void;
  /** The JSON pointer of the field in the request body. */
  pointer: string;
  notes: FieldNotes;
  hint?: string;
  placeholder?: string;
  disabled?: boolean;
}

/** A one-line template, such as a URL or a header value, with its problem and the literal-credential warning. */
export function TemplateInput({
  id,
  label,
  hideLabel = false,
  value,
  onChange,
  pointer,
  notes,
  hint,
  placeholder,
  disabled = false,
}: TemplateInputProps) {
  const { t } = useTranslation();
  const problem = notes.problem(pointer);
  const literal = notes.literal(pointer);
  const described = [
    hint === undefined ? undefined : `${id}-hint`,
    problem === undefined ? undefined : `${id}-error`,
    literal ? `${id}-literal` : undefined,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {!hideLabel && <Label htmlFor={id}>{label}</Label>}
      <Input
        id={id}
        className="font-mono"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        aria-label={hideLabel ? label : undefined}
        aria-invalid={problem !== undefined}
        aria-describedby={described === "" ? undefined : described}
        onChange={(e) => onChange(e.target.value)}
      />
      {hint !== undefined && (
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {hint}
        </p>
      )}
      {problem !== undefined && (
        <p
          id={`${id}-error`}
          className="text-sm wrap-anywhere text-destructive"
          data-testid={`${id}-error-text`}
        >
          {problemText(t, pointer, problem, value, notes.unreadable(pointer, value))}
        </p>
      )}
      {literal && <LiteralWarning id={`${id}-literal`} />}
    </div>
  );
}

export interface HeaderEditorProps {
  id: string;
  /** The JSON pointer of the headers, such as /events/headers. */
  base: string;
  value: HeaderTemplate[];
  onChange: (value: HeaderTemplate[]) => void;
  notes: FieldNotes;
  disabled?: boolean;
}

export function HeaderEditor({
  id,
  base,
  value,
  onChange,
  notes,
  disabled = false,
}: HeaderEditorProps) {
  const { t } = useTranslation();
  const rows = useRowKeys(value.length);
  const set = (index: number, patch: Partial<HeaderTemplate>) =>
    onChange(value.map((h, i) => (i === index ? { ...h, ...patch } : h)));
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" data-testid={`${id}-headers`}>
      <legend className="mb-2 text-sm font-medium">{t("destinations.webhook.headers")}</legend>
      {value.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("destinations.webhook.noHeaders")}</p>
      )}
      {value.map((header, index) => {
        const number = index + 1;
        const at = `${base}/${index}`;
        const nameId = `${id}-header-${index}-name`;
        const nameProblem = notes.problem(`${at}/name`);
        return (
          <div
            key={rows.keys[index] ?? index}
            className="grid min-w-0 gap-2 rounded-lg border p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto] sm:border-0 sm:p-0"
            data-testid="header-row"
          >
            <div className="flex min-w-0 flex-col gap-2">
              <Input
                id={nameId}
                className="font-mono"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                placeholder="Authorization"
                value={header.name}
                disabled={disabled}
                aria-label={t("destinations.webhook.headerName", { number })}
                aria-invalid={nameProblem !== undefined}
                aria-describedby={nameProblem === undefined ? undefined : `${nameId}-error`}
                onChange={(e) => set(index, { name: e.target.value })}
              />
              {nameProblem !== undefined && (
                <p
                  id={`${nameId}-error`}
                  className="text-sm wrap-anywhere text-destructive"
                  data-testid={`${nameId}-error-text`}
                >
                  {problemText(t, `${at}/name`, nameProblem, header.name)}
                </p>
              )}
            </div>
            <TemplateInput
              id={`${id}-header-${index}-value`}
              label={t("destinations.webhook.headerValue", { number })}
              hideLabel
              placeholder={`Bearer ${reference("Secrets", "token")}`}
              value={header.value}
              pointer={`${at}/value`}
              notes={notes}
              disabled={disabled}
              onChange={(next) => set(index, { value: next })}
            />
            {!disabled && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="justify-self-end"
                aria-label={t("destinations.webhook.removeHeader", { number })}
                onClick={() => {
                  rows.remove(index);
                  onChange(value.filter((_, i) => i !== index));
                }}
              >
                <XIcon aria-hidden="true" />
              </Button>
            )}
          </div>
        );
      })}
      <ListProblem id={`${id}-headers-error`} pointer={base} notes={notes} />
      {!disabled && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="w-fit"
          onClick={() => {
            rows.add();
            onChange([...value, { name: "", value: "" }]);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("destinations.webhook.addHeader")}
        </Button>
      )}
    </fieldset>
  );
}
