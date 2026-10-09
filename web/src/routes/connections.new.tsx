// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create connection" (C-13.FR-1, C-14.FR-1): the choice of the messenger, then its form; the new Connection's page
// opens after it is created, with "Check connection" and, for Mattermost, its callback address.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createConnection,
  getGetConnectionQueryKey,
  getListConnectionsQueryKey,
} from "../api/gen/endpoints/connections/connections";
import { RequirePermission } from "../components/app-shell";
import { ConnectionForm, type ConnectionType } from "../components/connection-form";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/connections/new")({
  staticData: { shell: true },
  component: NewConnectionPage,
});

const KINDS: readonly { kind: ConnectionType; name: string }[] = [
  { kind: "mattermost", name: "Mattermost" },
  { kind: "telegram", name: "Telegram" },
];

function NewConnection() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [kind, setKind] = useState<ConnectionType>();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/connections"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("connections.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("connections.create.title")}</h1>
      </div>
      <fieldset className="flex min-w-0 flex-col gap-3">
        <legend className="mb-2 text-sm font-medium">{t("connections.create.type")}</legend>
        <div className="flex flex-wrap gap-3">
          {KINDS.map((k) => (
            <label
              key={k.kind}
              className="flex cursor-pointer items-center gap-2 rounded-lg border px-4 py-3 text-sm font-medium has-checked:border-primary has-checked:bg-accent has-focus-visible:ring-2 has-focus-visible:ring-ring"
            >
              <input
                type="radio"
                name="connection-type"
                value={k.kind}
                className="size-4 accent-primary outline-none"
                checked={kind === k.kind}
                onChange={() => setKind(k.kind)}
              />
              {k.name}
            </label>
          ))}
        </div>
      </fieldset>
      {kind !== undefined && (
        <ConnectionForm
          // A form per messenger: switching starts from the other's defaults.
          key={kind}
          type={kind}
          submitLabel={t("common.save")}
          save={async (input) => {
            const created = await createConnection(input);
            queryClient.setQueryData(getGetConnectionQueryKey(created.id), created);
            void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
            await navigate({
              to: "/connections/$connectionId",
              params: { connectionId: created.id },
            });
            return undefined;
          }}
          onCancel={() => void navigate({ to: "/connections" })}
        />
      )}
    </div>
  );
}

function NewConnectionPage() {
  return (
    <RequirePermission permission="connections:write">
      <NewConnection />
    </RequirePermission>
  );
}
