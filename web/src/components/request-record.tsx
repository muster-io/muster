// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A record of the Request log (C-23.FR-6): the request — method, URL, headers, body — and the answer — status, headers,
// body —, side by side from 1024 px and one under the other below, with when it was made in the user's time zone, its
// direction, latency, path and attempt, the kind of check, what failed, and the links to the Destination, the Alert
// Group and the requests of its check. The server masks every secret before a record is written; headers, bodies and
// URLs are untrusted text and are shown as text only, a JSON body indented to be read, a cut body marked with its full
// size, and each has a copy button, which copies the raw text also on a plain-http installation. A record of a
// Connection needs the permission to read Connections, which a Destination's list does not: the dialog then says so
// instead of the record.

import { Link } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { CheckIcon, CopyIcon } from "lucide-react";
import { type ReactNode, useEffect, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { useGetRequestRecord } from "../api/gen/endpoints/request-log/request-log";
import type {
  NullableCheckKind,
  NullableRequestErrorClass,
  RequestDirection,
  RequestRecord,
  RequestRecordSummary,
} from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { SHEET } from "../lib/commands";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { checkCodeText } from "./connection-check";
import { unreachable } from "./destination-test-panel";
import { copyText } from "./integration-token-dialog";
import { CodeText, readableBody } from "./rendered-request";
import { COPIED_MS } from "./setup-link-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";

/** The accessible name of a direction: "Sent" for a request Muster made, "Received" for one that reached it. */
function directionText(t: TFunction, direction: RequestDirection): string {
  switch (direction) {
    case "outbound":
      return t("requestLog.direction.outbound");
    case "inbound":
      return t("requestLog.direction.inbound");
    default:
      return unreachable(direction);
  }
}

/** The arrow of a direction, named for screen readers and in its tooltip. */
export function DirectionArrow({ direction }: { direction: RequestDirection }) {
  const { t } = useTranslation();
  const name = directionText(t, direction);
  return (
    <span role="img" aria-label={name} title={name} data-testid="request-direction">
      {direction === "outbound" ? "→" : "←"}
    </span>
  );
}

/** The kind of check that made a request. */
export function checkKindText(t: TFunction, kind: NonNullable<NullableCheckKind>): string {
  switch (kind) {
    case "connection_check":
      return t("requestLog.checks.connectionCheck");
    case "draft_check":
      return t("requestLog.checks.draftCheck");
    case "destination_check":
      return t("requestLog.checks.destinationCheck");
    case "test":
      return t("requestLog.checks.test");
    case "probe":
      return t("requestLog.checks.probe");
    default:
      return unreachable(kind);
  }
}

/** How a failed attempt was classified. */
export function errorClassText(t: TFunction, errorClass: NonNullable<NullableRequestErrorClass>) {
  switch (errorClass) {
    case "retry_after":
      return t("requestLog.errorClasses.retryAfter");
    case "transient":
      return t("requestLog.errorClasses.transient");
    case "fatal":
      return t("requestLog.errorClasses.fatal");
    case "unknown":
      return t("requestLog.errorClasses.unknown");
    case "blocked":
      return t("requestLog.errorClasses.blocked");
    case "redirect":
      return t("requestLog.errorClasses.redirect");
    default:
      return unreachable(errorClass);
  }
}

/** Formats the time of a record with its seconds, in the user's time zone and language. */
export function useRecordTime(): (iso: string) => string {
  const { i18n } = useTranslation();
  const { timeZone } = useTimeFormat();
  const locale = i18n.resolvedLanguage ?? "en";
  return useMemo(() => {
    const format = new Intl.DateTimeFormat(locale, {
      dateStyle: "medium",
      timeStyle: "medium",
      hourCycle: "h23",
      timeZone,
    });
    return (iso: string) => format.format(new Date(iso));
  }, [locale, timeZone]);
}

/**
 * A size in bytes as kilobytes of 1024 bytes, rounded up to one decimal, so that a body cut at 16 KB never reads as
 * being of 16 KB itself.
 */
function sizeKb(bytes: number, locale: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(
    Math.ceil((bytes * 10) / 1024) / 10,
  );
}

/** A button that copies a text exactly and says "Copied" for a moment. */
function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) {
      return undefined;
    }
    const timer = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(timer);
  }, [copied]);
  return (
    <>
      <Button
        variant="outline"
        size="xs"
        onClick={() => {
          void copyText(text).then(setCopied);
        }}
      >
        {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
        {copied ? t("requestLog.record.copied") : label}
      </Button>
      <span role="status" className="sr-only">
        {copied ? t("requestLog.record.copied") : ""}
      </span>
    </>
  );
}

/** The headers as name and value pairs, one per value, in the order the record holds them. */
function headerPairs(headers: Record<string, string[]> | null): [string, string][] {
  return Object.entries(headers ?? {}).flatMap(([name, values]) =>
    values.map((value): [string, string] => [name, value]),
  );
}

/** A labelled part of the request or the answer, with its copy button. */
function Labelled({
  label,
  action,
  children,
}: {
  label: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">{label}</h4>
        {action}
      </div>
      {children}
    </div>
  );
}

function Headers({
  headers,
  testId,
}: {
  headers: Record<string, string[]> | null;
  testId: string;
}) {
  const { t } = useTranslation();
  const pairs = headerPairs(headers);
  return (
    <Labelled
      label={t("requestLog.record.headers")}
      action={
        pairs.length > 0 && (
          <CopyButton
            text={pairs.map(([name, value]) => `${name}: ${value}`).join("\n")}
            label={t("requestLog.record.copyHeaders")}
          />
        )
      }
    >
      {pairs.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("requestLog.record.noHeaders")}</p>
      ) : (
        <dl
          className="grid grid-cols-[fit-content(40%)_minmax(0,1fr)] gap-x-3 gap-y-0.5 font-mono text-xs"
          data-testid={testId}
        >
          {pairs.map(([name, value], index) => (
            <div key={`${name}-${index}`} className="contents" data-testid="record-header">
              <dt className="font-semibold wrap-anywhere">{name}</dt>
              <dd className="wrap-anywhere whitespace-pre-wrap">{value}</dd>
            </div>
          ))}
        </dl>
      )}
    </Labelled>
  );
}

/** Characters a body shows only as their escapes: controls, format characters, surrogates, private and unassigned. */
const HIDDEN = /[\p{C}\p{Zl}\p{Zp}]/u;

/** The UTF-16 unit of the four hexadecimal digits at an index. */
function hexUnit(text: string, index: number): number {
  return Number.parseInt(text.slice(index, index + 4), 16);
}

/**
 * Valid JSON text without the whitespace between its tokens, its strings with \/ and the \uXXXX escapes of characters
 * that can be shown — a surrogate pair as the one character it encodes — written as the characters, except " and \.
 * Every other escape stays as it is: \\ is a backslash, never the start of an escape.
 */
function unescapedJson(text: string): string {
  let out = "";
  let inString = false;
  for (let i = 0; i < text.length; i++) {
    const c = text.charAt(i);
    if (!inString) {
      if (c !== " " && c !== "\t" && c !== "\n" && c !== "\r") {
        out += c;
      }
      inString = c === '"';
      continue;
    }
    if (c !== "\\") {
      out += c;
      inString = c !== '"';
      continue;
    }
    const next = text.charAt(i + 1);
    if (next !== "u") {
      out += next === "/" ? "/" : c + next;
      i++;
      continue;
    }
    const unit = hexUnit(text, i + 2);
    let length = 6;
    let char = String.fromCharCode(unit);
    if (unit >= 0xd800 && unit <= 0xdbff && text.startsWith("\\u", i + 6)) {
      const low = hexUnit(text, i + 8);
      if (low >= 0xdc00 && low <= 0xdfff) {
        length = 12;
        char = String.fromCharCode(unit, low);
      }
    }
    // A lone surrogate is a character of its own to the expression, and hidden.
    out += char === '"' || char === "\\" || HIDDEN.test(char) ? text.slice(i, i + length) : char;
    i += length - 1;
  }
  return out;
}

/**
 * A body as the record shows it: indented when it is JSON that reads back the same apart from its spacing and the
 * escapes of characters that can be shown, which a messenger may send for every character beyond ASCII; else as it
 * is, such as a number beyond the precision of JavaScript, 1.0, or a key given twice.
 */
function shownBody(body: string): string {
  const readable = readableBody(body);
  const trimmed = body.trim();
  if (readable !== body || (!trimmed.startsWith("{") && !trimmed.startsWith("["))) {
    return readable;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return body;
  }
  return JSON.stringify(parsed) === unescapedJson(trimmed) ? JSON.stringify(parsed, null, 2) : body;
}

/**
 * A body as one block of text: indented when it is JSON, else as it is; a cut body says how large it was, and an
 * inbound body that was not kept says why. Copying copies the body as it was recorded.
 */
function Body({
  text,
  truncated,
  bytes,
  testId,
}: {
  text: string;
  truncated: boolean;
  bytes: number;
  testId: string;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const readable = useMemo(() => shownBody(text), [text]);
  return (
    <Labelled
      label={t("requestLog.record.body")}
      action={text !== "" && <CopyButton text={text} label={t("requestLog.record.copyBody")} />}
    >
      {text === "" ? (
        <p className="text-xs text-muted-foreground" data-testid={testId}>
          {truncated
            ? t("requestLog.record.notKept", { size: sizeKb(bytes, locale) })
            : t("requestLog.record.noBody")}
        </p>
      ) : (
        <>
          <CodeText text={readable} testId={testId} />
          {truncated && (
            <p className="text-xs text-muted-foreground" data-testid={`${testId}-cut`}>
              {t("requestLog.record.truncated", { size: sizeKb(bytes, locale) })}
            </p>
          )}
        </>
      )}
    </Labelled>
  );
}

/** The request or the answer, a region named by its heading. */
function Part({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-3 rounded-lg border p-3">
      <h3 id={id} className="text-sm font-medium">
        {title}
      </h3>
      {children}
    </section>
  );
}

function RequestPart({ record }: { record: RequestRecord }) {
  const { t } = useTranslation();
  return (
    <Part title={t("requestLog.record.request")}>
      <Labelled
        label={t("requestLog.record.url")}
        action={<CopyButton text={record.url} label={t("requestLog.record.copyUrl")} />}
      >
        <p className="font-mono text-xs wrap-anywhere" data-testid="record-request-line">
          <span className="font-semibold">{record.method}</span> {record.url}
        </p>
      </Labelled>
      <Headers headers={record.request_headers} testId="record-request-headers" />
      <Body
        text={record.request_body}
        truncated={record.request_body_truncated}
        bytes={record.request_body_bytes}
        testId="record-request-body"
      />
    </Part>
  );
}

function AnswerPart({ record }: { record: RequestRecord }) {
  const { t } = useTranslation();
  return (
    <Part title={t("requestLog.record.answer")}>
      {record.status === null ? (
        <p className="text-sm text-muted-foreground" data-testid="record-status">
          {t("requestLog.record.noAnswer")}
        </p>
      ) : (
        <>
          <Labelled label={t("requestLog.record.status")}>
            <p
              className={
                record.status >= 400 ? "font-mono text-sm text-destructive" : "font-mono text-sm"
              }
              data-testid="record-status"
            >
              {record.status}
            </p>
          </Labelled>
          <Headers headers={record.response_headers} testId="record-response-headers" />
          <Body
            text={record.response_body ?? ""}
            truncated={record.response_body_truncated}
            bytes={record.response_body_bytes}
            testId="record-response-body"
          />
        </>
      )}
    </Part>
  );
}

function Fact({
  label,
  children,
  testId,
}: {
  label: string;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 wrap-anywhere" data-testid={testId}>
        {children}
      </dd>
    </div>
  );
}

const LINK = "text-primary underline-offset-4 hover:underline focus-visible:underline";

/** Where the requests of the record's check are listed: its owner's tab, filtered by the run. */
function RunLink({ record, onNavigate }: { record: RequestRecordSummary; onNavigate: () => void }) {
  const { t } = useTranslation();
  const canConnections = useCan("connections:read");
  const run = record.check_run;
  if (run === null) {
    return null;
  }
  const search = { tab: "request-log" as const, run };
  if (record.connection_id !== null) {
    // A Connection's tab needs the permission to read Connections; without it there is nothing to follow.
    if (!canConnections) {
      return null;
    }
    return (
      <Link
        to="/connections/$connectionId"
        params={{ connectionId: record.connection_id }}
        search={search}
        className={LINK}
        onClick={onNavigate}
      >
        {t("requestLog.ofThisCheck")}
      </Link>
    );
  }
  if (record.destination?.type === "webhook") {
    return (
      <Link
        to="/destinations/$destinationId"
        params={{ destinationId: record.destination.id }}
        search={search}
        className={LINK}
        onClick={onNavigate}
      >
        {t("requestLog.ofThisCheck")}
      </Link>
    );
  }
  return null;
}

function Facts({
  record,
  error,
  currentRun,
  onNavigate,
}: {
  record: RequestRecordSummary;
  error: string | null;
  currentRun: string | undefined;
  onNavigate: () => void;
}) {
  const { t } = useTranslation();
  const canDestinations = useCan("destinations:read");
  const canAlertGroups = useCan("alert-groups:read");
  const { destination, alert_group: group } = record;
  return (
    <dl
      className="grid grid-cols-[fit-content(40%)_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm"
      data-testid="record-facts"
    >
      <Fact label={t("requestLog.record.direction")}>{directionText(t, record.direction)}</Fact>
      <Fact label={t("requestLog.record.latency")}>
        {t("requestLog.latency", { ms: record.latency_ms })}
      </Fact>
      <Fact label={t("requestLog.record.via")}>
        {record.via === "proxy" ? t("requestLog.record.proxy") : t("requestLog.record.direct")}
      </Fact>
      <Fact label={t("requestLog.record.attempt")}>{record.attempt}</Fact>
      {record.check !== null && (
        <Fact label={t("requestLog.record.check")}>
          <span className="flex flex-wrap gap-x-3 gap-y-1">
            <span data-testid="record-check">{checkKindText(t, record.check)}</span>
            {record.check_run !== currentRun && <RunLink record={record} onNavigate={onNavigate} />}
          </span>
        </Fact>
      )}
      {record.error_class !== null && (
        <Fact label={t("requestLog.record.errorClass")}>
          {errorClassText(t, record.error_class)}
        </Fact>
      )}
      {record.error_code !== null && (
        <Fact label={t("requestLog.record.errorCode")}>{checkCodeText(t, record.error_code)}</Fact>
      )}
      {error !== null && error !== "" && (
        <Fact label={t("requestLog.record.error")} testId="record-error-text">
          <span className="whitespace-pre-wrap">{error}</span>
        </Fact>
      )}
      {destination !== null && (
        <Fact label={t("requestLog.record.destination")} testId="record-destination">
          {canDestinations ? (
            <Link
              to="/destinations/$destinationId"
              params={{ destinationId: destination.id }}
              className={LINK}
              onClick={onNavigate}
            >
              {destination.name}
            </Link>
          ) : (
            destination.name
          )}
        </Fact>
      )}
      {group !== null && (
        <Fact label={t("requestLog.record.alertGroup")} testId="record-alert-group">
          {canAlertGroups ? (
            <Link
              to="/alert-groups/$alertGroupId"
              params={{ alertGroupId: group.id }}
              className={LINK}
              onClick={onNavigate}
            >
              #{group.number}
            </Link>
          ) : (
            `#${group.number}`
          )}
        </Fact>
      )}
    </dl>
  );
}

/** The text of a record that cannot be read: a record of a Connection needs the permission to read Connections. */
function readErrorText(t: TFunction, err: unknown): string {
  if (isApiError(err) && err.status === 403 && err.code !== "csrf_invalid") {
    return t("requestLog.record.forbidden");
  }
  return problemText(t, err);
}

export interface RequestRecordDialogProps {
  /** The record as the list shows it, which opens the dialog; null closes it. */
  summary: RequestRecordSummary | null;
  onClose: () => void;
  /** The check run the list already shows, whose link the record leaves out. */
  currentRun?: string;
  /** Called when a link of the record is followed, after the dialog closes. */
  onNavigate?: () => void;
}

export function RequestRecordDialog({
  summary,
  onClose,
  currentRun,
  onNavigate,
}: RequestRecordDialogProps) {
  const { t } = useTranslation();
  const time = useRecordTime();
  // The last record shown stays in the dialog while it closes.
  const [shown, setShown] = useState(summary);
  if (summary !== null && summary !== shown) {
    setShown(summary);
  }
  const query = useGetRequestRecord(shown?.id ?? "", { query: { enabled: summary !== null } });
  const record = query.data !== undefined && query.data.id === shown?.id ? query.data : undefined;
  const navigate = () => {
    onClose();
    onNavigate?.();
  };
  return (
    <Dialog
      open={summary !== null}
      onOpenChange={(open) => {
        if (!open) {
          onClose();
        }
      }}
    >
      <DialogContent
        closeLabel={t("common.close")}
        className={`${SHEET} sm:max-w-2xl lg:max-w-6xl`}
        data-testid="request-record"
      >
        {shown !== null && (
          <>
            <DialogHeader className="min-w-0">
              <DialogTitle className="flex min-w-0 items-center gap-2 pr-8 wrap-anywhere">
                <DirectionArrow direction={shown.direction} />
                <code className="font-mono">{shown.operation}</code>
              </DialogTitle>
              <DialogDescription>
                <time dateTime={shown.at}>{time(shown.at)}</time>
              </DialogDescription>
            </DialogHeader>
            <Facts
              record={record ?? shown}
              error={record?.error ?? null}
              currentRun={currentRun}
              onNavigate={navigate}
            />
            {record === undefined ? (
              query.isError ? (
                <Alert variant="destructive" data-testid="record-error">
                  <AlertDescription className="text-current">
                    {readErrorText(t, query.error)}
                  </AlertDescription>
                </Alert>
              ) : (
                <p className="text-sm text-muted-foreground" role="status">
                  {t("common.loading")}
                </p>
              )
            ) : (
              <div className="grid min-w-0 gap-4 lg:grid-cols-2" data-testid="record-parts">
                <RequestPart record={record} />
                <AnswerPart record={record} />
              </div>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
