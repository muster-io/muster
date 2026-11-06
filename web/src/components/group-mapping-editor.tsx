// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mapping from identity provider groups to Roles of the OIDC settings (C-03.FR-5): one row per entry, added and
// removed freely; when a user's groups map to several Roles, the highest wins. Each entry reads its own claim, by
// default the groups claim: the claim editor it shares with the Teams (claim-entries-editor.tsx) offers the claims of
// the last connection check, and an entry of another claim is saved with it, never against the groups claim.

import { useTranslation } from "react-i18next";

import type { OidcGroupMapping, RoleName } from "../api/gen/model";
import {
  ClaimEntriesEditor,
  type ClaimEntryRow,
  type EntryErrors,
  newEntryKey,
} from "./claim-entries-editor";

/**
 * A row of the editor; key keeps a row's inputs in place when another row is removed. claim is the claim the entry
 * reads, kept as loaded; undefined or empty reads the groups claim.
 */
export interface MappingRow {
  key: string;
  claim?: string;
  group: string;
  role: RoleName;
}

export function mappingRows(mappings: readonly OidcGroupMapping[]): MappingRow[] {
  return mappings.map((m) => ({
    key: newEntryKey(),
    claim: m.claim,
    group: m.group,
    role: m.role,
  }));
}

export function mappingsOf(rows: readonly MappingRow[]): OidcGroupMapping[] {
  return rows.map((r) => {
    const entry: OidcGroupMapping = { group: r.group.trim(), role: r.role };
    const claim = r.claim?.trim();
    if (claim !== undefined && claim !== "") {
      entry.claim = claim;
    }
    return entry;
  });
}

export interface GroupMappingEditorProps {
  value: readonly MappingRow[];
  onChange: (rows: MappingRow[]) => void;
  /** Refusals by row index, already translated. */
  errors?: ReadonlyMap<number, EntryErrors>;
  /** The claims the last connection check listed in `claims_supported`. */
  claims?: readonly string[];
  /** The groups claim of the settings, which an entry without a claim reads. */
  groupsClaim: string;
  disabled?: boolean;
}

function asMapping(rows: readonly ClaimEntryRow[]): MappingRow[] {
  return rows.map((r) => ({
    key: r.key,
    ...(r.claim === undefined ? {} : { claim: r.claim }),
    group: r.group,
    role: r.role ?? "viewer",
  }));
}

export function GroupMappingEditor({
  value,
  onChange,
  errors,
  claims = [],
  groupsClaim,
  disabled = false,
}: GroupMappingEditorProps) {
  const { t } = useTranslation();
  return (
    <ClaimEntriesEditor
      value={value}
      onChange={(rows) => onChange(asMapping(rows))}
      claims={claims}
      groupsClaim={groupsClaim}
      withRole
      errors={errors}
      disabled={disabled}
      testId="group-mapping-editor"
      rowsTestId="group-mappings"
      emptyTestId="group-mapping-empty"
      texts={{
        legend: t("oidc.mapping.title"),
        hint: t("oidc.mapping.hint"),
        empty: t("oidc.mapping.empty"),
        add: t("oidc.mapping.add"),
        claim: (number) => t("oidc.mapping.claim", { number }),
        group: (number) => t("oidc.mapping.group", { number }),
        role: (number) => t("oidc.mapping.role", { number }),
        remove: (group) => t("oidc.mapping.remove", { group }),
      }}
    />
  );
}
