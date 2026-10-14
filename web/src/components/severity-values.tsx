// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The values of the severity label seen over the last days (C-20.AC-4): each value with its number of Alerts and its
// Severity level, an unmapped value marked as counting as warning with "Map", which adds it to the mapping, and the
// banner that lists the unmapped values.

import { useTranslation } from "react-i18next";

import type { SeverityMappingEntry, SeverityValueList } from "../api/gen/model";
import { severityLabel } from "./integration-alerts";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";

export function SeverityValues({
  list,
  mapping,
  onMap,
}: {
  list: SeverityValueList;
  /** The mapping as the form holds it: a value it maps already has no "Map". */
  mapping: readonly SeverityMappingEntry[];
  onMap: (value: string) => void;
}) {
  const { t } = useTranslation();
  const unmapped = list.items.filter((v) => !v.mapped).map((v) => v.value);
  const inForm = new Set(mapping.map((m) => m.value.trim()));
  const title = t("organization.severity.seen.title", { count: list.window_days });
  const head = "px-3 py-2 font-medium text-muted-foreground";
  return (
    <section className="flex min-w-0 flex-col gap-3" aria-labelledby="severity-values-title">
      <h2 id="severity-values-title" className="text-lg font-semibold">
        {title}
      </h2>
      {unmapped.length > 0 && (
        <Alert data-testid="severity-values-banner">
          <AlertDescription className="text-current">
            {t("organization.severity.seen.banner", { values: unmapped.join(", ") })}
          </AlertDescription>
        </Alert>
      )}
      {list.items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("organization.severity.seen.empty")}</p>
      ) : (
        <div className="min-w-0 rounded-lg border">
          <table
            className="w-full table-fixed border-collapse text-left text-sm"
            aria-label={t("organization.severity.seen.label")}
          >
            <thead className="bg-muted/50">
              <tr>
                <th scope="col" className={head}>
                  {t("organization.severity.seen.value")}
                </th>
                <th scope="col" className={`${head} w-16 text-right`}>
                  {t("organization.severity.seen.alerts")}
                </th>
                <th scope="col" className={head}>
                  {t("organization.severity.seen.level")}
                </th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((item) => (
                <tr key={item.value} className="border-t align-top">
                  <td className="px-3 py-2 font-mono wrap-anywhere">{item.value}</td>
                  <td className="px-3 py-2 text-right tabular-nums">{item.count}</td>
                  <td className="px-3 py-2">
                    <div className="flex flex-col items-start gap-1">
                      <span>{severityLabel(t, item.level)}</span>
                      {!item.mapped && (
                        <span className="text-muted-foreground">
                          {t("organization.severity.seen.unmapped")}
                        </span>
                      )}
                      {!item.mapped && !inForm.has(item.value) && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          aria-label={t("organization.severity.seen.mapValue", {
                            value: item.value,
                          })}
                          onClick={() => onMap(item.value)}
                        >
                          {t("organization.severity.seen.map")}
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
