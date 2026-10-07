// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The labels of an Alert Group (C-09.FR-14): its group labels (the Group key values), the common labels all its Alerts
// share and the common annotations, with summary and description first and as prose. They come from Alertmanager, so
// they show as text only; long values wrap.

import { useTranslation } from "react-i18next";

import type { AlertGroup } from "../api/gen/model";
import { orderedAnnotations } from "./alert-group-alerts";
import { orderedLabels } from "./integration-alerts";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

function LabelList({
  title,
  labels,
  testId,
}: {
  title: string;
  labels: Record<string, string> | undefined;
  testId: string;
}) {
  const { t } = useTranslation();
  const entries = orderedLabels(labels ?? {});
  return (
    <section className="flex min-w-0 flex-col gap-1.5" data-testid={testId}>
      <h3 className="text-sm font-medium">{title}</h3>
      {entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("alertGroups.labels.none")}</p>
      ) : (
        <ul className="flex flex-wrap gap-1" aria-label={title}>
          {entries.map(([name, value]) => (
            <li
              key={name}
              className="max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere"
            >
              {name}={value}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function AlertGroupLabels({ group }: { group: AlertGroup }) {
  const { t } = useTranslation();
  const annotations = orderedAnnotations(group.common_annotations ?? {});
  const prose = annotations.filter(([name]) => name === "summary" || name === "description");
  const rest = annotations.filter(([name]) => name !== "summary" && name !== "description");
  return (
    <Card data-testid="alert-group-labels">
      <CardHeader>
        <CardTitle>
          <h2>{t("alertGroups.labels.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <LabelList
          title={t("alertGroups.labels.group")}
          labels={group.group_labels}
          testId="group-labels"
        />
        <LabelList
          title={t("alertGroups.labels.common")}
          labels={group.common_labels}
          testId="common-labels"
        />
        <section className="flex min-w-0 flex-col gap-1.5" data-testid="common-annotations">
          <h3 className="text-sm font-medium">{t("alertGroups.labels.annotations")}</h3>
          {annotations.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("alertGroups.labels.none")}</p>
          ) : (
            <dl className="flex flex-col gap-2">
              {prose.map(([name, value]) => (
                <div key={name} className="flex min-w-0 flex-col gap-0.5">
                  <dt className="text-xs text-muted-foreground">{name}</dt>
                  <dd className="text-sm whitespace-pre-wrap wrap-anywhere">{value}</dd>
                </div>
              ))}
              {rest.map(([name, value]) => (
                <div key={name} className="flex min-w-0 flex-col gap-0.5">
                  <dt className="font-mono text-xs text-muted-foreground">{name}</dt>
                  <dd className="font-mono text-xs whitespace-pre-wrap wrap-anywhere">{value}</dd>
                </div>
              ))}
            </dl>
          )}
        </section>
      </CardContent>
    </Card>
  );
}
