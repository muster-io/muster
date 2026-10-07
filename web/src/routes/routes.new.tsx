// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create route" (C-08.FR-7, FR-1): first the choice of a Route profile, then the editor pre-filled with its values.
// The new Route goes just before the Default route, and the Routes list opens after it is created.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createRoute,
  getGetRouteQueryKey,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
} from "../api/gen/endpoints/routes/routes";
import type { RouteInput, RouteProfile } from "../api/gen/model";
import { RequirePermission } from "../components/app-shell";
import { ProfilePicker, profileName } from "../components/profile-picker";
import { RouteForm } from "../components/route-form";
import { Button, buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/routes/new")({
  staticData: { shell: true },
  component: NewRoutePage,
});

/** A new Route with the values of a profile: no name, no Matchers, no Destinations. */
function fromProfile(profile: RouteProfile): RouteInput {
  return {
    name: "",
    description: "",
    matchers: [],
    urgent: profile.urgent,
    group_key: [...profile.group_key],
    destination_ids: [],
    policy: profile.policy,
  };
}

function NewRoute() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [profile, setProfile] = useState<RouteProfile>();
  const [changed, setChanged] = useState(false);
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to="/routes" className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("routes.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("routes.create.title")}</h1>
      </div>
      {profile === undefined ? (
        <ProfilePicker onPick={setProfile} autoFocus={changed} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 text-sm" data-testid="route-profile">
            <span>{t("routes.create.profile", { profile: profileName(t, profile.id) })}</span>
            <Button
              type="button"
              variant="link"
              size="sm"
              className="px-0"
              onClick={() => {
                setChanged(true);
                setProfile(undefined);
              }}
            >
              {t("routes.create.changeProfile")}
            </Button>
          </div>
          <RouteForm
            key={profile.id}
            base={fromProfile(profile)}
            autoFocus
            submitLabel={t("routes.create.submit")}
            save={async (input) => {
              const created = await createRoute(input);
              queryClient.setQueryData(getGetRouteQueryKey(created.id), created);
              await queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() });
              void queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() });
              await navigate({ to: "/routes" });
            }}
            onCancel={() => void navigate({ to: "/routes" })}
          />
        </>
      )}
    </div>
  );
}

function NewRoutePage() {
  return (
    <RequirePermission permission="routes:write">
      <NewRoute />
    </RequirePermission>
  );
}
