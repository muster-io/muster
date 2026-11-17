// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A User's Notification rules on their page (C-31.FR-4): both rules in words with the switch and "Reset to the
// Organization's rules", which users:write changes and anyone else reads. A change shows in the Audit log as
// notification_rules.updated with its diff. A call is offered only for a gateway at which the User has a confirmed
// contact.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { useListUserGatewayContacts } from "../api/gen/endpoints/notification-gateways/notification-gateways";
import {
  getGetUserNotificationRulesQueryKey,
  updateUserNotificationRules,
  useGetUserNotificationRules,
} from "../api/gen/endpoints/notification-rules/notification-rules";
import type { NotificationRulesUpdate, User } from "../api/gen/model";
import { problemText } from "../lib/api";
import { DocsLink } from "./docs-link";
import { NotificationRulesBody, useKnownGateways } from "./notification-rules-section";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

export function UserNotificationRules({ user, canWrite }: { user: User; canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const rules = useGetUserNotificationRules(user.id);
  const contacts = useListUserGatewayContacts(user.id);
  const items = contacts.data?.items ?? [];
  const known = useKnownGateways(items);
  const key = getGetUserNotificationRulesQueryKey(user.id);
  const update = useMutation({
    mutationFn: (change: NotificationRulesUpdate) =>
      updateUserNotificationRules(user.id, change, {
        headers: { "If-Match": rules.data?.etag ?? "" },
      }),
    onSuccess: (fresh) => queryClient.setQueryData(key, fresh),
    onError: () => queryClient.invalidateQueries({ queryKey: key }),
  });
  const failed =
    rules.data === undefined && rules.error !== null
      ? rules.error
      : contacts.data === undefined && contacts.error !== null
        ? contacts.error
        : known.failed;
  const ready = rules.data !== undefined && contacts.data !== undefined && known.settled;
  return (
    <Card className="lg:col-span-2" data-testid="user-notification-rules">
      <CardHeader>
        <CardTitle>
          <h2>{t("userNotificationRules.title")}</h2>
        </CardTitle>
        <CardDescription>{t("userNotificationRules.hint", { name: user.name })}</CardDescription>
        <DocsLink
          to="field.notification_rules"
          kind="learn_more"
          about={t("docs.fields.notificationRules")}
        />
      </CardHeader>
      <CardContent>
        {failed !== null && failed !== undefined ? (
          <p className="text-sm text-destructive" role="alert">
            {problemText(t, failed)}
          </p>
        ) : !ready ? (
          <p className="text-sm text-muted-foreground" role="status">
            {t("common.loading")}
          </p>
        ) : (
          <NotificationRulesBody
            rules={rules.data}
            contacts={items}
            gateways={known.gateways}
            subject={user.name}
            editable={canWrite}
            pending={update.isPending}
            error={update.error}
            onChange={(change) => {
              update.reset();
              update.mutate(change);
            }}
          />
        )}
      </CardContent>
    </Card>
  );
}
