// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The before/after diff of an Audit log entry (C-03.FR-14, C-03.FR-15). A Secret shows only that it changed
// (C-03.FR-21); a value that deleting a user erased arrives as "[erased]" and is shown as such, translated (C-03.FR-13).

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import type { AuditDiffEntry } from "../api/gen/model";

/** The marker the API puts in place of a value that deleting a user erased. */
export const ERASED = "[erased]";

/** The names of the changed fields of a user. */
function userField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("audit.fields.name");
    case "/login":
      return t("audit.fields.login");
    case "/email":
      return t("audit.fields.email");
    case "/role":
      return t("audit.fields.role");
    case "/status":
      return t("audit.fields.status");
    case "/password":
      return t("audit.fields.password");
    case "/sign_in_method":
      return t("audit.fields.signInMethod");
    case "/time_zone":
      return t("profile.preferences.timeZone");
    case "/language":
      return t("profile.preferences.language");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Service account. */
function serviceAccountField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("serviceAccounts.fields.name");
    case "/role":
      return t("serviceAccounts.fields.role");
    case "/status":
      return t("audit.fields.status");
    default:
      return undefined;
  }
}

/** The names of the changed fields of an Integration. */
function integrationField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("integrations.fields.name");
    case "/description":
      return t("integrations.fields.description");
    case "/connection_mode":
      return t("integrations.fields.connectionMode");
    case "/static_labels":
      return t("integrations.fields.staticLabels");
    case "/duplicate_window_seconds":
      return t("audit.fields.duplicateWindowSeconds");
    case "/heartbeat/enabled":
      return t("audit.fields.heartbeatEnabled");
    case "/heartbeat/timeout_seconds":
      return t("audit.fields.heartbeatTimeoutSeconds");
    case "/deleted_at":
      return t("audit.fields.deletedAt");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Route, its policy fields included, and of the order of the Routes. */
function routeField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("routes.fields.name");
    case "/description":
      return t("routes.fields.description");
    case "/matchers":
      return t("routes.fields.matchers");
    case "/urgent":
      return t("routes.fields.urgent");
    case "/group_key":
      return t("routes.fields.groupKey");
    case "/destination_ids":
      return t("routes.destinations.title");
    case "/policy/reopen_window_seconds":
      return t("audit.fields.reopenWindowSeconds");
    case "/policy/grace_period_seconds":
      return t("audit.fields.gracePeriodSeconds");
    case "/policy/urgent_rise_removes_ack":
      return t("audit.fields.urgentRiseRemovesAck");
    case "/policy/snooze_durations_seconds":
      return t("audit.fields.snoozeDurationsSeconds");
    case "/policy/thread_batching_window_seconds":
      return t("audit.fields.threadBatchingWindowSeconds");
    case "/policy/storm_threshold":
      return t("audit.fields.stormThreshold");
    case "/policy/language":
      return t("audit.fields.messageLanguage");
    case "/policy/templates/root_message":
      return t("audit.fields.rootMessageTemplate");
    case "/policy/templates/line":
      return t("audit.fields.lineTemplate");
    case "/policy/templates/ack_timeout_notice":
      return t("audit.fields.ackTimeoutNoticeTemplate");
    case "/policy/ack_timeout/enabled":
      return t("audit.fields.ackTimeoutEnabled");
    case "/policy/ack_timeout/first_interval_seconds":
      return t("audit.fields.ackTimeoutFirstIntervalSeconds");
    case "/policy/reminders/enabled":
      return t("audit.fields.remindersEnabled");
    case "/policy/reminders/first_interval_seconds":
      return t("audit.fields.remindersFirstIntervalSeconds");
    case "/policy/reminders/cap_seconds":
      return t("audit.fields.remindersCapSeconds");
    case "/policy/auto_unacknowledge":
      return t("audit.fields.autoUnacknowledge");
    case "/deleted_at":
      return t("audit.fields.deletedAt");
    case "/route_ids":
      return t("audit.fields.routeOrder");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Lookup table. */
function lookupTableField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("lookupTables.fields.name");
    case "/description":
      return t("lookupTables.fields.description");
    case "/columns":
      return t("lookupTables.fields.columns");
    case "/entries":
      return t("lookupTables.fields.rows");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Link rule. */
function linkRuleField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("linkRules.fields.name");
    case "/matchers":
      return t("linkRules.fields.matchers");
    case "/scope/type":
      return t("linkRules.fields.scope");
    case "/scope/label":
      return t("linkRules.fields.scopeLabel");
    case "/url_template":
      return t("linkRules.fields.urlTemplate");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Connection; the bot token and the proxy password are Secrets. */
function connectionField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("connections.fields.name");
    case "/server_url":
      return t("connections.fields.serverUrl");
    case "/bot_token":
      return t("connections.fields.botToken");
    case "/limiter/limit":
      return t("audit.fields.limiterLimit");
    case "/limiter/per_seconds":
      return t("audit.fields.limiterPerSeconds");
    case "/proxy/enabled":
      return t("audit.fields.proxyEnabled");
    case "/proxy/type":
      return t("audit.fields.proxyType");
    case "/proxy/address":
      return t("audit.fields.proxyAddress");
    case "/proxy/username":
      return t("audit.fields.proxyUsername");
    case "/proxy/password":
      return t("audit.fields.proxyPassword");
    case "/deleted_at":
      return t("audit.fields.deletedAt");
    default:
      return undefined;
  }
}

/** The names of the changed fields of a Destination; the Mentions are one field. */
function destinationField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/name":
      return t("destinations.fields.name");
    case "/connection_id":
      return t("destinations.mattermost.connection");
    case "/team_id":
      return t("destinations.mattermost.team");
    case "/channel_id":
      return t("destinations.mattermost.channel");
    case "/mentions":
      return t("mentions.title");
    case "/limiter/limit":
      return t("audit.fields.limiterLimit");
    case "/limiter/per_seconds":
      return t("audit.fields.limiterPerSeconds");
    case "/deleted_at":
      return t("audit.fields.deletedAt");
    default:
      return undefined;
  }
}

/** The names of the changed fields of the OIDC settings. */
function oidcField(t: TFunction, pointer: string): string | undefined {
  switch (pointer) {
    case "/enabled":
      return t("audit.fields.oidcEnabled");
    case "/display_name":
      return t("oidc.fields.displayName");
    case "/issuer_url":
      return t("oidc.fields.issuerUrl");
    case "/client_id":
      return t("oidc.fields.clientId");
    case "/client_secret":
      return t("oidc.fields.clientSecret");
    case "/client_secret_expires_on":
      return t("oidc.fields.secretExpiresOn");
    case "/scopes":
      return t("oidc.fields.scopes");
    case "/groups_claim":
      return t("oidc.fields.groupsClaim");
    case "/group_mappings":
      return t("oidc.mapping.title");
    case "/unmatched_role":
      return t("oidc.fields.unmatchedRole");
    case "/sync_role":
      return t("oidc.fields.syncRole");
    case "/skip_totp_with_idp_mfa":
      return t("oidc.fields.skipTotp");
    case "/proxy/enabled":
      return t("audit.fields.proxyEnabled");
    case "/proxy/type":
      return t("audit.fields.proxyType");
    case "/proxy/address":
      return t("audit.fields.proxyAddress");
    case "/proxy/username":
      return t("audit.fields.proxyUsername");
    case "/proxy/password":
      return t("audit.fields.proxyPassword");
    default:
      return undefined;
  }
}

/**
 * The name of a changed field of a resource type; a field without a name of its own shows its JSON pointer. Each
 * resource type whose changes the Audit log records names its fields here.
 */
export function fieldLabel(
  t: TFunction,
  resourceType: string | null | undefined,
  pointer: string,
): string {
  let label: string | undefined;
  switch (resourceType) {
    case "user":
      label = userField(t, pointer);
      break;
    case "service_account":
      label = serviceAccountField(t, pointer);
      break;
    case "integration":
      label = integrationField(t, pointer);
      break;
    case "oidc_settings":
      label = oidcField(t, pointer);
      break;
    case "route":
      label = routeField(t, pointer);
      break;
    case "organization":
      label = pointer === "/totp_required" ? t("audit.fields.totpRequired") : undefined;
      break;
    case "lookup_table":
      label = lookupTableField(t, pointer);
      break;
    case "link_rule":
      label = linkRuleField(t, pointer);
      break;
    case "connection":
      label = connectionField(t, pointer);
      break;
    case "destination":
      label = destinationField(t, pointer);
      break;
    default:
      break;
  }
  return label ?? pointer;
}

/** A value of a diff in one line: text as it is, other JSON compact. */
function valueText(t: TFunction, value: unknown): string {
  if (value === undefined || value === null) {
    return "—";
  }
  if (value === "") {
    return t("audit.emptyValue");
  }
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "boolean") {
    return value ? t("audit.yes") : t("audit.no");
  }
  return JSON.stringify(value);
}

function Value({ value }: { value: unknown }) {
  const { t } = useTranslation();
  if (value === ERASED) {
    return (
      <span className="text-muted-foreground italic" data-erased="true">
        {t("audit.erased")}
      </span>
    );
  }
  const empty = value === undefined || value === null || value === "";
  return (
    <span className={empty ? "text-muted-foreground" : "font-mono text-xs break-all"}>
      {valueText(t, value)}
    </span>
  );
}

export function AuditDiff({
  diff,
  resourceType,
}: {
  diff: readonly AuditDiffEntry[] | undefined;
  resourceType: string | null | undefined;
}) {
  const { t } = useTranslation();
  if (diff === undefined || diff.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <dl className="flex flex-col gap-1" data-testid="audit-diff">
      {diff.map((entry) => (
        <div key={entry.pointer} className="flex flex-wrap items-baseline gap-x-1.5">
          <dt className="font-medium">{fieldLabel(t, resourceType, entry.pointer)}:</dt>
          <dd className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
            {entry.secret_changed === true ? (
              <span>{t("audit.secretChanged")}</span>
            ) : (
              <>
                <Value value={entry.before} />
                <span aria-hidden="true">→</span>
                <span className="sr-only">{t("audit.to")}</span>
                <Value value={entry.after} />
              </>
            )}
          </dd>
        </div>
      ))}
    </dl>
  );
}
