// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Keyring (C-20.FR-6, C-20.AC-2): the master keys and their state, read-only. A key is activated with
// `muster secrets rotate-key`; the page follows the activation and the release of the old key through the organization
// hint.

import { createFileRoute } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";

import { useGetKeyring } from "../api/gen/endpoints/organization/organization";
import { RequirePermission } from "../components/app-shell";
import { KeyringStatus } from "../components/keyring-status";
import { PageHeading } from "../components/organization-form";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/keyring")({
  staticData: { shell: true },
  component: KeyringPage,
});

/** The activation command, which Trans puts into the explanation. */
function Command() {
  const { t } = useTranslation();
  return <code className="font-mono text-foreground">{t("organization.keyring.command")}</code>;
}

function KeyringView() {
  const { t } = useTranslation();
  const query = useGetKeyring();
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageHeading title={t("organization.nav.keyring")} hint={t("organization.keyring.hint")} />
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="flex max-w-3xl min-w-0 flex-col gap-4">
          <KeyringStatus keys={query.data.keys} />
          <p className="text-sm text-muted-foreground wrap-anywhere">
            {/* The command goes in as an element, never into the text that Trans parses. */}
            <Trans i18nKey="organization.keyring.explain" components={{ command: <Command /> }} />
          </p>
        </div>
      )}
    </div>
  );
}

function KeyringPage() {
  return (
    <RequirePermission permission="organization:read">
      <KeyringView />
    </RequirePermission>
  );
}
