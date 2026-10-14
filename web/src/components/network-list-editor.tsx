// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A list of short text entries with add and remove, each with its own error: the allowed and denied entries of the
// outbound address policy (C-20.FR-3), and the Instance labels. A refusal at "/denied/1" shows next to the second
// entry of the denied list.

import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import { useRowKeys } from "./header-editor";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

export function NetworkListEditor({
  id,
  label,
  value,
  onChange,
  errors,
  addLabel,
  placeholder,
  hint,
  readOnly = false,
}: {
  id: string;
  label: string;
  value: readonly string[];
  onChange: (value: string[]) => void;
  /** The error text of each entry, by its position. */
  errors: readonly (string | undefined)[];
  addLabel: string;
  placeholder?: string;
  hint?: string;
  readOnly?: boolean;
}) {
  const { t } = useTranslation();
  const rows = useRowKeys(value.length);
  const list = useRef<HTMLUListElement>(null);
  // The entry just added takes the focus once it is shown.
  const focusIndex = useRef<number | null>(null);
  useEffect(() => {
    if (focusIndex.current !== null) {
      list.current
        ?.querySelector<HTMLInputElement>(`[data-index="${focusIndex.current}"]`)
        ?.focus();
      focusIndex.current = null;
    }
  });
  const hintId = hint === undefined ? undefined : `${id}-hint`;
  return (
    <fieldset className="flex min-w-0 flex-col gap-2" aria-describedby={hintId}>
      <legend className="mb-1 text-sm font-medium">{label}</legend>
      {hint !== undefined && (
        <p id={hintId} className="text-sm text-muted-foreground">
          {hint}
        </p>
      )}
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("organization.list.empty")}</p>
      ) : (
        <ul ref={list} className="flex flex-col gap-2">
          {value.map((entry, index) => {
            const key = rows.keys[index] ?? index;
            const error = errors[index];
            const errorId = `${id}-${key}-error`;
            return (
              <li key={key} className="flex min-w-0 flex-col gap-1">
                <div className="flex min-w-0 items-center gap-2">
                  <Input
                    data-index={index}
                    aria-label={t("organization.list.entry", { list: label, row: index + 1 })}
                    placeholder={placeholder}
                    value={entry}
                    readOnly={readOnly}
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono"
                    aria-invalid={error !== undefined}
                    aria-describedby={error === undefined ? undefined : errorId}
                    onChange={(e) =>
                      onChange(value.map((v, i) => (i === index ? e.target.value : v)))
                    }
                  />
                  {!readOnly && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t("organization.list.remove", { list: label, row: index + 1 })}
                      onClick={() => {
                        rows.remove(index);
                        onChange(value.filter((_, i) => i !== index));
                      }}
                    >
                      <XIcon aria-hidden="true" />
                    </Button>
                  )}
                </div>
                {error !== undefined && (
                  <p id={errorId} className="text-sm text-destructive">
                    {error}
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {!readOnly && (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              focusIndex.current = value.length;
              rows.add();
              onChange([...value, ""]);
            }}
          >
            <PlusIcon aria-hidden="true" />
            {addLabel}
          </Button>
        </div>
      )}
    </fieldset>
  );
}
