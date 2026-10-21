// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sections of the System status (C-19.FR-10), one per field of the status read and in its order: the version, the
// replicas with the Leader and their key ids, the recovery after downtime, the delivery queues, the Broken
// Destinations, the Integrations that need attention, the template errors, the Storms and the outgoing heartbeat. The
// page only reads: every entity links to its page, where it is fixed, and there is no other control. Texts that come
// from outside (reasons, errors) are shown as plain text and wrap anywhere, so that the page fits a phone.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { type ReactNode, useId } from "react";
import { useTranslation } from "react-i18next";

import { useGetSystemStatus } from "../api/gen/endpoints/system/system";
import type {
  EntityRef,
  NullableOutgoingHeartbeatErrorClass,
  OutgoingHeartbeatResult,
  SystemStatus,
  SystemStatusRecovery,
  TemplateKind,
} from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { unreachable } from "./destination-test-panel";
import { DocsLink } from "./docs-link";

type OutgoingHeartbeatErrorClass = NonNullable<NullableOutgoingHeartbeatErrorClass>;

/** How often the System status is read again, besides its hints. */
export const SYSTEM_STATUS_REFRESH_MS = 30_000;

/** The System status, read again on its hints and every 30 seconds; disabled, it is not read. */
export function useSystemStatus(enabled = true) {
  return useGetSystemStatus({ query: { refetchInterval: SYSTEM_STATUS_REFRESH_MS, enabled } });
}

/** Why an outgoing heartbeat failed, by its class, without the status of the answer. */
function heartbeatClassText(t: TFunction, errorClass: OutgoingHeartbeatErrorClass): string {
  switch (errorClass) {
    case "transient":
      return t("outgoingHeartbeat.failure.transient");
    case "retry_after":
      return t("outgoingHeartbeat.failure.retryAfter");
    case "fatal":
      return t("outgoingHeartbeat.failure.fatal");
    case "redirect":
      return t("outgoingHeartbeat.failure.redirect");
    case "unknown":
      return t("outgoingHeartbeat.failure.unknown");
    case "blocked":
      return t("outgoingHeartbeat.failure.blocked");
    case "dns":
      return t("outgoingHeartbeat.failure.dns");
    case "connect":
      return t("outgoingHeartbeat.failure.connect");
    case "tls":
      return t("outgoingHeartbeat.failure.tls");
    case "timeout":
      return t("outgoingHeartbeat.failure.timeout");
    case "proxy_auth":
      return t("outgoingHeartbeat.failure.proxyAuth");
    case "internal":
      return t("outgoingHeartbeat.failure.internal");
    default:
      return unreachable(errorClass);
  }
}

/**
 * Why the last outgoing heartbeat failed, in the reader's language: its class, with the status of the answer when there
 * was one; the server's English text without a class, null without either. The server's text stays the detail
 * (heartbeatFailureDetail).
 */
export function heartbeatFailureText(t: TFunction, result: OutgoingHeartbeatResult): string | null {
  const errorClass = result.last_error_class;
  if (errorClass === null || errorClass === undefined) {
    return result.last_error ? result.last_error : null;
  }
  const reason = heartbeatClassText(t, errorClass);
  return typeof result.last_status === "number"
    ? t("outgoingHeartbeat.failure.withStatus", { reason, status: result.last_status })
    : reason;
}

/** The server's text of the last failed heartbeat as the detail of its translated reason; null without both. */
export function HeartbeatFailureDetail({
  result,
  testId,
}: {
  result: OutgoingHeartbeatResult;
  testId: string;
}) {
  const { t } = useTranslation();
  if (result.last_ok !== false || !result.last_error || !result.last_error_class) {
    return null;
  }
  return (
    <span className="block text-xs text-muted-foreground wrap-anywhere" data-testid={testId}>
      {t("outgoingHeartbeat.failure.detail", { detail: result.last_error })}
    </span>
  );
}

/** The last outgoing heartbeat as the System status shows it; null when no URL is set. */
export function heartbeatSentText(
  t: TFunction,
  result: OutgoingHeartbeatResult,
  dateTime: (iso: string) => string,
): string | null {
  if (!result.configured) {
    return null;
  }
  if (!result.last_attempt_at) {
    return t("systemStatus.heartbeat.notSent");
  }
  const time = dateTime(result.last_attempt_at);
  if (result.last_ok === true) {
    return t("systemStatus.heartbeat.ok", { time });
  }
  const error = heartbeatFailureText(t, result);
  return error !== null
    ? t("systemStatus.heartbeat.failed", { time, error })
    : t("systemStatus.heartbeat.failedNoError", { time });
}

/** The name of a template kind, as the template editors call it. */
export function templateKindLabel(t: TFunction, kind: TemplateKind): string {
  switch (kind) {
    case "root_message":
      return t("systemStatus.templates.rootMessage");
    case "line":
      return t("systemStatus.templates.line");
    case "ack_timeout_notice":
      return t("systemStatus.templates.ackTimeoutNotice");
    case "link_rule":
      return t("systemStatus.templates.linkRule");
    default:
      return t("systemStatus.templates.webhookRequest");
  }
}

function recoveryText(
  t: TFunction,
  recovery: SystemStatusRecovery,
  dateTime: (iso: string) => string,
): string {
  if (!recovery.active) {
    return t("systemStatus.recovery.inactive");
  }
  if (!recovery.until) {
    return t("systemStatus.recovery.activeNoTime");
  }
  const until = dateTime(recovery.until);
  return recovery.period_from && recovery.period_to
    ? t("systemStatus.recovery.active", {
        until,
        from: dateTime(recovery.period_from),
        to: dateTime(recovery.period_to),
      })
    : t("systemStatus.recovery.activeNoPeriod", { until });
}

const LINK =
  "wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline";

/** A link to the page of a listed entity. */
function EntityLink({
  kind,
  entity,
}: {
  kind: "destination" | "integration" | "route";
  entity: EntityRef;
}) {
  switch (kind) {
    case "destination":
      return (
        <Link
          to="/destinations/$destinationId"
          params={{ destinationId: entity.id }}
          className={LINK}
        >
          {entity.name}
        </Link>
      );
    case "integration":
      return (
        <Link
          to="/integrations/$integrationId"
          params={{ integrationId: entity.id }}
          className={LINK}
        >
          {entity.name}
        </Link>
      );
    default:
      return (
        <Link to="/routes/$routeId" params={{ routeId: entity.id }} className={LINK}>
          {entity.name}
        </Link>
      );
  }
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-2 rounded-lg border p-4">
      <h2 id={id} className="text-base font-semibold">
        {title}
      </h2>
      {children}
    </section>
  );
}

/** The rows of a section, or "None". */
function Rows({ children }: { children: ReactNode[] }) {
  const { t } = useTranslation();
  if (children.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("systemStatus.none")}</p>;
  }
  return <ul className="flex min-w-0 flex-col gap-2 text-sm">{children}</ul>;
}

/** Free text from outside, such as a reason or an error, on a line of its own. */
function Detail({ children }: { children: string }) {
  return <p className="mt-0.5 wrap-anywhere whitespace-pre-wrap text-destructive">{children}</p>;
}

export function SystemStatusSections({
  status,
  canSetUpHeartbeat,
}: {
  status: SystemStatus;
  /** The user may change the Organization, so the suggestion links to the outgoing heartbeat settings. */
  canSetUpHeartbeat: boolean;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const since = (iso: string) => t("systemStatus.since", { time: dateTime(iso) });
  const heartbeat = heartbeatSentText(t, status.outgoing_heartbeat, dateTime);
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <p className="text-sm text-muted-foreground" data-testid="system-status-version">
        {status.commit
          ? t("systemStatus.version", { version: status.version, commit: status.commit })
          : t("systemStatus.versionNoCommit", { version: status.version })}
      </p>
      <Section title={t("systemStatus.replicas.title")}>
        <Rows>
          {status.replicas.map((r) => (
            <li key={r.id} className="min-w-0" data-testid="replica">
              <code className="font-mono wrap-anywhere">{r.id}</code>
              {r.leader && (
                <span
                  className="ml-2 inline-block rounded-md bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary"
                  data-testid="replica-leader"
                >
                  {t("systemStatus.replicas.leader")}
                </span>
              )}
              {" · "}
              <span className="wrap-anywhere">
                {r.key_ids.length === 0
                  ? t("systemStatus.replicas.noKeys")
                  : t("systemStatus.replicas.keys", { keys: r.key_ids.join(", ") })}
              </span>
              {" · "}
              <span className="text-muted-foreground">
                {t("systemStatus.replicas.refreshed", { time: dateTime(r.refreshed_at) })}
              </span>
            </li>
          ))}
        </Rows>
      </Section>
      <Section title={t("systemStatus.recovery.title")}>
        <p className="text-sm" data-testid="system-status-recovery">
          {recoveryText(t, status.recovery, dateTime)}
        </p>
      </Section>
      <Section title={t("systemStatus.queues.title")}>
        <Rows>
          {status.delivery_queues.map((q) => (
            <li key={q.destination.id} className="min-w-0">
              <EntityLink kind="destination" entity={q.destination} />
              {" · "}
              {t("systemStatus.queues.queued", { count: q.queued })}
            </li>
          ))}
        </Rows>
      </Section>
      <Section title={t("systemStatus.broken.title")}>
        <Rows>
          {status.broken_destinations.map((b) => (
            <li key={b.destination.id} className="min-w-0">
              <EntityLink kind="destination" entity={b.destination} />
              {" · "}
              <span className="text-muted-foreground">{since(b.since)}</span>
              <Detail>{b.reason}</Detail>
            </li>
          ))}
        </Rows>
      </Section>
      <Section title={t("systemStatus.attention.title")}>
        <Rows>
          {status.integrations_attention.map((a) => {
            let issue: string;
            if (a.issue === "snapshot_truncated") {
              issue = t("systemStatus.attention.snapshotTruncated");
            } else if (a.since) {
              issue = t("systemStatus.attention.heartbeatLostSince", { time: dateTime(a.since) });
            } else {
              issue = t("systemStatus.attention.heartbeatLost");
            }
            return (
              <li key={`${a.integration.id}-${a.issue}`} className="min-w-0">
                <EntityLink kind="integration" entity={a.integration} />
                {" — "}
                {issue}
              </li>
            );
          })}
        </Rows>
      </Section>
      <Section title={t("systemStatus.templates.title")}>
        <Rows>
          {status.template_errors.map((e) => (
            <li key={`${e.subject_kind}-${e.subject.id}-${e.template}`} className="min-w-0">
              <EntityLink kind={e.subject_kind} entity={e.subject} />
              {" · "}
              {e.subject_kind === "route"
                ? t("systemStatus.templates.route")
                : t("systemStatus.templates.destination")}
              {" · "}
              {templateKindLabel(t, e.template)}
              {" · "}
              <span className="text-muted-foreground">{since(e.since)}</span>
              <Detail>{e.error}</Detail>
            </li>
          ))}
        </Rows>
      </Section>
      <Section title={t("systemStatus.storms.title")}>
        <Rows>
          {status.active_storms.map((s) => (
            <li key={s.route.id} className="min-w-0">
              <EntityLink kind="route" entity={s.route} />
              {" · "}
              <span className="text-muted-foreground">{since(s.since)}</span>
              {" · "}
              {t("systemStatus.storms.count", { count: s.alert_group_count })}
            </li>
          ))}
        </Rows>
      </Section>
      <Section title={t("systemStatus.heartbeat.title")}>
        <p className="text-sm wrap-anywhere" data-testid="system-status-heartbeat">
          {heartbeat ?? (
            <>
              {canSetUpHeartbeat ? (
                <Link to="/admin/organization/outgoing-heartbeat" className={LINK}>
                  {t("systemStatus.heartbeat.suggestion")}
                </Link>
              ) : (
                <span>{t("systemStatus.heartbeat.suggestion")}</span>
              )}{" "}
              <DocsLink
                to="fix.outgoing_heartbeat"
                kind="how_to_fix"
                about={t("docs.fixes.outgoingHeartbeat")}
              />
            </>
          )}
        </p>
        <HeartbeatFailureDetail
          result={status.outgoing_heartbeat}
          testId="system-status-heartbeat-detail"
        />
      </Section>
    </div>
  );
}
