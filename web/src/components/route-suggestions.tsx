// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Route suggestions at the top of the Routes page (C-08.FR-11, C-08.AC-8): a Route for MusterHeartbeatLost while
// an Integration has a Heartbeat and only the Default route takes that alert. "Create the route" adds the suggested
// Route at the top of the list; "Dismiss" hides the suggestion for the current user. A suggestion that no longer
// applies (409 suggestion_obsolete) makes the page read the suggestions again. Without routes:write the suggestion
// shows without its actions. The suggestion for Internal alerts needs a Destination and comes with the Destinations.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LightbulbIcon } from "lucide-react";
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
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";

function isObsolete(err: unknown): boolean {
  return isApiError(err) && err.status === 409 && err.code === "suggestion_obsolete";
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
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: getListRouteSuggestionsQueryKey() }),
      queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() }),
    ]);
  };
  const accept = useMutation({
    mutationFn: (id: RouteSuggestionId) => acceptRouteSuggestion(id, {}),
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
  return (
    <li
      className="flex flex-col gap-3 rounded-lg border border-primary/30 bg-primary/5 p-4 sm:flex-row sm:items-center"
      data-testid="route-suggestion"
    >
      <LightbulbIcon aria-hidden="true" className="hidden size-5 shrink-0 text-primary sm:block" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="text-sm">{t("routes.suggestions.heartbeatLost")}</p>
        {error !== null && !isObsolete(error) && (
          <p className="text-sm text-destructive" role="alert">
            {problemText(t, error)}
          </p>
        )}
      </div>
      {canWrite && (
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button type="button" disabled={busy} onClick={() => accept.mutate(suggestion.id)}>
            {t("routes.suggestions.accept")}
          </Button>
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
  const items = (suggestions.data?.items ?? []).filter((s) => s.id === "heartbeat_lost");
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
