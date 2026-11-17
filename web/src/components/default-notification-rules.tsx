// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Organization's default Notification rules on Organization → Alert handling (C-20.FR-1, C-31.FR-2): the normal and
// the important rule in words, and the note that they change only through the API (`updateOrganization`) in 0.2.0,
// with the link to the documentation. A User who follows the Organization's rules sees a change here at once.

import { useTranslation } from "react-i18next";

import type { Organization } from "../api/gen/model";
import { ruleWords } from "../lib/notification-rules";
import { DocsLink } from "./docs-link";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

function RuleLine({ label, text, testId }: { label: string; text: string; testId: string }) {
  return (
    <div className="rounded-lg border bg-muted/30 p-3.5">
      <p data-testid={testId} className="text-sm wrap-anywhere">
        <span className="font-semibold">{label}:</span> {text}.
      </p>
    </div>
  );
}

export function DefaultNotificationRules({
  organization,
}: {
  organization: Pick<Organization, "notification_rules">;
}) {
  const { t } = useTranslation();
  const rules = organization.notification_rules;
  return (
    <Card data-testid="default-notification-rules">
      <CardHeader>
        <CardTitle>
          <h2>{t("organization.defaultRules.title")}</h2>
        </CardTitle>
        <CardDescription>{t("organization.defaultRules.hint")}</CardDescription>
        <DocsLink
          to="field.notification_rules"
          kind="learn_more"
          about={t("docs.fields.notificationRules")}
        />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <RuleLine
            label={t("notificationRules.normal")}
            text={ruleWords(t, rules.normal, null)}
            testId="default-rule-normal"
          />
          <RuleLine
            label={t("notificationRules.important")}
            text={ruleWords(t, rules.important, null)}
            testId="default-rule-important"
          />
        </div>
        <p className="text-sm text-muted-foreground" data-testid="default-rules-note">
          {t("organization.defaultRules.note")}
        </p>
      </CardContent>
    </Card>
  );
}
