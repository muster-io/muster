// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The preview "Where alerts go" of the Route editor (C-26.FR-12, AC-9): the largest Alert Groups that the recent Alerts
// the Route would take form under the unsaved Matchers, Group key, Minimum severity, Destinations and option, with the
// Teams of each and the Destinations each reaches — a Destination of a Team marked "via {Team}" —, and for every
// Destination how many of all those Alert Groups reach it. It follows the form: a change asks again after a pause, and
// only the latest request counts. It reads only; key values come from Alerts and show as text.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { previewGroupKey } from "../api/gen/endpoints/routes/routes";
import type {
  DeliveryEntry,
  DeliveryPreview,
  DeliveryPreviewDestination,
  DeliveryPreviewGroup,
  GroupKeyPreview,
  GroupKeyPreviewRequest,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { teamNameText } from "./team-membership";

/** What the preview asks for: the form's unsaved settings; the period is the default one. */
export type DeliveryPreviewQuery = Omit<GroupKeyPreviewRequest, "period_seconds">;

/** The pause after the last change of the form before the preview asks again, in milliseconds. */
export const PREVIEW_PAUSE = 400;

/** The value as it was `pause` milliseconds ago, once it stopped changing. */
function usePaused<T>(value: T, pause: number): T {
  const [paused, setPaused] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setPaused(value), pause);
    return () => clearTimeout(timer);
  }, [value, pause]);
  return paused;
}

/** How a Destination was reached: "via payments, search", with "Route" first when the Route's own entry reached it. */
export function reachText(t: TFunction, entries: readonly DeliveryEntry[]): string {
  const teams = entries
    .filter((e) => e.kind === "team" && e.team != null)
    .map((e) => (e.team == null ? "" : teamNameText(t, e.team)));
  if (teams.length === 0) {
    return "";
  }
  const via = t("routes.deliveryPreview.via", { teams: teams.join(", ") });
  return entries.some((e) => e.kind === "route")
    ? t("routes.deliveryPreview.routeAnd", { via })
    : via;
}

/** The Severity level of an Alert Group, as the API names it. */
function levelText(t: TFunction, level: DeliveryPreviewGroup["severity_level"]): string {
  switch (level) {
    case "critical":
      return t("routes.deliveryPreview.level.critical");
    case "warning":
      return t("routes.deliveryPreview.level.warning");
    case "info":
      return t("routes.deliveryPreview.level.info");
    default:
      return level satisfies never;
  }
}

function GroupValues({ group }: { group: DeliveryPreviewGroup }) {
  const { t } = useTranslation();
  const pairs = Object.entries(group.group_key_values);
  return (
    <p className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      {pairs.length === 0 ? (
        <span className="text-muted-foreground">{t("routes.preview.oneGroup")}</span>
      ) : (
        pairs.map(([name, value]) => (
          <span key={name} className="font-mono text-xs wrap-anywhere" data-testid="delivery-pair">
            {name}={value === "" ? '""' : value}
          </span>
        ))
      )}
      <span className="text-xs text-muted-foreground">
        {t("routes.deliveryPreview.alerts", { count: group.alert_count })} ·{" "}
        {levelText(t, group.severity_level)}
      </span>
    </p>
  );
}

function DestinationLine({ item }: { item: DeliveryPreviewDestination }) {
  const { t } = useTranslation();
  const via = reachText(t, item.entries);
  return (
    <li className="wrap-anywhere" data-testid="delivery-preview-destination">
      <span className="font-medium">{item.destination.name}</span>
      {via !== "" && (
        <span className="text-muted-foreground" data-testid="delivery-preview-via">
          {" "}
          {via}
        </span>
      )}
    </li>
  );
}

function Group({ group }: { group: DeliveryPreviewGroup }) {
  const { t } = useTranslation();
  return (
    <li
      className="flex min-w-0 flex-col gap-1 rounded-md border p-2.5"
      data-testid="delivery-group"
    >
      <GroupValues group={group} />
      <p className="text-sm" data-testid="delivery-group-teams">
        <span className="text-muted-foreground">{t("routes.deliveryPreview.teams")}: </span>
        {group.teams.length === 0
          ? t("routes.deliveryPreview.noTeams")
          : group.teams.map((team) => teamNameText(t, team)).join(", ")}
      </p>
      {group.destinations.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t("routes.deliveryPreview.noDestinations")}
        </p>
      ) : (
        <ul
          className="flex flex-col gap-0.5 text-sm"
          aria-label={t("routes.deliveryPreview.reaches")}
        >
          {group.destinations.map((item) => (
            <DestinationLine key={item.destination.id} item={item} />
          ))}
        </ul>
      )}
    </li>
  );
}

function Counts({ delivery, total }: { delivery: DeliveryPreview; total: number }) {
  const { t } = useTranslation();
  if (delivery.destination_counts.length === 0) {
    return null;
  }
  return (
    <div className="flex flex-col gap-1">
      <h4 className="text-sm font-medium">{t("routes.deliveryPreview.counts")}</h4>
      <ul className="flex flex-col gap-0.5 text-sm" data-testid="delivery-counts">
        {delivery.destination_counts.map((c) => (
          <li key={c.destination.id} className="wrap-anywhere" data-testid="delivery-count">
            <span className="font-medium">{c.destination.name}</span>
            <span className="text-muted-foreground">
              {" "}
              {t("routes.deliveryPreview.countOf", { reaching: c.alert_group_count, total })}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The text of a preview that failed; a refusal about the fields of the form asks to fix them. */
function failureText(t: TFunction, err: unknown): string {
  return isApiError(err) && err.status === 422
    ? t("routes.preview.fixFields")
    : problemText(t, err);
}

export function RouteDeliveryPreview({
  query,
  enabled,
  teamLabel,
}: {
  query: DeliveryPreviewQuery;
  /** Whether the request is complete: a new Route needs a Matcher. */
  enabled: boolean;
  teamLabel: string | undefined;
}) {
  const { t } = useTranslation();
  const request = usePaused(JSON.stringify(query), PREVIEW_PAUSE);
  const current = JSON.stringify(query);
  const result = useQuery({
    queryKey: ["route-delivery-preview", request],
    queryFn: ({ signal }) => {
      const body: GroupKeyPreviewRequest = JSON.parse(request);
      return previewGroupKey(body, { signal });
    },
    enabled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    placeholderData: keepPreviousData,
  });
  // A refresh that failed keeps the last list that was read, with the failure shown above it.
  const [lastRead, setLastRead] = useState<GroupKeyPreview>();
  if (result.data !== undefined && !result.isPlaceholderData && result.data !== lastRead) {
    setLastRead(result.data);
  }
  const data = result.data ?? lastRead;
  const delivery = data?.delivery;
  const updating = enabled && (request !== current || result.isFetching);
  return (
    <section
      aria-labelledby="route-delivery-preview-title"
      className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
      data-testid="route-delivery-preview"
      aria-busy={updating}
    >
      <div className="flex flex-col gap-1">
        <h3 id="route-delivery-preview-title" className="text-base font-semibold">
          {t("routes.deliveryPreview.title")}
        </h3>
        <p className="text-sm text-muted-foreground">{t("routes.deliveryPreview.hint")}</p>
      </div>
      {!enabled ? (
        <p className="text-sm text-muted-foreground" data-testid="delivery-preview-needs-matchers">
          {t("routes.deliveryPreview.needMatchers")}
        </p>
      ) : data === undefined ? (
        <p
          className={result.isError ? "text-sm text-destructive" : "text-sm text-muted-foreground"}
          role={result.isError ? "alert" : "status"}
        >
          {result.isError ? failureText(t, result.error) : t("common.loading")}
        </p>
      ) : (
        <>
          {result.isError && (
            <p className="text-sm break-words text-destructive" role="alert">
              {failureText(t, result.error)}
            </p>
          )}
          {updating && !result.isError && (
            <p className="text-sm text-muted-foreground" role="status">
              {t("routes.deliveryPreview.updating")}
            </p>
          )}
          {delivery === undefined ? (
            <p className="text-sm text-muted-foreground" data-testid="delivery-preview-none">
              {t("routes.deliveryPreview.noEntries")}
            </p>
          ) : (
            <>
              {delivery.shared_key_warning && teamLabel !== undefined && (
                <p
                  className="rounded-md border border-warning/60 bg-warning-surface px-2.5 py-1.5 text-sm"
                  data-testid="preview-shared-key-warning"
                >
                  {t("routes.teams.sharedKey", { label: teamLabel })}
                </p>
              )}
              {delivery.alert_groups.length === 0 ? (
                <p className="text-sm text-muted-foreground" data-testid="delivery-preview-empty">
                  {t("routes.deliveryPreview.noAlerts")}
                </p>
              ) : (
                <>
                  <ul
                    className="flex flex-col gap-2"
                    aria-label={t("routes.deliveryPreview.title")}
                    data-testid="delivery-groups"
                  >
                    {delivery.alert_groups.map((group) => (
                      <Group
                        key={JSON.stringify(Object.entries(group.group_key_values))}
                        group={group}
                      />
                    ))}
                  </ul>
                  <Counts delivery={delivery} total={data.proposed.alert_group_count} />
                  <p className="text-xs text-muted-foreground">
                    {t("routes.deliveryPreview.grow")}
                  </p>
                </>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}
