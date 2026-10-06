// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Permissions of a new Personal access token (C-04.FR-1, FR-7): a checkbox for each Permission the user holds, and
// nothing else, so a token never has more than its owner. The API refuses any other with permission_not_held.

import { useTranslation } from "react-i18next";

import { Permission } from "../api/gen/model";
import { Button } from "./ui/button";

/** Every Permission in the order of the API specification, which groups them by resource. */
const ORDER: readonly Permission[] = Object.values(Permission);

/** The Permissions held, in the order of the specification. */
export function heldInOrder(held: readonly Permission[]): Permission[] {
  return ORDER.filter((p) => held.includes(p));
}

export function PermissionPicker({
  id,
  held,
  value,
  onChange,
  error,
}: {
  id: string;
  /** The Permissions the user holds now; only these are offered. */
  held: readonly Permission[];
  value: readonly Permission[];
  onChange: (permissions: Permission[]) => void;
  error?: string;
}) {
  const { t } = useTranslation();
  const offered = heldInOrder(held);
  const toggle = (permission: Permission, on: boolean) =>
    onChange(offered.filter((p) => (p === permission ? on : value.includes(p))));
  return (
    <fieldset
      className="flex min-w-0 flex-col gap-2"
      aria-describedby={[`${id}-hint`, error ? `${id}-error` : null].filter(Boolean).join(" ")}
    >
      <legend className="mb-2 text-sm leading-none font-medium">
        {t("tokens.fields.permissions")}
      </legend>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("tokens.permissions.hint")}
      </p>
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={value.length === offered.length}
          onClick={() => onChange(offered)}
        >
          {t("tokens.permissions.all")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={value.length === 0}
          onClick={() => onChange([])}
        >
          {t("tokens.permissions.none")}
        </Button>
      </div>
      <ul className="grid gap-x-4 gap-y-1.5 rounded-lg border p-3 sm:grid-cols-2" data-testid={id}>
        {offered.map((permission) => {
          const box = `${id}-${permission.replace(":", "-")}`;
          return (
            <li key={permission} className="flex min-w-0 items-center gap-2">
              <input
                id={box}
                type="checkbox"
                className="size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
                checked={value.includes(permission)}
                aria-invalid={error !== undefined}
                onChange={(e) => toggle(permission, e.target.checked)}
              />
              <label htmlFor={box} className="min-w-0 font-mono text-xs break-all">
                {permission}
              </label>
            </li>
          );
        })}
      </ul>
      {error && (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      )}
    </fieldset>
  );
}
