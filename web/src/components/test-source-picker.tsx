// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// What a Destination test or preview is built from (C-16.FR-1, FR-4): the built-in example, the default, or a recent
// Alert Group of one of the Destination's Routes by its #N and title, in a native select. A search narrows the Alert
// Groups by #N or text; the list covers the Alert Groups of the last 7 days (alert_group.list_range) of every status.
// Without alert-groups:read, or without a Route, only the example is offered.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { listAlertGroups } from "../api/gen/endpoints/alert-groups/alert-groups";
import type { AlertGroupStatus, TestSource } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useCan } from "./app-shell";
import { useDebounced } from "./template-preview";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOptGroup, NativeSelectOption } from "./ui/native-select";

/** What a test or a preview is built from; an Alert Group keeps its #N and title for the select. */
export type PickedSource =
  | { kind: "example" }
  | { kind: "alert_group"; id: string; number: number; title: string };

export const EXAMPLE: PickedSource = { kind: "example" };

/** The TestSource of the API for a picked source. */
export function testSource(source: PickedSource): TestSource {
  return source.kind === "example"
    ? { kind: "example" }
    : { kind: "alert_group", alert_group_id: source.id };
}

/** The first part of the query key of the offered Alert Groups. */
export const TEST_SOURCE_QUERY = "test-source-picker";

/** How many Alert Groups the select offers. */
const ALERT_GROUPS = 20;
const ALL_STATUSES: AlertGroupStatus[] = ["firing", "acknowledged", "snoozed", "resolved"];
const SEARCH_DELAY_MS = 300;

function optionText(g: { number: number; title: string }): string {
  return `#${g.number} ${g.title}`;
}

export function TestSourcePicker({
  routeIds,
  value,
  onChange,
}: {
  /** The Destination's Routes, whose Alert Groups are offered. */
  routeIds: string[];
  value: PickedSource;
  onChange: (source: PickedSource) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const canAlertGroups = useCan("alert-groups:read");
  const [search, setSearch] = useState("");
  const q = useDebounced(search.trim(), search.trim(), SEARCH_DELAY_MS);
  const offersGroups = canAlertGroups && routeIds.length > 0;
  const groups = useQuery({
    queryKey: [TEST_SOURCE_QUERY, routeIds, q],
    queryFn: ({ signal }) =>
      listAlertGroups(
        { limit: ALERT_GROUPS, status: ALL_STATUSES, route: routeIds, q: q === "" ? undefined : q },
        { signal },
      ),
    enabled: offersGroups,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  });
  const items = groups.data?.items ?? [];
  // The chosen Alert Group stays in the select while a search shows others.
  const chosen =
    value.kind === "alert_group" && !items.some((g) => g.id === value.id) ? value : null;
  const hintId = `${id}-hint`;
  let hint: string;
  if (!canAlertGroups) {
    hint = t("destinationTest.source.exampleOnly");
  } else if (routeIds.length === 0) {
    hint = t("destinationTest.source.noRoutes");
  } else {
    hint = t("destinationTest.source.hint");
  }
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="test-source-picker">
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={`${id}-select`}>{t("destinationTest.source.label")}</Label>
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <NativeSelect
            id={`${id}-select`}
            className="w-full max-w-sm min-w-0"
            aria-describedby={hintId}
            value={value.kind === "example" ? "example" : value.id}
            onChange={(e) => {
              const picked = e.target.value;
              if (picked === "example") {
                onChange(EXAMPLE);
                return;
              }
              const g = items.find((x) => x.id === picked);
              if (g !== undefined) {
                onChange({ kind: "alert_group", id: g.id, number: g.number, title: g.title });
              }
            }}
            data-testid="test-source-select"
          >
            <NativeSelectOption value="example">
              {t("destinationTest.source.example")}
            </NativeSelectOption>
            {(chosen !== null || items.length > 0) && (
              <NativeSelectOptGroup label={t("destinationTest.source.alertGroups")}>
                {chosen !== null && (
                  <NativeSelectOption value={chosen.id}>{optionText(chosen)}</NativeSelectOption>
                )}
                {items.map((g) => (
                  <NativeSelectOption key={g.id} value={g.id}>
                    {optionText(g)}
                  </NativeSelectOption>
                ))}
              </NativeSelectOptGroup>
            )}
          </NativeSelect>
          {offersGroups && (
            <Input
              type="search"
              className="w-full max-w-56 min-w-0"
              aria-label={t("destinationTest.source.search")}
              placeholder={t("destinationTest.source.searchPlaceholder")}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          )}
        </div>
      </div>
      <p id={hintId} className="text-sm text-muted-foreground">
        {hint}
      </p>
      {/* Stays mounted, so that a change of its text is announced. */}
      <p className="text-sm text-muted-foreground empty:hidden" role="status">
        {offersGroups && groups.isError && problemText(t, groups.error)}
        {offersGroups &&
          groups.isSuccess &&
          items.length === 0 &&
          (q === "" ? t("destinationTest.source.none") : t("destinationTest.source.noMatch"))}
      </p>
    </div>
  );
}
