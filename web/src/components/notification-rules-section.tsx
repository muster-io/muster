// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Personal notification rules" of the profile (C-31.FR-4, FR-5, C-30.FR-9): the normal and the important rule in
// words, and the switch "Also call through the Notification gateway" with "As in the Organization", "Call" and "Don't
// call". The switch is unavailable without a Notification gateway ("Unavailable: ...") and until the person has a
// confirmed contact at one ("Confirm your contact for calls and SMS below first."); with contacts at several gateways
// the person chooses which one the call uses. Custom rules, set through the API or by an Admin, show in words without
// the switch and with "Reset to the Organization's rules". NotificationRulesBody is the same view on a User's page.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import { useListMyGatewayContacts } from "../api/gen/endpoints/notification-gateways/notification-gateways";
import {
  getGetMyNotificationRulesQueryKey,
  resetMyNotificationRules,
  updateMyNotificationRules,
  useGetMyNotificationRules,
} from "../api/gen/endpoints/notification-rules/notification-rules";
import type {
  EntityRef,
  GatewayContact,
  NotificationRules,
  NotificationRulesState,
  NotificationRulesUpdate,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { confirmedContact, ruleWords } from "../lib/notification-rules";
import { useCan } from "./app-shell";
import { DocsLink } from "./docs-link";
import { GatewayUnavailable } from "./gateway-unavailable";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/**
 * The Notification gateways a person can see: those of the list of Connections, then any gateway only a contact names,
 * for a person who cannot read Connections.
 */
export function useKnownGateways(contacts: readonly GatewayContact[]): {
  gateways: EntityRef[];
  settled: boolean;
  failed: unknown;
} {
  const canRead = useCan("connections:read");
  const list = useListConnections({ role: "gateway", limit: 500 }, { query: { enabled: canRead } });
  const gateways: EntityRef[] = (list.data?.items ?? []).map((c) => ({ id: c.id, name: c.name }));
  for (const c of contacts) {
    if (!gateways.some((g) => g.id === c.connection.id)) {
      gateways.push(c.connection);
    }
  }
  const failed = list.data === undefined && canRead && list.error !== null ? list.error : null;
  return { gateways, settled: !canRead || !list.isPending, failed };
}

export interface RulesBodyProps {
  rules: NotificationRules;
  contacts: readonly GatewayContact[];
  gateways: readonly EntityRef[];
  /** The person whose rules these are, when not the viewer: the texts name them. */
  subject?: string;
  editable: boolean;
  pending: boolean;
  error: unknown;
  onChange: (update: NotificationRulesUpdate) => void;
}

const STATES: NotificationRulesState[] = ["organization", "call", "no_call"];

/** The rules in words, the switch with its gateway choice and, for custom rules, "Reset to the Organization's rules". */
export function NotificationRulesBody({
  rules,
  contacts,
  gateways,
  subject,
  editable,
  pending,
  error,
  onChange,
}: RulesBodyProps) {
  const { t } = useTranslation();
  const confirmed = contacts.filter((c) => c.confirmed);
  const callable = gateways.filter((g) => confirmed.some((c) => c.connection.id === g.id));
  const [picked, setPicked] = useState<string | null>(null);
  // The gateway of the rules in the state `call` wins over a pick that was refused; otherwise the pick, then the first.
  const inUse =
    rules.state === "call" ? callable.find((g) => g.id === rules.gateway?.id) : undefined;
  const choice =
    inUse?.id ??
    (picked !== null && callable.some((g) => g.id === picked) ? picked : callable[0]?.id);
  const custom = rules.state === "custom";
  const optionLabel: Record<NotificationRulesState, string> = {
    organization: t("notificationRules.switch.organization"),
    call: t("notificationRules.switch.call"),
    no_call: t("notificationRules.switch.noCall"),
    custom: "",
  };
  // Without a way to call, "Call" is unavailable; a person who follows the Organization has nothing to switch.
  const off = !editable || pending;
  const optionOff = (state: NotificationRulesState) =>
    off ||
    (state === "call" && callable.length === 0) ||
    (rules.state === "organization" && callable.length === 0);
  const toContact = confirmedContact(contacts, rules.gateway?.id);
  const choose = (state: NotificationRulesState) => {
    if (state === "call") {
      onChange({ state, gateway_id: choice });
    } else {
      onChange({ state });
    }
  };
  const line = (label: string, rule: typeof rules.normal, testId: string) => (
    <p data-testid={testId} className="text-sm wrap-anywhere">
      <span className="font-semibold">{label}:</span> {ruleWords(t, rule, confirmed)}.
    </p>
  );
  return (
    <div className="flex flex-col gap-4" data-testid="notification-rules" data-state={rules.state}>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-2 rounded-lg border bg-muted/30 p-3.5">
          {line(t("notificationRules.normal"), rules.normal, "rule-normal")}
        </div>
        <div className="flex flex-col gap-2 rounded-lg border bg-muted/30 p-3.5">
          {line(t("notificationRules.important"), rules.important, "rule-important")}
        </div>
      </div>
      {custom ? (
        <div className="flex flex-col items-start gap-2" data-testid="rules-custom">
          <p className="text-sm text-muted-foreground">{t("notificationRules.custom")}</p>
          {editable && (
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => onChange({ state: "organization" })}
            >
              {t("notificationRules.reset")}
            </Button>
          )}
        </div>
      ) : (
        <fieldset className="flex min-w-0 flex-col gap-2" data-testid="rules-switch">
          <legend className="flex flex-wrap items-center gap-x-3 text-sm font-medium">
            {t("notificationRules.switch.title")}
            <DocsLink
              to="field.notification_rules_switch"
              kind="learn_more"
              about={t("docs.fields.notificationRulesSwitch")}
            />
          </legend>
          <div className="flex flex-wrap gap-x-5 gap-y-2" role="radiogroup">
            {STATES.map((state) => (
              <label
                key={state}
                className="flex items-center gap-2 text-sm has-disabled:text-muted-foreground"
              >
                <input
                  type="radio"
                  name="notification-rules-switch"
                  className="size-4 accent-primary"
                  checked={rules.state === state}
                  disabled={optionOff(state)}
                  onChange={() => choose(state)}
                />
                {optionLabel[state]}
              </label>
            ))}
          </div>
          {callable.length > 1 && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="notification-rules-gateway">
                {t("notificationRules.gatewayChoice")}
              </Label>
              <NativeSelect
                id="notification-rules-gateway"
                className="w-full max-w-xs"
                value={choice ?? ""}
                disabled={off}
                onChange={(e) => {
                  setPicked(e.target.value);
                  if (rules.state === "call") {
                    onChange({ state: "call", gateway_id: e.target.value });
                  }
                }}
              >
                {callable.map((g) => (
                  <NativeSelectOption key={g.id} value={g.id}>
                    {g.name}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
          )}
          {rules.state === "call" && toContact !== undefined && (
            <p className="text-sm text-muted-foreground" data-testid="rules-call-to">
              {t("notificationRules.callTo", { contact: toContact.contact })}
            </p>
          )}
          {gateways.length === 0 ? (
            <GatewayUnavailable />
          ) : (
            callable.length === 0 && (
              <p className="text-sm text-muted-foreground" data-testid="rules-no-contact">
                {subject === undefined
                  ? t("notificationRules.noContact")
                  : t("notificationRules.noContactUser", { name: subject })}
              </p>
            )
          )}
        </fieldset>
      )}
      {error !== null && error !== undefined && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {isApiError(error) && error.status === 412
              ? t("notificationRules.stale")
              : problemText(t, error)}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

export function NotificationRulesSection() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const rules = useGetMyNotificationRules();
  const contacts = useListMyGatewayContacts();
  const items = contacts.data?.items ?? [];
  const known = useKnownGateways(items);
  const key = getGetMyNotificationRulesQueryKey();
  const update = useMutation({
    mutationFn: (change: NotificationRulesUpdate) =>
      change.state === "organization"
        ? resetMyNotificationRules()
        : updateMyNotificationRules(change, {
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
    <Card className="lg:col-span-2" data-testid="notification-rules-section">
      <CardHeader>
        <CardTitle>
          <h2>{t("notificationRules.title")}</h2>
        </CardTitle>
        <CardDescription>{t("notificationRules.hint")}</CardDescription>
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
            editable
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
