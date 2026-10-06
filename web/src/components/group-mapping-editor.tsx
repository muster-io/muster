// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mapping from identity provider groups to Roles of the OIDC settings (C-03.FR-5): one row per group, added and
// removed freely; when a user's groups map to several Roles, the highest wins.

import { PlusIcon, Trash2Icon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { OidcGroupMapping, RoleName } from "../api/gen/model";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { ROLES, roleLabel } from "./user-create-dialog";

/** A row of the editor; key keeps a row's inputs in place when another row is removed. */
export interface MappingRow {
  key: string;
  group: string;
  role: RoleName;
}

let nextKey = 0;

function newKey(): string {
  nextKey += 1;
  return `mapping-${nextKey}`;
}

export function mappingRows(mappings: readonly OidcGroupMapping[]): MappingRow[] {
  return mappings.map((m) => ({ key: newKey(), group: m.group, role: m.role }));
}

export function mappingsOf(rows: readonly MappingRow[]): OidcGroupMapping[] {
  return rows.map((r) => ({ group: r.group.trim(), role: r.role }));
}

export interface GroupMappingEditorProps {
  value: readonly MappingRow[];
  onChange: (rows: MappingRow[]) => void;
  /** Field errors by row index, already translated. */
  errors?: ReadonlyMap<number, string>;
  disabled?: boolean;
}

export function GroupMappingEditor({
  value,
  onChange,
  errors,
  disabled = false,
}: GroupMappingEditorProps) {
  const { t } = useTranslation();
  const update = (index: number, patch: Partial<MappingRow>) =>
    onChange(value.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  // Removing a row hands the focus to the next one (or "Add"); adding one focuses its input.
  const inputs = useRef(new Map<string, HTMLInputElement>());
  const addButton = useRef<HTMLButtonElement>(null);
  const focusNext = useRef<string | null>(null);
  useEffect(() => {
    const key = focusNext.current;
    if (key === null) {
      return;
    }
    focusNext.current = null;
    (inputs.current.get(key) ?? addButton.current)?.focus();
  });
  const remove = (index: number) => {
    focusNext.current = value[index + 1]?.key ?? value[index - 1]?.key ?? "";
    onChange(value.filter((_, i) => i !== index));
  };
  const add = () => {
    const row: MappingRow = { key: newKey(), group: "", role: "viewer" };
    focusNext.current = row.key;
    onChange([...value, row]);
  };
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" disabled={disabled}>
      <legend className="mb-1 text-sm font-medium">{t("oidc.mapping.title")}</legend>
      <p className="text-sm text-muted-foreground">{t("oidc.mapping.hint")}</p>
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="group-mapping-empty">
          {t("oidc.mapping.empty")}
        </p>
      ) : (
        <ul className="flex flex-col gap-3" data-testid="group-mappings">
          {value.map((row, index) => {
            const error = errors?.get(index);
            const groupId = `mapping-${row.key}-group`;
            const roleId = `mapping-${row.key}-role`;
            return (
              <li
                key={row.key}
                className="grid grid-cols-[1fr_auto] items-end gap-2 sm:grid-cols-[1fr_12rem_auto]"
              >
                <div className="col-span-2 flex flex-col gap-1.5 sm:col-span-1">
                  <Label htmlFor={groupId}>{t("oidc.mapping.group", { number: index + 1 })}</Label>
                  <Input
                    id={groupId}
                    ref={(el: HTMLInputElement | null) => {
                      if (el === null) {
                        inputs.current.delete(row.key);
                      } else {
                        inputs.current.set(row.key, el);
                      }
                    }}
                    value={row.group}
                    spellCheck={false}
                    autoComplete="off"
                    aria-invalid={error !== undefined}
                    aria-describedby={error === undefined ? undefined : `${groupId}-error`}
                    onChange={(e) => update(index, { group: e.target.value })}
                  />
                  {error !== undefined && (
                    <p id={`${groupId}-error`} className="text-sm text-destructive">
                      {error}
                    </p>
                  )}
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={roleId}>{t("oidc.mapping.role", { number: index + 1 })}</Label>
                  <NativeSelect
                    id={roleId}
                    className="w-full"
                    value={row.role}
                    onChange={(e) => {
                      const role = ROLES.find((r) => r === e.target.value);
                      if (role !== undefined) {
                        update(index, { role });
                      }
                    }}
                  >
                    {ROLES.map((role) => (
                      <NativeSelectOption key={role} value={role}>
                        {roleLabel(t, role)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("oidc.mapping.remove", {
                    group: row.group.trim() || String(index + 1),
                  })}
                  onClick={() => remove(index)}
                >
                  <Trash2Icon aria-hidden="true" />
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      <div>
        <Button variant="outline" ref={addButton} onClick={add}>
          <PlusIcon aria-hidden="true" />
          {t("oidc.mapping.add")}
        </Button>
      </div>
    </fieldset>
  );
}
