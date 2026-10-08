// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sample a template preview renders against (C-12.FR-5), in a native select: the Route's most recent Stored
// Snapshots (the default; the server falls back to a built-in example when there are none, and a Link rule, which has
// no Route, starts from the example), one recent Stored Snapshot by its time (with stored-snapshots:read), or one
// recent Alert Group by its #N (with alert-groups:read). Stored Snapshots belong to an Integration, so the picker
// reads the snapshots of the Integrations that received one most recently.

import { useQueries, useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import { listAlertGroups } from "../api/gen/endpoints/alert-groups/alert-groups";
import {
  listIntegrations,
  listStoredSnapshots,
} from "../api/gen/endpoints/integrations/integrations";
import type { AlertGroupStatus, TemplatePreviewRequest } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { NativeSelect, NativeSelectOptGroup, NativeSelectOption } from "./ui/native-select";

/** The sample of a preview. */
export type Sample =
  | { type: "default" }
  | { type: "stored_snapshot"; id: string }
  | { type: "alert_group"; id: string };

export const DEFAULT_SAMPLE: Sample = { type: "default" };

/** The fields of a preview request that name its sample. */
export function sampleFields(
  sample: Sample,
): Pick<TemplatePreviewRequest, "stored_snapshot_id" | "alert_group_id"> {
  switch (sample.type) {
    case "stored_snapshot":
      return { stored_snapshot_id: sample.id };
    case "alert_group":
      return { alert_group_id: sample.id };
    default:
      return {};
  }
}

function sampleValue(sample: Sample): string {
  return sample.type === "default" ? "default" : `${sample.type}:${sample.id}`;
}

/** The sample an option of the select stands for. */
export function sampleOf(value: string): Sample {
  const [type, id = ""] = value.split(":", 2);
  if (type === "stored_snapshot" && id !== "") {
    return { type, id };
  }
  if (type === "alert_group" && id !== "") {
    return { type, id };
  }
  return DEFAULT_SAMPLE;
}

/** How many Integrations, and how many Stored Snapshots of each, the picker offers. */
const INTEGRATIONS = 5;
const SNAPSHOTS = 10;
const ALERT_GROUPS = 20;
const ALL_STATUSES: AlertGroupStatus[] = ["firing", "acknowledged", "snoozed", "resolved"];

export function SamplePicker({
  id,
  value,
  onChange,
  routeId,
  defaultLabel,
}: {
  id: string;
  value: Sample;
  onChange: (sample: Sample) => void;
  /** The Route whose Alert Groups are offered; without one, the recent Alert Groups of every Route. */
  routeId?: string;
  /** The text of the default sample, such as "Recent snapshots of this route". */
  defaultLabel: string;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const canReadSnapshots = useCan("stored-snapshots:read");
  const canReadIntegrations = useCan("integrations:read");
  const canSnapshots = canReadSnapshots && canReadIntegrations;
  const canAlertGroups = useCan("alert-groups:read");
  const integrations = useQuery({
    queryKey: ["sample-picker", "integrations"],
    queryFn: ({ signal }) => listIntegrations({ limit: 50 }, { signal }),
    enabled: canSnapshots,
    staleTime: 30_000,
  });
  const recent = (integrations.data?.items ?? [])
    .filter((i) => i.last_snapshot_at)
    .toSorted((a, b) => (b.last_snapshot_at ?? "").localeCompare(a.last_snapshot_at ?? ""))
    .slice(0, INTEGRATIONS);
  const snapshots = useQueries({
    queries: recent.map((integration) => ({
      queryKey: ["sample-picker", "stored-snapshots", integration.id],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listStoredSnapshots(
          { integration: integration.id, limit: SNAPSHOTS, state: ["processed"] },
          { signal },
        ),
      staleTime: 30_000,
    })),
  });
  const alertGroups = useQuery({
    queryKey: ["sample-picker", "alert-groups", routeId ?? null],
    queryFn: ({ signal }) =>
      listAlertGroups(
        {
          limit: ALERT_GROUPS,
          status: ALL_STATUSES,
          route: routeId === undefined ? undefined : [routeId],
        },
        { signal },
      ),
    enabled: canAlertGroups,
    staleTime: 30_000,
  });
  const groups = alertGroups.data?.items ?? [];
  // A chosen sample that is no longer offered (past retention, or a list read again) falls back to the default, so that
  // the select and the preview never disagree.
  const settled =
    !integrations.isFetching && !alertGroups.isFetching && snapshots.every((q) => !q.isFetching);
  const offered =
    value.type === "default" ||
    (value.type === "alert_group"
      ? groups.some((g) => g.id === value.id)
      : snapshots.some((q) => q.data?.items.some((item) => item.id === value.id) === true));
  useEffect(() => {
    if (settled && !offered) {
      onChange(DEFAULT_SAMPLE);
    }
  }, [settled, offered, onChange]);
  return (
    <NativeSelect
      id={id}
      className="w-full max-w-sm min-w-0"
      value={sampleValue(value)}
      onChange={(e) => onChange(sampleOf(e.target.value))}
      data-testid="sample-picker"
    >
      <NativeSelectOption value="default">{defaultLabel}</NativeSelectOption>
      {recent.map((integration, index) => {
        const items = snapshots[index]?.data?.items ?? [];
        return items.length === 0 ? null : (
          <NativeSelectOptGroup
            key={integration.id}
            label={t("templates.sample.snapshotsOf", { integration: integration.name })}
          >
            {items.map((s) => (
              <NativeSelectOption
                key={s.id}
                value={sampleValue({ type: "stored_snapshot", id: s.id })}
              >
                {t("templates.sample.snapshot", {
                  time: dateTime(s.received_at),
                  count: s.alert_count ?? 0,
                })}
              </NativeSelectOption>
            ))}
          </NativeSelectOptGroup>
        );
      })}
      {groups.length > 0 && (
        <NativeSelectOptGroup label={t("templates.sample.alertGroups")}>
          {groups.map((g) => (
            <NativeSelectOption key={g.id} value={sampleValue({ type: "alert_group", id: g.id })}>
              {`#${g.number} ${g.title}`}
            </NativeSelectOption>
          ))}
        </NativeSelectOptGroup>
      )}
    </NativeSelect>
  );
}
