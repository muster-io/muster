// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Severity levels of the Organization (C-20.FR-1): the mapping of severity label values to critical, warning and
// info, where a value mapped twice is refused next to it, and the emoji and colour of each level.

import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { SeverityLevel, SeverityLevelStyle, SeverityMappingEntry } from "../api/gen/model";
import { useRowKeys } from "./header-editor";
import { severityLabel } from "./integration-alerts";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

export const SEVERITY_LEVELS: readonly SeverityLevel[] = ["critical", "warning", "info"];

/** The positions of the rows whose value an earlier row already maps; empty values are left to the required check. */
export function duplicateIndexes(rows: readonly SeverityMappingEntry[]): Set<number> {
  const seen = new Set<string>();
  const out = new Set<number>();
  rows.forEach((row, index) => {
    const value = row.value.trim();
    if (value === "") {
      return;
    }
    if (seen.has(value)) {
      out.add(index);
    }
    seen.add(value);
  });
  return out;
}

/** The worded errors of a mapping row. */
export interface MappingRowErrors {
  value?: string;
  level?: string;
}

export function SeverityMappingEditor({
  value,
  onChange,
  errors,
  focusLevelOf,
}: {
  value: readonly SeverityMappingEntry[];
  onChange: (value: SeverityMappingEntry[]) => void;
  errors: readonly (MappingRowErrors | undefined)[];
  /** A request to focus the level of the row at this position; a new request has a new seq. */
  focusLevelOf?: { index: number; seq: number };
}) {
  const { t } = useTranslation();
  const rows = useRowKeys(value.length);
  const list = useRef<HTMLUListElement>(null);
  const focusValue = useRef<number | null>(null);
  const duplicates = duplicateIndexes(value);
  useEffect(() => {
    if (focusValue.current !== null) {
      list.current
        ?.querySelector<HTMLInputElement>(`[data-value-index="${focusValue.current}"]`)
        ?.focus();
      focusValue.current = null;
    }
  });
  useEffect(() => {
    const focusIndex = focusLevelOf?.index;
    if (focusIndex !== undefined) {
      list.current?.querySelector<HTMLSelectElement>(`[data-level-index="${focusIndex}"]`)?.focus();
    }
  }, [focusLevelOf]);
  const update = (index: number, patch: Partial<SeverityMappingEntry>) =>
    onChange(value.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {value.length > 0 && (
        <ul ref={list} className="flex flex-col gap-2">
          {value.map((row, index) => {
            const key = rows.keys[index] ?? index;
            const valueError =
              errors[index]?.value ??
              (duplicates.has(index) ? t("organization.severity.mappingDuplicate") : undefined);
            const levelError = errors[index]?.level;
            const errorId = `severity-mapping-${key}-error`;
            const name = row.value.trim();
            return (
              <li
                key={key}
                className="flex min-w-0 flex-col gap-1"
                data-testid="severity-mapping-row"
              >
                <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,8rem)_auto] items-center gap-2">
                  <Input
                    data-value-index={index}
                    aria-label={t("organization.severity.valueOfRow", { row: index + 1 })}
                    placeholder={t("organization.severity.valuePlaceholder")}
                    value={row.value}
                    spellCheck={false}
                    autoComplete="off"
                    className="font-mono"
                    aria-invalid={valueError !== undefined}
                    aria-describedby={valueError === undefined ? undefined : errorId}
                    onChange={(e) => update(index, { value: e.target.value })}
                  />
                  <NativeSelect
                    data-level-index={index}
                    className="w-full"
                    aria-label={
                      name === ""
                        ? t("organization.severity.levelOfRow", { row: index + 1 })
                        : t("organization.severity.levelOf", { value: name })
                    }
                    value={row.level}
                    aria-invalid={levelError !== undefined}
                    onChange={(e) => {
                      const level = SEVERITY_LEVELS.find((l) => l === e.target.value);
                      if (level !== undefined) {
                        update(index, { level });
                      }
                    }}
                  >
                    {SEVERITY_LEVELS.map((level) => (
                      <NativeSelectOption key={level} value={level}>
                        {severityLabel(t, level)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={
                      name === ""
                        ? t("organization.severity.removeRow", { row: index + 1 })
                        : t("organization.severity.remove", { value: name })
                    }
                    onClick={() => {
                      rows.remove(index);
                      onChange(value.filter((_, i) => i !== index));
                    }}
                  >
                    <XIcon aria-hidden="true" />
                  </Button>
                </div>
                {(valueError ?? levelError) !== undefined && (
                  <p id={errorId} className="text-sm text-destructive">
                    {valueError ?? levelError}
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
            focusValue.current = value.length;
            rows.add();
            onChange([...value, { value: "", level: "warning" }]);
          }}
        >
          <PlusIcon aria-hidden="true" />
          {t("organization.severity.addValue")}
        </Button>
      </div>
    </div>
  );
}

/** The worded errors of a level's style. */
export interface StyleErrors {
  emoji?: string;
  color?: string;
}

/** The emoji and colour of each Severity level; the styles keep their positions, which the field errors name. */
export function SeverityStylesEditor({
  value,
  onChange,
  errors,
}: {
  value: readonly SeverityLevelStyle[];
  onChange: (value: SeverityLevelStyle[]) => void;
  errors: readonly (StyleErrors | undefined)[];
}) {
  const { t } = useTranslation();
  const update = (index: number, patch: Partial<SeverityLevelStyle>) =>
    onChange(value.map((s, i) => (i === index ? { ...s, ...patch } : s)));
  return (
    <ul className="flex flex-col gap-3">
      {SEVERITY_LEVELS.map((level) => {
        const index = value.findIndex((s) => s.level === level);
        const style = value[index];
        if (style === undefined) {
          return null;
        }
        const name = severityLabel(t, level);
        const error = errors[index]?.emoji ?? errors[index]?.color;
        const errorId = `severity-style-${level}-error`;
        const colour = style.color.toLowerCase();
        return (
          <li key={level} className="flex min-w-0 flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="w-16 text-sm font-medium">{name}</span>
              <Input
                aria-label={t("organization.severity.emojiOf", { level: name })}
                value={style.emoji}
                autoComplete="off"
                className="w-20"
                aria-invalid={errors[index]?.emoji !== undefined}
                aria-describedby={error === undefined ? undefined : errorId}
                onChange={(e) => update(index, { emoji: e.target.value })}
              />
              <input
                type="color"
                aria-label={t("organization.severity.colourOf", { level: name })}
                value={/^#[0-9a-f]{6}$/.test(colour) ? colour : "#000000"}
                className="h-8 w-12 shrink-0 cursor-pointer rounded-md border border-input bg-transparent p-0.5"
                aria-invalid={errors[index]?.color !== undefined}
                aria-describedby={error === undefined ? undefined : errorId}
                onChange={(e) => update(index, { color: e.target.value.toLowerCase() })}
              />
              <span className="font-mono text-sm text-muted-foreground">{colour}</span>
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
  );
}
