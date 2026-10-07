// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Matcher builder of the Route editor (C-08.FR-2): rows of a label name, an operator (=, !=, =~, !~) and a value,
// combined with AND, "Add matcher" and a remove button per row. The form owns the rows; the builder shows them with
// the error of each field. A Problem of the server names a field by its pointer, such as /matchers/1/value for a
// regular expression that does not compile, and lands under that field of the row it was sent from.

import type { TFunction } from "i18next";
import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import { type Matcher, MatcherOp } from "../api/gen/model";
import { fieldErrorText, isApiError } from "../lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** A row of the builder; key tells the rows apart while they are edited. */
export interface MatcherRow {
  key: number;
  label: string;
  op: MatcherOp;
  value: string;
}

/** The field of a row that an error is about. */
export type MatcherField = "label" | "op" | "value";

/** An error of one field of a row: a code of the server or of the form, and the server's explanation. */
export interface MatcherFieldError {
  code: string;
  detail?: string;
}

/** The errors of a row, by field. */
export type MatcherRowErrors = Partial<Record<MatcherField, MatcherFieldError>>;

const OPS: readonly MatcherOp[] = Object.values(MatcherOp);

let nextKey = 0;

/** A new row; the keys only need to differ within one page. */
export function matcherRow(label = "", op: MatcherOp = "=", value = ""): MatcherRow {
  nextKey += 1;
  return { key: nextKey, label, op, value };
}

/** The rows of a Route's Matchers. */
export function matcherRows(matchers: readonly Matcher[]): MatcherRow[] {
  return matchers.map((m) => matcherRow(m.label, m.op, m.value));
}

/** Whether a row was left empty: it is not sent. */
export function isEmptyRow(row: MatcherRow): boolean {
  return row.label.trim() === "" && row.value === "";
}

/** The rows that are sent, in order: every row that is not empty. The index of each is the index of its pointer. */
export function sentRows(rows: readonly MatcherRow[]): MatcherRow[] {
  return rows.filter((r) => !isEmptyRow(r));
}

/** The Matchers of the rows, as the API takes them. */
export function matchersOf(rows: readonly MatcherRow[]): Matcher[] {
  return sentRows(rows).map((r) => ({ label: r.label.trim(), op: r.op, value: r.value }));
}

/** Matcher in Alertmanager syntax, as the Routes list shows it: label, operator and the quoted value. */
export function matcherText(m: Matcher): string {
  return `${m.label}${m.op}${JSON.stringify(m.value)}`;
}

const POINTER = /^\/matchers\/(\d+)\/(label|op|value)$/;

/**
 * The errors of a refusal about the Matchers, by the key of the row each was sent from; sent is the list of rows the
 * request carried, in order. Errors that name no row of the list are left out.
 */
export function matcherErrors(
  err: unknown,
  sent: readonly MatcherRow[],
): Map<number, MatcherRowErrors> {
  const out = new Map<number, MatcherRowErrors>();
  if (!isApiError(err)) {
    return out;
  }
  for (const item of err.errors ?? []) {
    const match = POINTER.exec(item.pointer);
    const row = match ? sent[Number(match[1])] : undefined;
    const field = match?.[2];
    if (row === undefined || (field !== "label" && field !== "op" && field !== "value")) {
      continue;
    }
    out.set(row.key, {
      ...out.get(row.key),
      [field]: { code: item.code, detail: item.detail },
    });
  }
  return out;
}

/** Whether a pointer of a Problem names a field of a Matcher. */
export function isMatcherPointer(pointer: string): boolean {
  return POINTER.test(pointer);
}

/** The text of an error of a Matcher field. */
export function matcherErrorText(
  t: TFunction,
  error: MatcherFieldError,
  field: MatcherField = "value",
): string {
  if (field === "label" && (error.code === "required" || error.code === "invalid_format")) {
    // The server's invalid_format on a label is an empty name.
    return t("routes.matchers.labelRequired");
  }
  switch (error.code) {
    case "invalid_regex":
      return error.detail
        ? t("matchers.errors.invalidRegexDetail", { detail: error.detail })
        : t("matchers.errors.invalidRegex");
    default:
      return fieldErrorText(t, error.code);
  }
}

export function MatcherBuilder({
  id,
  rows,
  onChange,
  errors,
  readOnly = false,
}: {
  id: string;
  rows: readonly MatcherRow[];
  onChange: (rows: MatcherRow[]) => void;
  /** The errors of each row, by its key. */
  errors: ReadonlyMap<number, MatcherRowErrors>;
  readOnly?: boolean;
}) {
  const { t } = useTranslation();
  const list = useRef<HTMLOListElement>(null);
  // The label field of a row just added takes the focus once the row is shown; removing a row moves it to the next
  // row, or to "Add matcher" after the last one.
  const focusKey = useRef<number | "add" | null>(null);
  const add = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (focusKey.current === "add") {
      add.current?.focus();
    } else if (focusKey.current !== null) {
      list.current?.querySelector<HTMLInputElement>(`[data-row="${focusKey.current}"]`)?.focus();
    }
    focusKey.current = null;
  });
  const update = (key: number, patch: Partial<MatcherRow>) =>
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  if (readOnly) {
    return rows.length === 0 ? (
      <p className="text-sm text-muted-foreground">{t("routes.matchers.none")}</p>
    ) : (
      <ul className="flex flex-wrap gap-1.5" aria-label={t("routes.fields.matchers")}>
        {rows.map((row) => (
          <li
            key={row.key}
            className="max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere"
            data-testid="matcher-text"
          >
            {matcherText(row)}
          </li>
        ))}
      </ul>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      {rows.length > 0 && (
        <ol ref={list} className="flex flex-col gap-3 sm:gap-2">
          {rows.map((row, index) => {
            const rowErrors = errors.get(row.key) ?? {};
            const number = index + 1;
            const fieldId = (field: MatcherField) => `${id}-${row.key}-${field}`;
            const errorId = (field: MatcherField) => `${fieldId(field)}-error`;
            const messages = (["label", "op", "value"] as const).flatMap((field) => {
              const error = rowErrors[field];
              return error === undefined
                ? []
                : [{ field, text: matcherErrorText(t, error, field) }];
            });
            return (
              <li key={row.key} className="flex flex-col gap-1" data-testid="matcher-row">
                {/* One line from sm up; on a phone the label and the remove button, then the operator and the value. */}
                <div className="grid grid-cols-[5rem_minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[minmax(0,1fr)_5rem_minmax(0,1fr)_auto]">
                  <Input
                    id={fieldId("label")}
                    data-row={row.key}
                    aria-label={t("routes.matchers.labelRow", { row: number })}
                    placeholder={t("routes.matchers.labelPlaceholder")}
                    value={row.label}
                    spellCheck={false}
                    autoComplete="off"
                    className="col-span-2 font-mono sm:col-span-1"
                    aria-invalid={rowErrors.label !== undefined}
                    aria-describedby={rowErrors.label ? errorId("label") : undefined}
                    onChange={(e) => update(row.key, { label: e.target.value })}
                  />
                  <NativeSelect
                    id={fieldId("op")}
                    className="col-start-1 row-start-2 w-full font-mono sm:col-start-2 sm:row-start-1"
                    aria-label={t("routes.matchers.opRow", { row: number })}
                    aria-invalid={rowErrors.op !== undefined}
                    aria-describedby={rowErrors.op ? errorId("op") : undefined}
                    value={row.op}
                    onChange={(e) => {
                      const op = OPS.find((o) => o === e.target.value);
                      if (op !== undefined) {
                        update(row.key, { op });
                      }
                    }}
                  >
                    {OPS.map((op) => (
                      <NativeSelectOption key={op} value={op}>
                        {op}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <Input
                    id={fieldId("value")}
                    aria-label={t("routes.matchers.valueRow", { row: number })}
                    placeholder={
                      row.op === "=~" || row.op === "!~"
                        ? t("routes.matchers.regexPlaceholder")
                        : t("routes.matchers.valuePlaceholder")
                    }
                    value={row.value}
                    spellCheck={false}
                    autoComplete="off"
                    className="col-span-2 col-start-2 row-start-2 font-mono sm:col-span-1 sm:col-start-3 sm:row-start-1"
                    aria-invalid={rowErrors.value !== undefined}
                    aria-describedby={rowErrors.value ? errorId("value") : undefined}
                    onChange={(e) => update(row.key, { value: e.target.value })}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-3 row-start-1 sm:col-start-4"
                    aria-label={
                      row.label.trim() === ""
                        ? t("routes.matchers.removeRow", { row: number })
                        : t("routes.matchers.remove", { label: row.label.trim() })
                    }
                    onClick={() => {
                      const next = rows[index + 1];
                      focusKey.current = next === undefined ? "add" : next.key;
                      onChange(rows.filter((r) => r.key !== row.key));
                    }}
                  >
                    <XIcon aria-hidden="true" />
                  </Button>
                </div>
                {messages.map(({ field, text }) => (
                  <p
                    key={field}
                    id={errorId(field)}
                    className="text-sm break-words text-destructive"
                    data-testid={`matcher-${field}-error`}
                  >
                    {text}
                  </p>
                ))}
              </li>
            );
          })}
        </ol>
      )}
      <div>
        <Button
          ref={add}
          type="button"
          variant="outline"
          size="sm"
          onClick={() => {
            const row = matcherRow();
            focusKey.current = row.key;
            onChange([...rows, row]);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("matchers.add")}
        </Button>
      </div>
    </div>
  );
}
