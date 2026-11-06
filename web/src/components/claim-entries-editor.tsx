// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The entries of a claim and a group, shared by the Team page's "Identity provider groups" (C-25.FR-4) and the Role
// mapping of the OIDC settings (C-03.FR-5): a row per entry with the claim it reads and the group it matches, and for
// the Role mapping the Role. The claim is a combobox over the `claims_supported` of the last connection check with the
// groups claim first; any claim may be typed, because the list is optional (C-03.FR-6). An empty claim reads the groups
// claim of the OIDC settings. A refusal of the server shows at the claim or the group of the entry it names.

import { PlusIcon, Trash2Icon } from "lucide-react";
import { useEffect, useId, useRef } from "react";

import type { RoleName } from "../api/gen/model";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { ROLES, roleLabel } from "./user-create-dialog";
import { useTranslation } from "react-i18next";

/** A row of the editor; key keeps a row's inputs in place when another row is removed. */
export interface ClaimEntryRow {
  key: string;
  /** The claim the entry reads, as loaded or typed; empty or undefined reads the groups claim. */
  claim?: string;
  group: string;
  /** The Role of the Role mapping; the Team's entries have none. */
  role?: RoleName;
}

let nextKey = 0;

export function newEntryKey(): string {
  nextKey += 1;
  return `entry-${nextKey}`;
}

/** The refusals of one entry, already translated. */
export interface EntryErrors {
  claim?: string;
  group?: string;
}

/** The texts of an editor, which its page words in its own terms. */
export interface ClaimEntriesTexts {
  legend: string;
  hint: string;
  empty: string;
  add: string;
  claim: (number: number) => string;
  group: (number: number) => string;
  role: (number: number) => string;
  remove: (group: string, number: number) => string;
}

export interface ClaimEntriesEditorProps {
  value: readonly ClaimEntryRow[];
  onChange: (rows: ClaimEntryRow[]) => void;
  texts: ClaimEntriesTexts;
  /** The claims the identity provider's discovery listed at the last connection check. */
  claims: readonly string[];
  /** The groups claim of the OIDC settings, offered first and read by an entry without a claim. */
  groupsClaim: string;
  /** Whether each entry also picks a Role: the Role mapping. */
  withRole?: boolean;
  /** Refusals by row index. */
  errors?: ReadonlyMap<number, EntryErrors>;
  disabled?: boolean;
  /** Keeps the legend for screen readers only, where a heading above the editor already says it. */
  hideLegend?: boolean;
  /** The test ids of the editor, of its list of rows and of its empty state; the last two follow the first. */
  testId?: string;
  rowsTestId?: string;
  emptyTestId?: string;
}

/** The claims a combobox offers: the groups claim first, then the others as discovery listed them. */
export function offeredClaims(claims: readonly string[], groupsClaim: string): string[] {
  const rest = claims.filter((c, i) => c !== groupsClaim && claims.indexOf(c) === i);
  return groupsClaim === "" ? rest : [groupsClaim, ...rest];
}

export function ClaimEntriesEditor({
  value,
  onChange,
  texts,
  claims,
  groupsClaim,
  withRole = false,
  errors,
  disabled = false,
  hideLegend = false,
  testId = "claim-entries",
  rowsTestId = `${testId}-rows`,
  emptyTestId = `${testId}-empty`,
}: ClaimEntriesEditorProps) {
  const base = useId();
  const listId = `${base}-claims`;
  const update = (index: number, patch: Partial<ClaimEntryRow>) =>
    onChange(value.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  // Removing a row hands the focus to the next one (or "Add"); adding one focuses its claim.
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
    const row: ClaimEntryRow = withRole
      ? { key: newEntryKey(), group: "", role: "viewer" }
      : { key: newEntryKey(), group: "" };
    focusNext.current = row.key;
    onChange([...value, row]);
  };
  return (
    <fieldset className="flex min-w-0 flex-col gap-3" disabled={disabled} data-testid={testId}>
      <legend className={hideLegend ? "sr-only" : "mb-1 text-sm font-medium"}>
        {texts.legend}
      </legend>
      <p className="text-sm text-muted-foreground">{texts.hint}</p>
      <datalist id={listId}>
        {offeredClaims(claims, groupsClaim).map((claim) => (
          <option key={claim} value={claim} />
        ))}
      </datalist>
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid={emptyTestId}>
          {texts.empty}
        </p>
      ) : (
        <ul className="flex flex-col gap-4" data-testid={rowsTestId}>
          {value.map((row, index) => {
            const number = index + 1;
            const error = errors?.get(index);
            const claimId = `${base}-${row.key}-claim`;
            const groupId = `${base}-${row.key}-group`;
            const roleId = `${base}-${row.key}-role`;
            return (
              <li
                key={row.key}
                className={
                  withRole
                    ? "grid grid-cols-[1fr_auto] items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)_9rem_auto]"
                    : "grid grid-cols-[1fr_auto] items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)_auto]"
                }
              >
                <div className="col-span-2 flex min-w-0 flex-col gap-1.5 sm:col-span-1">
                  <Label htmlFor={claimId}>{texts.claim(number)}</Label>
                  <Input
                    id={claimId}
                    ref={(el: HTMLInputElement | null) => {
                      if (el === null) {
                        inputs.current.delete(row.key);
                      } else {
                        inputs.current.set(row.key, el);
                      }
                    }}
                    list={listId}
                    value={row.claim ?? ""}
                    placeholder={groupsClaim}
                    spellCheck={false}
                    autoComplete="off"
                    aria-invalid={error?.claim !== undefined}
                    aria-describedby={error?.claim === undefined ? undefined : `${claimId}-error`}
                    onChange={(e) => update(index, { claim: e.target.value })}
                  />
                  {error?.claim !== undefined && (
                    <p id={`${claimId}-error`} className="text-sm text-destructive">
                      {error.claim}
                    </p>
                  )}
                </div>
                <div className="col-span-2 flex min-w-0 flex-col gap-1.5 sm:col-span-1">
                  <Label htmlFor={groupId}>{texts.group(number)}</Label>
                  <Input
                    id={groupId}
                    value={row.group}
                    spellCheck={false}
                    autoComplete="off"
                    aria-invalid={error?.group !== undefined}
                    aria-describedby={error?.group === undefined ? undefined : `${groupId}-error`}
                    onChange={(e) => update(index, { group: e.target.value })}
                  />
                  {error?.group !== undefined && (
                    <p id={`${groupId}-error`} className="text-sm text-destructive">
                      {error.group}
                    </p>
                  )}
                </div>
                {withRole && (
                  <RoleSelect
                    id={roleId}
                    label={texts.role(number)}
                    value={row.role ?? "viewer"}
                    onChange={(role) => update(index, { role })}
                  />
                )}
                <Button
                  variant="ghost"
                  size="icon"
                  className="col-start-2 mt-6 justify-self-end sm:col-start-auto"
                  aria-label={texts.remove(row.group.trim() || String(number), number)}
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
          {texts.add}
        </Button>
      </div>
    </fieldset>
  );
}

function RoleSelect({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: RoleName;
  onChange: (role: RoleName) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <NativeSelect
        id={id}
        className="w-full"
        value={value}
        onChange={(e) => {
          const role = ROLES.find((r) => r === e.target.value);
          if (role !== undefined) {
            onChange(role);
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
  );
}
