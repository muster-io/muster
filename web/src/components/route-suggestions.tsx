// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Route suggestions at the top of the Routes page (C-08.FR-11, C-08.AC-8, C-13.FR-11): a Route for
// MusterHeartbeatLost while an Integration has a Heartbeat and only the Default route takes that alert, and from the
// first Destination on "Send Muster's own alerts to a Destination" with a Destination picker of every type (a native
// select, D250). "Create the route" adds the suggested Route — at the top of the list, or for Internal alerts directly
// below a Route that already takes one (D268); "Dismiss" hides the suggestion for the current user. A suggestion that
// no longer applies (409 suggestion_obsolete) makes the page read the suggestions again. Without routes:write the
// suggestion shows without its actions.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LightbulbIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  acceptRouteSuggestion,
  dismissRouteSuggestion,
  getGetRouteQueryKey,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
  useListRouteSuggestions,
} from "../api/gen/endpoints/routes/routes";
import type { Route, RouteSuggestion, RouteSuggestionId } from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { destinationOptionLabel, useDestinationChoices } from "./route-destinations";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

function isObsolete(err: unknown): boolean {
  return isApiError(err) && err.status === 409 && err.code === "suggestion_obsolete";
}

/** A refusal that names no Destination for the Internal alerts Route (422 required at /destination_ids). */
function needsDestination(err: unknown): boolean {
  return (
    isApiError(err) &&
    (err.errors ?? []).some(
      (e) => e.pointer.startsWith("/destination_ids") && e.code === "required",
    )
  );
}

/** The Destination picker of the Internal alerts suggestion. */
function DestinationChoice({
  value,
  onChange,
  invalid,
  errorId,
  disabled,
}: {
  value: string;
  onChange: (next: string) => void;
  invalid: boolean;
  /** The id of the paragraph that explains a refusal. */
  errorId: string;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const id = useId();
  const choices = useDestinationChoices();
  return (
    <div className="flex max-w-sm min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{t("routes.suggestions.destination")}</Label>
      <NativeSelect
        id={id}
        className="w-full"
        value={value}
        disabled={disabled}
        aria-invalid={invalid}
        aria-describedby={invalid ? errorId : undefined}
        onChange={(e) => onChange(e.target.value)}
      >
        <NativeSelectOption value="">
          {t("routes.suggestions.chooseDestination")}
        </NativeSelectOption>
        {choices.items.map((d) => (
          <NativeSelectOption key={d.id} value={d.id}>
            {destinationOptionLabel(t, d)}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}

function Suggestion({
  suggestion,
  canWrite,
  onAccepted,
  onDismissed,
}: {
  suggestion: RouteSuggestion;
  canWrite: boolean;
  onAccepted: (route: Route) => void;
  onDismissed: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canDestinations = useCan("destinations:read");
  const internal = suggestion.id === "internal_alerts";
  const [destinationId, setDestinationId] = useState("");
  const [missing, setMissing] = useState(false);
  const errorId = useId();
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() }),
      queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() }),
    ]);
  };
  const accept = useMutation({
    mutationFn: (id: RouteSuggestionId) =>
      acceptRouteSuggestion(id, internal ? { destination_ids: [destinationId] } : {}),
    onSuccess: async (route) => {
      queryClient.setQueryData(getGetRouteQueryKey(route.id), route);
      await refresh();
      onAccepted(route);
    },
    onError: async (err) => {
      if (isObsolete(err)) {
        await refresh();
      }
    },
  });
  const dismiss = useMutation({
    mutationFn: (id: RouteSuggestionId) => dismissRouteSuggestion(id),
    onSettled: async (_data, err) => {
      if (err === null || isObsolete(err)) {
        await refresh();
      }
      // The suggestion is gone either way: dismissed, or no longer applying.
      if (err === null || isObsolete(err)) {
        onDismissed();
      }
    },
  });
  const error = accept.error ?? dismiss.error;
  const busy = accept.isPending || dismiss.isPending;
  const noDestination = missing || needsDestination(accept.error);
  return (
    <li
      className="flex flex-col gap-3 rounded-lg border border-primary/30 bg-primary/5 p-4 sm:flex-row sm:items-center"
      data-testid="route-suggestion"
    >
      <LightbulbIcon aria-hidden="true" className="hidden size-5 shrink-0 text-primary sm:block" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        {internal ? (
          <>
            <p className="text-sm font-medium">{t("routes.suggestions.internalAlerts")}</p>
            <p className="text-sm text-muted-foreground">
              {t("routes.suggestions.internalAlertsHint")}
            </p>
            {canWrite && canDestinations && (
              <DestinationChoice
                value={destinationId}
                disabled={busy}
                invalid={noDestination}
                errorId={errorId}
                onChange={(next) => {
                  setDestinationId(next);
                  setMissing(false);
                  accept.reset();
                }}
              />
            )}
          </>
        ) : (
          <p className="text-sm">{t("routes.suggestions.heartbeatLost")}</p>
        )}
        {noDestination ? (
          <p id={errorId} className="text-sm text-destructive" role="alert">
            {t("routes.suggestions.destinationRequired")}
          </p>
        ) : (
          error !== null &&
          !isObsolete(error) && (
            <p className="text-sm text-destructive" role="alert">
              {problemText(t, error)}
            </p>
          )
        )}
      </div>
      {canWrite && (
        <div className="flex shrink-0 flex-wrap gap-2">
          {/* The Internal alerts Route needs a Destination, which only a reader of Destinations can pick. */}
          {(!internal || canDestinations) && (
            <Button
              type="button"
              disabled={busy}
              onClick={() => {
                if (internal && destinationId === "") {
                  setMissing(true);
                  return;
                }
                accept.mutate(suggestion.id);
              }}
            >
              {t("routes.suggestions.accept")}
            </Button>
          )}
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => dismiss.mutate(suggestion.id)}
          >
            {t("routes.suggestions.dismiss")}
          </Button>
        </div>
      )}
    </li>
  );
}

export function RouteSuggestions({
  canWrite,
  onAccepted,
  onDismissed,
}: {
  canWrite: boolean;
  /** Told about the Route an accepted suggestion created, for the page to move the focus to it. */
  onAccepted: (route: Route) => void;
  /** Told that a dismissed suggestion is gone, for the page to move the focus on. */
  onDismissed: () => void;
}) {
  const { t } = useTranslation();
  const suggestions = useListRouteSuggestions();
  const items = suggestions.data?.items ?? [];
  if (suggestions.isError) {
    return (
      <Alert variant="destructive">
        <AlertDescription className="text-current">
          {problemText(t, suggestions.error)}
        </AlertDescription>
      </Alert>
    );
  }
  if (items.length === 0) {
    return null;
  }
  return (
    <section aria-label={t("routes.suggestions.title")}>
      <ul className="flex flex-col gap-3">
        {items.map((s) => (
          <Suggestion
            key={s.id}
            suggestion={s}
            canWrite={canWrite}
            onAccepted={onAccepted}
            onDismissed={onDismissed}
          />
        ))}
      </ul>
    </section>
  );
}
