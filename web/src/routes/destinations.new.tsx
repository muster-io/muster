// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create destination" (C-13.FR-9, C-14.FR-2, C-15.FR-1): the choice of the type — Mattermost, Telegram or outgoing
// webhook — then its form; the new Destination's page opens after it is saved. Creating an outgoing webhook first shows
// its Signing secret once (C-15.FR-5): the value is kept in this page's state only, never in the query cache, and the
// page of the Destination opens when the dialog closes.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { getListConnectionsQueryKey } from "../api/gen/endpoints/connections/connections";
import {
  createDestination,
  getGetDestinationQueryKey,
  getListDestinationsQueryKey,
} from "../api/gen/endpoints/destinations/destinations";
import { getListRouteSuggestionsQueryKey } from "../api/gen/endpoints/routes/routes";
import type { Destination, DestinationInput, DestinationType } from "../api/gen/model";
import { RequirePermission } from "../components/app-shell";
import { DestinationForm } from "../components/destination-form";
import { destinationTypeName } from "../components/destination-health";
import { MATTERMOST_KIND } from "../components/mattermost-destination-fields";
import { SigningSecretDialog } from "../components/signing-secret-dialog";
import { TELEGRAM_KIND } from "../components/telegram-destination-fields";
import { buttonVariants } from "../components/ui/button";
import { WEBHOOK_KIND } from "../components/webhook-destination-fields";

export const Route = createFileRoute("/destinations/new")({
  staticData: { shell: true },
  component: NewDestinationPage,
});

const TYPES: readonly DestinationType[] = ["mattermost", "telegram", "webhook"];

function NewDestination() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [type, setType] = useState<DestinationType>();
  // The Signing secret of a created outgoing webhook, until its dialog closes.
  const [signing, setSigning] = useState<{ destinationId: string; secret: string } | null>(null);
  const open = (destinationId: string) =>
    navigate({ to: "/destinations/$destinationId", params: { destinationId } });
  const save = async (input: DestinationInput): Promise<Destination | undefined> => {
    const created = await createDestination(input);
    queryClient.setQueryData(
      getGetDestinationQueryKey(created.destination.id),
      created.destination,
    );
    void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
    void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
    if (created.signing_secret) {
      setSigning({ destinationId: created.destination.id, secret: created.signing_secret });
    } else {
      await open(created.destination.id);
    }
    return undefined;
  };
  const cancel = () => void navigate({ to: "/destinations" });
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/destinations"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("destinations.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("destinations.create.title")}</h1>
      </div>
      <fieldset className="flex min-w-0 flex-col gap-3">
        <legend className="mb-2 text-sm font-medium">{t("destinations.create.type")}</legend>
        <div className="flex flex-wrap gap-3">
          {TYPES.map((k) => (
            <label
              key={k}
              className="flex cursor-pointer items-center gap-2 rounded-lg border px-4 py-3 text-sm font-medium has-checked:border-primary has-checked:bg-accent has-focus-visible:ring-2 has-focus-visible:ring-ring"
            >
              <input
                type="radio"
                name="destination-type"
                value={k}
                className="size-4 accent-primary outline-none"
                checked={type === k}
                onChange={() => setType(k)}
              />
              {destinationTypeName(t, k)}
            </label>
          ))}
        </div>
      </fieldset>
      {type === "mattermost" && (
        <DestinationForm
          kind={MATTERMOST_KIND}
          submitLabel={t("common.save")}
          save={save}
          onCancel={cancel}
        />
      )}
      {type === "telegram" && (
        <DestinationForm
          kind={TELEGRAM_KIND}
          submitLabel={t("common.save")}
          save={save}
          onCancel={cancel}
        />
      )}
      {type === "webhook" && (
        <DestinationForm
          kind={WEBHOOK_KIND}
          submitLabel={t("common.save")}
          save={save}
          onCancel={cancel}
        />
      )}
      <SigningSecretDialog
        secret={signing?.secret ?? null}
        onClose={() => {
          const id = signing?.destinationId;
          setSigning(null);
          if (id !== undefined) {
            void open(id);
          }
        }}
      />
    </div>
  );
}

function NewDestinationPage() {
  return (
    <RequirePermission permission="destinations:write">
      <NewDestination />
    </RequirePermission>
  );
}
