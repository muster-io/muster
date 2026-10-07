// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The choice of a Route profile that starts "Create route" (C-08.FR-7): On-call and Informational, as listRouteProfiles
// serves them from the built-in defaults, each with a line on what it is for. A profile only pre-fills the editor.

import type { TFunction } from "i18next";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import { useListRouteProfiles } from "../api/gen/endpoints/routes/routes";
import type { RouteProfile, RouteProfileId } from "../api/gen/model";
import { problemText } from "../lib/api";

/** The name of a Route profile in the language of the page. */
export function profileName(t: TFunction, id: RouteProfileId): string {
  return id === "on_call" ? t("routes.profiles.onCall") : t("routes.profiles.informational");
}

function profileLine(t: TFunction, id: RouteProfileId): string {
  return id === "on_call"
    ? t("routes.profiles.onCallLine")
    : t("routes.profiles.informationalLine");
}

export function ProfilePicker({
  onPick,
  autoFocus = false,
}: {
  onPick: (profile: RouteProfile) => void;
  /** Puts the focus on the heading once the choice shows, as after "Choose another profile". */
  autoFocus?: boolean;
}) {
  const { t } = useTranslation();
  const profiles = useListRouteProfiles();
  const heading = useRef<HTMLHeadingElement>(null);
  const shown = profiles.data !== undefined;
  useEffect(() => {
    if (autoFocus && shown) {
      heading.current?.focus();
    }
  }, [autoFocus, shown]);
  if (profiles.data === undefined) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {profiles.isError ? problemText(t, profiles.error) : t("common.loading")}
      </p>
    );
  }
  return (
    <section aria-labelledby="route-profile-title" className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2
          ref={heading}
          id="route-profile-title"
          tabIndex={-1}
          className="text-lg font-semibold outline-none"
        >
          {t("routes.profiles.title")}
        </h2>
        <p className="text-sm text-muted-foreground">{t("routes.profiles.hint")}</p>
      </div>
      <ul className="grid gap-3 sm:grid-cols-2">
        {profiles.data.items.map((profile) => (
          <li key={profile.id} className="flex">
            <button
              type="button"
              className="flex w-full flex-col gap-1 rounded-lg border bg-card p-4 text-left outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50"
              data-testid={`route-profile-${profile.id}`}
              aria-labelledby={`route-profile-${profile.id}-name`}
              aria-describedby={`route-profile-${profile.id}-line`}
              onClick={() => onPick(profile)}
            >
              <span id={`route-profile-${profile.id}-name`} className="font-medium">
                {profileName(t, profile.id)}
              </span>
              <span
                id={`route-profile-${profile.id}-line`}
                className="text-sm text-muted-foreground"
              >
                {profileLine(t, profile.id)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
