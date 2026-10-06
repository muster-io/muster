// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Static labels of an Integration (C-05.FR-1, FR-6): rows of a label name and a value, "Add label" and a remove
// button per row. The form owns the rows; the editor shows them and the error of each row.

import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "./ui/button";
import { Input } from "./ui/input";

/** A row of the editor; key tells the rows apart while they are edited. */
export interface LabelRow {
  key: number;
  name: string;
  value: string;
}

/** A label name as Alertmanager and the API take it. */
export const LABEL_NAME = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

let nextKey = 0;

/** A new row; the keys only need to differ within one page. */
export function labelRow(name = "", value = ""): LabelRow {
  nextKey += 1;
  return { key: nextKey, name, value };
}

/** The rows of a label map, sorted by name. */
export function labelRows(labels: Record<string, string>): LabelRow[] {
  return Object.keys(labels)
    .toSorted()
    .map((name) => labelRow(name, labels[name] ?? ""));
}

/** The label map of the rows; a row with neither a name nor a value is left out. */
export function labelsOf(rows: readonly LabelRow[]): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (name !== "" || row.value !== "") {
      labels[name] = row.value;
    }
  }
  return labels;
}

export function LabelsEditor({
  id,
  rows,
  onChange,
  errors,
}: {
  id: string;
  rows: readonly LabelRow[];
  onChange: (rows: LabelRow[]) => void;
  /** The error text of each row, by its position. */
  errors: readonly (string | undefined)[];
}) {
  const { t } = useTranslation();
  const list = useRef<HTMLUListElement>(null);
  // The name field of a row just added takes the focus once the row is shown.
  const focusKey = useRef<number | null>(null);
  useEffect(() => {
    if (focusKey.current !== null) {
      list.current?.querySelector<HTMLInputElement>(`[data-row="${focusKey.current}"]`)?.focus();
      focusKey.current = null;
    }
  });
  const update = (key: number, patch: Partial<LabelRow>) =>
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  return (
    <div className="flex flex-col gap-2">
      {rows.length > 0 && (
        <ul ref={list} className="flex flex-col gap-2">
          {rows.map((row, index) => {
            const error = errors[index];
            const errorId = `${id}-${row.key}-error`;
            return (
              <li key={row.key} className="flex flex-col gap-1" data-testid="label-row">
                <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2">
                  <Input
                    data-row={row.key}
                    aria-label={t("labels.nameRow", { row: index + 1 })}
                    placeholder={t("labels.namePlaceholder")}
                    value={row.name}
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono"
                    aria-invalid={error !== undefined}
                    aria-describedby={error ? errorId : undefined}
                    onChange={(e) => update(row.key, { name: e.target.value })}
                  />
                  <Input
                    aria-label={t("labels.valueRow", { row: index + 1 })}
                    placeholder={t("labels.valuePlaceholder")}
                    value={row.value}
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono"
                    onChange={(e) => update(row.key, { value: e.target.value })}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={
                      row.name.trim() === ""
                        ? t("labels.removeEmpty")
                        : t("labels.remove", { name: row.name.trim() })
                    }
                    onClick={() => onChange(rows.filter((r) => r.key !== row.key))}
                  >
                    <XIcon aria-hidden="true" />
                  </Button>
                </div>
                {error && (
                  <p id={errorId} className="text-sm text-destructive">
                    {error}
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => {
            const row = labelRow();
            focusKey.current = row.key;
            onChange([...rows, row]);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("labels.add")}
        </Button>
      </div>
    </div>
  );
}
