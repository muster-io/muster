// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destinations section of the Route editor (C-08.FR-1, C-13.FR-11, C-11.FR-9): the Route's Destinations with their
// type and health, the Broken banner of each Broken one, and a native select (D250) that adds a Destination of any
// type to the list. While the Route's Storm is active it shows the Storm banner: "Storm since HH:MM: N new Alert
// Groups. Destinations receive a Storm summary." Health follows the Destinations list, which the destination hint
// reads again; the Route's own references name a Destination the list does not show.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { CloudLightningIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useListDestinations } from "../api/gen/endpoints/destinations/destinations";
import { useGetRoute } from "../api/gen/endpoints/routes/routes";
import type { DestinationRef, Route } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { BrokenBanner, HealthBadge, destinationTypeName, healthText } from "./destination-health";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The text of a refused Destination of a Route. */
export function routeDestinationErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "unknown_id":
      return t("routes.destinations.unknown");
    case "duplicate":
      return t("routes.destinations.duplicate");
    default:
      return fieldErrorText(t, code);
  }
}

/** Every Destination the Route editor can offer, with its health; page size at most 500 (api.page_size). */
export function useDestinationChoices(): { items: DestinationRef[]; loaded: boolean } {
  const canRead = useCan("destinations:read");
  const list = useListDestinations({ limit: 500 }, { query: { enabled: canRead } });
  return {
    items: (list.data?.items ?? []).map((d) => ({
      id: d.id,
      name: d.name,
      type: d.type,
      health: d.health,
    })),
    loaded: list.isSuccess,
  };
}

/** A Destination as an option of a picker: its name, type and, when Broken, its health. */
export function destinationOptionLabel(t: TFunction, d: DestinationRef): string {
  return d.health.state === "broken"
    ? t("routes.destinations.optionBroken", {
        name: d.name,
        type: destinationTypeName(t, d.type),
        health: healthText(t, d.health),
      })
    : t("routes.destinations.option", { name: d.name, type: destinationTypeName(t, d.type) });
}

/**
 * The Storm banner of a Route while its Storm is active. The editor keeps the version of the Route it was read at; the
 * Storm comes from the Route as last read, which the route hint reads again when a Storm starts or ends.
 */
export function StormBanner({ route: stored }: { route: Route | undefined }) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  const current = useGetRoute(stored?.id ?? "", { query: { enabled: stored !== undefined } });
  const route = current.data ?? stored;
  if (route?.storm_active !== true || route.storm === undefined) {
    return null;
  }
  return (
    <div
      className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm text-foreground"
      data-testid="storm-banner"
    >
      <CloudLightningIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <p className="min-w-0 wrap-anywhere">
        {t("routes.destinations.storm", {
          time: time(route.storm.since),
          count: route.storm.alert_group_count,
        })}
      </p>
    </div>
  );
}

function DestinationName({ destination }: { destination: DestinationRef }) {
  const canRead = useCan("destinations:read");
  if (!canRead) {
    return <span className="font-medium wrap-anywhere">{destination.name}</span>;
  }
  return (
    <Link
      to="/destinations/$destinationId"
      params={{ destinationId: destination.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {destination.name}
    </Link>
  );
}

export interface RouteDestinationsProps {
  /** The base of the ids of the inputs, unique on the page. */
  id: string;
  /** The public_ids of the Route's Destinations, in their order. */
  value: readonly string[];
  onChange?: (next: string[]) => void;
  /** The stored Route of an edit: the names of its Destinations and its Storm. */
  route?: Route;
  /** Error codes of a refused save, by the Destination they are about. */
  errors?: Record<string, string>;
  readOnly?: boolean;
}

export function RouteDestinations({
  id,
  value,
  onChange,
  route,
  errors = {},
  readOnly = false,
}: RouteDestinationsProps) {
  const { t } = useTranslation();
  const choices = useDestinationChoices();
  // A Destination neither the list nor the Route names, such as one deleted meanwhile, shows its public_id.
  const chosen = value.map((destinationId) => ({
    id: destinationId,
    ref:
      choices.items.find((d) => d.id === destinationId) ??
      route?.destinations.find((d) => d.id === destinationId),
  }));
  const rest = choices.items.filter((d) => !value.includes(d.id));
  const Heading = readOnly ? "h2" : "legend";
  const body = (
    <>
      <Heading className="mb-2 text-base font-semibold">{t("routes.destinations.title")}</Heading>
      <p id={`${id}-hint`} className="text-sm text-muted-foreground">
        {t("routes.destinations.hint")}
      </p>
      <StormBanner route={route} />
      {chosen.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="route-destinations-none">
          {t("routes.destinations.none")}
        </p>
      ) : (
        <ul className="flex flex-col gap-2" aria-label={t("routes.destinations.list")}>
          {chosen.map(({ id: destinationId, ref }) => {
            const error = errors[destinationId];
            const name = ref?.name ?? destinationId;
            return (
              <li
                key={destinationId}
                className="flex min-w-0 flex-col gap-2 rounded-lg border px-3 py-2"
                data-testid="route-destination"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                  {ref === undefined ? (
                    <span className="font-mono text-sm wrap-anywhere">{destinationId}</span>
                  ) : (
                    <>
                      <DestinationName destination={ref} />
                      <span className="text-sm text-muted-foreground">
                        {destinationTypeName(t, ref.type)}
                      </span>
                      <HealthBadge health={ref.health} />
                    </>
                  )}
                  {!readOnly && onChange !== undefined && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      className="ml-auto"
                      aria-label={t("routes.destinations.remove", { name })}
                      onClick={() => onChange(value.filter((v) => v !== destinationId))}
                    >
                      <XIcon aria-hidden="true" />
                    </Button>
                  )}
                </div>
                {ref !== undefined && <BrokenBanner health={ref.health} />}
                {error !== undefined && (
                  <p className="text-sm text-destructive" role="alert">
                    {routeDestinationErrorText(t, error)}
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {!readOnly && onChange !== undefined && (
        <div className="flex max-w-md min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-add`}>{t("routes.destinations.add")}</Label>
          <NativeSelect
            id={`${id}-add`}
            className="w-full"
            value=""
            disabled={rest.length === 0}
            aria-describedby={`${id}-hint`}
            onChange={(e) => {
              if (e.target.value !== "" && !value.includes(e.target.value)) {
                onChange([...value, e.target.value]);
              }
            }}
          >
            <NativeSelectOption value="">
              {choices.loaded && choices.items.length === 0
                ? t("routes.destinations.noneExist")
                : t("routes.destinations.choose")}
            </NativeSelectOption>
            {rest.map((d) => (
              <NativeSelectOption key={d.id} value={d.id}>
                {destinationOptionLabel(t, d)}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      )}
    </>
  );
  return readOnly ? (
    <section className="flex min-w-0 flex-col gap-3" data-testid="route-destinations">
      {body}
    </section>
  ) : (
    <fieldset className="flex min-w-0 flex-col gap-3" data-testid="route-destinations">
      {body}
    </fieldset>
  );
}
