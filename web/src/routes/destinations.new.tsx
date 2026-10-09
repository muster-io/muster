// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create destination" (C-13.FR-9, C-14.FR-2): the choice of the type, then its form; the new Destination's page opens
// after it is saved. Mattermost and Telegram are here; the outgoing webhook (C-15) joins the choice with its own fields.

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
import { TELEGRAM_KIND } from "../components/telegram-destination-fields";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/destinations/new")({
  staticData: { shell: true },
  component: NewDestinationPage,
});

const TYPES: readonly DestinationType[] = ["mattermost", "telegram"];

function NewDestination() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [type, setType] = useState<DestinationType>();
  const save = async (input: DestinationInput): Promise<Destination | undefined> => {
    const created = await createDestination(input);
    queryClient.setQueryData(
      getGetDestinationQueryKey(created.destination.id),
      created.destination,
    );
    void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
    void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
    void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
    await navigate({
      to: "/destinations/$destinationId",
      params: { destinationId: created.destination.id },
    });
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
