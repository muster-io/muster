// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Keyring status (C-20.FR-6, C-20.AC-2): every master key by its id, which one is active, the replicas that hold or
// lack it, and whether an older key is still needed or can be removed from MUSTER_SECRET_KEYS.

import { useTranslation } from "react-i18next";

import type { KeyringKey } from "../api/gen/model";

function KeyState({ keyring }: { keyring: KeyringKey }) {
  const { t } = useTranslation();
  switch (keyring.state) {
    case "active":
      return <span className="font-medium">{t("organization.keyring.active")}</span>;
    case "needed":
      return <span>{t("organization.keyring.stillNeeded")}</span>;
    case "not_active_yet":
      return <span>{t("organization.keyring.notActiveYet")}</span>;
    default:
      return <span>{t("organization.keyring.removable", { id: keyring.id })}</span>;
  }
}

export function KeyringStatus({ keys }: { keys: readonly KeyringKey[] }) {
  const { t } = useTranslation();
  const head = "px-3 py-2 font-medium text-muted-foreground";
  return (
    <div className="min-w-0 rounded-lg border">
      <table
        className="w-full table-fixed border-collapse text-left text-sm"
        aria-label={t("organization.keyring.keys")}
      >
        <thead className="bg-muted/50">
          <tr>
            <th scope="col" className={head}>
              {t("organization.keyring.id")}
            </th>
            <th scope="col" className={head}>
              {t("organization.keyring.state")}
            </th>
            <th scope="col" className={head}>
              {t("organization.keyring.replicas")}
            </th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => (
            <tr key={key.id} className="border-t align-top" data-testid="keyring-key">
              <td className="px-3 py-2 font-mono wrap-anywhere">{key.id}</td>
              <td className="px-3 py-2 wrap-anywhere" data-testid="keyring-key-state">
                <KeyState keyring={key} />
              </td>
              <td className="px-3 py-2 wrap-anywhere">
                <div className="flex flex-col gap-1">
                  {key.held_by.length > 0 && (
                    <span>
                      {t("organization.keyring.heldBy", { replicas: key.held_by.join(", ") })}
                    </span>
                  )}
                  {key.missing_on.length > 0 && (
                    <span className="text-destructive">
                      {t("organization.keyring.missingOn", { replicas: key.missing_on.join(", ") })}
                    </span>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
