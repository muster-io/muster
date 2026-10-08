// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The editor of a Lookup table (C-12.FR-9): its name, its description and a grid of rows — a key column and one
// column per named column. Columns and rows are added and removed; a save replaces the stored rows as a whole and
// sends the version the editor read as If-Match. Without lookup-tables:write the table only shows. A refusal lands on
// its field: a column, a key, a cell, or the row whose values do not match the columns (column_mismatch); a new name
// for a table that a Link rule reads is refused (409 in_use) with the rules its link_rules names, as is the deletion
// of such a table.
// The limits are those of lookup_table.size_max.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteLookupTable,
  getGetLookupTableQueryKey,
  getListLookupTablesQueryKey,
} from "../api/gen/endpoints/links/links";
import type { LookupTable, LookupTableBase } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
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

/** The limits of a Lookup table (lookup_table.size_max). */
export const LIMITS = {
  columns: 50,
  rows: 10_000,
  name: 200,
  description: 2_000,
  value: 4_096,
} as const;

/** A column of the grid; id tells the columns apart while they are edited. */
export interface GridColumn {
  id: number;
  name: string;
}

/** A row of the grid: its key and one value per column, in the order of the columns. */
export interface GridRow {
  id: number;
  key: string;
  values: string[];
}

export interface Grid {
  columns: GridColumn[];
  rows: GridRow[];
}

let nextId = 0;
function newId(): number {
  nextId += 1;
  return nextId;
}

/** The grid of a stored table, or of a new one with one empty column. */
export function gridOf(table: LookupTableBase | undefined): Grid {
  if (table === undefined) {
    return { columns: [{ id: newId(), name: "" }], rows: [] };
  }
  return {
    columns: table.columns.map((name) => ({ id: newId(), name })),
    rows: table.entries.map((e) => ({
      id: newId(),
      key: e.key,
      values: table.columns.map((c) => e.values[c] ?? ""),
    })),
  };
}

/** The grid with a column added at the end, every row given an empty value for it. */
export function addColumn(grid: Grid): Grid {
  return {
    columns: [...grid.columns, { id: newId(), name: "" }],
    rows: grid.rows.map((r) => ({ ...r, values: [...r.values, ""] })),
  };
}

/** The grid without a column and the values of its rows in it. */
export function removeColumn(grid: Grid, id: number): Grid {
  const index = grid.columns.findIndex((c) => c.id === id);
  if (index < 0) {
    return grid;
  }
  return {
    columns: grid.columns.filter((c) => c.id !== id),
    rows: grid.rows.map((r) => ({ ...r, values: r.values.filter((_, i) => i !== index) })),
  };
}

/** The grid with an empty row added at the end. */
export function addRow(grid: Grid): Grid {
  return {
    ...grid,
    rows: [...grid.rows, { id: newId(), key: "", values: grid.columns.map(() => "") }],
  };
}

/** The rows of the grid as the API takes them: the key and the value of every column by its name. */
export function entriesOf(grid: Grid): LookupTableBase["entries"] {
  return grid.rows.map((r) => ({
    key: r.key.trim(),
    values: Object.fromEntries(grid.columns.map((c, i) => [c.name.trim(), r.values[i] ?? ""])),
  }));
}

/** Where an error of the editor belongs. */
export type ErrorPlace =
  | "name"
  | "description"
  | "columns"
  | "entries"
  | `column:${number}`
  | `key:${number}`
  | `row:${number}`
  | `cell:${number}:${number}`;

/** The error codes of the editor by their place. */
export type GridErrors = Partial<Record<ErrorPlace, string>>;

/** The errors the editor finds itself before a save: empty and repeated names and keys, and the limits. */
export function checkGrid(name: string, grid: Grid): GridErrors {
  const errors: GridErrors = {};
  if (name.trim() === "") {
    errors.name = "required";
  }
  if (grid.columns.length === 0) {
    errors.columns = "columns_required";
  }
  const seenColumns = new Set<string>();
  for (const c of grid.columns) {
    const n = c.name.trim();
    if (n === "") {
      errors[`column:${c.id}`] = "required";
    } else if (seenColumns.has(n)) {
      errors[`column:${c.id}`] = "duplicate";
    }
    seenColumns.add(n);
  }
  const seenKeys = new Set<string>();
  for (const r of grid.rows) {
    const k = r.key.trim();
    if (k === "") {
      errors[`key:${r.id}`] = "required";
    } else if (seenKeys.has(k)) {
      errors[`key:${r.id}`] = "duplicate";
    }
    seenKeys.add(k);
  }
  return errors;
}

/** Undoes the escapes of a JSON pointer token. */
function unescapePointer(token: string): string {
  return token.replaceAll("~1", "/").replaceAll("~0", "~");
}

/** The errors of a refusal by their place, for the grid the request was sent with. */
export function serverErrors(err: unknown, sent: Grid): GridErrors {
  const errors: GridErrors = {};
  if (!isApiError(err)) {
    return errors;
  }
  for (const item of err.errors ?? []) {
    const parts = item.pointer.split("/").slice(1);
    const [field, index, sub, cell] = parts;
    if (field === "name" || field === "description") {
      errors[field] = item.code;
    } else if (field === "columns") {
      const column = index === undefined ? undefined : sent.columns[Number(index)];
      if (column === undefined) {
        errors.columns = item.code;
      } else {
        errors[`column:${column.id}`] = item.code;
      }
    } else if (field === "entries") {
      const row = index === undefined ? undefined : sent.rows[Number(index)];
      if (row === undefined) {
        errors.entries = item.code;
      } else if (sub === "key") {
        errors[`key:${row.id}`] = item.code;
      } else if (sub === "values" && cell !== undefined) {
        const name = unescapePointer(cell);
        const column = sent.columns.find((c) => c.name.trim() === name);
        if (column === undefined) {
          errors[`row:${row.id}`] = item.code;
        } else {
          errors[`cell:${row.id}:${column.id}`] = item.code;
        }
      } else {
        errors[`row:${row.id}`] = item.code;
      }
    } else {
      errors.entries = item.code;
    }
  }
  return errors;
}

/** The text of an error code of the editor. */
export function gridErrorText(
  t: TFunction,
  code: string,
  place: "column" | "key" | "other",
): string {
  switch (code) {
    case "duplicate":
      return place === "column"
        ? t("lookupTables.errors.duplicateColumn")
        : place === "key"
          ? t("lookupTables.errors.duplicateKey")
          : fieldErrorText(t, code);
    case "required":
    case "invalid_format":
      return place === "column"
        ? t("lookupTables.errors.columnRequired")
        : place === "key"
          ? t("lookupTables.errors.keyRequired")
          : fieldErrorText(t, "required");
    case "columns_required":
      return t("lookupTables.errors.columnsRequired");
    case "column_mismatch":
      return t("lookupTables.errors.columnMismatch");
    case "too_long":
      return place === "other"
        ? t("lookupTables.errors.tooLarge", { columns: LIMITS.columns, rows: LIMITS.rows })
        : fieldErrorText(t, code);
    default:
      return fieldErrorText(t, code);
  }
}

/** The text of an in_use refusal: the sentence of the action, and the rules that read the table, as the refusal
 * names them in link_rules. */
function InUseText({ error, action }: { error: unknown; action: "delete" | "rename" }) {
  const { t } = useTranslation();
  const readers = isApiError(error) ? (error.link_rules ?? []).map((r) => r.name) : [];
  return (
    <span data-testid="lookup-table-in-use">
      {action === "delete"
        ? t("lookupTables.errors.inUseDelete")
        : t("lookupTables.errors.inUseRename")}
      {readers.length > 0 && (
        <> {t("lookupTables.errors.readers", { rules: readers.join(", ") })}</>
      )}
    </span>
  );
}

function isInUse(err: unknown): boolean {
  return isApiError(err) && err.status === 409 && err.code === "in_use";
}

/** A save refused because the item changed since it was read (412), or sent without a version (428). */
function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

export interface LookupTableEditorProps {
  /** The stored table of an edit. */
  table?: LookupTable;
  readOnly?: boolean;
  submitLabel: string;
  save: (input: LookupTableBase) => Promise<unknown>;
  /** A newer version than the editor's was read while the editor had changes. */
  stale?: boolean;
  onReload?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  onCancel: () => void;
}

const ID = "lookup-table";

export function LookupTableEditor({
  table,
  readOnly = false,
  submitLabel,
  save,
  stale = false,
  onReload,
  onDirtyChange,
  onCancel,
}: LookupTableEditorProps) {
  const { t } = useTranslation();
  const [name, setName] = useState(table?.name ?? "");
  const [description, setDescription] = useState(table?.description ?? "");
  const [grid, setGrid] = useState(() => gridOf(table));
  const [errors, setErrors] = useState<GridErrors>({});
  const [dirty, setDirty] = useState(false);
  // The place that takes the focus once the grid shows it: the name of a new column, the key of a new row.
  const focus = useRef<string | null>(null);
  useEffect(() => {
    if (focus.current !== null) {
      document.getElementById(focus.current)?.focus();
      focus.current = null;
    }
  });
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const submit = useMutation({
    mutationFn: ({ input }: { input: LookupTableBase; sent: Grid }) => save(input),
    onMutate: () => setErrors({}),
    onError: (err, { sent }) => {
      if (isApiError(err) && err.status === 409 && err.code === "name_taken") {
        setErrors({ name: "name_taken" });
        document.getElementById(`${ID}-name`)?.focus();
        return;
      }
      const found = serverErrors(err, sent);
      setErrors(found);
      focusFirst(found);
    },
  });

  const change = (next: Grid) => {
    setGrid(next);
    setDirty(true);
  };
  const clear = (...places: ErrorPlace[]) => {
    if (places.some((p) => errors[p] !== undefined)) {
      setErrors((prev) => {
        const kept = { ...prev };
        for (const p of places) {
          delete kept[p];
        }
        return kept;
      });
    }
  };

  /** Drops the errors of whole rows, such as column_mismatch, which a change of the columns makes moot. */
  const clearRows = () => {
    setErrors((prev) =>
      Object.fromEntries(Object.entries(prev).filter(([place]) => !place.startsWith("row:"))),
    );
  };

  const focusFirst = (found: GridErrors) => {
    const first = Object.keys(found)[0];
    if (first === undefined) {
      return;
    }
    const [kind, a, b] = first.split(":");
    const target =
      kind === "column"
        ? `${ID}-column-${a}`
        : kind === "key" || kind === "row"
          ? `${ID}-key-${a}`
          : kind === "cell"
            ? `${ID}-cell-${a}-${b}`
            : kind === "columns"
              ? `${ID}-add-column`
              : kind === "entries"
                ? `${ID}-add-row`
                : `${ID}-${kind}`;
    document.getElementById(target)?.focus();
  };

  const errorText = (place: ErrorPlace, kind: "column" | "key" | "other" = "other") => {
    const code = errors[place];
    if (code === undefined) {
      return undefined;
    }
    if (place === "name" && code === "name_taken") {
      return t("lookupTables.errors.nameTaken");
    }
    return gridErrorText(t, code, kind);
  };

  if (readOnly) {
    return (
      <div className="flex flex-col gap-6" data-testid="lookup-table-read-only">
        <p className="text-sm text-muted-foreground">{t("lookupTables.readOnly")}</p>
        {table?.description ? (
          <p className="text-sm break-words whitespace-pre-wrap">{table.description}</p>
        ) : null}
        <div className="relative overflow-x-auto rounded-lg border">
          <table
            className="w-full border-collapse text-left text-sm"
            aria-label={t("lookupTables.grid.label")}
          >
            <thead className="bg-muted/50">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  {t("lookupTables.grid.key")}
                </th>
                {grid.columns.map((c) => (
                  <th key={c.id} scope="col" className="px-3 py-2 font-mono font-medium">
                    {c.name}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {grid.rows.length === 0 ? (
                <tr>
                  <td colSpan={grid.columns.length + 1} className="px-3 py-3 text-muted-foreground">
                    {t("lookupTables.grid.noRows")}
                  </td>
                </tr>
              ) : (
                grid.rows.map((r) => (
                  <tr key={r.id} className="border-t" data-testid="lookup-row">
                    <th scope="row" className="px-3 py-2 font-mono font-medium wrap-anywhere">
                      {r.key}
                    </th>
                    {r.values.map((v, i) => (
                      <td
                        key={grid.columns[i]?.id ?? i}
                        className="px-3 py-2 font-mono wrap-anywhere"
                      >
                        {v}
                      </td>
                    ))}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <div>
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("lookupTables.back")}
          </Button>
        </div>
      </div>
    );
  }

  const nameError = errorText("name");
  const descriptionError = errorText("description");
  const tableError = errorText("columns") ?? errorText("entries");
  const showStale = (stale && !submit.isPending) || isOutdated(submit.error);
  const fieldHandled =
    isApiError(submit.error) &&
    ((submit.error.status === 409 && submit.error.code === "name_taken") ||
      Boolean(submit.error.errors?.length));

  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={(e) => {
        e.preventDefault();
        const found = checkGrid(name, grid);
        if (Object.keys(found).length > 0) {
          submit.reset();
          setErrors(found);
          focusFirst(found);
          return;
        }
        const sent: Grid = { columns: [...grid.columns], rows: [...grid.rows] };
        submit.mutate({
          input: {
            name: name.trim(),
            description: description.trim(),
            columns: grid.columns.map((c) => c.name.trim()),
            entries: entriesOf(grid),
          },
          sent,
        });
      }}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${ID}-name`}>{t("lookupTables.fields.name")}</Label>
        <Input
          id={`${ID}-name`}
          value={name}
          maxLength={LIMITS.name}
          autoComplete="off"
          spellCheck={false}
          className="max-w-md font-mono"
          aria-invalid={nameError !== undefined}
          aria-describedby={`${ID}-name-hint${nameError === undefined ? "" : ` ${ID}-name-error`}`}
          onChange={(e) => {
            setName(e.target.value);
            setDirty(true);
            clear("name");
          }}
        />
        <p id={`${ID}-name-hint`} className="text-sm text-muted-foreground">
          {t("lookupTables.nameHint")}
        </p>
        {nameError !== undefined && (
          <p id={`${ID}-name-error`} className="text-sm text-destructive">
            {nameError}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${ID}-description`}>{t("lookupTables.fields.description")}</Label>
        <textarea
          id={`${ID}-description`}
          rows={2}
          value={description}
          maxLength={LIMITS.description}
          className="w-full max-w-md min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive dark:bg-input/30"
          aria-invalid={descriptionError !== undefined}
          aria-describedby={descriptionError === undefined ? undefined : `${ID}-description-error`}
          onChange={(e) => {
            setDescription(e.target.value);
            setDirty(true);
            clear("description");
          }}
        />
        {descriptionError !== undefined && (
          <p id={`${ID}-description-error`} className="text-sm text-destructive">
            {descriptionError}
          </p>
        )}
      </div>
      <fieldset
        className="flex min-w-0 flex-col gap-2"
        aria-describedby={`${ID}-grid-hint${tableError === undefined ? "" : ` ${ID}-grid-error`}`}
      >
        <legend className="mb-2 text-base font-semibold">{t("lookupTables.grid.title")}</legend>
        <p id={`${ID}-grid-hint`} className="text-sm text-muted-foreground">
          {t("lookupTables.grid.hint")}
        </p>
        {/* The grid scrolls inside its own box, so the page never scrolls sideways. */}
        <div className="relative overflow-x-auto rounded-lg border">
          <table
            className="border-collapse text-left text-sm"
            aria-label={t("lookupTables.grid.label")}
          >
            <thead className="bg-muted/50 align-top">
              <tr>
                <th scope="col" className="px-2 py-2 font-medium whitespace-nowrap">
                  {t("lookupTables.grid.key")}
                </th>
                {grid.columns.map((c, index) => {
                  const error = errorText(`column:${c.id}`, "column");
                  return (
                    <th key={c.id} scope="col" className="min-w-44 px-2 py-2 font-normal">
                      <div className="flex items-center gap-1">
                        <Input
                          id={`${ID}-column-${c.id}`}
                          value={c.name}
                          maxLength={LIMITS.name}
                          autoComplete="off"
                          spellCheck={false}
                          placeholder={t("lookupTables.grid.columnPlaceholder")}
                          className="font-mono"
                          aria-label={t("lookupTables.grid.columnName", { column: index + 1 })}
                          aria-invalid={error !== undefined}
                          aria-describedby={
                            error === undefined ? undefined : `${ID}-column-${c.id}-error`
                          }
                          onChange={(e) => {
                            change({
                              ...grid,
                              columns: grid.columns.map((col) =>
                                col.id === c.id ? { ...col, name: e.target.value } : col,
                              ),
                            });
                            clear(`column:${c.id}`, "columns");
                          }}
                        />
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          disabled={grid.columns.length <= 1}
                          aria-label={
                            c.name.trim() === ""
                              ? t("lookupTables.grid.removeColumnNumber", { column: index + 1 })
                              : t("lookupTables.grid.removeColumn", { column: c.name.trim() })
                          }
                          onClick={() => {
                            const next = grid.columns[index + 1] ?? grid.columns[index - 1];
                            focus.current = next === undefined ? null : `${ID}-column-${next.id}`;
                            change(removeColumn(grid, c.id));
                            clearRows();
                          }}
                        >
                          <XIcon aria-hidden="true" />
                        </Button>
                      </div>
                      {error !== undefined && (
                        <p
                          id={`${ID}-column-${c.id}-error`}
                          className="mt-1 text-xs font-normal text-destructive"
                        >
                          {error}
                        </p>
                      )}
                    </th>
                  );
                })}
                <th scope="col" className="w-10 px-2 py-2">
                  <span className="sr-only">{t("lookupTables.grid.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody className="align-top">
              {grid.rows.length === 0 && (
                <tr>
                  <td colSpan={grid.columns.length + 2} className="px-3 py-3 text-muted-foreground">
                    {t("lookupTables.grid.noRows")}
                  </td>
                </tr>
              )}
              {grid.rows.map((r, rowIndex) => {
                const keyError = errorText(`key:${r.id}`, "key");
                const rowError = errorText(`row:${r.id}`);
                const rowName = r.key.trim() === "" ? String(rowIndex + 1) : r.key.trim();
                return (
                  <tr
                    key={r.id}
                    className="border-t"
                    data-testid="lookup-row"
                    data-invalid={rowError === undefined ? undefined : "true"}
                  >
                    <td
                      className={
                        rowError === undefined ? "px-2 py-2" : "bg-destructive/10 px-2 py-2"
                      }
                    >
                      <Input
                        id={`${ID}-key-${r.id}`}
                        value={r.key}
                        maxLength={LIMITS.value}
                        autoComplete="off"
                        spellCheck={false}
                        className="min-w-32 font-mono"
                        aria-label={t("lookupTables.grid.keyOfRow", { row: rowIndex + 1 })}
                        aria-invalid={keyError !== undefined || rowError !== undefined}
                        aria-describedby={
                          [
                            keyError === undefined ? null : `${ID}-key-${r.id}-error`,
                            rowError === undefined ? null : `${ID}-row-${r.id}-error`,
                          ]
                            .filter((v) => v !== null)
                            .join(" ") || undefined
                        }
                        onChange={(e) => {
                          change({
                            ...grid,
                            rows: grid.rows.map((row) =>
                              row.id === r.id ? { ...row, key: e.target.value } : row,
                            ),
                          });
                          clear(`key:${r.id}`, `row:${r.id}`);
                        }}
                      />
                      {keyError !== undefined && (
                        <p id={`${ID}-key-${r.id}-error`} className="mt-1 text-xs text-destructive">
                          {keyError}
                        </p>
                      )}
                      {rowError !== undefined && (
                        <p id={`${ID}-row-${r.id}-error`} className="mt-1 text-xs text-destructive">
                          {rowError}
                        </p>
                      )}
                    </td>
                    {grid.columns.map((c, i) => {
                      const cellError = errorText(`cell:${r.id}:${c.id}`);
                      const column = c.name.trim() === "" ? String(i + 1) : c.name.trim();
                      return (
                        <td
                          key={c.id}
                          className={
                            rowError === undefined ? "px-2 py-2" : "bg-destructive/10 px-2 py-2"
                          }
                        >
                          <Input
                            id={`${ID}-cell-${r.id}-${c.id}`}
                            value={r.values[i] ?? ""}
                            maxLength={LIMITS.value}
                            autoComplete="off"
                            spellCheck={false}
                            className="font-mono"
                            aria-label={t("lookupTables.grid.cell", { column, row: rowName })}
                            aria-invalid={cellError !== undefined}
                            aria-describedby={
                              cellError === undefined
                                ? undefined
                                : `${ID}-cell-${r.id}-${c.id}-error`
                            }
                            onChange={(e) => {
                              change({
                                ...grid,
                                rows: grid.rows.map((row) =>
                                  row.id === r.id
                                    ? {
                                        ...row,
                                        values: row.values.map((v, j) =>
                                          j === i ? e.target.value : v,
                                        ),
                                      }
                                    : row,
                                ),
                              });
                              clear(`cell:${r.id}:${c.id}`, `row:${r.id}`);
                            }}
                          />
                          {cellError !== undefined && (
                            <p
                              id={`${ID}-cell-${r.id}-${c.id}-error`}
                              className="mt-1 text-xs text-destructive"
                            >
                              {cellError}
                            </p>
                          )}
                        </td>
                      );
                    })}
                    <td className="px-2 py-2">
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t("lookupTables.grid.removeRow", { row: rowName })}
                        onClick={() => {
                          const next = grid.rows[rowIndex + 1] ?? grid.rows[rowIndex - 1];
                          focus.current =
                            next === undefined ? `${ID}-add-row` : `${ID}-key-${next.id}`;
                          change({ ...grid, rows: grid.rows.filter((row) => row.id !== r.id) });
                          clear(`key:${r.id}`, `row:${r.id}`, "entries");
                        }}
                      >
                        <XIcon aria-hidden="true" />
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {tableError !== undefined && (
          <p id={`${ID}-grid-error`} className="text-sm text-destructive">
            {tableError}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            id={`${ID}-add-row`}
            type="button"
            variant="outline"
            size="sm"
            disabled={grid.rows.length >= LIMITS.rows}
            onClick={() => {
              const next = addRow(grid);
              const added = next.rows[next.rows.length - 1];
              focus.current = added === undefined ? null : `${ID}-key-${added.id}`;
              change(next);
            }}
          >
            <PlusIcon aria-hidden="true" />
            {t("lookupTables.grid.addRow")}
          </Button>
          <Button
            id={`${ID}-add-column`}
            type="button"
            variant="outline"
            size="sm"
            disabled={grid.columns.length >= LIMITS.columns}
            onClick={() => {
              const next = addColumn(grid);
              const added = next.columns[next.columns.length - 1];
              focus.current = added === undefined ? null : `${ID}-column-${added.id}`;
              change(next);
              clear("columns");
              clearRows();
            }}
          >
            <PlusIcon aria-hidden="true" />
            {t("lookupTables.grid.addColumn")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground" data-testid="lookup-row-count">
          {t("lookupTables.grid.rowCount", { count: grid.rows.length })}
        </p>
      </fieldset>
      {showStale && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>
              {isOutdated(submit.error)
                ? t("lookupTables.errors.stale")
                : t("lookupTables.errors.changedElsewhere")}
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
            {isInUse(submit.error) ? (
              <InUseText error={submit.error} action="rename" />
            ) : (
              problemText(t, submit.error)
            )}
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

/** "Delete" of a Lookup table: refused while a Link rule reads it, with the rules named. */
export function LookupTableDeleteDialog({ table }: { table: LookupTable }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteLookupTable(table.id, { headers: { "If-Match": table.etag ?? "" } }),
    onSuccess: async () => {
      await navigate({ to: "/admin/organization/lookup-tables" });
      queryClient.removeQueries({ queryKey: getGetLookupTableQueryKey(table.id) });
      void queryClient.invalidateQueries({ queryKey: getListLookupTablesQueryKey() });
    },
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({ queryKey: getGetLookupTableQueryKey(table.id) });
      }
    },
  });
  return (
    <>
      <Button
        variant="destructive"
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("lookupTables.delete.action")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("lookupTables.delete.title", { name: table.name })}
            </DialogTitle>
            <DialogDescription>{t("lookupTables.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                {isInUse(remove.error) ? (
                  <InUseText error={remove.error} action="delete" />
                ) : isStale(remove.error) ? (
                  <>
                    <span>{t("lookupTables.errors.stale")}</span>
                    {/* The page has read the newer version; the dialog closes to show it. */}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => setOpen(false)}
                    >
                      {t("common.reload")}
                    </Button>
                  </>
                ) : (
                  problemText(t, remove.error)
                )}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              variant="destructive"
              disabled={remove.isPending || isInUse(remove.error)}
              onClick={() => remove.mutate()}
            >
              {t("lookupTables.delete.action")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
